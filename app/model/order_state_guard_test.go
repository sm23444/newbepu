package model

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func TestSetExpiredRejectsNonWaitingOrder(t *testing.T) {
	db := newTestDB(t, "set-expired-guard")
	now := time.Now()
	zero := time.Unix(0, 0)
	order := Order{
		OrderId: "expired-guard-test", TradeId: "t1", TradeType: UsdtTrc20,
		Status: OrderStatusSuccess, ExpiredAt: now.Add(-time.Hour), ConfirmedAt: &zero,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}

	err := order.SetExpired()
	if err == nil {
		t.Fatal("SetExpired should reject already-success order")
	}
	if err.Error() != "order 1 is no longer waiting" {
		t.Fatalf("unexpected error: %v", err)
	}

	var persisted Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if persisted.Status != OrderStatusSuccess {
		t.Fatalf("status changed from success to %d", persisted.Status)
	}
}

func TestSetExpiredRejectsConfirmingOrder(t *testing.T) {
	db := newTestDB(t, "set-expired-confirming-guard")
	now := time.Now()
	order := stateGuardOrder(now, OrderStatusConfirming, "current-hash")
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}

	if err := order.SetExpired(); err == nil {
		t.Fatal("SetExpired should reject a confirming order")
	}

	var persisted Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if persisted.Status != OrderStatusConfirming || persisted.RefHash != "current-hash" {
		t.Fatalf("confirming order changed: status=%d ref_hash=%q", persisted.Status, persisted.RefHash)
	}
}

func TestMarkConfirmingRestoresExpiredOrderForTransferInsideWindow(t *testing.T) {
	db := newTestDB(t, "mark-confirming-expired")
	now := time.Now()
	order := stateGuardOrder(now, OrderStatusExpired, "")
	order.ExpiredAt = now.Add(-time.Minute)
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}

	transferAt := now.Add(-5 * time.Minute)
	if err := order.MarkConfirming(100, "from", "late-hash", transferAt, decimal.NewFromInt(1)); err != nil {
		t.Fatalf("mark expired order confirming: %v", err)
	}

	var persisted Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if persisted.Status != OrderStatusConfirming || persisted.RefHash != "late-hash" {
		t.Fatalf("late transfer was not claimed: status=%d ref_hash=%q", persisted.Status, persisted.RefHash)
	}
}

func TestMarkConfirmingDoesNotReplaceExistingTransaction(t *testing.T) {
	db := newTestDB(t, "mark-confirming-hash-guard")
	now := time.Now()
	order := stateGuardOrder(now, OrderStatusConfirming, "first-hash")
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}

	err := order.MarkConfirming(200, "other", "second-hash", now, decimal.NewFromInt(1))
	if err == nil {
		t.Fatal("MarkConfirming should reject a different transaction for a confirming order")
	}

	var persisted Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if persisted.RefHash != "first-hash" || persisted.RefBlockNum != 10 {
		t.Fatalf("existing transaction was replaced: ref_hash=%q block=%d", persisted.RefHash, persisted.RefBlockNum)
	}
}

func TestSetSuccessRequiresSameTransactionHash(t *testing.T) {
	db := newTestDB(t, "set-success-hash-guard")
	now := time.Now()
	order := stateGuardOrder(now, OrderStatusConfirming, "current-hash")
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}

	stale := order
	stale.RefHash = "stale-hash"
	if err := stale.SetSuccess(); err == nil {
		t.Fatal("SetSuccess should reject a stale transaction snapshot")
	}

	var persisted Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if persisted.Status != OrderStatusConfirming || persisted.RefHash != "current-hash" {
		t.Fatalf("stale confirmation changed order: status=%d ref_hash=%q", persisted.Status, persisted.RefHash)
	}
}

func TestSetCanceledDoesNotOverwriteConcurrentSuccess(t *testing.T) {
	db := newTestDB(t, "set-canceled-guard")
	now := time.Now()
	confirmedAt := now
	order := Order{
		OrderId: "cancel-guard-test", TradeId: "cancel-guard-trade", TradeType: UsdtTrc20,
		Status: OrderStatusWaiting, Rate: "7", Amount: "1", Money: "7",
		Address: "addr", ExpiredAt: now.Add(time.Hour), ConfirmedAt: &confirmedAt,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}

	staleCancelSnapshot := order
	if err := db.Model(&Order{}).Where("id = ?", order.ID).Updates(map[string]any{
		"status":       OrderStatusSuccess,
		"ref_hash":     "confirmed-hash",
		"confirmed_at": confirmedAt,
	}).Error; err != nil {
		t.Fatalf("mark order successful: %v", err)
	}

	if err := staleCancelSnapshot.SetCanceled(); err == nil {
		t.Fatal("SetCanceled should reject an order that became successful")
	}

	var persisted Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if persisted.Status != OrderStatusSuccess {
		t.Fatalf("status changed from success to %d", persisted.Status)
	}
	if persisted.RefHash != "confirmed-hash" {
		t.Fatalf("ref_hash was overwritten: %q", persisted.RefHash)
	}
}

func TestNotifyClaimPreventsConcurrentDelivery(t *testing.T) {
	db := newTestDB(t, "notify-claim-atomic")
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	now := time.Now()
	order := Order{
		OrderId: "notify-claim-test", TradeId: "notify-claim-trade", TradeType: UsdtTrc20,
		Status: OrderStatusSuccess, NotifyState: OrderNotifyStateFail,
		ExpiredAt: now.Add(time.Hour), ConfirmedAt: &now,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}

	type claimResult struct {
		order Order
		err   error
	}
	start := make(chan struct{})
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func(snapshot Order) {
			defer wg.Done()
			<-start
			claimed, err := snapshot.ClaimNotify(false)
			results <- claimResult{order: claimed, err: err}
		}(order)
	}
	close(start)
	wg.Wait()
	close(results)

	var winner Order
	successes := 0
	failures := 0
	for result := range results {
		if result.err != nil {
			failures++
			continue
		}
		successes++
		winner = result.order
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("claim results: successes=%d failures=%d, want 1/1", successes, failures)
	}

	var persisted Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if persisted.NotifyNum != 1 {
		t.Fatalf("notify_num = %d, want 1", persisted.NotifyNum)
	}
	if persisted.NotifyClaimToken == "" {
		t.Fatal("winning notification was not marked in flight")
	}
	if err := winner.CompleteNotify(false); err != nil {
		t.Fatalf("release failed claim: %v", err)
	}
}

func TestNotifySuccessIsStickyAcrossExpiredClaim(t *testing.T) {
	db := newTestDB(t, "notify-success-sticky")
	now := time.Now()
	order := Order{
		OrderId: "notify-sticky-test", TradeId: "notify-sticky-trade", TradeType: UsdtTrc20,
		Status: OrderStatusSuccess, NotifyState: OrderNotifyStateFail,
		ExpiredAt: now.Add(time.Hour), ConfirmedAt: &now,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}

	first, err := order.ClaimNotify(true)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := db.Model(&Order{}).Where("id = ?", order.ID).
		Update("notify_claim_until", now.Add(-time.Second)).Error; err != nil {
		t.Fatalf("expire first claim: %v", err)
	}

	secondSnapshot := order
	second, err := secondSnapshot.ClaimNotify(true)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if err := second.CompleteNotify(true); err != nil {
		t.Fatalf("complete second claim: %v", err)
	}
	if err := first.CompleteNotify(false); err == nil {
		t.Fatal("stale failed claim should not overwrite a newer completion")
	}

	var persisted Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if persisted.NotifyState != OrderNotifyStateSucc {
		t.Fatalf("notify_state = %d, want success", persisted.NotifyState)
	}
	if persisted.NotifyNum != 2 {
		t.Fatalf("notify_num = %d, want 2", persisted.NotifyNum)
	}
}

func TestMarkConfirmingRejectsCanceledOrder(t *testing.T) {
	db := newTestDB(t, "mark-confirming-guard")
	now := time.Now()
	zero := time.Unix(0, 0)
	order := Order{
		OrderId: "mark-guard-test", TradeId: "t2", TradeType: UsdtTrc20,
		Status: OrderStatusCanceled, Rate: "7", Amount: "1", Money: "7",
		Address: "addr", MatchAddress: "addr", ExpiredAt: now.Add(time.Hour),
		ConfirmedAt: &zero,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}

	err := order.MarkConfirming(100, "0xfrom", "0xhash", now, decimal.NewFromInt(1))
	if err == nil {
		t.Fatal("MarkConfirming should reject canceled order")
	}

	var persisted Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if persisted.Status != OrderStatusCanceled {
		t.Fatalf("status changed from canceled to %d", persisted.Status)
	}
	if persisted.RefHash != "" {
		t.Fatalf("ref_hash was written despite rejection: %q", persisted.RefHash)
	}
}

func TestSetCanceledClosesPendingPaymentReview(t *testing.T) {
	db := newTestDB(t, "cancel-closes-payment-review")
	if err := db.AutoMigrate(&PaymentReview{}); err != nil {
		t.Fatalf("migrate payment review: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	zero := time.Unix(0, 0)
	order := Order{
		OrderId: "cancel-review-order", TradeId: "cancel-review-trade", TradeType: UsdtTrc20,
		Status: OrderStatusWaiting, Rate: "7", Amount: "1", Money: "7", Address: "addr", MatchAddress: "addr",
		ExpiredAt: now.Add(time.Hour), ConfirmedAt: &zero,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	pending := PaymentReview{
		TradeID: order.TradeId, Status: PaymentReviewPending, TransactionHash: "pending-hash", Description: "pending review",
		EvidencePath: "pending.png", EvidenceType: "image/png", EvidenceSize: 1, EvidenceSHA256: "pending-sha",
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatal(err)
	}

	if err := order.SetCanceled(); err != nil {
		t.Fatalf("cancel order: %v", err)
	}
	var review PaymentReview
	if err := db.First(&review, pending.ID).Error; err != nil {
		t.Fatal(err)
	}
	if review.Status != PaymentReviewRejected || review.ReviewedBy != "system" || review.ResolutionNote != "订单已取消，系统自动关闭复核" || review.ReviewedAt == nil {
		t.Fatalf("closed review = %+v", review)
	}
}

func TestSetCanceledRollsBackWhenReviewClosureFails(t *testing.T) {
	db := newTestDB(t, "cancel-review-rollback")
	if err := db.AutoMigrate(&PaymentReview{}); err != nil {
		t.Fatalf("migrate payment review: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	zero := time.Unix(0, 0)
	order := Order{
		OrderId: "cancel-rollback-order", TradeId: "cancel-rollback-trade", TradeType: UsdtTrc20,
		Status: OrderStatusWaiting, Rate: "7", Amount: "1", Money: "7", Address: "addr", MatchAddress: "addr",
		ExpiredAt: now.Add(time.Hour), ConfirmedAt: &zero,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PaymentReview{
		TradeID: order.TradeId, Status: PaymentReviewPending, TransactionHash: "rollback-hash", Description: "rollback review",
		EvidencePath: "rollback.png", EvidenceType: "image/png", EvidenceSize: 1, EvidenceSHA256: "rollback-sha",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_payment_review_update
        BEFORE UPDATE ON bep_payment_review
        BEGIN SELECT RAISE(ABORT, 'forced review closure failure'); END`).Error; err != nil {
		t.Fatal(err)
	}

	if err := order.SetCanceled(); err == nil {
		t.Fatal("expected cancellation to fail")
	}
	var persisted Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Status != OrderStatusWaiting {
		t.Fatalf("order status = %d, want waiting after rollback", persisted.Status)
	}
}

func stateGuardOrder(now time.Time, status int, refHash string) Order {
	createdAt := now.Add(-10 * time.Minute)
	updatedAt := createdAt
	confirmedAt := now.Add(-5 * time.Minute)
	return Order{
		OrderId: "state-guard-order", TradeId: "state-guard-trade", TradeType: UsdtTrc20,
		Status: status, Rate: "7", Amount: "1", Money: "7", Address: "addr", MatchAddress: "addr",
		RefHash: refHash, RefBlockNum: 10, ExpiredAt: now.Add(10 * time.Minute), ConfirmedAt: &confirmedAt,
		AutoTimeAt: AutoTimeAt{CreatedAt: (*Datetime)(&createdAt), UpdatedAt: (*Datetime)(&updatedAt)},
	}
}

func newTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), name+".db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&Order{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	oldDB := Db
	Db = db
	t.Cleanup(func() {
		Db = oldDB
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}
