package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"github.com/smallnest/chanx"
	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/notifier"
	"github.com/v03413/bepusdt/app/task/notify"
	"github.com/v03413/tronprotocol/core"
)

type transfer struct {
	Network     string          `json:"network"`
	TxHash      string          `json:"tx_hash"`
	Amount      decimal.Decimal `json:"amount"`
	FromAddress string          `json:"from_address"`
	RecvAddress string          `json:"recv_address"`
	Timestamp   time.Time       `json:"timestamp"`
	TradeType   model.TradeType `json:"trade_type"`
	BlockNum    int             `json:"block_num"`
	Final       bool            `json:"final"`
	Source      string          `json:"source"`
	SourceID    string          `json:"source_id"`
}

type resource struct {
	ID           string
	Type         core.Transaction_Contract_ContractType
	Balance      int64
	FromAddress  string
	RecvAddress  string
	Timestamp    time.Time
	ResourceCode core.ResourceCode
}

var resourceQueue = chanx.NewUnboundedChan[[]resource](context.Background(), 30) // 资源队列
var notOrderQueue = chanx.NewUnboundedChan[[]transfer](context.Background(), 30) // 非订单队列
var transferQueue = chanx.NewUnboundedChan[[]transfer](context.Background(), 30) // 交易转账队列

// lookbackDone 记录已触发过回溯的订单 ID，每个订单只回溯一次。
var lookbackDone sync.Map // key: int64 order ID, value: struct{}

const batchInterval = time.Second * 1       // 批处理缓解数据库读取压力
const orderCheckInterval = time.Second * 10 // 订单过期检查间隔
const confirmWorkerLimit = 8

func init() {
	Register(Task{Callback: orderTransferHandle})
	Register(Task{Callback: notOrderTransferHandle})
	Register(Task{Callback: tronResourceHandle})
}

func orderTransferHandle(ctx context.Context) {
	var batch = make([]transfer, 0, 1000)
	var lastCheckTime = time.Now()
	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case transfers, ok := <-transferQueue.Out:
			if !ok {
				return
			}
			batch = append(batch, transfers...)
		case <-ticker.C:
			// 每10秒强制检查一次过期订单，即使没有交易，防止无交易时订单不过期
			var shouldCheck = time.Since(lastCheckTime) >= orderCheckInterval
			if shouldCheck {
				lastCheckTime = time.Now()
			}

			if len(batch) == 0 {
				if shouldCheck {
					expireWaitingOrders()
				}

				continue
			}

			var other = make([]transfer, 0)
			var retry = make([]transfer, 0)
			orders, err := getReceivableOrders(batch)
			if err != nil {
				log.Task.Warn("load receivable orders failed:", err)
				continue
			}

			for _, t := range batch {
				// 判断数额是否在允许范围内
				if !model.IsAmountValid(t.TradeType, t.Amount) {
					continue
				}

				mqttPublish(t)

				key := fmt.Sprintf("%s%s", t.RecvAddress, t.TradeType)
				orderList, ok := orders[key]
				if !ok {
					other = append(other, t)
					continue
				}

				var matched bool
				var discarded bool
				for i, o := range orderList {
					if !orderTransferMatch(o, t) {
						continue
					}

					// 订单匹配 进入确认流程
					if t.Source != "" && t.SourceID != "" {
						if !t.Final {
							log.Task.Warn("exchange transaction is not final: ", t.Source, "/", t.SourceID)
							continue
						}
						if err := model.CompleteExchangeTransaction(&o, t.Source, t.SourceID, t.BlockNum, t.FromAddress, t.Timestamp, t.Amount); err != nil {
							log.Task.Warn("complete exchange transaction failed:", err)
							retry = append(retry, t)
							break
						}
						notifyOrderSuccess(o)
					} else {
						if err := o.MarkConfirming(t.BlockNum, t.FromAddress, t.TxHash, t.Timestamp, t.Amount); err != nil {
							log.Task.Warn("mark order confirming failed:", err)
							if errors.Is(err, model.ErrPaymentTransactionAlreadyClaimed) {
								// The transfer is already accounted for. Keep the current
								// order in the candidate list for another transfer, but do
								// not retry or report this one as unmatched.
								discarded = true
								break
							}
							retry = append(retry, t)
							break
						}
						if t.Final {
							if err := markFinalConfirmed(o); err != nil {
								retry = append(retry, t)
								break
							}
						}
					}

					// 从内存 map 中移除已匹配订单，防止同批次其他 transfer 重复匹配
					orders[key] = append(orderList[:i], orderList[i+1:]...)
					matched = true
					break
				}

				if !matched && !discarded && t.Source == "" {
					other = append(other, t)
				}
			}

			if len(other) > 0 {
				notOrderQueue.In <- other
			}
			if len(retry) > 0 {
				transferQueue.In <- retry
			}

			batch = batch[:0]

			if shouldCheck {
				expireWaitingOrders()
			}
		}
	}
}

func orderTransferMatch(o model.Order, t transfer) bool {
	if o.TradeType != t.TradeType || orderMatchAddress(o) != t.RecvAddress {
		return false
	}
	if !o.AddressLocked && !amountMatchForOrder(t.Amount, o.Amount, o.TradeType) {
		return false
	}
	if !o.CreatedAt.Before(t.Timestamp) || !o.ExpiredAt.After(t.Timestamp) {
		return false
	}

	return true
}

func amountMatchForOrder(amount decimal.Decimal, target string, tradeType model.TradeType) bool {
	if model.IsExchangeTradeType(tradeType) {
		expected, err := decimal.NewFromString(target)
		return err == nil && amount.Equal(expected)
	}
	return amountMatch(amount, target, string(tradeType))
}

func orderMatchAddress(o model.Order) string {
	if o.MatchAddress != "" {
		return o.MatchAddress
	}

	return o.Address
}

func notOrderTransferHandle(ctx context.Context) {
	var batch = make([]transfer, 0, 1000)
	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case transfers, ok := <-notOrderQueue.Out:
			if !ok {
				return
			}
			batch = append(batch, transfers...)
		case <-ticker.C:
			if len(batch) == 0 {
				continue
			}

			var was = make([]model.Wallet, 0)
			if err := model.Db.Where("other_notify = ?", model.WaOtherEnable).Find(&was).Error; err != nil {
				log.Task.Warn("load non-order notification wallets failed:", err)
				continue
			}
			for _, wa := range was {
				for _, t := range batch {
					if t.RecvAddress != wa.MatchAddr && t.FromAddress != wa.MatchAddr {
						continue
					}

					if !model.IsNeedNotifyByTxid(t.TxHash) {
						continue
					}

					var record = model.NotifyRecord{Txid: t.TxHash}
					model.Db.Create(&record)

					notifier.NonOrderTransfer(model.TronTransfer{
						Network:     t.Network,
						TxHash:      t.TxHash,
						Amount:      t.Amount,
						FromAddress: t.FromAddress,
						RecvAddress: t.RecvAddress,
						Timestamp:   t.Timestamp,
						TradeType:   t.TradeType,
						BlockNum:    t.BlockNum,
					}, wa)
				}
			}
			batch = batch[:0]
		}
	}
}

func tronResourceHandle(ctx context.Context) {
	var batch = make([]resource, 0, 1000)
	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case resources, ok := <-resourceQueue.Out:
			if !ok {
				return
			}
			batch = append(batch, resources...)
		case <-ticker.C:
			if len(batch) == 0 {
				continue
			}

			var was []model.Wallet
			model.Db.Where("status = ? and other_notify = ?", model.WaStatusEnable, model.WaOtherEnable).Find(&was)

			for _, wa := range was {
				if wa.GetNetwork() != conf.Tron {
					// 只有 Tron 网络目前才有资源变更通知
					continue
				}

				for _, r := range batch {
					if r.RecvAddress != wa.Address && r.FromAddress != wa.Address {
						continue
					}
					if r.ResourceCode != core.ResourceCode_ENERGY {
						continue
					}
					if !model.IsNeedNotifyByTxid(r.ID) {
						continue
					}

					var record = model.NotifyRecord{Txid: r.ID}
					model.Db.Create(&record)

					notifier.TronResourceChange(model.TronResource(r))
				}
			}

			batch = batch[:0]
		}
	}
}

func markFinalConfirmed(o model.Order) error {
	if err := o.SetSuccess(); err != nil {
		log.Task.Warn("markFinalConfirmed SetSuccess failed:", err)
		return err
	}
	notifyOrderSuccess(o)
	return nil
}

func runOrderConfirmations(ctx context.Context, orders []model.Order, handle func(model.Order)) {
	if len(orders) == 0 {
		return
	}

	workerCount := len(orders)
	if workerCount > confirmWorkerLimit {
		workerCount = confirmWorkerLimit
	}

	jobs := make(chan model.Order)
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case order, ok := <-jobs:
					if !ok {
						return
					}
					handle(order)
				}
			}
		}()
	}

	for _, order := range orders {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		case jobs <- order:
		}
	}
	close(jobs)
	wg.Wait()
}

func receivableOrderStatuses() []int {
	return []int{model.OrderStatusWaiting, model.OrderStatusExpired, model.OrderStatusConfirming}
}

func getReceivableOrders(transfers []transfer) (map[string][]model.Order, error) {
	var orders []model.Order
	db := model.Db.Where("status in (?)", receivableOrderStatuses()).
		Order("created_at asc")
	if len(transfers) > 0 {
		startAt := transfers[0].Timestamp
		endAt := transfers[0].Timestamp
		tradeTypes := make([]model.TradeType, 0)
		seen := make(map[model.TradeType]struct{})
		for _, item := range transfers {
			if item.Timestamp.Before(startAt) {
				startAt = item.Timestamp
			}
			if item.Timestamp.After(endAt) {
				endAt = item.Timestamp
			}
			if _, exists := seen[item.TradeType]; !exists {
				seen[item.TradeType] = struct{}{}
				tradeTypes = append(tradeTypes, item.TradeType)
			}
		}
		db = db.Where("created_at < ? and expired_at > ?", endAt, startAt)
		if len(tradeTypes) > 0 {
			db = db.Where("trade_type in (?)", tradeTypes)
		}
	}
	if err := db.Find(&orders).Error; err != nil {
		return nil, err
	}

	data := make(map[string][]model.Order)
	for _, t := range orders {
		key := orderMatchAddress(t) + string(t.TradeType)
		data[key] = append(data[key], t)
	}

	return data, nil
}

func hasLookbackOrders(tradeType []model.TradeType) bool {
	var count int64
	db := model.Db.Model(&model.Order{}).
		Where("status in (?)", receivableOrderStatuses()).
		Where("expired_at > ?", time.Now().Add(model.GetLookbackHour()))
	if len(tradeType) > 0 {
		db = db.Where("trade_type in (?)", tradeType)
	}

	if err := db.Count(&count).Error; err != nil {
		log.Task.Warn("lookback order count failed:", err)
		return true
	}

	return count > 0
}

func getLookbackUnix(network model.Network) (startAt, endAt int64, ok bool) {
	startAt, endAt, orderIDs, ok := pendingLookbackUnix(network)
	if ok {
		markLookbackDone(orderIDs)
	}

	return startAt, endAt, ok
}

func pendingLookbackUnix(network model.Network) (startAt, endAt int64, orderIDs []int64, ok bool) {
	trade := model.GetNetworkTrades(network)
	if len(trade) == 0 {
		return
	}

	lookback := time.Now().Add(model.GetLookbackHour())
	var all []model.Order
	model.Db.Model(&model.Order{}).
		Where("status in (?) and trade_type in (?)", receivableOrderStatuses(), trade).
		Where("expired_at > ?", lookback).
		Order("created_at asc").
		Find(&all)

	// 过滤掉已经回溯过的订单
	pending := make([]model.Order, 0, len(all))
	for _, o := range all {
		if _, done := lookbackDone.Load(o.ID); !done {
			pending = append(pending, o)
		}
	}
	if len(pending) == 0 {
		return
	}
	orderIDs = make([]int64, 0, len(pending))
	for _, o := range pending {
		orderIDs = append(orderIDs, o.ID)
	}

	// 起点：最早的创建时间（已按 created_at asc 排序）
	startAt = pending[0].CreatedAt.Time().Unix()

	// 终点：最晚的已过期 expired_at；若全部尚未过期则用当前时间
	endAt = time.Now().Unix()
	for _, o := range pending {
		if o.ExpiredAt.Before(time.Now()) && o.ExpiredAt.Unix() > startAt {
			endAt = o.ExpiredAt.Unix()
		}
	}

	ok = true

	return
}

func markLookbackDone(orderIDs []int64) {
	for _, orderID := range orderIDs {
		lookbackDone.Store(orderID, struct{}{})
	}
}

func expireWaitingOrders() {
	orders, err := model.GetOrderByStatus(model.OrderStatusWaiting)
	if err != nil {
		log.Task.Warn("load waiting orders for expiration failed:", err)
		return
	}
	for _, t := range orders {
		if time.Now().Unix() < t.ExpiredAt.Unix() {
			continue
		}

		if err := t.SetExpired(); err != nil {
			log.Task.Warn("expireWaitingOrders SetExpired failed:", err)
			continue
		}
		notify.Bepusdt(t)
	}
}

func getConfirmingOrders(tradeType []model.TradeType) []model.Order {
	var orders = make([]model.Order, 0)
	var data = make([]model.Order, 0)
	var db = model.Db.Where("status = ?", model.OrderStatusConfirming)
	if len(tradeType) > 0 {
		db = db.Where("trade_type in (?)", tradeType)
	}

	if err := db.Find(&orders).Error; err != nil {
		log.Task.Warn("load confirming orders failed:", err)
		return data
	}

	for _, order := range orders {
		if time.Now().Unix() >= order.ExpiredAt.Unix() {
			if order.ConfirmedAt == nil || order.ConfirmedAt.IsZero() || !order.ConfirmedAt.Before(order.ExpiredAt) {
				if err := order.SetFailed(); err != nil {
					log.Task.Warn("getConfirmingOrders SetFailed failed:", err)
					continue
				}
				notify.Bepusdt(order)

				continue
			}
		}

		data = append(data, order)
	}

	return data
}

func amountMatch(amount decimal.Decimal, target, tradeType string) bool {
	mode := model.GetC(model.PaymentMatchMode)
	switch model.MatchMode(mode) {
	case model.Classic:
		return amount.String() == target
	case model.HasPrefix:
		s := amount.String()
		if !strings.HasPrefix(s, target) {
			return false
		}
		rest := s[len(target):]
		if rest == "" {
			return true
		}

		return strings.Contains(target, ".") || strings.HasPrefix(rest, ".")
	case model.RoundOff:
		t, err := decimal.NewFromString(target)
		if err != nil {
			log.Warn(err.Error())

			return false
		}

		_, precision := model.GetAtomicity(model.TradeType(tradeType)) // 标准精度
		precision2 := abs(t.Exponent())                                // 实际精度
		if precision2 != precision {
			precision = precision2
		}

		a := amount.Round(precision)
		t = t.Round(precision)

		return a.Equal(t)
	}

	return false
}

func abs(n int32) int32 {
	if n < 0 {
		return -n
	}
	return n
}
