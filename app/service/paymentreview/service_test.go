package paymentreview

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/v03413/bepusdt/app/model"
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
	t.Cleanup(func() {
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
	result, err := Create(CreateInput{TradeID: tradeID, TransactionHash: "submitted-" + tradeID, Description: description, File: header})
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
	_, err := Create(CreateInput{TradeID: order.TradeId, TransactionHash: "second-submission", Description: req.PostFormValue("description"), File: header})
	if !errors.Is(err, ErrReviewExists) {
		t.Fatalf("expected duplicate pending error, got %v", err)
	}
}

func TestReviewSubmissionRejectsOrderCanceledAfterInitialCheck(t *testing.T) {
	db := setupPaymentReviewTestDB(t)
	order := paymentReviewTestOrder("cancel-during-review", model.OrderStatusWaiting)
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	canceled := false
	if err := db.Callback().Query().After("gorm:query").Register("test:cancel_after_initial_review_check", func(tx *gorm.DB) {
		if canceled || tx.Statement.Table != "bep_order" {
			return
		}
		canceled = true
		if err := db.Model(&model.Order{}).Where("id = ?", order.ID).Update("status", model.OrderStatusCanceled).Error; err != nil {
			t.Errorf("cancel order during review submission: %v", err)
		}
	}); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("trade_id", order.TradeId)
	_ = writer.WriteField("description", "提交复核期间订单已被管理员取消")
	part, _ := writer.CreateFormFile("evidence", "payment.png")
	_, _ = part.Write([]byte("\x89PNG\r\n\x1a\nreview"))
	_ = writer.Close()
	req := httptest.NewRequest("POST", "/", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	_ = req.ParseMultipartForm(1 << 20)
	file, header, _ := req.FormFile("evidence")
	_ = file.Close()
	_, err := Create(CreateInput{
		TradeID: order.TradeId, TransactionHash: "canceled-transaction", Description: req.PostFormValue("description"), File: header,
	})
	if !errors.Is(err, ErrReviewUnavailable) {
		t.Fatalf("create error = %v, want %v", err, ErrReviewUnavailable)
	}
	var count int64
	if err := db.Model(&model.PaymentReview{}).Where("trade_id = ?", order.TradeId).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("pending review count = %d, want 0", count)
	}
}

func TestFailedOrderCanBeSubmittedAndApprovedForManualReview(t *testing.T) {
	db := setupPaymentReviewTestDB(t)
	order := paymentReviewTestOrder("failed-manual-review", model.OrderStatusFailed)
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	review := reviewUpload(t, order.TradeId, "确认失败后提交截图申请人工核实")
	if err := Resolve(review.ID, "approve", "failed-order-transaction", "已核实钱包收款记录", "admin"); err != nil {
		t.Fatalf("approve failed-order review: %v", err)
	}
	var persisted model.Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.OrderStatusSuccess || persisted.RefHash != "failed-order-transaction" {
		t.Fatalf("order = status %d hash %q, want success/failed-order-transaction", persisted.Status, persisted.RefHash)
	}
}

func TestManualReviewApprovalStoresHashAndMarksOrderSuccess(t *testing.T) {
	db := setupPaymentReviewTestDB(t)
	order := paymentReviewTestOrder("chain-review", model.OrderStatusWaiting)
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	reviewID := reviewUpload(t, order.TradeId, "链上转账已完成但订单没有到账")
	txHash := "0x" + "a1" + strings.Repeat("0", 62)
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
	var persistedReview model.PaymentReview
	if err := db.First(&persistedReview, reviewID.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persistedReview.Status != model.PaymentReviewApproved || persistedReview.TransactionHash != txHash {
		t.Fatalf("review = status %s hash %q, want approved/%q", persistedReview.Status, persistedReview.TransactionHash, txHash)
	}
}

func TestManualReviewApprovalDoesNotRequireAutomaticTransactionMatch(t *testing.T) {
	db := setupPaymentReviewTestDB(t)
	order := paymentReviewTestOrder("chain-review-fail", model.OrderStatusWaiting)
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	reviewID := reviewUpload(t, order.TradeId, "链上转账已完成但订单没有到账")
	txHash := "bill-or-hash-not-in-automatic-scan"
	if err := Resolve(reviewID.ID, "approve", txHash, "已通过区块浏览器和收款记录人工核验", "admin"); err != nil {
		t.Fatalf("manual approval should not require automatic match: %v", err)
	}
	var persisted model.PaymentReview
	if err := db.First(&persisted, reviewID.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.PaymentReviewApproved || persisted.TransactionHash != txHash {
		t.Fatalf("review = status %s hash %q, want approved/%q", persisted.Status, persisted.TransactionHash, txHash)
	}
}

func TestManualReviewApprovalDoesNotParseOrderRate(t *testing.T) {
	db := setupPaymentReviewTestDB(t)
	order := paymentReviewTestOrder("chain-review-rollback", model.OrderStatusWaiting)
	order.AddressLocked = true
	order.Rate = "not-a-number"
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	reviewID := reviewUpload(t, order.TradeId, "链上转账已完成但订单没有到账")
	txHash := "0x" + strings.Repeat("c", 64)
	if err := Resolve(reviewID.ID, "approve", txHash, "已人工核验收款记录", "admin"); err != nil {
		t.Fatalf("manual approval should not parse order rate: %v", err)
	}
	var persisted model.Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.OrderStatusSuccess || persisted.RefHash != txHash {
		t.Fatalf("order = status %d hash %q, want success/%q", persisted.Status, persisted.RefHash, txHash)
	}
}

func TestManualExchangeReviewApprovalDoesNotRequireScannedBill(t *testing.T) {
	db := setupPaymentReviewTestDB(t)
	order := paymentReviewTestOrder("okx-manual-review", model.OrderStatusWaiting)
	order.TradeType = model.UsdtOKX
	order.Address = "604336395154821439"
	order.MatchAddress = order.Address
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	reviewID := reviewUpload(t, order.TradeId, "已完成欧易内部转账并提交付款截图")
	billID := "104278866690"
	if err := Resolve(reviewID.ID, "approve", billID, "已在欧易资金账单和收款余额中人工核实", "admin"); err != nil {
		t.Fatalf("approve unscanned OKX bill: %v", err)
	}
	var persisted model.Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != model.OrderStatusSuccess || persisted.RefHash != billID || persisted.FromAddress != "manual-review" {
		t.Fatalf("order = status %d hash %q from %q", persisted.Status, persisted.RefHash, persisted.FromAddress)
	}
}

func TestManualReviewCannotReuseTransactionReference(t *testing.T) {
	db := setupPaymentReviewTestDB(t)
	first := paymentReviewTestOrder("manual-review-first", model.OrderStatusWaiting)
	second := paymentReviewTestOrder("manual-review-second", model.OrderStatusWaiting)
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	firstReview := reviewUpload(t, first.TradeId, "第一笔订单已经人工核实付款记录")
	secondReview := reviewUpload(t, second.TradeId, "第二笔订单申请使用同一付款记录")
	txHash := "0x" + strings.Repeat("d", 64)
	if err := Resolve(firstReview.ID, "approve", txHash, "第一笔人工核实通过", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := Resolve(secondReview.ID, "approve", txHash, "第二笔人工核实通过", "admin"); !errors.Is(err, ErrReviewTxUsed) {
		t.Fatalf("reuse error = %v, want %v", err, ErrReviewTxUsed)
	}
	var persistedReview model.PaymentReview
	if err := db.First(&persistedReview, secondReview.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persistedReview.Status != model.PaymentReviewPending {
		t.Fatalf("second review status = %s, want pending", persistedReview.Status)
	}
}
