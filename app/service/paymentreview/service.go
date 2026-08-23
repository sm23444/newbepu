package paymentreview

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/utils"
	"gorm.io/gorm"
)

const MaxEvidenceSize int64 = 5 << 20

var (
	ErrInvalidReview     = errors.New("付款复核资料无效")
	ErrReviewUnavailable = errors.New("当前订单不允许申请付款复核")
	ErrReviewExists      = errors.New("该订单已有待处理的付款复核")
	ErrReviewNotFound    = errors.New("付款复核不存在")
	ErrReviewResolved    = errors.New("付款复核已经处理")
	ErrReviewTxUsed      = errors.New("该交易编号已用于其他订单")
)

type CreateInput struct {
	TradeID         string
	TransactionHash string
	Description     string
	File            *multipart.FileHeader
}

type CreateResult struct {
	ID        int64  `json:"id"`
	TradeID   string `json:"trade_id"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

func Create(input CreateInput) (CreateResult, error) {
	tradeID := strings.TrimSpace(input.TradeID)
	description := strings.TrimSpace(input.Description)
	txHash := strings.TrimSpace(input.TransactionHash)
	if tradeID == "" || len(tradeID) > 128 || len(description) < 10 || len(description) > 1000 || txHash == "" || len(txHash) > 256 || input.File == nil {
		return CreateResult{}, ErrInvalidReview
	}
	if input.File.Size <= 0 || input.File.Size > MaxEvidenceSize {
		return CreateResult{}, ErrInvalidReview
	}

	order, ok := model.GetTradeOrder(tradeID)
	if !ok || !reviewableOrder(order) {
		return CreateResult{}, ErrReviewUnavailable
	}
	var existing int64
	if err := model.Db.Model(&model.PaymentReview{}).
		Where("trade_id = ? AND status = ?", tradeID, model.PaymentReviewPending).
		Count(&existing).Error; err != nil {
		return CreateResult{}, err
	}
	if existing > 0 {
		return CreateResult{}, ErrReviewExists
	}

	file, err := input.File.Open()
	if err != nil {
		return CreateResult{}, ErrInvalidReview
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxEvidenceSize+1))
	if err != nil || int64(len(data)) > MaxEvidenceSize {
		return CreateResult{}, ErrInvalidReview
	}
	contentType, extension, ok := imageType(data)
	if !ok {
		return CreateResult{}, ErrInvalidReview
	}

	if err := os.MkdirAll(model.PaymentReviewDir(), 0o700); err != nil {
		return CreateResult{}, fmt.Errorf("创建复核证据目录失败: %w", err)
	}
	name, err := utils.GenerateTradeId()
	if err != nil {
		return CreateResult{}, err
	}
	digest := sha256.Sum256(data)
	filename := name + "." + extension
	path := filepath.Join(model.PaymentReviewDir(), filename)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return CreateResult{}, fmt.Errorf("保存复核证据失败: %w", err)
	}

	review := &model.PaymentReview{
		TradeID:         tradeID,
		Status:          model.PaymentReviewPending,
		TransactionHash: txHash,
		Description:     description,
		EvidencePath:    path,
		EvidenceType:    contentType,
		EvidenceSize:    int64(len(data)),
		EvidenceSHA256:  hex.EncodeToString(digest[:]),
	}
	if err := model.Db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.PaymentReview{}).
			Where("trade_id = ? AND status = ?", tradeID, model.PaymentReviewPending).
			Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrReviewExists
		}
		return tx.Create(review).Error
	}); err != nil {
		_ = os.Remove(path)
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			return CreateResult{}, ErrReviewExists
		}
		return CreateResult{}, err
	}

	return CreateResult{
		ID:        review.ID,
		TradeID:   tradeID,
		Status:    review.Status,
		CreatedAt: time.Now().Format(time.RFC3339),
	}, nil
}

func reviewableOrder(order model.Order) bool {
	return order.Status == model.OrderStatusWaiting ||
		order.Status == model.OrderStatusConfirming ||
		order.Status == model.OrderStatusExpired ||
		order.Status == model.OrderStatusFailed
}

func imageType(data []byte) (contentType, extension string, ok bool) {
	if len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png", "png", true
	}
	if len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff {
		return "image/jpeg", "jpg", true
	}
	if len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP" {
		return "image/webp", "webp", true
	}
	return "", "", false
}

func Get(id int64) (*model.PaymentReview, error) {
	var review model.PaymentReview
	if err := model.Db.First(&review, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrReviewNotFound
		}
		return nil, err
	}
	return &review, nil
}

func Resolve(reviewID int64, decision, txHash, note, reviewer string) error {
	review, err := Get(reviewID)
	if err != nil {
		return err
	}
	if review.Status != model.PaymentReviewPending {
		return ErrReviewResolved
	}
	decision = strings.ToLower(strings.TrimSpace(decision))
	note = strings.TrimSpace(note)
	if decision != "approve" && decision != "reject" || len(note) < 3 || len(note) > 1000 {
		return ErrInvalidReview
	}
	if decision == "approve" && strings.TrimSpace(txHash) == "" {
		txHash = strings.TrimSpace(review.TransactionHash)
	}
	if decision == "approve" && txHash == "" {
		return ErrInvalidReview
	}
	now := time.Now()
	status := model.PaymentReviewRejected
	if decision == "approve" {
		status = model.PaymentReviewApproved
	}
	return model.Db.Transaction(func(tx *gorm.DB) error {
		var locked model.PaymentReview
		if err := tx.Where("id = ?", reviewID).First(&locked).Error; err != nil {
			return err
		}
		if locked.Status != model.PaymentReviewPending {
			return ErrReviewResolved
		}
		if status == model.PaymentReviewApproved {
			if err := approveOrder(tx, locked.TradeID, txHash, now); err != nil {
				return err
			}
		}
		return tx.Model(&model.PaymentReview{}).Where("id = ? AND status = ?", reviewID, model.PaymentReviewPending).
			Updates(map[string]any{
				"status":           status,
				"transaction_hash": txHash,
				"resolution_note":  note,
				"reviewed_by":      reviewer,
				"reviewed_at":      now,
			}).Error
	})
}

// approveOrder records an administrator's manual decision. The administrator
// has already checked the submitted evidence and the provider's own records,
// so approval must not depend on a live provider scan or on an exact match in
// the automatic transaction tables.
func approveOrder(tx *gorm.DB, tradeID, txHash string, at time.Time) error {
	var order model.Order
	if err := tx.Where("trade_id = ?", tradeID).First(&order).Error; err != nil {
		return ErrReviewUnavailable
	}
	if !reviewableOrder(order) {
		return ErrReviewUnavailable
	}
	if err := claimManualReviewReference(tx, order, txHash); err != nil {
		return err
	}
	updates := map[string]any{
		"status":        model.OrderStatusSuccess,
		"from_address":  "manual-review",
		"confirmed_at":  at,
		"ref_block_num": at.Unix(),
	}
	if strings.TrimSpace(txHash) != "" {
		updates["ref_hash"] = strings.TrimSpace(txHash)
	}
	updated := tx.Model(&model.Order{}).
		Where("id = ? AND status IN (?)", order.ID, []int{model.OrderStatusWaiting, model.OrderStatusExpired, model.OrderStatusConfirming, model.OrderStatusFailed}).
		Updates(updates)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return ErrReviewUnavailable
	}
	return nil
}

func claimManualReviewReference(tx *gorm.DB, order model.Order, txHash string) error {
	trade, ok := model.GetTradeConfig(order.TradeType)
	if !ok {
		return ErrReviewUnavailable
	}
	network := string(trade.Network)
	canonicalHash := strings.ToLower(strings.TrimSpace(txHash))

	var claim model.ManualPaymentClaim
	err := tx.Where("network = ? AND LOWER(tx_hash) = ?", network, canonicalHash).Limit(1).Find(&claim).Error
	if err != nil {
		return err
	}
	if claim.ID != 0 {
		if claim.TradeID == order.TradeId {
			return nil
		}
		return ErrReviewTxUsed
	}

	var used int64
	if err := tx.Model(&model.Order{}).
		Where("id <> ? AND status IN (?)", order.ID, []int{model.OrderStatusConfirming, model.OrderStatusSuccess}).
		Where("trade_type IN (?)", model.GetNetworkTrades(trade.Network)).
		Where("LOWER(ref_hash) = ?", canonicalHash).
		Count(&used).Error; err != nil {
		return err
	}
	if used > 0 {
		return ErrReviewTxUsed
	}

	err = tx.Create(&model.ManualPaymentClaim{
		Network: network,
		TxHash:  strings.TrimSpace(txHash),
		TradeID: order.TradeId,
	}).Error
	if err != nil {
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "unique") || strings.Contains(message, "duplicate") {
			return ErrReviewTxUsed
		}
	}
	return err
}
