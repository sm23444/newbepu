package paymentreview

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/task"
	"gorm.io/gorm"
)

func setupPaymentReviewTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "review.db")+"?cache=shared&mode=rwc&_pragma=journal_mode(WAL)&_pragma=busy_timeout(1000)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Order{}, &model.PaymentReview{}, &model.ManualPaymentClaim{}); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	previousDB := model.Db
	model.Db = db
	t.Setenv("BEPUSDT_PAYMENT_REVIEW_DIR", t.TempDir())
	previousVerifier := verifyManualPayment
	t.Cleanup(func() {
		verifyManualPayment = previousVerifier
		model.Db = previousDB
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func paymentReviewTestOrder(tradeID string, status int) model.Order {
	now := time.Now().UTC().Truncate(time.Second)
	zero := time.Unix(0, 0)
	return model.Order{
		OrderId: "merchant-" + tradeID, TradeId: tradeID, TradeType: model.UsdtPolygon,
		Fiat: model.CNY, Crypto: model.USDT, Rate: "7", Amount: "10", Money: "70",
		Address: "0x1111111111111111111111111111111111111111", MatchAddress: "0x1111111111111111111111111111111111111111",
		Status: status, RefHash: tradeID, ExpiredAt: now.Add(time.Hour), ConfirmedAt: &zero,
		AutoTimeAt: model.AutoTimeAt{CreatedAt: (*model.Datetime)(&now), UpdatedAt: (*model.Datetime)(&now)},
	}
}

func reviewUpload(t *testing.T, tradeID, description string) CreateResult {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("trade_id", tradeID); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("description", description); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("evidence", "payment.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("\x89PNG\r\n\x1a\nreview"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	file, header, err := req.FormFile("evidence")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	result, err := Create(CreateInput{TradeID: tradeID, Description: description, File: header})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRejectedReviewCanBeResubmitted(t *testing.T) {
	db := setupPaymentReviewTestDB(t)
	order := paymentReviewTestOrder("retry-review", model.OrderStatusWaiting)
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	first := reviewUpload(t, order.TradeId, "第一次提交付款说明，等待人工处理")
	if err := db.Model(&model.PaymentReview{}).Where("id = ?", first.ID).Update("status", model.PaymentReviewRejected).Error; err != nil {
		t.Fatal(err)
	}
	second := reviewUpload(t, order.TradeId, "第二次提交付款说明，补充转账时间")
	if second.ID == first.ID || second.Status != model.PaymentReviewPending {
		t.Fatalf("second review = %+v, first = %+v", second, first)
	}
}

func TestPendingReviewIsStillRejectedAsDuplicate(t *testing.T) {
	db := setupPaymentReviewTestDB(t)
	order := paymentReviewTestOrder("duplicate-review", model.OrderStatusWaiting)
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	_ = reviewUpload(t, order.TradeId, "第一次提交付款说明，等待人工处理")
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("trade_id", order.TradeId)
	_ = writer.WriteField("description", "第二次提交付款说明，等待人工处理")
	part, _ := writer.CreateFormFile("evidence", "payment.png")
	_, _ = part.Write([]byte("\x89PNG\r\n\x1a\nreview"))
	_ = writer.Close()
	req := httptest.NewRequest("POST", "/", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	_ = req.ParseMultipartForm(1 << 20)
	file, header, _ := req.FormFile("evidence")
	_ = file.Close()
	_, err := Create(CreateInput{TradeID: order.TradeId, Description: req.PostFormValue("description"), File: header})
	if !errors.Is(err, ErrReviewExists) {
		t.Fatalf("expected duplicate pending error, got %v", err)
	}
}

func TestChainReviewApprovalIsAtomic(t *testing.T) {
	db := setupPaymentReviewTestDB(t)
	order := paymentReviewTestOrder("chain-review", model.OrderStatusWaiting)
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	reviewID := reviewUpload(t, order.TradeId, "链上转账已完成但订单没有到账")
	txHash := "0x" + "a1" + strings.Repeat("0", 62)
	verifyManualPayment = func(context.Context, *model.Order, string) (task.ManualPaymentVerification, error) {
		return task.ManualPaymentVerification{Network: "polygon", TxHash: txHash, Amount: decimal.NewFromInt(10), FromAddress: "0x2222222222222222222222222222222222222222", RecvAddress: order.MatchAddress, Timestamp: time.Now(), TradeType: order.TradeType, BlockNum: 99}, nil
	}
	if err := Resolve(reviewID.ID, "approve", txHash, "链上交易已核验", "admin"); err != nil {
		t.Fatalf("approve review: %v", err)
	}
	var persisted model.Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.OrderStatusSuccess || persisted.RefHash != txHash {
		t.Fatalf("order = status %d ref %s", persisted.Status, persisted.RefHash)
	}
	var claimCount int64
	if err := db.Model(&model.ManualPaymentClaim{}).Where("trade_id = ?", order.TradeId).Count(&claimCount).Error; err != nil {
		t.Fatal(err)
	}
	if claimCount != 1 {
		t.Fatalf("claim count = %d, want 1", claimCount)
	}
}

func TestChainReviewVerificationFailureDoesNotChangeState(t *testing.T) {
	db := setupPaymentReviewTestDB(t)
	order := paymentReviewTestOrder("chain-review-fail", model.OrderStatusWaiting)
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	reviewID := reviewUpload(t, order.TradeId, "链上转账已完成但订单没有到账")
	verifyManualPayment = func(context.Context, *model.Order, string) (task.ManualPaymentVerification, error) {
		return task.ManualPaymentVerification{}, errors.New("transaction mismatch")
	}
	if err := Resolve(reviewID.ID, "approve", "0x"+strings.Repeat("b", 64), "人工核验失败", "admin"); !errors.Is(err, ErrReviewTxMismatch) {
		t.Fatalf("expected mismatch, got %v", err)
	}
	var persisted model.PaymentReview
	if err := db.First(&persisted, reviewID.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.PaymentReviewPending {
		t.Fatalf("review status = %s, want pending", persisted.Status)
	}
}

func TestChainReviewApprovalRollsBackClaimWhenOrderUpdateFails(t *testing.T) {
	db := setupPaymentReviewTestDB(t)
	order := paymentReviewTestOrder("chain-review-rollback", model.OrderStatusWaiting)
	order.AddressLocked = true
	order.Rate = "not-a-number"
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	reviewID := reviewUpload(t, order.TradeId, "链上转账已完成但订单没有到账")
	txHash := "0x" + strings.Repeat("c", 64)
	verifyManualPayment = func(context.Context, *model.Order, string) (task.ManualPaymentVerification, error) {
		return task.ManualPaymentVerification{Network: "polygon", TxHash: txHash, Amount: decimal.NewFromInt(10), FromAddress: "0x2222222222222222222222222222222222222222", Timestamp: time.Now(), TradeType: order.TradeType, BlockNum: 100}, nil
	}
	if err := Resolve(reviewID.ID, "approve", txHash, "链上交易已核验", "admin"); err == nil {
		t.Fatal("expected approval to fail for invalid order rate")
	}
	var persisted model.PaymentReview
	if err := db.First(&persisted, reviewID.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.PaymentReviewPending {
		t.Fatalf("review status = %s, want pending", persisted.Status)
	}
	var claimCount int64
	if err := db.Model(&model.ManualPaymentClaim{}).Where("trade_id = ?", order.TradeId).Count(&claimCount).Error; err != nil {
		t.Fatal(err)
	}
	if claimCount != 0 {
		t.Fatalf("claim count = %d, want 0 after rollback", claimCount)
	}
}
