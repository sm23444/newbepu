package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cast"
	"github.com/v03413/bepusdt/app"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/notifier"
	"github.com/v03413/bepusdt/app/utils"

	"github.com/v03413/go-cache"
	"gorm.io/gorm"
)

type EpNotify struct {
	TradeId            string  `json:"trade_id"`             //  本地订单号
	OrderId            string  `json:"order_id"`             //  客户交易id
	Amount             float64 `json:"amount"`               //  订单金额 CNY
	ActualAmount       string  `json:"actual_amount"`        //  USDT 交易数额
	Token              string  `json:"token"`                //  收款钱包地址
	BlockTransactionId string  `json:"block_transaction_id"` // 区块id
	Signature          string  `json:"signature"`            // 签名
	Status             int     `json:"status"`               //  1：等待支付，2：支付成功，3：订单超时
}

const maxCallbackResponseBytes = 4 << 10

const (
	statusNotifyMaxAttempts = 5
	statusNotifyBaseDelay   = time.Second
	statusNotifyMaxDelay    = 30 * time.Second
)

type statusNotifyCall struct {
	done chan struct{}
	err  error
}

var statusNotifyFlights sync.Map

func Handle(order model.Order) error {
	return handleWithClient(order, false, nil)
}

func HandleManual(order model.Order) error {
	return handleWithClient(order, true, nil)
}

func handleWithClient(order model.Order, force bool, client *http.Client) error {
	if order.Status != model.OrderStatusSuccess {
		return errors.New("订单未支付 无法回调")
	}

	claimed, err := order.ClaimNotify(force)
	if err != nil {
		return err
	}

	markFailure := func(deliveryErr error) error {
		completeErr := claimed.CompleteNotify(false)
		if completeErr != nil {
			return errors.Join(deliveryErr, completeErr)
		}
		markNotifyFail(claimed, deliveryErr.Error())
		return deliveryErr
	}

	if !utils.IsAllowedCallbackURL(claimed.NotifyUrl) {
		return markFailure(errors.New("notify_url must use HTTPS"))
	}
	if client == nil {
		client = utils.NewCallbackHttpClient(5 * time.Second)
	}
	client = utils.WithCallbackRedirectPolicy(client)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if claimed.ApiType == model.OrderApiTypeEpay {
		err = epay(ctx, claimed, client)
	} else {
		err = epusdt(ctx, claimed, client)
	}
	if err != nil {
		return markFailure(err)
	}
	if err = claimed.CompleteNotify(true); err != nil {
		return err
	}

	log.Info("订单回调成功：", claimed.TradeId)
	return nil
}

func epay(ctx context.Context, order model.Order, client *http.Client) error {
	notifyURL, err := utils.AppendURLQuery(order.NotifyUrl, order.BuildNotifyParams())
	if err != nil {
		return err
	}

	postReq, err := http.NewRequestWithContext(ctx, "GET", notifyURL, nil)
	if err != nil {
		return err
	}

	postReq.Header.Set("Powered-By", "https://github.com/sm23444/newbepu")
	resp, err := client.Do(postReq)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	return validateCallbackResponse(resp)
}

func epusdt(ctx context.Context, order model.Order, client *http.Client) error {
	var data = make(map[string]interface{})
	var body = EpNotify{
		TradeId:            order.TradeId,
		OrderId:            order.OrderId,
		Amount:             cast.ToFloat64(order.Money),
		ActualAmount:       order.Amount,
		Token:              order.Address,
		BlockTransactionId: order.RefHash,
		Status:             order.Status,
	}
	var jsonBody, err = json.Marshal(body)
	if err != nil {
		return err
	}

	if err = json.Unmarshal(jsonBody, &data); err != nil {
		return err
	}

	// 签名
	body.Signature = utils.EpusdtSign(data, model.AuthToken())

	// 再次序列化
	jsonBody, err = json.Marshal(body)
	var postReq, err2 = http.NewRequestWithContext(ctx, "POST", order.NotifyUrl, strings.NewReader(string(jsonBody)))
	if err2 != nil {
		return err2
	}

	postReq.Header.Set("Content-Type", "application/json")
	postReq.Header.Set("Powered-By", "https://github.com/sm23444/newbepu")
	postReq.Header.Set("User-Agent", "BEpusdt/"+app.Version)
	resp, err := client.Do(postReq)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	return validateCallbackResponse(resp)
}

func validateCallbackResponse(resp *http.Response) error {
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("商户系统返回状态码错误：%d（必须是200）", resp.StatusCode)
	}

	body, err := readBoundedCallbackResponse(resp.Body)
	if err != nil {
		return err
	}

	ack := strings.ToLower(strings.TrimSpace(string(body)))
	if ack != "success" && ack != "ok" {
		return fmt.Errorf("商户系统必须响应 success 或 ok 才会认定回调成功，实际响应：%q", string(body))
	}
	return nil
}

func readBoundedCallbackResponse(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxCallbackResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取商户回调响应失败: %w", err)
	}
	if len(data) > maxCallbackResponseBytes {
		return nil, fmt.Errorf("商户回调响应超过 %d 字节", maxCallbackResponseBytes)
	}

	return data, nil
}

func Bepusdt(o model.Order) {
	if o.ApiType != model.OrderApiTypeEpusdt && o.ApiType != model.OrderApiTypeEpusdtOrder {

		return
	}

	var authToken = model.AuthToken()
	var client = utils.NewCallbackHttpClient(time.Second * 5)
	go func() {
		if err := retryBepusdtStatusUpdate(context.Background(), model.Db, client, authToken, o, statusNotifyMaxAttempts, statusNotifyBaseDelay); err != nil {
			log.Warn("notify BEpusdt Error:", err.Error())
		}
	}()
}

func retryBepusdtStatusUpdate(ctx context.Context, db *gorm.DB, client *http.Client, authToken string, o model.Order, attempts int, initialDelay time.Duration) error {
	if attempts < 1 {
		attempts = 1
	}
	delay := initialDelay
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := deliverBepusdtStatusUpdate(db, client, authToken, o); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt == attempts {
			break
		}
		if delay <= 0 {
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(lastErr, ctx.Err())
		case <-timer.C:
		}
		if delay < statusNotifyMaxDelay {
			delay *= 2
			if delay > statusNotifyMaxDelay {
				delay = statusNotifyMaxDelay
			}
		}
	}

	return lastErr
}

func deliverBepusdtStatusUpdate(db *gorm.DB, client *http.Client, authToken string, o model.Order) error {
	flightKey := fmt.Sprintf("%d:%s", o.Status, o.TradeId)
	call := &statusNotifyCall{done: make(chan struct{})}
	actual, loaded := statusNotifyFlights.LoadOrStore(flightKey, call)
	if loaded {
		active := actual.(*statusNotifyCall)
		<-active.done
		return active.err
	}
	defer func() {
		close(call.done)
		statusNotifyFlights.Delete(flightKey)
	}()

	call.err = deliverBepusdtStatusUpdateOnce(db, client, authToken, o)
	return call.err
}

func deliverBepusdtStatusUpdateOnce(db *gorm.DB, client *http.Client, authToken string, o model.Order) error {
	if client == nil {
		client = utils.NewCallbackHttpClient(time.Second * 5)
	}
	client = utils.WithCallbackRedirectPolicy(client)

	var current model.Order
	tx := db.Where("trade_id = ? and status = ?", o.TradeId, o.Status).Limit(1).Find(&current)
	if tx.Error != nil {
		return tx.Error
	}
	if tx.RowsAffected == 0 {
		return nil
	}
	if !utils.IsAllowedCallbackURL(current.NotifyUrl) {
		return errors.New("notify_url must use HTTPS")
	}

	var key = fmt.Sprintf("bepusdt_notify_%d_%s", current.Status, current.TradeId)
	if _, ok := cache.Get(key); ok {
		return nil
	}

	var data = make(map[string]interface{})
	var body = EpNotify{
		TradeId:            current.TradeId,
		OrderId:            current.OrderId,
		Amount:             cast.ToFloat64(current.Money),
		ActualAmount:       current.Amount,
		Token:              current.Address,
		BlockTransactionId: current.RefHash,
		Status:             current.Status,
	}
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	if err = json.Unmarshal(jsonBody, &data); err != nil {
		return err
	}

	body.Signature = utils.EpusdtSign(data, authToken)

	jsonBody, _ = json.Marshal(body)
	req, err := http.NewRequest("POST", current.NotifyUrl, strings.NewReader(string(jsonBody)))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Powered-By", "https://github.com/sm23444/newbepu")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("resp.StatusCode != 200")
	}

	all, err := readBoundedCallbackResponse(resp.Body)
	if err != nil {
		return err
	}
	cache.Set(key, true, time.Minute)
	log.Info(fmt.Sprintf("订单回调成功[%d]：%s %s", current.Status, current.TradeId, string(all)))

	return nil
}

func markNotifyFail(o model.Order, reason string) {
	log.Warn(fmt.Sprintf("订单回调失败(%v)：%s", o.TradeId, reason))
	notifier.NotifyFail(o, reason)
}
