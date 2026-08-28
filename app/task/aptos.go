package task

import (
	"context"
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
	"github.com/v03413/bepusdt/app/utils"
)

type aptos struct {
	versionChunkSize       int
	versionConfirmedOffset int
	lastVersion            int
	heightMu               sync.RWMutex
	versionQueue           *chanx.UnboundedChan[version]
	client                 *http.Client
	retryMu                sync.Mutex
	retryAttempts          map[version]int
	progressMu             sync.Mutex
	forwardProgress        *scanProgress
}

const aptosVersionRetryMaxAttempts = 5

type version struct {
	Start   int
	Limit   int
	Forward bool
}

var apt aptos

type aptEvent struct {
	Type    string
	Action  string
	Amount  decimal.Decimal
	Address string
}

type aptAmount struct {
	Amount string
	Type   model.TradeType
}

func init() {
	apt = newAptos()
	Register(Task{Callback: apt.versionDispatch})
	Register(Task{Callback: apt.syncVersionForward, Duration: time.Second * 3})
	Register(Task{Callback: apt.tradeConfirmHandle, Duration: time.Second * 5})
	Register(Task{Callback: apt.lookbackVersion, Duration: time.Second * 15})
}

func newAptos() aptos {
	return aptos{
		versionChunkSize:       100,
		versionConfirmedOffset: 1000,
		lastVersion:            0,
		versionQueue:           chanx.NewUnboundedChan[version](context.Background(), 30),
		client:                 utils.NewHttpClient(),
		retryAttempts:          make(map[version]int),
	}
}

func (a *aptos) syncVersionForward(ctx context.Context) {
	if syncBreak(conf.Aptos, a.versionQueue.Len()) {

		return
	}

	req, _ := http.NewRequestWithContext(ctx, "GET", model.Endpoint(conf.Aptos)+"/v1", nil)
	resp, err := a.client.Do(req)
	if err != nil {
		log.Task.Warn("aptos syncVersionForward Error sending request:", err)

		return
	}

	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Task.Warn("aptos syncVersionForward Error response status code:", resp.StatusCode)

		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Task.Warn("aptos syncVersionForward Error reading response body:", err)

		return
	}

	now := int(gjson.GetBytes(body, "ledger_version").Int())
	if now <= 0 {
		log.Task.Warn("syncVersionForward Error: invalid ledger_version:", now)

		return
	}

	a.heightMu.Lock()
	a.lastVersion = now
	a.heightMu.Unlock()
	available := blockQueueLimit - a.versionQueue.Len()
	ranges, err := a.getForwardProgress().schedule(int64(now), int64(a.versionChunkSize), available)
	if err != nil {
		log.Task.Warn("Aptos load scan cursor failed:", err)
		return
	}
	for _, item := range ranges {
		a.versionQueue.In <- version{
			Start:   int(item.From),
			Limit:   int(item.To - item.From + 1),
			Forward: true,
		}
	}
}

func (a *aptos) getForwardProgress() *scanProgress {
	a.progressMu.Lock()
	defer a.progressMu.Unlock()
	if a.forwardProgress == nil {
		a.forwardProgress = newScanProgress("aptos")
	}
	return a.forwardProgress
}

func (a *aptos) lookbackVersion(ctx context.Context) {
	if syncBreak(conf.Aptos, a.versionQueue.Len()) {
		return
	}

	window, ok, err := getLookbackWindow(conf.Aptos)
	if err != nil {
		log.Task.Warn("aptos lookback order query failed:", err)
		return
	}
	if !ok {
		return
	}

	start, end, err := blockapi.New().GetBoundaryHeights(window.startAt, window.endAt, conf.Aptos)
	if err != nil {
		log.Task.Warn("aptos lookback boundary query failed:", err)
		return
	}
	for i := int(start); i <= int(end); i += a.versionChunkSize {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if syncBreak(conf.Aptos, a.versionQueue.Len()) {
			return
		}
		limit := a.versionChunkSize
		if i+limit > int(end) {
			limit = int(end) - i + 1
		}
		a.versionQueue.In <- version{Start: i, Limit: limit}
		time.Sleep(time.Millisecond * 200) // 速率控制
	}
	markLookbackDone(window)
}

func (a *aptos) versionDispatch(ctx context.Context) {
	p, err := ants.NewPoolWithFunc(3, a.versionParse)
	if err != nil {
		log.Task.Warn("aptos versionDispatch Error:", err)

		return
	}

	defer p.Release()

	for {
		select {
		case n := <-a.versionQueue.Out:
			if err := p.Invoke(n); err != nil {
				a.retryVersion(n, fmt.Sprintf("versionDispatch invoke failed: %v", err))
				log.Task.Warn("versionDispatch Error invoking process slot:", err)
			}
		case <-ctx.Done():
			if err := ctx.Err(); err != nil {
				log.Task.Warn("versionDispatch context done:", err)
			}

			return
		}
	}
}

// 由于 aptos 网络特性，交易数据中不会显示存在交易转账 from => to 的对应关系，
// 所以目前此解析函数存在大量循环嵌套解析，逻辑较为复杂，希望未来有更好的方式进行解析 慢慢优化
func (a *aptos) versionParse(n any) {
	p := n.(version)

	var net = conf.Aptos
	var url = fmt.Sprintf("%sv1/transactions?start=%d&limit=%d", model.Endpoint(conf.Aptos), p.Start, p.Limit)

	resp, err := a.client.Get(url)
	if err != nil {
		conf.RecordFailure(net)
		a.retryVersion(p, fmt.Sprintf("versionParse request failed: %v", err))
		log.Task.Warn("versionParse Error sending request:", err)

		return
	}

	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		conf.RecordFailure(net)
		a.retryVersion(p, fmt.Sprintf("versionParse response status: %d", resp.StatusCode))
		log.Task.Warn("versionParse Error response status code:", resp.StatusCode)

		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		conf.RecordFailure(net)
		a.retryVersion(p, fmt.Sprintf("versionParse read response failed: %v", err))
		log.Task.Warn("versionParse Error reading response body:", err)

		return
	}

	if !gjson.ValidBytes(body) {
		conf.RecordFailure(net)
		a.retryVersion(p, "versionParse invalid JSON response")
		log.Task.Warn("versionParse Error: invalid JSON response body")

		return
	}
	data := gjson.ParseBytes(body)
	if !data.IsArray() {
		conf.RecordFailure(net)
		a.retryVersion(p, "versionParse response is not an array")
		log.Task.Warn("versionParse Error: response is not an array")

		return
	}
	conf.RecordSuccess(net, cast.ToString(p.Start+p.Limit-1))
	a.resetVersionRetry(p)

	transfers := make([]transfer, 0)
	for _, trans := range data.Array() {
		tsNano := trans.Get("timestamp").Int() * 1000
		timestamp := time.Unix(tsNano/1e9, tsNano%1e9)

		ver := int(trans.Get("version").Int())
		hash := trans.Get("hash").String()
		addrOwner := make(map[string]string)                                         // [address] => owner address
		addrType := make(map[string]model.TradeType)                                 // [address] => tradeType
		amtAddrMap := map[string]map[aptAmount]string{"deposit": {}, "withdraw": {}} // [amount] => address
		aptEvents := make([]aptEvent, 0)
		trans.Get("changes").ForEach(func(_, v gjson.Result) bool {
			if v.Get("type").String() != "write_resource" {

				return true
			}

			data := v.Get("data")
			if data.Get("type").String() == "0x1::fungible_asset::FungibleStore" {
				addr := v.Get("address").String()
				switch data.Get("data.metadata.inner").String() {
				case conf.UsdtAptos:
					addrType[addr] = model.UsdtAptos
				case conf.UsdcAptos:
					addrType[addr] = model.UsdcAptos
				}
			}
			if data.Get("type").String() == "0x1::object::ObjectCore" {
				addrOwner[v.Get("address").String()] = data.Get("data.owner").String()
			}

			return true
		})
		trans.Get("events").ForEach(func(_, v gjson.Result) bool {
			amount := v.Get("data.amount").String()
			amt, err := decimal.NewFromString(amount)
			if err != nil {

				return true
			}

			address := v.Get("data.store").String()
			switch v.Get("type").String() {
			case "0x1::fungible_asset::Deposit":
				aptEvents = append(aptEvents, aptEvent{Amount: amt, Address: address, Action: "deposit"})
				amtAddrMap["deposit"][aptAmount{Amount: amount, Type: addrType[address]}] = address
			case "0x1::fungible_asset::Withdraw":
				amtAddrMap["withdraw"][aptAmount{Amount: amount, Type: addrType[address]}] = address
				aptEvents = append(aptEvents, aptEvent{Amount: amt, Address: address, Action: "withdraw"})
			}
			return true
		})

		// 针对 一个withdraw 对应 一个deposit 且数额相同的情况
		for amt, to := range amtAddrMap["deposit"] {
			from, ok := amtAddrMap["withdraw"][amt]
			if !ok {

				continue
			}

			amount, ok := new(big.Int).SetString(amt.Amount, 10)
			if !ok {

				continue
			}

			tradeType, ok := addrType[to]
			if !ok {

				continue
			}

			transfers = append(transfers, transfer{
				Network:     net,
				TxHash:      hash,
				Amount:      decimal.NewFromBigInt(amount, model.GetTradeDecimal(tradeType)),
				FromAddress: a.padAddressLeadingZeros(addrOwner[from]),
				RecvAddress: a.padAddressLeadingZeros(addrOwner[to]),
				Timestamp:   timestamp,
				TradeType:   tradeType,
				BlockNum:    ver,
			})
		}

		// 针对 一个withdraw 对应 多个deposit(数额累计等于 withdraw) 的情况
		processEvents := func(tradeType model.TradeType, events []aptEvent) ([]aptEvent, map[string]string) {
			deposits := make([]aptEvent, 0)
			withdraws := make(map[decimal.Decimal]aptEvent)
			fromMap := make(map[string]string)

			// 分类事件
			for _, e := range events {
				if addrType[e.Address] == tradeType {
					if e.Action == "deposit" {
						deposits = append(deposits, e)
					}
					if e.Action == "withdraw" {
						withdraws[e.Amount] = e
					}
				}
			}

			// 穷举计算匹配关系，只穷举 A + B = C 的情况，实际上还存在 A + B + C + ... = D
			// 大部分这种情况都是合约 swap 等交易，非普通人1对1转账，所以选择忽视
			for k1, e1 := range deposits {
				for k2, e2 := range deposits {
					if k1 == k2 {
						continue
					}
					for sum, e3 := range withdraws {
						if e1.Amount.Add(e2.Amount).Equal(sum) {
							fromMap[e1.Address] = e3.Address
						}
					}
				}
			}

			return deposits, fromMap
		}
		generateTransfers := func(deposits []aptEvent, fromMap map[string]string, t model.TradeType, decimals int32) {
			for _, to := range deposits {
				if from, ok := fromMap[to.Address]; ok {
					transfers = append(transfers, transfer{
						Network:     net,
						TxHash:      hash,
						Amount:      decimal.NewFromBigInt(to.Amount.BigInt(), decimals),
						FromAddress: a.padAddressLeadingZeros(addrOwner[from]),
						RecvAddress: a.padAddressLeadingZeros(addrOwner[to.Address]),
						Timestamp:   timestamp,
						TradeType:   t,
						BlockNum:    ver,
					})
				}
			}
		}

		// 处理 USDT
		usdtDeposits, usdtFrom := processEvents(model.UsdtAptos, aptEvents)
		generateTransfers(usdtDeposits, usdtFrom, model.UsdtAptos, model.GetTradeDecimal(model.UsdtAptos))

		// 处理 USDC
		usdcDeposits, usdcFrom := processEvents(model.UsdcAptos, aptEvents)
		generateTransfers(usdcDeposits, usdcFrom, model.UsdcAptos, model.GetTradeDecimal(model.UsdcAptos))
	}

	if p.Forward {
		transfers = attachScanBatch(transfers, completeScanRange(
			a.getForwardProgress(),
			scanRange{From: int64(p.Start), To: int64(p.Start + p.Limit - 1)},
		))
	}
	if len(transfers) > 0 {
		transferQueue.In <- transfers
	}

	log.Task.Info(fmt.Sprintf("区块扫描完成(Aptos) %d.%d 成功率：%s", p.Start, p.Limit, conf.GetSuccessRate(net)))
}

func (a *aptos) retryVersion(v version, reason string) {
	a.retryMu.Lock()
	attempt := a.retryAttempts[v] + 1
	if attempt > aptosVersionRetryMaxAttempts {
		delete(a.retryAttempts, v)
		a.retryMu.Unlock()
		if v.Forward {
			a.getForwardProgress().retryLater()
		}
		log.Task.Warn(fmt.Sprintf("Aptos version %d-%d retry limit reached: %s", v.Start, v.Limit, reason))

		return
	}
	a.retryAttempts[v] = attempt
	a.retryMu.Unlock()

	a.versionQueue.In <- v
	log.Task.Warn(fmt.Sprintf("Aptos version %d-%d retry %d/%d: %s", v.Start, v.Limit, attempt, aptosVersionRetryMaxAttempts, reason))
}

func (a *aptos) resetVersionRetry(v version) {
	a.retryMu.Lock()
	delete(a.retryAttempts, v)
	a.retryMu.Unlock()
}

func (a *aptos) parseTransactions(data gjson.Result) []transfer {
	transfers := make([]transfer, 0)
	for _, trans := range data.Array() {
		if success := trans.Get("success"); success.Exists() && !success.Bool() {
			continue
		}
		timestampMicros := trans.Get("timestamp").Int()
		if timestampMicros <= 0 {
			continue
		}
		timestamp := time.Unix(0, timestampMicros*1000)
		version := int(trans.Get("version").Int())
		hash := strings.ToLower(trans.Get("hash").String())
		if version <= 0 || hash == "" {
			continue
		}

		addrOwner := make(map[string]string)
		addrType := make(map[string]model.TradeType)
		amountAddress := map[string]map[aptAmount]string{"deposit": {}, "withdraw": {}}
		events := make([]aptEvent, 0)
		trans.Get("changes").ForEach(func(_, change gjson.Result) bool {
			if change.Get("type").String() != "write_resource" {
				return true
			}
			resource := change.Get("data")
			if resource.Get("type").String() == "0x1::fungible_asset::FungibleStore" {
				store := change.Get("address").String()
				switch resource.Get("data.metadata.inner").String() {
				case conf.UsdtAptos:
					addrType[store] = model.UsdtAptos
				case conf.UsdcAptos:
					addrType[store] = model.UsdcAptos
				}
			}
			if resource.Get("type").String() == "0x1::object::ObjectCore" {
				addrOwner[change.Get("address").String()] = resource.Get("data.owner").String()
			}
			return true
		})
		trans.Get("events").ForEach(func(_, event gjson.Result) bool {
			amountText := event.Get("data.amount").String()
			amount, err := decimal.NewFromString(amountText)
			if err != nil || amount.Sign() <= 0 {
				return true
			}
			store := event.Get("data.store").String()
			tradeType := addrType[store]
			switch event.Get("type").String() {
			case "0x1::fungible_asset::Deposit":
				events = append(events, aptEvent{Amount: amount, Address: store, Action: "deposit"})
				amountAddress["deposit"][aptAmount{Amount: amountText, Type: tradeType}] = store
			case "0x1::fungible_asset::Withdraw":
				events = append(events, aptEvent{Amount: amount, Address: store, Action: "withdraw"})
				amountAddress["withdraw"][aptAmount{Amount: amountText, Type: tradeType}] = store
			}
			return true
		})

		for amountKey, toStore := range amountAddress["deposit"] {
			fromStore, ok := amountAddress["withdraw"][amountKey]
			if !ok || amountKey.Type == "" {
				continue
			}
			rawAmount, ok := new(big.Int).SetString(amountKey.Amount, 10)
			if !ok || rawAmount.Sign() <= 0 {
				continue
			}
			transfers = append(transfers, transfer{
				Network:     conf.Aptos,
				TxHash:      hash,
				Amount:      decimal.NewFromBigInt(rawAmount, model.GetTradeDecimal(amountKey.Type)),
				FromAddress: a.padAddressLeadingZeros(addrOwner[fromStore]),
				RecvAddress: a.padAddressLeadingZeros(addrOwner[toStore]),
				Timestamp:   timestamp,
				TradeType:   amountKey.Type,
				BlockNum:    version,
			})
		}

		for _, tradeType := range []model.TradeType{model.UsdtAptos, model.UsdcAptos} {
			deposits := make([]aptEvent, 0)
			withdraws := make(map[decimal.Decimal]aptEvent)
			for _, event := range events {
				if addrType[event.Address] != tradeType {
					continue
				}
				if event.Action == "deposit" {
					deposits = append(deposits, event)
				} else if event.Action == "withdraw" {
					withdraws[event.Amount] = event
				}
			}
			fromByDeposit := make(map[string]string)
			for first := 0; first < len(deposits); first++ {
				for second := first + 1; second < len(deposits); second++ {
					if withdraw, ok := withdraws[deposits[first].Amount.Add(deposits[second].Amount)]; ok {
						fromByDeposit[deposits[first].Address] = withdraw.Address
						fromByDeposit[deposits[second].Address] = withdraw.Address
					}
				}
			}
			for _, deposit := range deposits {
				fromStore, ok := fromByDeposit[deposit.Address]
				if !ok {
					continue
				}
				transfers = append(transfers, transfer{
					Network:     conf.Aptos,
					TxHash:      hash,
					Amount:      decimal.NewFromBigInt(deposit.Amount.BigInt(), model.GetTradeDecimal(tradeType)),
					FromAddress: a.padAddressLeadingZeros(addrOwner[fromStore]),
					RecvAddress: a.padAddressLeadingZeros(addrOwner[deposit.Address]),
					Timestamp:   timestamp,
					TradeType:   tradeType,
					BlockNum:    version,
				})
			}
		}
	}
	return transfers
}

func (a *aptos) padAddressLeadingZeros(addr string) string {
	addr = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(addr), "0x"), "0X")
	if addr == "" || len(addr) > 64 {
		return ""
	}
	addr = strings.Repeat("0", 64-len(addr)) + strings.ToLower(addr)

	return "0x" + addr
}

func (a *aptos) tradeConfirmHandle(ctx context.Context) {
	var orders = getConfirmingOrders(model.GetNetworkTrades(conf.Aptos))

	var handle = func(o model.Order) {
		if model.GetC(model.BlockOffsetConfirm) == "1" {
			a.heightMu.RLock()
			lastVersion := a.lastVersion
			a.heightMu.RUnlock()
			if lastVersion == 0 {
				return
			}
			if lastVersion-o.RefBlockNum < a.versionConfirmedOffset {
				return
			}
		}

		req, _ := http.NewRequestWithContext(ctx, "GET", model.Endpoint(conf.Aptos)+"v1/transactions/by_hash/"+o.RefHash, nil)
		resp, err := a.client.Do(req)
		if err != nil {
			log.Task.Warn("aptos tradeConfirmHandle Error sending request:", err)

			return
		}

		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			log.Task.Warn("aptos tradeConfirmHandle Error response status code:", resp.StatusCode)

			return
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Task.Warn("aptos tradeConfirmHandle Error reading response body:", err)

			return
		}

		data := gjson.ParseBytes(body)
		if data.Get("error_code").Exists() {
			log.Task.Warn("aptos tradeConfirmHandle Error:", data.Get("message").String())

			return
		}

		if data.Get("version").String() != "" &&
			data.Get("success").Bool() &&
			data.Get("vm_status").String() == "Executed successfully" {

			markFinalConfirmed(o)
		}
	}

	runOrderConfirmations(ctx, orders, handle)
}
