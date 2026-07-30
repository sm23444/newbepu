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

	s.heightMu.Lock()
	lastSlotNum := s.lastSlotNum
	if now-lastSlotNum > cast.ToInt(model.GetC(model.BlockHeightMaxDiff)) { // 区块高度变化过大，强制丢块重扫
		lastSlotNum = now
	}

	if now == lastSlotNum { // 区块高度没有变化
		s.heightMu.Unlock()

		return
	}
	s.lastSlotNum = now
	s.heightMu.Unlock()

	for n := lastSlotNum + 1; n <= now; n++ {
		// 待扫描区块入列

		s.slotQueue.In <- n
	}

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
		hash := trans.Get("transaction.signatures.0").String()

		// 解析账号索引
		accountKeys := make([]string, 0)
		for _, key := range trans.Get("transaction.message.accountKeys").Array() {
			accountKeys = append(accountKeys, key.String())
		}
		for _, v := range []string{"readonly", "writable"} {
			for _, key := range trans.Get("meta.loadedAddresses." + v).Array() {
				accountKeys = append(accountKeys, key.String())
			}
		}

		// 查找SPL Token索引
		splTokenIndex := int64(-1)
		for i, v := range accountKeys {
			if v == conf.SolSplToken {
				splTokenIndex = int64(i)

				break
			}
		}

		// SPL Token的Mint地址，即不包含 Token 交易信息
		if splTokenIndex == -1 {

			continue
		}

		// 解析 Token 账户 【Token Wallet => Owner Wallet】
		tokenAccountMap := make(map[string]solanaTokenOwner)
		for _, v := range []string{"postTokenBalances", "preTokenBalances"} {
			for _, itm := range trans.Get("meta." + v).Array() {
				tradeType, ok := model.GetContractTrade(itm.Get("mint").String())
				index := itm.Get("accountIndex").Int()
				if !ok || itm.Get("programId").String() != conf.SolSplToken || index < 0 || index >= int64(len(accountKeys)) {

					continue
				}

				tokenAccountMap[accountKeys[index]] = solanaTokenOwner{
					TradeType: tradeType,
					Address:   itm.Get("owner").String(),
				}
			}
		}

		transArr := make([]transfer, 0)

		// 解析外部指令
		for _, instr := range trans.Get("transaction.message.instructions").Array() {
			if instr.Get("programIdIndex").Int() != splTokenIndex {

				continue
			}

			transArr = append(transArr, s.parseTransfer(instr, accountKeys, tokenAccountMap))
		}

		// 解析内部指令
		for _, itm := range trans.Get("meta.innerInstructions").Array() {
			for _, instr := range itm.Get("instructions").Array() {
				if instr.Get("programIdIndex").Int() != splTokenIndex {

					continue
				}

				transArr = append(transArr, s.parseTransfer(instr, accountKeys, tokenAccountMap))
			}
		}

		// 过滤无关交易
		result := make([]transfer, 0)
		for _, t := range transArr {
			if t.FromAddress == "" || t.RecvAddress == "" || t.Amount.IsZero() {

				continue
			}

			t.TxHash = hash
			t.Network = conf.Solana
			t.BlockNum = slot
			t.Timestamp = timestamp

			result = append(result, t)
		}

		if len(result) > 0 {
			transferQueue.In <- result
		}
	}

	log.Task.Info(fmt.Sprintf("区块扫描完成(Solana) %d 成功率：%s", slot, conf.GetSuccessRate(network)))
}

func (s *solana) parseTransaction(trans gjson.Result, slot int, timestamp time.Time) []transfer {
	if txErr := trans.Get("meta.err"); txErr.Exists() && txErr.Type != gjson.Null {
		return nil
	}

	accountKeys := make([]string, 0)
	for _, key := range trans.Get("transaction.message.accountKeys").Array() {
		accountKeys = append(accountKeys, key.String())
	}
	for _, kind := range []string{"readonly", "writable"} {
		for _, key := range trans.Get("meta.loadedAddresses." + kind).Array() {
			accountKeys = append(accountKeys, key.String())
		}
	}

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

		if data.Get("result.value.0.confirmationStatus").String() == "finalized" {

			markFinalConfirmed(o)
		}
	}

	runOrderConfirmations(ctx, orders, handle)
}

func (s *solana) lookbackSlots(ctx context.Context) {
	if syncBreak(conf.Solana, s.slotQueue.Len()) {
		return
	}

	window, ok, err := getLookbackWindow(conf.Solana)
	if err != nil {
		log.Task.Warn("solana lookback order query failed:", err)
		return
	}
	if !ok {
		return
	}

	start, end, err := blockapi.New().GetBoundaryHeights(window.startAt, window.endAt, conf.Solana)
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
	markLookbackDone(window)
}
