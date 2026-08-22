package epusdt

import (
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/v03413/bepusdt/app/handler/base"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/service/paymentreview"
	"github.com/v03413/bepusdt/app/task/notify"
)

type PaymentReview struct{}

func (Epusdt) SubmitPaymentReview(ctx *gin.Context) {
	if err := ctx.Request.ParseMultipartForm(6 << 20); err != nil {
		base.Response(ctx, 422, "复核资料过大或格式错误")
		return
	}
	file, header, err := ctx.Request.FormFile("evidence")
	if err != nil {
		base.Response(ctx, 422, "请上传付款截图")
		return
	}
	_ = file.Close()
	result, err := paymentreview.Create(paymentreview.CreateInput{
		TradeID:         strings.TrimSpace(ctx.PostForm("trade_id")),
		TransactionHash: strings.TrimSpace(ctx.PostForm("transaction_hash")),
		Description:     ctx.PostForm("description"),
		File:            header,
	})
	if err != nil {
		reviewError(ctx, err)
		return
	}
	base.Response(ctx, http.StatusCreated, result)
}

func reviewError(ctx *gin.Context, err error) {
	switch {
	case errors.Is(err, paymentreview.ErrReviewExists):
		base.Response(ctx, 409, err.Error())
	case errors.Is(err, paymentreview.ErrReviewUnavailable):
		base.Response(ctx, 409, err.Error())
	case errors.Is(err, paymentreview.ErrInvalidReview):
		base.Response(ctx, 422, err.Error())
	default:
		base.Error(ctx, err)
	}
}

// AdminList returns pending reviews first; the existing admin token middleware
// protects the route before this handler runs.
func (PaymentReview) AdminList(ctx *gin.Context) {
	page, _ := strconv.Atoi(ctx.DefaultPostForm("page", "1"))
	size, _ := strconv.Atoi(ctx.DefaultPostForm("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	status := strings.TrimSpace(ctx.PostForm("status"))
	query := model.Db.Model(&model.PaymentReview{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		base.Error(ctx, err)
		return
	}
	var rows []model.PaymentReview
	if err := query.Order("CASE WHEN status = 'pending' THEN 0 ELSE 1 END, id DESC").
		Limit(size).Offset((page - 1) * size).Find(&rows).Error; err != nil {
		base.Error(ctx, err)
		return
	}
	items := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		var order model.Order
		model.Db.Select("trade_id, order_id, trade_type, amount, money, crypto, fiat, address, status").
			Where("trade_id = ?", row.TradeID).First(&order)
		items = append(items, reviewPayload(row, order))
	}
	base.Response(ctx, 200, items, total)
}

func (PaymentReview) AdminDetail(ctx *gin.Context) {
	var req base.IDRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		base.BadRequest(ctx, err.Error())
		return
	}
	review, err := paymentreview.Get(int64(req.ID))
	if err != nil {
		reviewError(ctx, err)
		return
	}
	data, err := os.ReadFile(review.EvidencePath)
	if err != nil {
		base.Error(ctx, err)
		return
	}
	var order model.Order
	model.Db.Where("trade_id = ?", review.TradeID).First(&order)
	payload := reviewPayload(*review, order)
	payload["evidence_data_url"] = "data:" + review.EvidenceType + ";base64," + base64.StdEncoding.EncodeToString(data)
	base.Ok(ctx, payload)
}

type resolveReviewRequest struct {
	ID              int    `json:"id" binding:"required"`
	Decision        string `json:"decision" binding:"required,oneof=approve reject"`
	TransactionHash string `json:"transaction_hash"`
	Note            string `json:"note" binding:"required,min=3,max=1000"`
}

func (PaymentReview) AdminResolve(ctx *gin.Context) {
	var req resolveReviewRequest
	if err := ctx.ShouldBindJSON(&req); err != nil {
		base.BadRequest(ctx, err.Error())
		return
	}
	err := paymentreview.Resolve(int64(req.ID), req.Decision, req.TransactionHash, req.Note, "admin")
	if err != nil {
		switch {
		case errors.Is(err, paymentreview.ErrReviewTxNotFound), errors.Is(err, paymentreview.ErrReviewTxMismatch):
			base.BadRequest(ctx, err.Error())
		case errors.Is(err, paymentreview.ErrReviewResolved), errors.Is(err, paymentreview.ErrReviewUnavailable):
			base.BadRequest(ctx, err.Error())
		default:
			base.Error(ctx, err)
		}
		return
	}
	if req.Decision == "approve" {
		if review, getErr := paymentreview.Get(int64(req.ID)); getErr == nil {
			order, ok := model.GetTradeOrder(review.TradeID)
			if !ok {
				base.Error(ctx, errors.New("审核成功但订单不存在"))
				return
			}
			go notify.Handle(order)
		}
	}
	base.Ok(ctx, gin.H{"id": req.ID, "status": req.Decision + "d"})
}

func reviewPayload(row model.PaymentReview, order model.Order) gin.H {
	return gin.H{
		"id":               row.ID,
		"trade_id":         row.TradeID,
		"order_id":         order.OrderId,
		"trade_type":       order.TradeType,
		"status":           row.Status,
		"transaction_hash": row.TransactionHash,
		"description":      row.Description,
		"evidence_type":    row.EvidenceType,
		"evidence_size":    row.EvidenceSize,
		"evidence_sha256":  row.EvidenceSHA256,
		"resolution_note":  row.ResolutionNote,
		"reviewed_by":      row.ReviewedBy,
		"reviewed_at":      row.ReviewedAt,
		"created_at":       row.CreatedAt,
		"order_status":     order.Status,
		"order_amount":     order.Amount,
		"order_money":      order.Money,
		"order_crypto":     order.Crypto,
		"order_fiat":       order.Fiat,
		"receive_target":   order.Address,
	}
}
