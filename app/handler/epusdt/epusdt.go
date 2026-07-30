package epusdt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/task"
	"github.com/v03413/bepusdt/app/utils"
)

type Epusdt struct{}

type createReq struct {
	OrderID     string     `json:"order_id" binding:"required"`
	NotifyURL   string     `json:"notify_url" binding:"required"`
	RedirectURL string     `json:"redirect_url" binding:"required"`
	Signature   string     `json:"signature" binding:"required"`
	Amount      float64    `json:"amount"`
	Name        string     `json:"name"`
	Fiat        model.Fiat `json:"fiat"`
	TradeType   string     `json:"trade_type"`
	Address     string     `json:"address"`
	Timeout     int64      `json:"timeout"`
	Rate        string     `json:"rate"`
}

type createOrderReq struct {
	OrderID     string     `json:"order_id" binding:"required"`
	NotifyURL   string     `json:"notify_url" binding:"required"`
	RedirectURL string     `json:"redirect_url" binding:"required"`
	Signature   string     `json:"signature" binding:"required"`
	Amount      float64    `json:"amount"`
	Name        string     `json:"name"`
	Fiat        model.Fiat `json:"fiat"`
	Currencies  string     `json:"currencies"`
	Timeout     int64      `json:"timeout"`
}

type updateOrderReq struct {
	TradeID  string `json:"trade_id" binding:"required"`
	Currency string `json:"currency" binding:"required"`
	Network  string `json:"network" binding:"required"`
}

type cancelReq struct {
	TradeID   string `json:"trade_id" binding:"required"`
	Signature string `json:"signature" binding:"required"`
}

type infoReq struct {
	TradeID string `json:"trade_id" binding:"required"`
}

type methodsReq struct {
	TradeID  string `json:"trade_id" binding:"required"`
	Currency string `json:"currency"`
}

type submitTxHashReq struct {
	TradeID            string `json:"trade_id" binding:"required"`
	TxHash             string `json:"tx_hash"`
	BlockTransactionID string `json:"block_transaction_id"`
}

type manualPaymentAttempt struct {
	StartedAt time.Time
	Count     int
}

var manualPaymentAttempts = struct {
	sync.Mutex
	Items map[string]manualPaymentAttempt
}{Items: make(map[string]manualPaymentAttempt)}

const manualPaymentAttemptWindow = 5 * time.Minute
const manualPaymentAttemptLimit = 5
const manualPaymentAttemptMaxEntries = 2048

func allowManualPaymentAttempt(key string, now time.Time) bool {
	manualPaymentAttempts.Lock()
	defer manualPaymentAttempts.Unlock()

	for itemKey, item := range manualPaymentAttempts.Items {
		if now.Sub(item.StartedAt) >= manualPaymentAttemptWindow {
			delete(manualPaymentAttempts.Items, itemKey)
		}
	}

	item, ok := manualPaymentAttempts.Items[key]
	if !ok {
		if len(manualPaymentAttempts.Items) >= manualPaymentAttemptMaxEntries {
			return false
		}
		manualPaymentAttempts.Items[key] = manualPaymentAttempt{StartedAt: now, Count: 1}
		return true
	}
	if item.Count >= manualPaymentAttemptLimit {
		return false
	}
	item.Count++
	manualPaymentAttempts.Items[key] = item
	return true
}

func loadPayOrder(ctx *gin.Context, tradeID string) (model.Order, bool) {
	order, ok := model.GetTradeOrder(tradeID)
	if !ok {
		ctx.JSON(200, respFailJson("order not found"))
		return model.Order{}, false
	}

	if order.FingerprintBound() && !order.MatchFingerprint(utils.ClientFingerprint(ctx)) {
		ctx.JSON(200, respFailJson("order not found"))
		return model.Order{}, false
	}

	return order, true
}

func (Epusdt) Notify(ctx *gin.Context) {
	rawData, err := ctx.GetRawData()
	if err != nil {
		ctx.String(200, "fail")
		return
	}

	m := make(map[string]any)
	if err = json.Unmarshal(rawData, &m); err != nil {
		ctx.String(200, "fail")
		return
	}

	sign, ok := m["signature"]
	if !ok {
		ctx.String(200, "fail")
		return
	}

	if utils.EpusdtSign(m, model.AuthToken()) != sign {
		ctx.String(200, "fail")
		return
	}

	ctx.String(200, "ok")
}

func (Epusdt) CreateOrder(ctx *gin.Context) {
	var req createOrderReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(200, respFailJson(fmt.Sprintf("CreateOrder: request error: %s", err.Error())))

		return
	}

	if !utils.IsAllowedCallbackURL(req.NotifyURL) {
		ctx.JSON(200, respFailJson("notify_url 地址不合法"))

		return
	}
	if !utils.IsAllowedCallbackURL(req.RedirectURL) {
		ctx.JSON(200, respFailJson("redirect_url 地址不合法"))

		return
	}

	// 解析请求地址
	host := "http://" + ctx.Request.Host
	if ctx.Request.TLS != nil {
		host = "https://" + ctx.Request.Host
	}

	if req.Fiat == "" {
		req.Fiat = model.CNY
	}

	// 创建待付款订单
	order, err := model.BuildPendingOrder(model.OrderParams{
		Money:         decimal.NewFromFloat(req.Amount),
		ApiType:       model.OrderApiTypeEpusdtOrder,
		OrderId:       req.OrderID,
		RedirectUrl:   req.RedirectURL,
		NotifyUrl:     req.NotifyURL,
		Name:          req.Name,
		Timeout:       req.Timeout,
		Fiat:          req.Fiat,
		CurrencyLimit: req.Currencies,
	})
	if err != nil {
		ctx.JSON(200, respFailJson(fmt.Sprintf("CreateOrder: order create failed: %s", err.Error())))
		return
	}

	log.Info(fmt.Sprintf("订单创建成功 商户订单：%s", req.OrderID))

	// 返回响应数据
	ctx.JSON(200, respSuccJson(gin.H{
		"fiat":            order.Fiat,
		"trade_id":        order.TradeId,
		"order_id":        order.OrderId,
		"name":            order.Name,
		"status":          order.Status,
		"amount":          order.Money,
		"expiration_time": uint64(order.ExpiredAt.Sub(time.Now()).Seconds()),
		"payment_url":     model.CheckoutUrl(host, order.TradeId),
		"network":         order.GetMethods(""),
		"reselect":        order.CanReselectPayment(),
	}))
}

func (Epusdt) UpdateOrder(ctx *gin.Context) {
	// 接收 trade_id, currency, network 三个参数
	var req updateOrderReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(200, respFailJson(fmt.Sprintf("UpdateOrder: request error: %s", err.Error())))
		return
	}

	// 解析请求地址
	host := "http://" + ctx.Request.Host
	if ctx.Request.TLS != nil {
		host = "https://" + ctx.Request.Host
	}

	order, ok := loadPayOrder(ctx, req.TradeID)
	if !ok {
		return
	}

	// 仅待付订单才可更新订单
	if order.Status != model.OrderStatusWaiting {
		ctx.JSON(200, respFailJson("update order failed: order status does not allow payment updates"))
		return
	}

	if order.TradeType != "" && !order.CanReselectPayment() {
		ctx.JSON(200, respFailJson("update order failed: order status does not allow payment updates"))
		return
	}

	fp := utils.ClientFingerprint(ctx)

	// 拒绝过期订单
	remaining := time.Until(order.ExpiredAt)
	if remaining <= 0 {
		ctx.JSON(200, respFailJson("update order failed: order expired"))
		return
	}

	// 根据 currency 和 network 解析出 TradeType
	tradeType, err := model.GetTradeTypeByCurrencyAndNetwork(req.Currency, req.Network)
	if err != nil {
		ctx.JSON(200, respFailJson(fmt.Sprintf("UpdateOrder: unsupported payment method: %s - %s", req.Currency, req.Network)))
		return
	}

	// 重建订单（更新支付方式）
	// 注意：RebuildOrder 需要 OrderParams，我们需要从现有订单构造参数
	money, _ := decimal.NewFromString(order.Money)
	params := model.OrderParams{
		Money:             money,
		OrderId:           order.OrderId,
		TradeType:         tradeType, // 新的交易类型
		RedirectUrl:       order.ReturnUrl,
		NotifyUrl:         order.NotifyUrl,
		Name:              order.Name,
		Timeout:           int64(math.Ceil(remaining.Seconds())),
		Fiat:              order.Fiat,
		ClientFingerprint: fp,
	}

	newOrder, err := model.RebuildOrder(order, params)
	if err != nil {
		ctx.JSON(200, respFailJson(fmt.Sprintf("update order failed: %s", err.Error())))
		return
	}

	// 返回响应数据
	ctx.JSON(200, respSuccJson(gin.H{
		"fiat":            newOrder.Fiat,
		"trade_type":      newOrder.TradeType,
		"trade_id":        newOrder.TradeId,
		"order_id":        newOrder.OrderId,
		"status":          newOrder.Status,
		"amount":          newOrder.Money,
		"actual_amount":   newOrder.Amount,
		"token":           newOrder.Address,
		"payment_kind":    newOrder.PaymentKind(),
		"provider":        newOrder.PaymentProvider(),
		"receiver_label":  newOrder.ReceiverLabel(),
		"expiration_time": uint64(newOrder.ExpiredAt.Sub(time.Now()).Seconds()),
		"payment_url":     model.CheckoutUrl(host, newOrder.TradeId),
	}))
}

func (Epusdt) CreateTransaction(ctx *gin.Context) {
	var req createReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(200, respFailJson(fmt.Sprintf("请求参数错误：%s", err.Error())))

		return
	}

	if !utils.IsAllowedCallbackURL(req.NotifyURL) {
		ctx.JSON(200, respFailJson("notify_url 地址不合法"))

		return
	}
	if !utils.IsAllowedCallbackURL(req.RedirectURL) {
		ctx.JSON(200, respFailJson("redirect_url 地址不合法"))

		return
	}

	if req.Fiat == "" {
		req.Fiat = model.CNY
	}
	if req.TradeType == "" {
		req.TradeType = string(model.UsdtTrc20)
	}

	order, err := model.StartBuildOrder(model.OrderParams{
		Money:         decimal.NewFromFloat(req.Amount),
		ApiType:       model.OrderApiTypeEpusdt,
		Address:       req.Address,
		AddressLocked: req.Amount == 0,
		OrderId:       req.OrderID,
		TradeType:     model.TradeType(req.TradeType),
		RedirectUrl:   req.RedirectURL,
		NotifyUrl:     req.NotifyURL,
		Name:          req.Name,
		Timeout:       req.Timeout,
		Rate:          req.Rate,
		Fiat:          req.Fiat,
	})
	if err != nil {
		ctx.JSON(200, respFailJson(fmt.Sprintf("订单创建失败：%s", err.Error())))

		return
	}

	log.Info(fmt.Sprintf("订单创建成功 商户订单：%s", req.OrderID))

	// 返回响应数据
	ctx.JSON(200, respSuccJson(gin.H{
		"fiat":            order.Fiat,
		"trade_type":      order.TradeType,
		"trade_id":        order.TradeId,
		"order_id":        order.OrderId,
		"status":          order.Status,
		"amount":          order.Money,
		"actual_amount":   order.Amount,
		"token":           order.Address,
		"payment_kind":    order.PaymentKind(),
		"provider":        order.PaymentProvider(),
		"receiver_label":  order.ReceiverLabel(),
		"expiration_time": uint64(order.ExpiredAt.Sub(time.Now()).Seconds()),
		"payment_url":     model.CheckoutUrl(utils.GetRequestHost(ctx.Request), order.TradeId),
	}))
}

func (Epusdt) CancelTransaction(ctx *gin.Context) {
	var req cancelReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(200, respFailJson(fmt.Sprintf("请求参数错误：%s", err.Error())))

		return
	}

	order, ok := model.GetTradeOrder(req.TradeID)
	if !ok {
		ctx.JSON(200, respFailJson("订单不存在"))

		return
	}

	if order.Status != model.OrderStatusWaiting {
		ctx.JSON(200, respFailJson(fmt.Sprintf("当前订单(%s)状态不允许取消", req.TradeID)))

		return
	}

	if err := order.SetCanceled(); err != nil {
		ctx.JSON(200, respFailJson(fmt.Sprintf("订单取消失败：%s", err.Error())))

		return
	}

	ctx.JSON(200, respSuccJson(gin.H{"trade_id": req.TradeID}))
}

func (Epusdt) Checkout(ctx *gin.Context) {
	tradeId := ctx.Param("trade_id")
	if _, ok := model.GetTradeOrder(tradeId); !ok {
		ctx.String(200, "order not found")

		return
	}

	// 收银台模板
	tmpl := model.GetC(model.PaymentCheckout) + "/checkout.html"

	ctx.HTML(200, tmpl, gin.H{"trade_id": tradeId})
}

func (Epusdt) GetMethods(ctx *gin.Context) {
	var req methodsReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(200, respFailJson(fmt.Sprintf("request error: %s", err.Error())))
		return
	}

	order, ok := loadPayOrder(ctx, req.TradeID)
	if !ok {
		return
	}

	if order.Status != model.OrderStatusWaiting {
		ctx.JSON(200, respFailJson("error: Invalid order status"))
		return
	}

	if order.TradeType != "" && !order.CanReselectPayment() {
		ctx.JSON(200, respFailJson("error: The order status does not allow retrieving the payment method"))
		return
	}

	ctx.JSON(200, respSuccJson(gin.H{
		"methods":      order.GetMethods(model.Crypto(req.Currency)),
		"network_sort": model.GetC(model.PaymentNetworkSort),
	}))
}

func (Epusdt) Info(ctx *gin.Context) {
	var req infoReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(200, respFailJson(fmt.Sprintf("参数错误：%s", err.Error())))
		return
	}

	order, ok := loadPayOrder(ctx, req.TradeID)
	if !ok {
		return
	}
	supportURL := model.GetC(model.PaymentSupportUrl)
	if !utils.IsAllowedHTTPSURL(supportURL) {
		supportURL = ""
	}

	ctx.JSON(200, respSuccJson(gin.H{
		"network":          order.Network(),                     // 网络信息
		"trade_id":         order.TradeId,                       // 交易编号
		"order_id":         order.OrderId,                       // 商户订单
		"trade_type":       order.TradeType,                     // 交易类型
		"status":           order.Status,                        // 订单状态
		"money":            order.Money,                         // 订单金额
		"actual_amount":    order.Amount,                        // 实付数额
		"token":            order.Address,                       // 收款地址
		"payment_kind":     order.PaymentKind(),                 // chain 或 exchange
		"provider":         order.PaymentProvider(),             // Binance / OKX
		"receiver_label":   order.ReceiverLabel(),               // 收款地址或交易所 UID
		"fiat":             order.Fiat,                          // 法币类型
		"name":             order.Name,                          // 商品名称
		"expired_at":       order.ExpiredAt.Unix(),              // 截止时间
		"created_at":       order.CreatedAt.Time().Unix(),       // 创建时间
		"trade_url":        order.GetTxUrl(),                    // 链上详情
		"support_url":      supportURL,                          // 客服链接
		"redirect_url":     order.RedirectUrl(),                 // 跳转地址
		"reselect":         order.CanReselectPayment(),          // 是否允许确认交易类型后重选
		"manual_tx_submit": task.ManualPaymentAvailable(&order), // 是否允许提交链上交易哈希
	}))
}

func (Epusdt) SubmitTxHash(ctx *gin.Context) {
	var req submitTxHashReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		ctx.JSON(200, respFailJson("请求参数错误"))
		return
	}

	txHash := strings.TrimSpace(req.TxHash)
	if txHash == "" {
		txHash = strings.TrimSpace(req.BlockTransactionID)
	}
	if txHash == "" || len(txHash) > 200 {
		ctx.JSON(200, respFailJson("交易哈希格式不正确"))
		return
	}

	order, ok := loadPayOrder(ctx, strings.TrimSpace(req.TradeID))
	if !ok {
		return
	}
	if !allowManualPaymentAttempt(order.TradeId, time.Now()) {
		ctx.JSON(200, respFailJson("提交过于频繁，请稍后再试"))
		return
	}

	result, err := task.SubmitManualPayment(ctx.Request.Context(), &order, txHash)
	if err != nil {
		switch {
		case errors.Is(err, task.ErrManualPaymentInvalidTxID):
			ctx.JSON(200, respFailJson("交易哈希格式不正确"))
		case errors.Is(err, task.ErrManualPaymentTxUsed):
			ctx.JSON(200, respFailJson("该交易哈希已被使用"))
		case errors.Is(err, task.ErrManualPaymentExchangeOrder):
			ctx.JSON(200, respFailJson("交易所订单不支持提交链上哈希"))
		case errors.Is(err, task.ErrManualPaymentNetworkDisabled):
			ctx.JSON(200, respFailJson("当前订单网络未启用"))
		case errors.Is(err, task.ErrManualPaymentNotEligible), errors.Is(err, task.ErrManualPaymentInvalidOrder):
			ctx.JSON(200, respFailJson("当前订单状态不支持提交交易哈希"))
		case errors.Is(err, task.ErrManualPaymentMismatch):
			ctx.JSON(200, respFailJson("未找到与订单地址、币种和金额一致的链上转账"))
		default:
			log.Warn(fmt.Sprintf("manual payment verification failed trade_id=%s tx_hash=%s: %v", order.TradeId, txHash, err))
			ctx.JSON(200, respFailJson("链上验证暂时失败，请稍后重试"))
		}
		return
	}

	ctx.JSON(200, respSuccJson(result))
}

func (Epusdt) SignVerify(ctx *gin.Context) {
	const maxSignedRequestBytes = 1 << 20
	ctx.Request.Body = http.MaxBytesReader(ctx.Writer, ctx.Request.Body, maxSignedRequestBytes)

	rawData, err := ctx.GetRawData()
	if err != nil {
		ctx.JSON(200, respFailJson(fmt.Sprintf("json 数据读取错误 %s", err.Error())))
		ctx.Abort()

		return
	}

	m := make(map[string]any)
	if err = json.Unmarshal(rawData, &m); err != nil {
		ctx.JSON(200, respFailJson(fmt.Sprintf("json 数据解析错误 %s", err.Error())))
		ctx.Abort()

		return
	}

	sign, ok := m["signature"]
	if !ok {
		ctx.JSON(200, respFailJson("签名丢失"))
		ctx.Abort()

		return
	}

	if utils.EpusdtSign(m, model.AuthToken()) != sign {
		ctx.JSON(200, respFailJson("签名错误"))
		ctx.Abort()

		return
	}

	ctx.Request.Body = io.NopCloser(bytes.NewBuffer(rawData)) // 回写数据
	ctx.Next()
}

func respFailJson(message string) gin.H {

	return gin.H{"status_code": 400, "message": message}
}

func respSuccJson(data interface{}) gin.H {
	return gin.H{"status_code": 200, "message": "success", "data": data, "request_id": ""}
}
