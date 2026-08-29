package task

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/panjf2000/ants/v2"
	"github.com/shopspring/decimal"
	"github.com/smallnest/chanx"
	"github.com/spf13/cast"
	"github.com/tidwall/gjson"
	"github.com/v03413/bepusdt/app/conf"
	blockapi "github.com/v03413/bepusdt/app/core"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	tasknotify "github.com/v03413/bepusdt/app/task/notify"
	"github.com/v03413/bepusdt/app/utils"
)

// 参考文档
//  - https://solana.com/zh/docs/rpc
//  - https://github.com/solana-program/token/blob/6d18ff73b1dd30703a30b1ca941cb0f1d18c2b2a/program/src/instruction.rs

type solana struct {
	slotConfirmedOffset int
	lastSlotNum         int
	heightMu            sync.RWMutex
	slotQueue           *chanx.UnboundedChan[int]
	client              *http.Client
}

type solanaTokenOwner struct {
	TradeType model.TradeType
	Address   string
}

var sol solana

func init() {
	sol = newSolana()
	Register(Task{Callback: sol.slotDispatch})
	Register(Task{Callback: sol.syncSlotForward, Duration: time.Second * 5})
	Register(Task{Callback: sol.tradeConfirmHandle, Duration: time.Second * 5})
	Register(Task{Callback: sol.lookbackSlots, Duration: time.Second * 15})
}

func newSolana() solana {
	return solana{
		slotConfirmedOffset: 60,
		lastSlotNum:         0,
		slotQueue:           chanx.NewUnboundedChan[int](context.Background(), 30),
		client:              utils.NewHttpClient(),
	}
}

func (s *solana) syncSlotForward(ctx context.Context) {
	if syncBreak(conf.Solana, s.slotQueue.Len()) {

		return
	}

	req, _ := http.NewRequestWithContext(ctx, "POST", model.Endpoint(conf.Solana), bytes.NewBuffer([]byte(`{"jsonrpc":"2.0","id":1,"method":"getSlot"}`)))
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		log.Task.Warn("syncSlotForward Error sending request:", err)

		return
	}

	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Task.Warn("syncSlotForward Error response status code:", resp.StatusCode)

		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Task.Warn("syncSlotForward Error reading response body:", err)

		return
	}

	now := int(gjson.GetBytes(body, "result").Int())
	if now <= 0 {
		log.Task.Warn("syncSlotForward Error: invalid slot number:", now)

		return
	}

	start, end, ok := s.advanceSlotCursor(now, cast.ToInt(model.GetC(model.BlockHeightMaxDiff)))
	if !ok {
		return
	}

	for n := start; n <= end; n++ {
		// 待扫描区块入列

		s.slotQueue.In <- n
	}

}

func (s *solana) advanceSlotCursor(now, maxDiff int) (start, end int, ok bool) {
	s.heightMu.Lock()
	defer s.heightMu.Unlock()

	last := s.lastSlotNum
	if now <= last {
		return 0, 0, false
	}
	if now-last > maxDiff {
		s.lastSlotNum = now
		return 0, 0, false
	}

	s.lastSlotNum = now
	return last + 1, now, true
}

func (s *solana) slotDispatch(ctx context.Context) {
	p, err := ants.NewPoolWithFunc(3, s.slotParse)
	if err != nil {
		log.Task.Warn("Error creating pool:", err)

		return
	}

	defer p.Release()

	for {
		select {
		case slot := <-s.slotQueue.Out:
			if err := p.Invoke(slot); err != nil {
				s.slotQueue.In <- slot
				log.Task.Warn("slotDispatch Error invoking process slot:", err)
			}
		case <-ctx.Done():
			if err := ctx.Err(); err != nil {
				log.Task.Warn("slotDispatch context done:", err)
			}

			return
		}
	}
}

func (s *solana) slotParse(n any) {
	slot := n.(int)
	post := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"getBlock","params":[%d,{"encoding":"json","maxSupportedTransactionVersion":0,"transactionDetails":"full","rewards":false}]}`, slot))
	network := conf.Solana

	resp, err := s.client.Post(model.Endpoint(conf.Solana), "application/json", bytes.NewBuffer(post))
	if err != nil {
		conf.RecordFailure(network)
		s.slotQueue.In <- slot
		log.Task.Warn("slotParse Error sending request:", err)

		return
	}

	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		conf.RecordFailure(network)
		s.slotQueue.In <- slot
		log.Task.Warn("slotParse Error response status code:", resp.StatusCode)

		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		conf.RecordFailure(network)
		s.slotQueue.In <- slot
		log.Task.Warn("slotParse Error reading response body:", err)

		return
	}
	if !gjson.ValidBytes(body) {
		conf.RecordFailure(network)
		s.slotQueue.In <- slot
		log.Task.Warn("slotParse Error: invalid JSON response body")

		return
	}
	data := gjson.ParseBytes(body)
	if !data.IsObject() || !data.Get("result").Exists() || (data.Get("error").Exists() && data.Get("error").Type != gjson.Null) {
		conf.RecordFailure(network)
		s.slotQueue.In <- slot
		log.Task.Warn("slotParse Error: invalid JSON-RPC response")

		return
	}
	if data.Get("result").Type != gjson.Null && !data.Get("result").IsObject() {
		conf.RecordFailure(network)
		s.slotQueue.In <- slot
		log.Task.Warn("slotParse Error: invalid getBlock result")

		return
	}
	conf.RecordSuccess(network, cast.ToString(slot))

	timestamp := time.Unix(data.Get("result.blockTime").Int(), 0)

	for _, trans := range data.Get("result.transactions").Array() {
		result := s.parseTransaction(trans, slot, timestamp)
		if len(result) > 0 {
			transferQueue.In <- result
		}
	}

	log.Task.Info(fmt.Sprintf("区块扫描完成(Solana) %d 成功率：%s", slot, conf.GetSuccessRate(network)))
}

func (s *solana) parseTransaction(trans gjson.Result, slot int, timestamp time.Time) []transfer {
	if !solanaTransactionSucceeded(trans) {
		return nil
	}

	accountKeys := solanaAccountKeys(trans)

	splTokenIndex := int64(-1)
	for i, key := range accountKeys {
		if key == conf.SolSplToken {
			splTokenIndex = int64(i)
			break
		}
	}
	if splTokenIndex == -1 {
		return nil
	}

	tokenAccountMap := make(map[string]solanaTokenOwner)
	for _, kind := range []string{"postTokenBalances", "preTokenBalances"} {
		for _, item := range trans.Get("meta." + kind).Array() {
			tradeType, ok := model.GetContractTrade(item.Get("mint").String())
			index := item.Get("accountIndex").Int()
			if !ok || item.Get("programId").String() != conf.SolSplToken || index < 0 || index >= int64(len(accountKeys)) {
				continue
			}
			tokenAccountMap[accountKeys[index]] = solanaTokenOwner{
				TradeType: tradeType,
				Address:   item.Get("owner").String(),
			}
		}
	}

	parsed := make([]transfer, 0)
	for _, instruction := range trans.Get("transaction.message.instructions").Array() {
		if instruction.Get("programIdIndex").Int() == splTokenIndex {
			parsed = append(parsed, s.parseTransfer(instruction, accountKeys, tokenAccountMap))
		}
	}
	for _, inner := range trans.Get("meta.innerInstructions").Array() {
		for _, instruction := range inner.Get("instructions").Array() {
			if instruction.Get("programIdIndex").Int() == splTokenIndex {
				parsed = append(parsed, s.parseTransfer(instruction, accountKeys, tokenAccountMap))
			}
		}
	}

	hash := trans.Get("transaction.signatures.0").String()
	result := make([]transfer, 0, len(parsed))
	for _, item := range parsed {
		if item.FromAddress == "" || item.RecvAddress == "" || item.Amount.IsZero() {
			continue
		}
		item.TxHash = hash
		item.Network = conf.Solana
		item.BlockNum = slot
		item.Timestamp = timestamp
		result = append(result, item)
	}
	return result
}

// Versioned Solana transactions index static keys first, then ALT writable and readonly keys.
func solanaAccountKeys(trans gjson.Result) []string {
	keys := make([]string, 0)
	for _, key := range trans.Get("transaction.message.accountKeys").Array() {
		keys = append(keys, key.String())
	}
	for _, kind := range []string{"writable", "readonly"} {
		for _, key := range trans.Get("meta.loadedAddresses." + kind).Array() {
			keys = append(keys, key.String())
		}
	}

	return keys
}

func solanaTransactionSucceeded(trans gjson.Result) bool {
	txErr := trans.Get("meta.err")
	return txErr.Exists() && txErr.Type == gjson.Null
}

func solanaSignatureStatus(status gjson.Result) (finalized, failed bool) {
	if !status.Exists() || status.Type == gjson.Null {
		return false, false
	}
	txErr := status.Get("err")
	if !txErr.Exists() {
		return false, false
	}
	if txErr.Type != gjson.Null {
		return false, true
	}

	return status.Get("confirmationStatus").String() == "finalized", false
}

func (s *solana) parseTransfer(instr gjson.Result, accountKeys []string, tokenAccountMap map[string]solanaTokenOwner) transfer {
	accounts := instr.Get("accounts").Array()
	trans := transfer{}
	if len(accounts) < 3 { // from to singer，至少存在3个账户索引，如果是多签则 > 3

		return trans
	}

	data := base58.Decode(instr.Get("data").String())
	dLen := len(data)
	if dLen < 9 {

		return trans
	}

	isTransfer := data[0] == 3 && dLen == 9
	isTransferChecked := data[0] == 12 && dLen == 10
	if !isTransfer && !isTransferChecked {

		return trans
	}

	var exp int32 = -6
	if isTransferChecked {
		exp = int32(data[9]) * -1
	}

	keyAt := func(index int64) (string, bool) {
		if index < 0 || index >= int64(len(accountKeys)) {
			return "", false
		}
		return accountKeys[index], true
	}

	fromKey, ok := keyAt(accounts[0].Int())
	if !ok {
		return trans
	}
	from, ok := tokenAccountMap[fromKey]
	if !ok {

		return trans
	}

	trans.FromAddress = from.Address
	recvAccount := accounts[1].Int()
	if isTransferChecked {
		recvAccount = accounts[2].Int()
	}
	recvKey, ok := keyAt(recvAccount)
	if !ok {
		return transfer{}
	}
	recv, ok := tokenAccountMap[recvKey]
	if !ok {
		return transfer{}
	}
	trans.RecvAddress = recv.Address

	buf := make([]byte, 8)
	copy(buf[:], data[1:9])
	number := binary.LittleEndian.Uint64(buf)
	b := new(big.Int)
	b.SetUint64(number)
	trans.TradeType = from.TradeType
	trans.Amount = decimal.NewFromBigInt(b, exp)

	return trans
}

func (s *solana) tradeConfirmHandle(ctx context.Context) {
	var orders = getConfirmingOrders(model.GetNetworkTrades(conf.Solana))

	var handle = func(o model.Order) {
		if model.GetC(model.BlockOffsetConfirm) == "1" {
			s.heightMu.RLock()
			lastSlotNum := s.lastSlotNum
			s.heightMu.RUnlock()
			if lastSlotNum == 0 {
				return
			}
			if lastSlotNum-o.RefBlockNum < s.slotConfirmedOffset {
				return
			}
		}

		post := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"getSignatureStatuses","params":[["%s"],{"searchTransactionHistory":true}]}`, o.RefHash))
		req, _ := http.NewRequestWithContext(ctx, "POST", model.Endpoint(conf.Solana), bytes.NewBuffer(post))
		resp, err := s.client.Do(req)
		if err != nil {
			log.Task.Warn("solana tradeConfirmHandle Error sending request:", err)

			return
		}

		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			log.Task.Warn("solana tradeConfirmHandle Error response status code:", resp.StatusCode)

			return
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Task.Warn("solana tradeConfirmHandle Error reading response body:", err)

			return
		}

		data := gjson.ParseBytes(body)
		if data.Get("error").Exists() {
			log.Task.Warn("solana tradeConfirmHandle Error:", data.Get("error").String())

			return
		}

		finalized, failed := solanaSignatureStatus(data.Get("result.value.0"))
		if failed {
			if err := o.SetFailed(); err != nil {
				log.Task.Warn("solana tradeConfirmHandle SetFailed failed:", err)

				return
			}
			tasknotify.Bepusdt(o)

			return
		}
		if finalized {
			markFinalConfirmed(o)
		}
	}

	runOrderConfirmations(ctx, orders, handle)
}

func (s *solana) lookbackSlots(ctx context.Context) {
	if syncBreak(conf.Solana, s.slotQueue.Len()) {
		return
	}

	startAt, endAt, ok := getLookbackUnix(conf.Solana)
	if !ok {
		return
	}

	start, end, err := blockapi.New().GetBoundaryHeights(startAt, endAt, conf.Solana)
	if err != nil {
		log.Task.Warn("solana lookback boundary query failed:", err)
		return
	}
	for i := int(start); i <= int(end); i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if syncBreak(conf.Solana, s.slotQueue.Len()) {
			return
		}
		s.slotQueue.In <- i
		time.Sleep(time.Millisecond * 200)
	}
}
