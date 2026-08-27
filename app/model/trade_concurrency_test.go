package model

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func TestBuildPendingOrderConcurrentRequestsAreIdempotent(t *testing.T) {
	const concurrentRequests = 2

	db := newBuildPendingOrderTestDB(t)
	twoInitialQueriesStarted := make(chan struct{})
	twoOrderQueriesDone := make(chan struct{})
	var initialQueries atomic.Int32
	var completedOrderQueries atomic.Int32
	var initialQuerySignalOnce sync.Once
	var completedQuerySignalOnce sync.Once

	const queryStartCallbackName = "test:coordinate_build_pending_order_initial_queries"
	if err := db.Callback().Query().Before("gorm:query").Register(queryStartCallbackName, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*Order); !ok {
			return
		}

		queryNumber := initialQueries.Add(1)
		if queryNumber > concurrentRequests {
			return
		}
		if queryNumber == concurrentRequests {
			initialQuerySignalOnce.Do(func() { close(twoInitialQueriesStarted) })
		}

		select {
		case <-twoInitialQueriesStarted:
		case <-time.After(5 * time.Second):
			tx.AddError(errors.New("timed out waiting for concurrent initial order lookup"))
		}
	}); err != nil {
		t.Fatalf("register initial query callback: %v", err)
	}

	const queryCallbackName = "test:count_build_pending_order_queries"
	if err := db.Callback().Query().After("gorm:query").Register(queryCallbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != (&Order{}).TableName() {
			return
		}

		if completedOrderQueries.Add(1) >= concurrentRequests {
			completedQuerySignalOnce.Do(func() { close(twoOrderQueriesDone) })
		}
	}); err != nil {
		t.Fatalf("register query callback: %v", err)
	}

	const createCallbackName = "test:wait_for_concurrent_build_pending_order_queries"
	if err := db.Callback().Create().Before("gorm:create").Register(createCallbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != (&Order{}).TableName() || completedOrderQueries.Load() >= concurrentRequests {
			return
		}

		select {
		case <-twoOrderQueriesDone:
		case <-time.After(5 * time.Second):
			tx.AddError(errors.New("timed out waiting for concurrent order lookup"))
		}
	}); err != nil {
		t.Fatalf("register create callback: %v", err)
	}

	type result struct {
		order Order
		err   error
	}
	results := make(chan result, concurrentRequests)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(concurrentRequests)

	params := OrderParams{
		Money:   decimal.Zero,
		ApiType: OrderApiTypeEpusdtOrder,
		OrderId: "concurrent-pending-order",
		Fiat:    CNY,
		Timeout: 600,
	}
	for range concurrentRequests {
		go func() {
			ready.Done()
			<-start
			order, err := BuildPendingOrder(params)
			results <- result{order: order, err: err}
		}()
	}

	ready.Wait()
	close(start)

	returned := make([]Order, 0, concurrentRequests)
	for range concurrentRequests {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatalf("BuildPendingOrder returned error: %v", got.err)
			}
			returned = append(returned, got.order)
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent BuildPendingOrder call did not return")
		}
	}

	if initialQueries.Load() < concurrentRequests {
		t.Fatalf("observed %d initial order queries, want at least %d", initialQueries.Load(), concurrentRequests)
	}
	if returned[0].ID != returned[1].ID || returned[0].TradeId != returned[1].TradeId {
		t.Fatalf("concurrent requests returned different orders: (%d, %q) and (%d, %q)",
			returned[0].ID, returned[0].TradeId, returned[1].ID, returned[1].TradeId)
	}

	var stored []Order
	if err := db.Where("order_id = ?", params.OrderId).Find(&stored).Error; err != nil {
		t.Fatalf("reload pending orders: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("stored %d orders for order_id %q, want 1", len(stored), params.OrderId)
	}
	if stored[0].ID != returned[0].ID || stored[0].TradeId != returned[0].TradeId {
		t.Fatalf("stored order (%d, %q) differs from returned order (%d, %q)",
			stored[0].ID, stored[0].TradeId, returned[0].ID, returned[0].TradeId)
	}
}

func TestBuildPendingOrderLookupErrorDoesNotCreateOrder(t *testing.T) {
	tests := []struct {
		name        string
		failOnQuery int32
	}{
		{name: "initial lookup", failOnQuery: 1},
		{name: "locked lookup", failOnQuery: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newBuildPendingOrderTestDB(t)
			lookupErr := errors.New("injected order lookup failure")
			var orderQueries atomic.Int32

			callbackName := "test:fail_build_pending_order_lookup"
			if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if _, ok := tx.Statement.Dest.(*Order); !ok {
					return
				}
				if orderQueries.Add(1) == tt.failOnQuery {
					tx.AddError(lookupErr)
				}
			}); err != nil {
				t.Fatalf("register failing query callback: %v", err)
			}

			params := OrderParams{
				Money:   decimal.Zero,
				ApiType: OrderApiTypeEpusdtOrder,
				OrderId: "lookup-error-pending-order",
				Fiat:    CNY,
				Timeout: 600,
			}
			if _, err := BuildPendingOrder(params); !errors.Is(err, lookupErr) {
				t.Fatalf("BuildPendingOrder error = %v, want %v", err, lookupErr)
			}

			var count int64
			if err := db.Model(&Order{}).Where("order_id = ?", params.OrderId).Count(&count).Error; err != nil {
				t.Fatalf("count pending orders: %v", err)
			}
			if count != 0 {
				t.Fatalf("stored %d orders after lookup failure, want 0", count)
			}
		})
	}
}

func TestStartBuildOrderLookupErrorDoesNotCreateOrder(t *testing.T) {
	tests := []struct {
		name        string
		failOnQuery int32
	}{
		{name: "initial lookup", failOnQuery: 1},
		{name: "locked lookup", failOnQuery: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newBuildPendingOrderTestDB(t)
			lookupErr := errors.New("injected order lookup failure")
			var orderQueries atomic.Int32

			callbackName := "test:fail_start_build_order_lookup"
			if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if _, ok := tx.Statement.Dest.(*Order); !ok {
					return
				}
				if orderQueries.Add(1) == tt.failOnQuery {
					tx.AddError(lookupErr)
				}
			}); err != nil {
				t.Fatalf("register failing query callback: %v", err)
			}

			params := OrderParams{
				Money:         decimal.NewFromInt(1),
				ApiType:       OrderApiTypeEpusdt,
				OrderId:       "lookup-error-start-order",
				TradeType:     UsdtTrc20,
				Fiat:          CNY,
				Timeout:       600,
				AddressLocked: true,
			}
			if _, err := StartBuildOrder(params); !errors.Is(err, lookupErr) {
				t.Fatalf("StartBuildOrder error = %v, want %v", err, lookupErr)
			}

			var count int64
			if err := db.Model(&Order{}).Where("order_id = ?", params.OrderId).Count(&count).Error; err != nil {
				t.Fatalf("count orders: %v", err)
			}
			if count != 0 {
				t.Fatalf("stored %d orders after lookup failure, want 0", count)
			}
		})
	}
}

func TestStartBuildOrderRechecksLatestOrderAfterLock(t *testing.T) {
	db := newBuildPendingOrderTestDB(t)
	now := time.Now()
	zero := time.Unix(0, 0)
	expired := Order{
		OrderId:     "start-order-recheck",
		TradeId:     "expired-trade",
		TradeType:   UsdtTrc20,
		Fiat:        CNY,
		Crypto:      USDT,
		Status:      OrderStatusExpired,
		ExpiredAt:   now.Add(-time.Minute),
		ConfirmedAt: &zero,
	}
	if err := db.Create(&expired).Error; err != nil {
		t.Fatalf("seed expired order: %v", err)
	}

	current := Order{
		OrderId:     expired.OrderId,
		TradeId:     "current-trade",
		TradeType:   UsdtTrc20,
		Fiat:        CNY,
		Crypto:      USDT,
		Money:       "1",
		Status:      OrderStatusWaiting,
		ExpiredAt:   now.Add(10 * time.Minute),
		ConfirmedAt: &zero,
	}
	var orderQueries atomic.Int32
	const callbackName = "test:insert_latest_order_before_locked_lookup"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*Order); !ok || orderQueries.Add(1) != 2 {
			return
		}
		if err := db.Create(&current).Error; err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatalf("register latest order callback: %v", err)
	}

	returned, err := StartBuildOrder(OrderParams{
		Money:         decimal.NewFromInt(1),
		ApiType:       OrderApiTypeEpusdt,
		OrderId:       expired.OrderId,
		TradeType:     UsdtTrc20,
		Fiat:          CNY,
		Timeout:       600,
		AddressLocked: true,
	})
	if err != nil {
		t.Fatalf("StartBuildOrder: %v", err)
	}
	if returned.ID != current.ID || returned.TradeId != current.TradeId {
		t.Fatalf("returned order = (%d, %q), want latest (%d, %q)", returned.ID, returned.TradeId, current.ID, current.TradeId)
	}

	var count int64
	if err := db.Model(&Order{}).Where("order_id = ?", expired.OrderId).Count(&count).Error; err != nil {
		t.Fatalf("count orders: %v", err)
	}
	if count != 2 {
		t.Fatalf("stored %d orders, want one expired and one current order", count)
	}
}

func TestLockTradeAddressLookupErrorDoesNotAllocateWallet(t *testing.T) {
	db := newBuildPendingOrderTestDB(t)
	lookupErr := errors.New("injected address lock lookup failure")

	const callbackName = "test:fail_lock_trade_address_lookup"
	if err := db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*Order); ok {
			tx.AddError(lookupErr)
		}
	}); err != nil {
		t.Fatalf("register failing query callback: %v", err)
	}

	wallet := Wallet{
		Address:   "0x1111111111111111111111111111111111111111",
		MatchAddr: "0x1111111111111111111111111111111111111111",
		TradeType: string(UsdtPolygon),
	}
	allocated, _, err := LockTradeAddress([]Wallet{wallet}, UsdtPolygon)
	if !errors.Is(err, lookupErr) {
		t.Fatalf("LockTradeAddress error = %v, want %v", err, lookupErr)
	}
	if allocated.GetMatchAddr() != "" {
		t.Fatalf("allocated wallet %q after lookup failure", allocated.GetMatchAddr())
	}
}

func newBuildPendingOrderTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "build-pending-order-test.db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?cache=shared&mode=rwc&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&Order{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	oldDB := Db
	Db = db
	t.Cleanup(func() {
		Db = oldDB
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close test db: %v", err)
		}
	})

	return db
}
