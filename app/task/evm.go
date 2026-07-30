package task

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/panjf2000/ants/v2"
	"github.com/shopspring/decimal"
	"github.com/smallnest/chanx"
	"github.com/spf13/cast"
	"github.com/tidwall/gjson"
	"github.com/v03413/bepusdt/app/conf"
	blockapi "github.com/v03413/bepusdt/app/core"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
)

const (
	blockParseMaxNum = 10 // 每次解析区块的最大数量
	evmTransferEvent = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
)

var chainBlockNum sync.Map

type block struct {
	RollDelayOffset int64 // 延迟偏移量，某些RPC节点如果不延迟，会报错 block is out of range，目前发现 https://rpc.xlayer.tech/ 存在此问题
	ConfirmedOffset int   // 确认偏移量，开启交易确认后，区块高度需要减去此值认为交易已确认
}

type evmNative struct {
	Parse     bool
	Decimal   int32
	TradeType model.TradeType
}

type evm struct {
	Network          string
	Block            block
	Native           evmNative
	Client           *http.Client
	blockScanQueue   *chanx.UnboundedChan[evmBlock]
	LookbackInterval time.Duration // 回溯时每批入队的间隔，控制 RPC 调用速率；默认 500ms
}

type evmBlock struct {
	From int64
	To   int64
}

func parseEVMQuantity(value string) (int64, error) {
	if len(value) < 3 || !strings.EqualFold(value[:2], "0x") {
		return 0, fmt.Errorf("invalid EVM quantity %q", value)
	}

	n, ok := new(big.Int).SetString(value[2:], 16)
	if !ok || n.Sign() < 0 || !n.IsInt64() {
		return 0, fmt.Errorf("invalid EVM quantity %q", value)
	}

	return n.Int64(), nil
}

func parseEVMTopicAddress(value string) (string, error) {
	if len(value) != 66 || !strings.EqualFold(value[:2], "0x") {
		return "", fmt.Errorf("invalid EVM address topic %q", value)
	}
	if _, ok := new(big.Int).SetString(value[2:], 16); !ok {
		return "", fmt.Errorf("invalid EVM address topic %q", value)
	}

	return "0x" + strings.ToLower(value[26:]), nil
}

func parseEVMAmount(value string) (*big.Int, error) {
	if len(value) != 66 || !strings.EqualFold(value[:2], "0x") {
		return nil, fmt.Errorf("invalid EVM amount %q", value)
	}

	amount, ok := new(big.Int).SetString(value[2:], 16)
	if !ok || amount.Sign() <= 0 {
		return nil, fmt.Errorf("invalid EVM amount %q", value)
	}

	return amount, nil
}

func (e *evm) retryBlock(b evmBlock, reason string) {
	conf.RecordFailure(e.Network)
	e.blockScanQueue.In <- b
	log.Task.Warn(reason)
}

func (e *evm) syncBlocksForward(ctx context.Context) {
	if syncBreak(e.Network, e.blockScanQueue.Len()) {

		return
	}

	post := []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)
	req, err := http.NewRequestWithContext(ctx, "POST", e.rpcEndpoint(), bytes.NewBuffer(post))
	if err != nil {
		log.Task.Warn("Error creating request:", err)

		return
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := e.Client.Do(req)
	if err != nil {
		log.Task.Warn("Error sending request:", err)

		return
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Task.Warn("eth_blockNumber response status code:", resp.StatusCode)

		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Task.Warn("Error reading response body:", err)

		return
	}

	if !gjson.ValidBytes(body) {
		log.Task.Warn(fmt.Sprintf("EVM invalid JSON response (%s)", e.Network))

		return
	}

	var res = gjson.ParseBytes(body)
	if !res.IsObject() || (res.Get("error").Exists() && res.Get("error").Type != gjson.Null) {
		log.Task.Warn(fmt.Sprintf("EVM 数据解析错误(%s): %s", e.Network, string(body)))

		return
	}

	result := res.Get("result")
	nowValue, err := parseEVMQuantity(result.String())
	if !result.Exists() || result.Type != gjson.String || err != nil {
		log.Task.Warn(fmt.Sprintf("EVM invalid block number response (%s): %s", e.Network, string(body)))

		return
	}
	var now = nowValue - e.Block.RollDelayOffset
	if now <= 0 {

		return
	}

	var lastBlockNumber int64
	if v, ok := chainBlockNum.Load(e.Network); ok {
		lastBlockNumber = v.(int64)
	}

	if now-lastBlockNumber > cast.ToInt64(model.GetC(model.BlockHeightMaxDiff)) {

		lastBlockNumber = now - 1
	}

	chainBlockNum.Store(e.Network, now)
	if now <= lastBlockNumber {

		return
	}

	for from := lastBlockNumber + 1; from <= now; from += blockParseMaxNum {
		to := from + blockParseMaxNum - 1
		if to > now {
			to = now
		}

		e.blockScanQueue.In <- evmBlock{From: from, To: to}
	}
}

func (e *evm) lookbackBlocks(ctx context.Context) {
	if syncBreak(e.Network, e.blockScanQueue.Len()) {
		return
	}

	window, ok, err := getLookbackWindow(model.Network(e.Network))
	if err != nil {
		log.Task.Warn(e.Network, " lookback order query failed:", err)
		return
	}
	if !ok {
		return
	}

	interval := e.LookbackInterval
	if interval <= 0 {
		interval = time.Millisecond * 300
	}

	start, end, err := blockapi.New().GetBoundaryHeights(window.startAt, window.endAt, e.Network)
	if err != nil {
		log.Task.Warn(e.Network, " lookback boundary query failed:", err)
		return
	}
	for i := start; i <= end; i += blockParseMaxNum {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if syncBreak(e.Network, e.blockScanQueue.Len()) {
			return
		}
		to := i + blockParseMaxNum - 1
		if to > end {
			to = end
		}
		e.blockScanQueue.In <- evmBlock{From: i, To: to}
		time.Sleep(interval)
	}
	markLookbackDone(window)
}

func (e *evm) blockDispatch(ctx context.Context) {
	p, err := ants.NewPoolWithFunc(3, e.getBlockByNumber)
	if err != nil {
		log.Task.Warn("Error creating pool:", err)

		return
	}

	defer p.Release()

	for {
		select {
		case <-ctx.Done():
			return
		case n := <-e.blockScanQueue.Out:
			if err := p.Invoke(n); err != nil {
				e.blockScanQueue.In <- n

				log.Task.Warn("Evm Block Dispatch Error invoking process block:", err)
			}
		}
	}
}

func (e *evm) getBlockByNumber(a any) {
	b, ok := a.(evmBlock)
	if !ok {
		log.Task.Warn("Evm Block Parse Error: expected []int64, got", a)

		return
	}

	items := make([]string, 0)
	for i := b.From; i <= b.To; i++ {
		items = append(items, fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_getBlockByNumber","params":["0x%x",%t],"id":%d}`, i, e.Native.Parse, i))
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", e.rpcEndpoint(), bytes.NewBuffer([]byte(fmt.Sprintf(`[%s]`, strings.Join(items, ",")))))
	if err != nil {
		e.retryBlock(b, "eth_getBlockByNumber Error creating request")
		log.Task.Warn("Error creating request:", err)

		return
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := e.Client.Do(req)
	if err != nil {
		conf.RecordFailure(e.Network)
		e.blockScanQueue.In <- b
		log.Task.Warn("eth_getBlockByNumber Error sending request:", err)

		return
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		e.retryBlock(b, fmt.Sprintf("%s eth_getBlockByNumber response status code: %d", e.Network, resp.StatusCode))

		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		e.retryBlock(b, "eth_getBlockByNumber Error reading response body")
		log.Task.Warn("eth_getBlockByNumber Error reading response body:", err)

		return
	}

	nativeTransfers := make([]transfer, 0)
	blockTimestamp := make(map[int64]time.Time)
	if !gjson.ValidBytes(body) {
		e.retryBlock(b, fmt.Sprintf("%s eth_getBlockByNumber returned invalid JSON", e.Network))

		return
	}
	responses := gjson.ParseBytes(body)
	if !responses.IsArray() {
		e.retryBlock(b, fmt.Sprintf("%s eth_getBlockByNumber response is not an array", e.Network))

		return
	}
	expected := int(b.To - b.From + 1)
	if len(responses.Array()) != expected {
		e.retryBlock(b, fmt.Sprintf("%s eth_getBlockByNumber returned %d results, want %d", e.Network, len(responses.Array()), expected))

		return
	}
	seenBlocks := make(map[int64]struct{}, expected)
	for _, itm := range responses.Array() {
		if !itm.IsObject() {
			e.retryBlock(b, fmt.Sprintf("%s eth_getBlockByNumber returned a non-object result", e.Network))

			return
		}
		rpcError := itm.Get("error")
		if rpcError.Exists() && rpcError.Type != gjson.Null {
			e.retryBlock(b, fmt.Sprintf("%s eth_getBlockByNumber response error %s", e.Network, rpcError.String()))

			return
		}
		result := itm.Get("result")
		if !result.IsObject() {
			e.retryBlock(b, fmt.Sprintf("%s eth_getBlockByNumber returned an invalid block", e.Network))

			return
		}
		blockNum, err := parseEVMQuantity(result.Get("number").String())
		if err != nil || blockNum < b.From || blockNum > b.To {
			e.retryBlock(b, fmt.Sprintf("%s eth_getBlockByNumber returned an invalid block number", e.Network))

			return
		}
		if _, exists := seenBlocks[blockNum]; exists {
			e.retryBlock(b, fmt.Sprintf("%s eth_getBlockByNumber returned a duplicate block", e.Network))

			return
		}
		seenBlocks[blockNum] = struct{}{}
		timestamp, err := parseEVMQuantity(result.Get("timestamp").String())
		if err != nil {
			e.retryBlock(b, fmt.Sprintf("%s eth_getBlockByNumber returned an invalid timestamp", e.Network))

			return
		}
		blockTime := time.Unix(timestamp, 0)
		blockTimestamp[blockNum] = blockTime

		array := result.Get("transactions")
		if e.Native.Parse && !array.IsArray() {
			e.retryBlock(b, fmt.Sprintf("%s eth_getBlockByNumber returned invalid transactions", e.Network))

			return
		}
		if e.Native.Parse && len(array.Array()) != 0 {
			nativeTransfers = append(nativeTransfers, e.parseNativeTransfer(array.Array(), int(blockNum), blockTime)...)
		}
	}
	if len(seenBlocks) != expected {
		e.retryBlock(b, fmt.Sprintf("%s eth_getBlockByNumber returned incomplete blocks", e.Network))

		return
	}

	transfers, err := e.parseEventTransfer(b, blockTimestamp)
	if err != nil {
		e.retryBlock(b, "Evm Block Parse Error parsing block transfer")
		log.Task.Warn("Evm Block Parse Error parsing block transfer:", err)

		return
	}
	conf.RecordSuccess(e.Network, cast.ToString(b.To))

	if len(nativeTransfers) > 0 {
		transferQueue.In <- nativeTransfers
	}
	if len(transfers) > 0 {
		transferQueue.In <- transfers
	}

	log.Task.Info(fmt.Sprintf("区块扫描完成(%s): %d → %d 成功率：%s", e.Network, b.From, b.To, conf.GetSuccessRate(e.Network)))
}

func (e *evm) parseNativeTransfer(array []gjson.Result, num int, timestamp time.Time) []transfer {
	nativeTransfers := make([]transfer, 0)
	for _, tx := range array {
		if tx.Get("input").String() != "0x" {
			// 非原生币交易

			continue
		}

		valStr := tx.Get("value").String()
		if valStr == "0x0" || len(valStr) < 3 {
			// 过滤 0 值交易

			continue
		}

		amount, ok := big.NewInt(0).SetString(valStr[2:], 16)
		if !ok || amount.Sign() <= 0 {

			continue
		}

		toAddress := tx.Get("to").String()
		if toAddress == "" { // 合约创建交易 to 为空

			continue
		}

		nativeTransfers = append(nativeTransfers, transfer{
			Network:     e.Network,
			FromAddress: tx.Get("from").String(),
			RecvAddress: toAddress,
			Amount:      decimal.NewFromBigInt(amount, e.Native.Decimal),
			TxHash:      tx.Get("hash").String(),
			BlockNum:    num,
			Timestamp:   timestamp,
			TradeType:   e.Native.TradeType,
		})
	}

	return nativeTransfers
}

func (e *evm) parseEventTransfer(b evmBlock, timestamp map[int64]time.Time) ([]transfer, error) {
	transfers := make([]transfer, 0)
	post := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_getLogs","params":[{"fromBlock":"0x%x","toBlock":"0x%x","topics":["%s"]}],"id":1}`, b.From, b.To, evmTransferEvent))
	resp, err := e.Client.Post(e.rpcEndpoint(), "application/json", bytes.NewBuffer(post))
	if err != nil {
		return transfers, fmt.Errorf("eth_getLogs post: %w", err)
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return transfers, fmt.Errorf("%s eth_getLogs response status code: %d", e.Network, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return transfers, fmt.Errorf("eth_getLogs read response: %w", err)
	}

	if !gjson.ValidBytes(body) {
		return transfers, errors.New("eth_getLogs returned invalid JSON")
	}
	data := gjson.ParseBytes(body)
	if !data.IsObject() {
		return transfers, errors.New("eth_getLogs response is not an object")
	}
	rpcError := data.Get("error")
	if rpcError.Exists() && rpcError.Type != gjson.Null {
		return transfers, fmt.Errorf("%s eth_getLogs response error %s", e.Network, rpcError.String())
	}
	result := data.Get("result")
	if !result.IsArray() {
		return transfers, errors.New("eth_getLogs result is not an array")
	}

	for _, itm := range result.Array() {
		if !itm.IsObject() {
			return transfers, errors.New("eth_getLogs contains a non-object log")
		}
		to := itm.Get("address").String()
		tradeType, ok := model.GetContractTrade(to)
		if !ok {

			continue
		}

		topics := itm.Get("topics").Array()
		if len(topics) < 3 {
			return transfers, errors.New("eth_getLogs transfer log has fewer than three topics")
		}

		if !strings.EqualFold(topics[0].String(), evmTransferEvent) { // transfer event signature

			continue
		}

		from, err := parseEVMTopicAddress(topics[1].String())
		if err != nil {
			return transfers, err
		}
		recv, err := parseEVMTopicAddress(topics[2].String())
		if err != nil {
			return transfers, err
		}
		amount, err := parseEVMAmount(itm.Get("data").String())
		if err != nil {
			return transfers, err
		}
		blockNumber, err := parseEVMQuantity(itm.Get("blockNumber").String())
		if err != nil {
			return transfers, err
		}
		blockTime, ok := timestamp[blockNumber]
		if !ok {
			return transfers, fmt.Errorf("missing timestamp for EVM block %d", blockNumber)
		}
		txHash := itm.Get("transactionHash").String()
		if txHash == "" {
			return transfers, errors.New("eth_getLogs transfer log has no transaction hash")
		}

		transfers = append(transfers, transfer{
			Network:     e.Network,
			FromAddress: from,
			RecvAddress: recv,
			Amount:      decimal.NewFromBigInt(amount, model.GetContractDecimal(to)),
			TxHash:      txHash,
			BlockNum:    int(blockNumber),
			Timestamp:   blockTime,
			TradeType:   tradeType,
		})
	}

	return transfers, nil
}

func (e *evm) tradeConfirmHandle(ctx context.Context) {
	var orders = getConfirmingOrders(model.GetNetworkTrades(model.Network(e.Network)))

	var handle = func(o model.Order) {
		if model.GetC(model.BlockOffsetConfirm) == "1" {
			last, ok := chainBlockNum.Load(e.Network)
			if !ok {
				return
			}
			if cast.ToInt(last)-o.RefBlockNum < e.Block.ConfirmedOffset {
				return
			}
		}

		post := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":"eth_getTransactionReceipt","params":["%s"],"id":1}`, o.RefHash))
		req, err := http.NewRequestWithContext(ctx, "POST", e.rpcEndpoint(), bytes.NewBuffer(post))
		if err != nil {
			log.Task.Warn("evm tradeConfirmHandle Error creating request:", err)

			return
		}

		req.Header.Set("Content-Type", "application/json")
		resp, err := e.Client.Do(req)
		if err != nil {
			log.Task.Warn("evm tradeConfirmHandle Error sending request:", err)

			return
		}

		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Task.Warn("evm tradeConfirmHandle Error response status code:", resp.StatusCode)

			return
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Task.Warn("evm tradeConfirmHandle Error reading response body:", err)

			return
		}
		if !gjson.ValidBytes(body) {
			log.Task.Warn("evm tradeConfirmHandle Error: invalid JSON response")

			return
		}

		data := gjson.ParseBytes(body)
		if !data.IsObject() {
			log.Task.Warn("evm tradeConfirmHandle Error: response is not an object")

			return
		}
		rpcError := data.Get("error")
		if rpcError.Exists() && rpcError.Type != gjson.Null {
			log.Task.Warn(fmt.Sprintf("%s eth_getTransactionReceipt response error %s", e.Network, data.Get("error").String()))

			return
		}

		if data.Get("result.status").String() == "0x1" {
			markFinalConfirmed(o)
		}
	}

	runOrderConfirmations(ctx, orders, handle)
}

func (e *evm) rpcEndpoint() string {

	return model.Endpoint(model.Network(e.Network))
}

func syncBreak(network string, num int) bool {
	if num >= blockQueueLimit {
		log.Task.Warn(fmt.Sprintf("%s 同步阻塞，当前区块消费堆积数量：%d", network, num))

		return true
	}

	if mqttSubscribed(network) {
		return false
	}

	trades := model.GetNetworkTrades(model.Network(network))
	if len(trades) == 0 {

		return true
	}

	var count int64
	result := model.Db.Model(&model.Wallet{}).
		Where("other_notify = ? and trade_type in (?)", model.WaOtherEnable, trades).
		Count(&count)
	if result.Error != nil {
		log.Task.Warn(network, " wallet query failed:", result.Error)
		return false
	}
	if count > 0 {

		return false
	}

	return !hasLookbackOrders(trades)
}
