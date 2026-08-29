package model

import (
	"errors"
	"fmt"
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

func TestMarkConfirmingClaimsTransactionOnlyOnce(t *testing.T) {
	db := newTestDB(t, "mark-confirming-transaction-claim")
	now := time.Now()
	first := stateGuardOrder(now, OrderStatusWaiting, "first-pending-reference")
	first.OrderId = "first-order"
	first.TradeId = "first-trade"
	second := stateGuardOrder(now, OrderStatusWaiting, "second-pending-reference")
	second.OrderId = "second-order"
	second.TradeId = "second-trade"
	if err := db.Create(&first).Error; err != nil {
		t.Fatalf("create first order: %v", err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatalf("create second order: %v", err)
	}

	const txHash = "0xABCDEF"
	if err := first.MarkConfirming(100, "first-sender", txHash, now, decimal.NewFromInt(1)); err != nil {
		t.Fatalf("first order claim: %v", err)
	}
	if err := second.MarkConfirming(100, "second-sender", txHash, now, decimal.NewFromInt(1)); !errors.Is(err, ErrPaymentTransactionAlreadyClaimed) {
		t.Fatalf("second order claim error = %v, want ErrPaymentTransactionAlreadyClaimed", err)
	}

	var persisted []Order
	if err := db.Order("id asc").Find(&persisted).Error; err != nil {
		t.Fatalf("load orders: %v", err)
	}
	if len(persisted) != 2 {
		t.Fatalf("order count = %d, want 2", len(persisted))
	}
	if persisted[0].Status != OrderStatusConfirming || persisted[0].RefHash != "0xabcdef" {
		t.Fatalf("first order status/ref = %d/%q", persisted[0].Status, persisted[0].RefHash)
	}
	if persisted[1].Status != OrderStatusWaiting || persisted[1].RefHash != "second-pending-reference" {
		t.Fatalf("second order changed: status/ref = %d/%q", persisted[1].Status, persisted[1].RefHash)
	}

	var claimCount int64
	if err := db.Model(&PaymentTransactionClaim{}).Count(&claimCount).Error; err != nil {
		t.Fatalf("count transaction claims: %v", err)
	}
	if claimCount != 1 {
		t.Fatalf("transaction claim count = %d, want 1", claimCount)
	}
}

func TestMarkConfirmingConcurrentClaimsHaveOneWinner(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mark-confirming-concurrent-transaction-claim.db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?mode=rwc&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open concurrent test db: %v", err)
	}
	if err := db.AutoMigrate(&Order{}, &PaymentTransactionClaim{}); err != nil {
		t.Fatalf("migrate concurrent test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(4)
	oldDB := Db
	Db = db
	t.Cleanup(func() {
		Db = oldDB
		_ = sqlDB.Close()
	})

	now := time.Now()
	orders := []Order{
		stateGuardOrder(now, OrderStatusWaiting, "first-pending-reference"),
		stateGuardOrder(now, OrderStatusWaiting, "second-pending-reference"),
	}
	for i := range orders {
		orders[i].OrderId = fmt.Sprintf("concurrent-order-%d", i)
		orders[i].TradeId = fmt.Sprintf("concurrent-trade-%d", i)
		if err := db.Create(&orders[i]).Error; err != nil {
			t.Fatalf("create order %d: %v", i, err)
		}
	}

	start := make(chan struct{})
	results := make(chan error, len(orders))
	var wg sync.WaitGroup
	for i := range orders {
		wg.Add(1)
		go func(order *Order) {
			defer wg.Done()
			<-start
			results <- order.MarkConfirming(100, "sender", "0xABCDEF", now, decimal.NewFromInt(1))
		}(&orders[i])
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	conflicts := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrPaymentTransactionAlreadyClaimed):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent claim error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("claim results: successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}

	var confirmingCount int64
	if err := db.Model(&Order{}).Where("status = ? AND ref_hash = ?", OrderStatusConfirming, "0xabcdef").Count(&confirmingCount).Error; err != nil {
		t.Fatalf("count confirming orders: %v", err)
	}
	if confirmingCount != 1 {
		t.Fatalf("confirming order count = %d, want 1", confirmingCount)
	}

	var claimCount int64
	if err := db.Model(&PaymentTransactionClaim{}).Where("network = ? AND tx_hash = ?", "tron", "0xabcdef").Count(&claimCount).Error; err != nil {
		t.Fatalf("count transaction claims: %v", err)
	}
	if claimCount != 1 {
		t.Fatalf("transaction claim count = %d, want 1", claimCount)
	}
}

func TestMarkConfirmingRejectsLegacyTransactionReference(t *testing.T) {
	db := newTestDB(t, "mark-confirming-legacy-claim")
	now := time.Now()
	legacy := stateGuardOrder(now, OrderStatusSuccess, "0xABCDEF")
	legacy.OrderId = "legacy-order"
	legacy.TradeId = "legacy-trade"
	pending := stateGuardOrder(now, OrderStatusWaiting, "pending-reference")
	pending.OrderId = "pending-order"
	pending.TradeId = "pending-trade"
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("create legacy order: %v", err)
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("create pending order: %v", err)
	}

	err := pending.MarkConfirming(100, "sender", "0xabcdef", now, decimal.NewFromInt(1))
	if !errors.Is(err, ErrPaymentTransactionAlreadyClaimed) {
		t.Fatalf("legacy duplicate error = %v, want ErrPaymentTransactionAlreadyClaimed", err)
	}

	var claimCount int64
	if err := db.Model(&PaymentTransactionClaim{}).Count(&claimCount).Error; err != nil {
		t.Fatalf("count transaction claims: %v", err)
	}
	if claimCount != 0 {
		t.Fatalf("legacy duplicate created %d claim rows, want 0", claimCount)
	}
}

func TestMarkConfirmingIgnoresWaitingOrderReferencePlaceholder(t *testing.T) {
	db := newTestDB(t, "mark-confirming-waiting-reference-placeholder")
	now := time.Now()
	placeholder := stateGuardOrder(now, OrderStatusWaiting, "0xABCDEF")
	placeholder.OrderId = "placeholder-order"
	placeholder.TradeId = "placeholder-trade"
	pending := stateGuardOrder(now, OrderStatusWaiting, "pending-reference")
	pending.OrderId = "pending-order"
	pending.TradeId = "pending-trade"
	if err := db.Create(&placeholder).Error; err != nil {
		t.Fatalf("create placeholder order: %v", err)
	}
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("create pending order: %v", err)
	}

	if err := pending.MarkConfirming(100, "sender", "0xabcdef", now, decimal.NewFromInt(1)); err != nil {
		t.Fatalf("waiting placeholder blocked real transaction claim: %v", err)
	}

	var persisted Order
	if err := db.First(&persisted, pending.ID).Error; err != nil {
		t.Fatalf("reload pending order: %v", err)
	}
	if persisted.Status != OrderStatusConfirming || persisted.RefHash != "0xabcdef" {
		t.Fatalf("pending order status/ref = %d/%q", persisted.Status, persisted.RefHash)
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

	var claimCount int64
	if err := db.Model(&PaymentTransactionClaim{}).Count(&claimCount).Error; err != nil {
		t.Fatalf("count transaction claims: %v", err)
	}
	if claimCount != 0 {
		t.Fatalf("rejected order left %d transaction claims, want 0", claimCount)
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
	if err := db.AutoMigrate(&Order{}, &PaymentTransactionClaim{}); err != nil {
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
