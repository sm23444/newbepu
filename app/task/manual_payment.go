package task

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/shopspring/decimal"
	"github.com/tidwall/gjson"
	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/utils"
	"github.com/v03413/tronprotocol/api"
	"github.com/v03413/tronprotocol/core"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	tgo "github.com/xssnick/tonutils-go/ton"
)

const manualPaymentTimeout = 20 * time.Second

var (
	ErrManualPaymentInvalidOrder    = errors.New("manual payment order is invalid")
	ErrManualPaymentNotEligible     = errors.New("order is not eligible for manual payment recovery")
	ErrManualPaymentExchangeOrder   = errors.New("exchange orders do not support transaction hash submission")
	ErrManualPaymentNetworkDisabled = errors.New("order network is not configured")
	ErrManualPaymentInvalidTxID     = errors.New("invalid transaction id")
	ErrManualPaymentTxUsed          = errors.New("transaction id has already been used")
	ErrManualPaymentMismatch        = errors.New("transaction does not match the order")
)

type ManualPaymentResult struct {
	TradeID string `json:"trade_id"`
	TxHash  string `json:"tx_hash"`
	Status  int    `json:"status"`
}

type manualPaymentVerifier func(context.Context, *model.Order, string) (transfer, error)

var defaultManualPaymentVerifiers = map[model.Network]manualPaymentVerifier{
	conf.Tron:     verifyManualTronPayment,
	conf.Solana:   verifyManualSolanaPayment,
	conf.Aptos:    verifyManualAptosPayment,
	conf.Ton:      verifyManualTonPayment,
	conf.Bsc:      verifyManualEVMPayment,
	conf.Ethereum: verifyManualEVMPayment,
	conf.Polygon:  verifyManualEVMPayment,
	conf.Arbitrum: verifyManualEVMPayment,
	conf.Xlayer:   verifyManualEVMPayment,
	conf.Base:     verifyManualEVMPayment,
	conf.Plasma:   verifyManualEVMPayment,
}

func SubmitManualPayment(ctx context.Context, order *model.Order, txID string) (ManualPaymentResult, error) {
	return submitManualPaymentWithVerifiers(ctx, order, txID, defaultManualPaymentVerifiers)
}

func ManualPaymentAvailable(order *model.Order) bool {
	if order == nil || order.ID == 0 || order.TradeType == "" || order.CreatedAt == nil {
		return false
	}
	if model.IsExchangeTradeType(order.TradeType) {
		return false
	}
	if order.Status != model.OrderStatusWaiting && order.Status != model.OrderStatusExpired {
		return false
	}
	if order.Status == model.OrderStatusExpired && order.ExpiredAt.Before(time.Now().Add(model.GetLookbackHour())) {
		return false
	}
	trade, ok := model.GetTradeConfig(order.TradeType)
	return ok && strings.TrimSpace(model.Endpoint(trade.Network)) != ""
}

func submitManualPaymentWithVerifiers(ctx context.Context, order *model.Order, txID string, verifiers map[model.Network]manualPaymentVerifier) (ManualPaymentResult, error) {
	var result ManualPaymentResult
	if order == nil || order.ID == 0 || order.CreatedAt == nil || order.TradeType == "" {
		return result, ErrManualPaymentInvalidOrder
	}
	if model.IsExchangeTradeType(order.TradeType) {
		return result, ErrManualPaymentExchangeOrder
	}
	if order.Status != model.OrderStatusWaiting && order.Status != model.OrderStatusExpired {
		return result, ErrManualPaymentNotEligible
	}
	if order.Status == model.OrderStatusExpired && order.ExpiredAt.Before(time.Now().Add(model.GetLookbackHour())) {
		return result, ErrManualPaymentNotEligible
	}

	trade, ok := model.GetTradeConfig(order.TradeType)
	if !ok {
		return result, ErrManualPaymentInvalidOrder
	}
	network := trade.Network
	verifier, ok := verifiers[network]
	if !ok {
		return result, fmt.Errorf("%w: %s", ErrManualPaymentNetworkDisabled, network)
	}
	if strings.TrimSpace(model.Endpoint(network)) == "" {
		return result, fmt.Errorf("%w: %s", ErrManualPaymentNetworkDisabled, network)
	}

	canonicalTxID, err := canonicalManualTransactionID(network, txID)
	if err != nil {
		return result, fmt.Errorf("%w: %v", ErrManualPaymentInvalidTxID, err)
	}
	caseInsensitive := manualTransactionIDCaseInsensitive(network)
	used, err := model.ManualPaymentTransactionUsed(string(network), canonicalTxID, caseInsensitive)
	if err != nil {
		return result, err
	}
	if used {
		return result, ErrManualPaymentTxUsed
	}

	verifyCtx, cancel := context.WithTimeout(ctx, manualPaymentTimeout)
	defer cancel()
	trans, err := verifier(verifyCtx, order, strings.TrimSpace(txID))
	if err != nil {
		return result, err
	}
	if trans.Network != string(network) || trans.TradeType != order.TradeType || !orderTransferMatch(*order, trans) {
		return result, ErrManualPaymentMismatch
	}

	if err = model.ClaimManualPayment(order, string(network), trans.TxHash, trans.BlockNum, trans.FromAddress, trans.Timestamp, trans.Amount, caseInsensitive); err != nil {
		if errors.Is(err, model.ErrManualPaymentAlreadyClaimed) {
			return result, ErrManualPaymentTxUsed
		}
		if errors.Is(err, model.ErrManualPaymentOrderChanged) {
			current, found := model.GetTradeOrder(order.TradeId)
			if found && (current.Status == model.OrderStatusConfirming || current.Status == model.OrderStatusSuccess) && strings.EqualFold(current.RefHash, trans.TxHash) {
				return ManualPaymentResult{TradeID: current.TradeId, TxHash: current.RefHash, Status: current.Status}, nil
			}
			return result, ErrManualPaymentNotEligible
		}
		return result, err
	}

	return ManualPaymentResult{TradeID: order.TradeId, TxHash: order.RefHash, Status: order.Status}, nil
}

func canonicalManualTransactionID(network model.Network, input string) (string, error) {
	switch network {
	case conf.Tron:
		return normalizeHexTransactionID(input, false)
	case conf.Solana:
		input = strings.TrimSpace(input)
		if len(base58.Decode(input)) != 64 {
			return "", errors.New("invalid Solana transaction signature")
		}
		return input, nil
	case conf.Ton:
		ref, err := parseManualTonTxRef(input)
		if err != nil {
			return "", err
		}
		return ref.HashHex, nil
	case conf.Aptos:
		return normalizeHexTransactionID(input, true)
	default:
		return normalizeHexTransactionID(input, true)
	}
}

func normalizeHexTransactionID(input string, withPrefix bool) (string, error) {
	value := strings.TrimSpace(input)
	value = strings.TrimPrefix(strings.TrimPrefix(value, "0x"), "0X")
	if len(value) != 64 {
		return "", errors.New("transaction id must contain 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", errors.New("transaction id is not hexadecimal")
	}
	value = strings.ToLower(value)
	if withPrefix {
		value = "0x" + value
	}
	return value, nil
}

func manualTransactionIDCaseInsensitive(network model.Network) bool {
	return network != conf.Solana
}

func findManualOrderTransfer(order *model.Order, transfers []transfer) (transfer, error) {
	for _, trans := range transfers {
		if orderTransferMatch(*order, trans) {
			return trans, nil
		}
	}
	return transfer{}, ErrManualPaymentMismatch
}

func verifyManualEVMPayment(ctx context.Context, order *model.Order, input string) (transfer, error) {
	txID, err := normalizeHexTransactionID(input, true)
	if err != nil {
		return transfer{}, err
	}
	trade, ok := model.GetTradeConfig(order.TradeType)
	if !ok {
		return transfer{}, ErrManualPaymentInvalidOrder
	}
	endpoint := model.Endpoint(trade.Network)
	client := utils.NewHttpClient()

	receipt, err := manualJSONRPCCall(ctx, client, endpoint, "eth_getTransactionReceipt", []any{txID})
	if err != nil {
		return transfer{}, fmt.Errorf("fetch transaction receipt: %w", err)
	}
	if receipt.Get("status").String() != "0x1" {
		return transfer{}, errors.New("transaction is not successful")
	}
	blockHex := receipt.Get("blockNumber").String()
	blockNum, err := parseHexInt(blockHex)
	if err != nil || blockNum <= 0 {
		return transfer{}, errors.New("transaction receipt has no valid block number")
	}
	block, err := manualJSONRPCCall(ctx, client, endpoint, "eth_getBlockByNumber", []any{blockHex, false})
	if err != nil {
		return transfer{}, fmt.Errorf("fetch transaction block: %w", err)
	}
	timestampUnix, err := parseHexInt(block.Get("timestamp").String())
	if err != nil || timestampUnix <= 0 {
		return transfer{}, errors.New("transaction block has no valid timestamp")
	}
	timestamp := time.Unix(timestampUnix, 0)

	if trade.Native {
		tx, err := manualJSONRPCCall(ctx, client, endpoint, "eth_getTransactionByHash", []any{txID})
		if err != nil {
			return transfer{}, fmt.Errorf("fetch transaction: %w", err)
		}
		if tx.Get("input").String() != "0x" {
			return transfer{}, errors.New("transaction is not a native coin transfer")
		}
		amount, err := parseHexBigInt(tx.Get("value").String())
		if err != nil || amount.Sign() <= 0 {
			return transfer{}, errors.New("transaction amount is invalid")
		}
		return transfer{
			Network:     string(trade.Network),
			TxHash:      txID,
			Amount:      decimal.NewFromBigInt(amount, trade.Decimal),
			FromAddress: strings.ToLower(tx.Get("from").String()),
			RecvAddress: strings.ToLower(tx.Get("to").String()),
			Timestamp:   timestamp,
			TradeType:   order.TradeType,
			BlockNum:    int(blockNum),
		}, nil
	}

	contract := strings.ToLower(trade.Contract)
	transfers := make([]transfer, 0)
	for _, event := range receipt.Get("logs").Array() {
		if !strings.EqualFold(event.Get("address").String(), contract) {
			continue
		}
		topics := event.Get("topics").Array()
		if len(topics) < 3 || !strings.EqualFold(topics[0].String(), evmTransferEvent) {
			continue
		}
		from, err := evmTopicAddress(topics[1].String())
		if err != nil {
			continue
		}
		recv, err := evmTopicAddress(topics[2].String())
		if err != nil {
			continue
		}
		amount, err := parseHexBigInt(event.Get("data").String())
		if err != nil || amount.Sign() <= 0 {
			continue
		}
		transfers = append(transfers, transfer{
			Network:     string(trade.Network),
			TxHash:      txID,
			Amount:      decimal.NewFromBigInt(amount, trade.Decimal),
			FromAddress: from,
			RecvAddress: recv,
			Timestamp:   timestamp,
			TradeType:   order.TradeType,
			BlockNum:    int(blockNum),
		})
	}

	return findManualOrderTransfer(order, transfers)
}

func manualJSONRPCCall(ctx context.Context, client *http.Client, endpoint, method string, params any) (gjson.Result, error) {
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return gjson.Result{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return gjson.Result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return gjson.Result{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return gjson.Result{}, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return gjson.Result{}, fmt.Errorf("RPC HTTP %d", resp.StatusCode)
	}
	if !gjson.ValidBytes(body) {
		return gjson.Result{}, errors.New("RPC returned invalid JSON")
	}
	data := gjson.ParseBytes(body)
	if rpcErr := data.Get("error"); rpcErr.Exists() {
		return gjson.Result{}, fmt.Errorf("RPC error: %s", rpcErr.Raw)
	}
	result := data.Get("result")
	if !result.Exists() || result.Type == gjson.Null {
		return gjson.Result{}, errors.New("transaction not found")
	}
	return result, nil
}

func parseHexInt(value string) (int64, error) {
	n, err := parseHexBigInt(value)
	if err != nil || !n.IsInt64() {
		return 0, errors.New("invalid hexadecimal integer")
	}
	return n.Int64(), nil
}

func parseHexBigInt(value string) (*big.Int, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(strings.TrimPrefix(value, "0x"), "0X")
	if value == "" {
		return nil, errors.New("empty hexadecimal integer")
	}
	n, ok := new(big.Int).SetString(value, 16)
	if !ok {
		return nil, errors.New("invalid hexadecimal integer")
	}
	return n, nil
}

func evmTopicAddress(topic string) (string, error) {
	topic = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(topic), "0x"), "0X")
	if len(topic) != 64 {
		return "", errors.New("invalid EVM address topic")
	}
	if _, err := hex.DecodeString(topic); err != nil {
		return "", err
	}
	return "0x" + strings.ToLower(topic[24:]), nil
}

func verifyManualTronPayment(ctx context.Context, order *model.Order, input string) (transfer, error) {
	txID, err := normalizeHexTransactionID(input, false)
	if err != nil {
		return transfer{}, err
	}
	id, _ := hex.DecodeString(txID)
	conn, err := tr.client()
	if err != nil {
		return transfer{}, fmt.Errorf("connect TRON RPC: %w", err)
	}
	client := api.NewWalletClient(conn)
	tx, err := client.GetTransactionById(ctx, &api.BytesMessage{Value: id})
	if err != nil {
		return transfer{}, fmt.Errorf("fetch TRON transaction: %w", err)
	}
	if tx == nil || tx.GetRawData() == nil || len(tx.GetRawData().GetContract()) == 0 {
		return transfer{}, errors.New("transaction not found")
	}
	if ret := tx.GetRet(); len(ret) > 0 && ret[0].GetContractRet() != core.Transaction_Result_SUCCESS {
		return transfer{}, errors.New("transaction is not successful")
	}
	info, err := client.GetTransactionInfoById(ctx, &api.BytesMessage{Value: id})
	if err != nil {
		return transfer{}, fmt.Errorf("fetch TRON transaction info: %w", err)
	}
	if info == nil || info.GetBlockNumber() <= 0 || info.GetBlockTimeStamp() <= 0 {
		return transfer{}, errors.New("transaction is not confirmed")
	}
	if info.GetReceipt() != nil && info.GetReceipt().GetResult() != core.Transaction_Result_SUCCESS {
		return transfer{}, errors.New("transaction is not successful")
	}
	timestamp := time.UnixMilli(info.GetBlockTimeStamp())
	transfers := make([]transfer, 0)
	for _, contract := range tx.GetRawData().GetContract() {
		transfers = append(transfers, tr.parseContractTransfers(contract, txID, int(info.GetBlockNumber()), timestamp)...)
	}
	return findManualOrderTransfer(order, transfers)
}

func verifyManualSolanaPayment(ctx context.Context, order *model.Order, input string) (transfer, error) {
	signature, err := canonicalManualTransactionID(conf.Solana, input)
	if err != nil {
		return transfer{}, err
	}
	result, err := manualJSONRPCCall(ctx, sol.client, model.Endpoint(conf.Solana), "getTransaction", []any{
		signature,
		map[string]any{"encoding": "json", "commitment": "finalized", "maxSupportedTransactionVersion": 0},
	})
	if err != nil {
		return transfer{}, fmt.Errorf("fetch Solana transaction: %w", err)
	}
	if got := result.Get("transaction.signatures.0").String(); got != signature {
		return transfer{}, errors.New("transaction signature mismatch")
	}
	if txErr := result.Get("meta.err"); txErr.Exists() && txErr.Type != gjson.Null {
		return transfer{}, errors.New("transaction is not successful")
	}
	slot := int(result.Get("slot").Int())
	blockTime := result.Get("blockTime").Int()
	if slot <= 0 || blockTime <= 0 {
		return transfer{}, errors.New("transaction has no valid slot or block time")
	}
	transfers := sol.parseTransaction(result, slot, time.Unix(blockTime, 0))
	return findManualOrderTransfer(order, transfers)
}

func verifyManualAptosPayment(ctx context.Context, order *model.Order, input string) (transfer, error) {
	txID, err := normalizeHexTransactionID(input, true)
	if err != nil {
		return transfer{}, err
	}
	endpoint := strings.TrimRight(model.Endpoint(conf.Aptos), "/") + "/v1/transactions/by_hash/" + txID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return transfer{}, err
	}
	resp, err := apt.client.Do(req)
	if err != nil {
		return transfer{}, fmt.Errorf("fetch Aptos transaction: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return transfer{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return transfer{}, fmt.Errorf("fetch Aptos transaction: HTTP %d", resp.StatusCode)
	}
	if !gjson.ValidBytes(body) {
		return transfer{}, errors.New("Aptos RPC returned invalid JSON")
	}
	data := gjson.ParseBytes(body)
	if !strings.EqualFold(data.Get("hash").String(), txID) {
		return transfer{}, errors.New("transaction hash mismatch")
	}
	if !data.Get("success").Bool() || data.Get("vm_status").String() != "Executed successfully" {
		return transfer{}, errors.New("transaction is not successful")
	}
	transfers := apt.parseTransactions(gjson.Parse("[" + string(body) + "]"))
	return findManualOrderTransfer(order, transfers)
}

type manualTonTxRef struct {
	LT      uint64
	HashHex string
	HasLT   bool
}

func parseManualTonTxRef(input string) (manualTonTxRef, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return manualTonTxRef{}, errors.New("transaction id is required")
	}
	if idx := strings.Index(raw, ":"); idx > 0 {
		lt, ok := new(big.Int).SetString(strings.TrimSpace(raw[:idx]), 10)
		if !ok || lt.Sign() <= 0 || !lt.IsUint64() {
			return manualTonTxRef{}, errors.New("invalid TON transaction logical time")
		}
		hash, err := normalizeHexTransactionID(raw[idx+1:], false)
		if err != nil {
			return manualTonTxRef{}, err
		}
		return manualTonTxRef{LT: lt.Uint64(), HashHex: hash, HasLT: true}, nil
	}
	hash, err := normalizeHexTransactionID(raw, false)
	if err != nil {
		return manualTonTxRef{}, err
	}
	return manualTonTxRef{HashHex: hash}, nil
}

func verifyManualTonPayment(ctx context.Context, order *model.Order, input string) (transfer, error) {
	ref, err := parseManualTonTxRef(input)
	if err != nil {
		return transfer{}, err
	}
	queryAddress := orderMatchAddress(*order)
	account, err := address.ParseAddr(queryAddress)
	if err != nil {
		return transfer{}, fmt.Errorf("invalid TON order address: %w", err)
	}
	master, err := tn.client().CurrentMasterchainInfo(ctx)
	if err != nil {
		return transfer{}, fmt.Errorf("fetch TON masterchain: %w", err)
	}
	waiter := tn.client().WaitForBlock(master.SeqNo)
	return verifyManualTonTransactions(ctx, waiter, master, account, order, ref)
}

func verifyManualTonTransactions(ctx context.Context, waiter tgo.APIClientWrapped, master *tgo.BlockIDExt, account *address.Address, order *model.Order, ref manualTonTxRef) (transfer, error) {
	var txs []*tlb.Transaction
	var err error
	if ref.HasLT {
		hash, _ := hex.DecodeString(ref.HashHex)
		txs, err = waiter.ListTransactions(ctx, account, 1, ref.LT, hash)
	} else {
		state, stateErr := waiter.GetAccount(ctx, master, account)
		if stateErr != nil {
			return transfer{}, fmt.Errorf("fetch TON account state: %w", stateErr)
		}
		if state == nil || state.LastTxLT == 0 || len(state.LastTxHash) == 0 {
			return transfer{}, errors.New("transaction not found")
		}
		txs, err = waiter.ListTransactions(ctx, account, 100, state.LastTxLT, state.LastTxHash)
	}
	if err != nil {
		return transfer{}, fmt.Errorf("list TON transactions: %w", err)
	}

	matches := make([]transfer, 0, 1)
	shard := &tgo.BlockIDExt{Workchain: account.Workchain()}
	for _, tx := range txs {
		if tx == nil || !strings.EqualFold(hex.EncodeToString(tx.Hash), ref.HashHex) {
			continue
		}
		if ref.HasLT && tx.LT != ref.LT {
			continue
		}
		var trans transfer
		var ok bool
		if order.TradeType == model.TonGram {
			trans, ok = tn.parseTonTransfer(tx, master.SeqNo)
		} else if order.TradeType == model.UsdtTon {
			trans, ok = tn.parseInternalTransfer(shard, tx, master.SeqNo)
		}
		if !ok {
			continue
		}
		if orderTransferMatch(*order, trans) {
			matches = append(matches, trans)
		}
	}
	if len(matches) == 0 {
		return transfer{}, ErrManualPaymentMismatch
	}
	if len(matches) > 1 && !ref.HasLT {
		return transfer{}, errors.New("TON transaction hash is ambiguous; submit lt:hash")
	}
	matches[0].TxHash = ref.HashHex
	return matches[0], nil
}
