package model

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func newExchangeTransactionTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "exchange.db")
	db, err := gorm.Open(sqlite.Open(path+"?cache=shared&mode=rwc&_pragma=busy_timeout(1000)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&Order{}, &ExchangeTransaction{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	previous := Db
	Db = db
	t.Cleanup(func() {
		Db = previous
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	return db
}

func newExchangeTestOrder(now time.Time) Order {
	createdAt := Datetime(now.Add(-time.Minute))
	updatedAt := Datetime(now.Add(-time.Minute))
	confirmedAt := now.Add(-time.Minute)

	return Order{
		OrderId:      "merchant-order-1",
		TradeId:      "trade-1",
		TradeType:    UsdtOKX,
		Fiat:         CNY,
		Crypto:       USDT,
		Rate:         "1",
		Amount:       "10.0001",
		Money:        "10.0001",
		Address:      "123456789",
		MatchAddress: "123456789",
		Status:       OrderStatusWaiting,
		RefHash:      "trade-1",
		ApiType:      OrderApiTypeEpusdtOrder,
		ExpiredAt:    now.Add(time.Hour),
		ConfirmedAt:  &confirmedAt,
		AutoTimeAt:   AutoTimeAt{CreatedAt: &createdAt, UpdatedAt: &updatedAt},
	}
}

func newExchangeTestTransaction(now time.Time, transactionID string) ExchangeTransaction {
	createdAt := Datetime(now)
	updatedAt := Datetime(now)

	return ExchangeTransaction{
		Provider:      "okx",
		TransactionID: transactionID,
		TradeType:     UsdtOKX,
		Asset:         "USDT",
		Amount:        "10.0001",
		ReceiverUID:   "123456789",
		OccurredAt:    now,
		Status:        ExchangeTransactionPending,
		AutoTimeAt:    AutoTimeAt{CreatedAt: &createdAt, UpdatedAt: &updatedAt},
	}
}

func TestPendingExchangeTransactionsRotatesPastBatchLimit(t *testing.T) {
	db := newExchangeTransactionTestDB(t)
	start := time.Now().UTC().Add(-12 * time.Hour).Truncate(time.Second)
	rows := make([]ExchangeTransaction, 0, 501)
	for i := 0; i < 500; i++ {
		rows = append(rows, newExchangeTestTransaction(start.Add(time.Duration(i)*time.Second), fmt.Sprintf("old-%03d", i)))
	}
	rows = append(rows, newExchangeTestTransaction(start.Add(501*time.Second), "newest"))
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("create transactions: %v", err)
	}

	pending, cursor, err := PendingExchangeTransactions("okx", UsdtOKX, 0, 500)
	if err != nil {
		t.Fatalf("query pending transactions: %v", err)
	}
	if len(pending) != 500 {
		t.Fatalf("pending count = %d, want 500", len(pending))
	}
	if pending[0].TransactionID != "old-000" {
		t.Fatalf("first pending transaction = %q, want oldest unvisited row", pending[0].TransactionID)
	}
	if cursor == 0 {
		t.Fatal("first full page did not advance the pending cursor")
	}

	next, cursor, err := PendingExchangeTransactions("okx", UsdtOKX, cursor, 500)
	if err != nil {
		t.Fatalf("query second pending page: %v", err)
	}
	if len(next) != 1 || next[0].TransactionID != "newest" {
		t.Fatalf("second pending page = %#v, want newest transaction", next)
	}
	if cursor != 0 {
		t.Fatalf("cursor after final partial page = %d, want wrap to zero", cursor)
	}
}

func TestPendingExchangeTransactionsBatchMarksLinkedOrders(t *testing.T) {
	db := newExchangeTransactionTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	firstOrder := newExchangeTestOrder(now)
	firstOrder.OrderId = "merchant-order-batch-1"
	firstOrder.TradeId = "trade-batch-1"
	firstOrder.Status = OrderStatusConfirming
	firstOrder.RefHash = "bill-batch-1"
	secondOrder := newExchangeTestOrder(now)
	secondOrder.OrderId = "merchant-order-batch-2"
	secondOrder.TradeId = "trade-batch-2"
	secondOrder.Status = OrderStatusSuccess
	secondOrder.RefHash = "bill-batch-2"
	linkedOrders := []Order{firstOrder, secondOrder}
	if err := db.Create(&linkedOrders).Error; err != nil {
		t.Fatalf("create linked orders: %v", err)
	}

	rows := []ExchangeTransaction{
		newExchangeTestTransaction(now, "bill-batch-1"),
		newExchangeTestTransaction(now.Add(time.Second), "bill-batch-2"),
		newExchangeTestTransaction(now.Add(2*time.Second), "bill-unmatched"),
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("create transactions: %v", err)
	}

	pending, _, err := PendingExchangeTransactions("okx", UsdtOKX, 0, 10)
	if err != nil {
		t.Fatalf("query pending transactions: %v", err)
	}
	if len(pending) != 1 || pending[0].TransactionID != "bill-unmatched" {
		t.Fatalf("unexpected pending transactions: %#v", pending)
	}

	var stored []ExchangeTransaction
	if err := db.Order("id asc").Find(&stored).Error; err != nil {
		t.Fatalf("reload transactions: %v", err)
	}
	if stored[0].Status != ExchangeTransactionProcessed || stored[0].OrderID != linkedOrders[0].ID {
		t.Fatalf("first transaction status/order=%d/%d", stored[0].Status, stored[0].OrderID)
	}
	if stored[1].Status != ExchangeTransactionProcessed || stored[1].OrderID != linkedOrders[1].ID {
		t.Fatalf("second transaction status/order=%d/%d", stored[1].Status, stored[1].OrderID)
	}
	if stored[2].Status != ExchangeTransactionPending || stored[2].OrderID != 0 {
		t.Fatalf("unmatched transaction status/order=%d/%d", stored[2].Status, stored[2].OrderID)
	}
}

func TestPendingExchangeTransactionsIsolatesTradeTypes(t *testing.T) {
	db := newExchangeTransactionTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	usdt := newExchangeTestTransaction(now, "bill-usdt")
	usdc := newExchangeTestTransaction(now.Add(time.Second), "bill-usdc")
	usdc.TradeType = UsdcOKX
	usdc.Asset = "USDC"
	rows := []ExchangeTransaction{usdt, usdc}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("create transactions: %v", err)
	}

	pendingUSDT, cursor, err := PendingExchangeTransactions("okx", UsdtOKX, 0, 10)
	if err != nil {
		t.Fatalf("query USDT transactions: %v", err)
	}
	if len(pendingUSDT) != 1 || pendingUSDT[0].TransactionID != "bill-usdt" {
		t.Fatalf("USDT query returned %#v", pendingUSDT)
	}
	if cursor != 0 {
		t.Fatalf("USDT cursor = %d, want 0 for its final page", cursor)
	}

	pendingUSDC, _, err := PendingExchangeTransactions("okx", UsdcOKX, 0, 10)
	if err != nil {
		t.Fatalf("query USDC transactions: %v", err)
	}
	if len(pendingUSDC) != 1 || pendingUSDC[0].TransactionID != "bill-usdc" {
		t.Fatalf("USDC query returned %#v", pendingUSDC)
	}
}

func TestMarkExchangeTransactionUnmatchedStopsPendingRedispatch(t *testing.T) {
	db := newExchangeTransactionTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	row := newExchangeTestTransaction(now, "bill-unmatched-final")
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create transaction: %v", err)
	}

	if err := MarkExchangeTransactionUnmatched(row.Provider, row.TransactionID); err != nil {
		t.Fatalf("mark transaction unmatched: %v", err)
	}

	var stored ExchangeTransaction
	if err := db.First(&stored, row.ID).Error; err != nil {
		t.Fatalf("reload transaction: %v", err)
	}
	if stored.Status != ExchangeTransactionProcessed || stored.OrderID != 0 {
		t.Fatalf("unmatched transaction status/order = %d/%d, want %d/0", stored.Status, stored.OrderID, ExchangeTransactionProcessed)
	}
	pending, cursor, err := PendingExchangeTransactions(row.Provider, row.TradeType, 0, 10)
	if err != nil {
		t.Fatalf("query pending transactions: %v", err)
	}
	if len(pending) != 0 || cursor != 0 {
		t.Fatalf("pending transactions after unmatched resolution = %d/%d, want 0/0", len(pending), cursor)
	}
}

func TestMarkExchangeTransactionUnmatchedDoesNotClearExistingClaim(t *testing.T) {
	db := newExchangeTransactionTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	row := newExchangeTestTransaction(now, "bill-already-claimed")
	row.Status = ExchangeTransactionProcessed
	row.OrderID = 77
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create claimed transaction: %v", err)
	}

	if err := MarkExchangeTransactionUnmatched(row.Provider, row.TransactionID); err != nil {
		t.Fatalf("mark claimed transaction unmatched: %v", err)
	}

	var stored ExchangeTransaction
	if err := db.First(&stored, row.ID).Error; err != nil {
		t.Fatalf("reload claimed transaction: %v", err)
	}
	if stored.Status != ExchangeTransactionProcessed || stored.OrderID != row.OrderID {
		t.Fatalf("claimed transaction status/order = %d/%d, want %d/%d", stored.Status, stored.OrderID, ExchangeTransactionProcessed, row.OrderID)
	}
}

func TestDeleteExchangeTransactionsBeforeBoundsPendingRetention(t *testing.T) {
	db := newExchangeTransactionTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	cutoff := now.Add(-7 * 24 * time.Hour)
	oldCreatedAt := Datetime(cutoff.Add(-time.Hour))
	oldUpdatedAt := oldCreatedAt

	stalePending := newExchangeTestTransaction(now.Add(-time.Hour), "bill-stale-pending")
	stalePending.CreatedAt = &oldCreatedAt
	stalePending.UpdatedAt = &oldUpdatedAt
	freshRecovered := newExchangeTestTransaction(now.Add(-30*24*time.Hour), "bill-fresh-recovered")
	freshCreatedAt := Datetime(now)
	freshUpdatedAt := freshCreatedAt
	freshRecovered.CreatedAt = &freshCreatedAt
	freshRecovered.UpdatedAt = &freshUpdatedAt
	oldProcessed := newExchangeTestTransaction(now.Add(-30*24*time.Hour), "bill-old-processed")
	oldProcessed.Status = ExchangeTransactionProcessed
	freshProcessed := newExchangeTestTransaction(now, "bill-fresh-processed")
	freshProcessed.Status = ExchangeTransactionProcessed
	rows := []ExchangeTransaction{stalePending, freshRecovered, oldProcessed, freshProcessed}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("create cleanup transactions: %v", err)
	}

	if err := DeleteExchangeTransactionsBefore(cutoff); err != nil {
		t.Fatalf("delete old transactions: %v", err)
	}

	var stored []ExchangeTransaction
	if err := db.Order("id asc").Find(&stored).Error; err != nil {
		t.Fatalf("reload cleanup transactions: %v", err)
	}
	if len(stored) != 2 || stored[0].TransactionID != freshRecovered.TransactionID || stored[1].TransactionID != freshProcessed.TransactionID {
		t.Fatalf("remaining transactions = %#v, want fresh pending and processed transactions", stored)
	}
}

func TestCompleteExchangeTransactionCommitsOrderAndClaimTogether(t *testing.T) {
	db := newExchangeTransactionTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	order := newExchangeTestOrder(now)
	row := newExchangeTestTransaction(now, "bill-1")
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create transaction: %v", err)
	}

	amount := decimal.RequireFromString(row.Amount)
	if err := CompleteExchangeTransaction(&order, row.Provider, row.TransactionID, 123, "okx-pay", now, amount); err != nil {
		t.Fatalf("complete exchange transaction: %v", err)
	}

	var storedOrder Order
	if err := db.First(&storedOrder, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if storedOrder.Status != OrderStatusSuccess || storedOrder.RefHash != row.TransactionID {
		t.Fatalf("stored order status/ref = %d/%q, want %d/%q", storedOrder.Status, storedOrder.RefHash, OrderStatusSuccess, row.TransactionID)
	}
	if storedOrder.FromAddress != "okx-pay" || storedOrder.RefBlockNum != 123 {
		t.Fatalf("stored order transfer metadata = %q/%d", storedOrder.FromAddress, storedOrder.RefBlockNum)
	}

	var storedRow ExchangeTransaction
	if err := db.First(&storedRow, row.ID).Error; err != nil {
		t.Fatalf("reload transaction: %v", err)
	}
	if storedRow.Status != ExchangeTransactionProcessed || storedRow.OrderID != order.ID {
		t.Fatalf("stored transaction status/order = %d/%d, want %d/%d", storedRow.Status, storedRow.OrderID, ExchangeTransactionProcessed, order.ID)
	}
}

func TestCompleteExchangeTransactionRollsBackClaimWhenOrderUpdateFails(t *testing.T) {
	db := newExchangeTransactionTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	order := newExchangeTestOrder(now)
	row := newExchangeTestTransaction(now, "bill-rollback")
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create transaction: %v", err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_order_success
		BEFORE UPDATE OF status ON bep_order
		WHEN NEW.status = 2
		BEGIN
			SELECT RAISE(ABORT, 'forced order failure');
		END`).Error; err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	err := CompleteExchangeTransaction(&order, row.Provider, row.TransactionID, 123, "okx-pay", now, decimal.RequireFromString(row.Amount))
	if err == nil {
		t.Fatal("complete exchange transaction unexpectedly succeeded")
	}

	var storedOrder Order
	if err := db.First(&storedOrder, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if storedOrder.Status != OrderStatusWaiting || storedOrder.RefHash != order.TradeId {
		t.Fatalf("order changed after rollback: status/ref = %d/%q", storedOrder.Status, storedOrder.RefHash)
	}

	var storedRow ExchangeTransaction
	if err := db.First(&storedRow, row.ID).Error; err != nil {
		t.Fatalf("reload transaction: %v", err)
	}
	if storedRow.Status != ExchangeTransactionPending || storedRow.OrderID != 0 {
		t.Fatalf("transaction claim survived rollback: status/order = %d/%d", storedRow.Status, storedRow.OrderID)
	}
}

func TestCompleteExchangeTransactionRejectsASecondClaim(t *testing.T) {
	db := newExchangeTransactionTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	order := newExchangeTestOrder(now)
	row := newExchangeTestTransaction(now, "bill-claimed")
	row.Status = ExchangeTransactionProcessed
	row.OrderID = 999
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create transaction: %v", err)
	}

	err := CompleteExchangeTransaction(&order, row.Provider, row.TransactionID, 123, "okx-pay", now, decimal.RequireFromString(row.Amount))
	if !errors.Is(err, ErrExchangeTransactionNotPending) {
		t.Fatalf("complete error = %v, want %v", err, ErrExchangeTransactionNotPending)
	}

	var storedOrder Order
	if err := db.First(&storedOrder, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if storedOrder.Status != OrderStatusWaiting || storedOrder.RefHash != order.TradeId {
		t.Fatalf("order changed after rejected claim: status/ref = %d/%q", storedOrder.Status, storedOrder.RefHash)
	}
}

func TestCompleteExchangeTransactionKeepsDifferentConfirmingTransfer(t *testing.T) {
	db := newExchangeTransactionTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	order := newExchangeTestOrder(now)
	order.Status = OrderStatusConfirming
	order.RefHash = "bill-original"
	row := newExchangeTestTransaction(now, "bill-new")
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("create transaction: %v", err)
	}

	err := CompleteExchangeTransaction(&order, row.Provider, row.TransactionID, 123, "okx-pay", now, decimal.RequireFromString(row.Amount))
	if !errors.Is(err, ErrExchangeOrderNotReceivable) {
		t.Fatalf("complete error = %v, want %v", err, ErrExchangeOrderNotReceivable)
	}

	var storedOrder Order
	if err := db.First(&storedOrder, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if storedOrder.Status != OrderStatusConfirming || storedOrder.RefHash != "bill-original" {
		t.Fatalf("confirming order was replaced: status/ref = %d/%q", storedOrder.Status, storedOrder.RefHash)
	}

	var storedRow ExchangeTransaction
	if err := db.First(&storedRow, row.ID).Error; err != nil {
		t.Fatalf("reload transaction: %v", err)
	}
	if storedRow.Status != ExchangeTransactionPending || storedRow.OrderID != 0 {
		t.Fatalf("new transaction was claimed: status/order = %d/%d", storedRow.Status, storedRow.OrderID)
	}
}
