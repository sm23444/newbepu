package model

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func newExchangeReselectionTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "exchange-reselection-test.db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?cache=shared&mode=rwc&_pragma=journal_mode(WAL)&_pragma=busy_timeout(1000)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&Order{}, &Wallet{}, &Rate{}, &Conf{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
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

func setExchangeReselectionTestConf(t *testing.T, key ConfKey, value string) {
	t.Helper()

	oldSnapshot := snapshotConfCache()
	updated := snapshotConfCache()
	updated[key] = value
	restoreConfCache(updated)
	t.Cleanup(func() {
		restoreConfCache(oldSnapshot)
	})
}

func setConfiguredBinanceForTest(t *testing.T, uid string) {
	t.Helper()
	setExchangeReselectionTestConf(t, ExchangeBinanceEnabled, "1")
	setExchangeReselectionTestConf(t, ExchangeBinanceAPIKey, "binance-key")
	setExchangeReselectionTestConf(t, ExchangeBinanceSecretKey, "binance-secret")
	setExchangeReselectionTestConf(t, ExchangeBinanceUID, uid)
}

func setConfiguredOKXForTest(t *testing.T, uid string) {
	t.Helper()
	setExchangeReselectionTestConf(t, ExchangeOKXEnabled, "1")
	setExchangeReselectionTestConf(t, ExchangeOKXAPIKey, "okx-key")
	setExchangeReselectionTestConf(t, ExchangeOKXSecretKey, "okx-secret")
	setExchangeReselectionTestConf(t, ExchangeOKXPassphrase, "okx-passphrase")
	setExchangeReselectionTestConf(t, ExchangeOKXUID, uid)
}

func newPendingReselectionOrder(id string) Order {
	now := time.Now()
	confirmedAt := now

	return Order{
		OrderId:     "merchant-" + id,
		TradeId:     "trade-" + id,
		Fiat:        CNY,
		Crypto:      USDT,
		Rate:        "0",
		Amount:      "0",
		Money:       "70",
		Status:      OrderStatusWaiting,
		ApiType:     OrderApiTypeEpusdtOrder,
		ExpiredAt:   now.Add(20 * time.Minute),
		ConfirmedAt: &confirmedAt,
		AutoTimeAt: AutoTimeAt{
			CreatedAt: (*Datetime)(&now),
			UpdatedAt: (*Datetime)(&now),
		},
	}
}

func TestExchangePaymentCannotBeReselected(t *testing.T) {
	for _, tradeType := range []TradeType{UsdtBinance, UsdtOKX, UsdcBinance, UsdcOKX} {
		order := newPendingReselectionOrder(string(tradeType))
		order.TradeType = tradeType
		if order.CanReselectPayment() {
			t.Fatalf("%s order must not allow payment reselection", tradeType)
		}
	}

	chainOrder := newPendingReselectionOrder("chain")
	chainOrder.TradeType = UsdtTrc20
	if !chainOrder.CanReselectPayment() {
		t.Fatal("eligible chain order should retain existing reselection behavior")
	}
}

func TestRebuildOrderRejectsStaleRequestAfterExchangeSelection(t *testing.T) {
	db := newExchangeReselectionTestDB(t)

	selected := newPendingReselectionOrder("selected")
	selected.TradeType = UsdtBinance
	selected.Rate = "7"
	selected.Amount = "10.0001"
	selected.Address = "123456"
	selected.MatchAddress = "123456"
	if err := db.Create(&selected).Error; err != nil {
		t.Fatalf("seed selected order: %v", err)
	}

	stale := selected
	stale.TradeType = ""
	stale.Rate = "0"
	stale.Amount = "0"
	stale.Address = ""
	stale.MatchAddress = ""

	_, err := RebuildOrder(stale, OrderParams{
		OrderId:   selected.OrderId,
		TradeType: UsdtTrc20,
		Money:     decimal.NewFromInt(70),
		Fiat:      CNY,
	})
	if err == nil || !strings.Contains(err.Error(), "exchange payment method cannot be reselected") {
		t.Fatalf("expected exchange reselection rejection, got %v", err)
	}

	var persisted Order
	if err := db.First(&persisted, selected.ID).Error; err != nil {
		t.Fatalf("reload selected order: %v", err)
	}
	if persisted.TradeType != UsdtBinance || persisted.Address != "123456" || persisted.Amount != "10.0001" {
		t.Fatalf("selected exchange payment was overwritten: %+v", persisted)
	}
}

func TestConcurrentExchangeSelectionAllocatesUniqueAmounts(t *testing.T) {
	db := newExchangeReselectionTestDB(t)
	setConfiguredBinanceForTest(t, "123456")
	setExchangeReselectionTestConf(t, PaymentLookbackHour, "3")

	if err := db.Create(&Conf{K: AtomExchangeUSDT, V: "0.0001"}).Error; err != nil {
		t.Fatalf("seed exchange atomicity: %v", err)
	}
	if err := db.Create(&Wallet{
		Name:      "Binance Pay",
		Status:    WaStatusEnable,
		Address:   "123456",
		MatchAddr: "123456",
		TradeType: string(UsdtBinance),
	}).Error; err != nil {
		t.Fatalf("seed exchange wallet: %v", err)
	}
	if err := db.Create(&Rate{Rate: "7", RawRate: 7, Fiat: string(CNY), Crypto: string(USDT)}).Error; err != nil {
		t.Fatalf("seed rate: %v", err)
	}

	orders := []Order{newPendingReselectionOrder("one"), newPendingReselectionOrder("two")}
	for i := range orders {
		if err := db.Create(&orders[i]).Error; err != nil {
			t.Fatalf("seed pending order %d: %v", i, err)
		}
	}

	start := make(chan struct{})
	results := make(chan error, len(orders))
	var ready sync.WaitGroup
	ready.Add(len(orders))
	for i := range orders {
		order := orders[i]
		go func() {
			ready.Done()
			<-start
			_, err := RebuildOrder(order, OrderParams{
				OrderId:           order.OrderId,
				TradeType:         UsdtBinance,
				Money:             decimal.NewFromInt(70),
				Fiat:              CNY,
				Timeout:           1200,
				ClientFingerprint: "test-fingerprint",
			})
			results <- err
		}()
	}
	ready.Wait()
	close(start)

	for range orders {
		if err := <-results; err != nil {
			t.Fatalf("select exchange payment: %v", err)
		}
	}

	var persisted []Order
	if err := db.Order("id").Find(&persisted).Error; err != nil {
		t.Fatalf("reload orders: %v", err)
	}
	if len(persisted) != 2 {
		t.Fatalf("expected 2 orders, got %d", len(persisted))
	}
	if persisted[0].Address != "123456" || persisted[1].Address != "123456" {
		t.Fatalf("unexpected exchange receiver UID: %q, %q", persisted[0].Address, persisted[1].Address)
	}
	if persisted[0].Amount == persisted[1].Amount {
		t.Fatalf("concurrent orders received duplicate UID+amount allocation: %s", persisted[0].Amount)
	}
}

func TestRebuildZeroAmountOrdersPreservesExclusiveAddressLock(t *testing.T) {
	db := newExchangeReselectionTestDB(t)

	if err := db.Create(&Conf{K: AtomUSDT, V: "0.01"}).Error; err != nil {
		t.Fatalf("seed USDT atomicity: %v", err)
	}
	if err := db.Create(&Wallet{
		Name:      "Polygon wallet",
		Status:    WaStatusEnable,
		Address:   "0x1111111111111111111111111111111111111111",
		MatchAddr: "0x1111111111111111111111111111111111111111",
		TradeType: string(UsdtPolygon),
	}).Error; err != nil {
		t.Fatalf("seed wallet: %v", err)
	}
	if err := db.Create(&Rate{Rate: "7", RawRate: 7, Fiat: string(CNY), Crypto: string(USDT)}).Error; err != nil {
		t.Fatalf("seed rate: %v", err)
	}

	orders := make([]Order, 0, 2)
	for _, id := range []string{"zero-one", "zero-two"} {
		order, err := BuildPendingOrder(OrderParams{
			Money:   decimal.Zero,
			ApiType: OrderApiTypeEpusdtOrder,
			OrderId: "merchant-" + id,
			Fiat:    CNY,
			Timeout: 1200,
		})
		if err != nil {
			t.Fatalf("build zero-amount order %s: %v", id, err)
		}
		if !order.AddressLocked {
			t.Fatalf("pending zero-amount order %s did not reserve an address lock", id)
		}
		orders = append(orders, order)
	}

	params := func(order Order) OrderParams {
		return OrderParams{
			OrderId:   order.OrderId,
			TradeType: UsdtPolygon,
			Money:     decimal.Zero,
			Fiat:      CNY,
			Timeout:   1200,
		}
	}
	selected, err := RebuildOrder(orders[0], params(orders[0]))
	if err != nil {
		t.Fatalf("select payment method for first zero-amount order: %v", err)
	}
	if !selected.AddressLocked || selected.Amount != "0" {
		t.Fatalf("rebuilt order lock/amount = %t/%q, want true/0", selected.AddressLocked, selected.Amount)
	}

	if _, err := RebuildOrder(orders[1], params(orders[1])); err == nil || !strings.Contains(err.Error(), "暂无可用钱包地址") {
		t.Fatalf("second zero-amount order should not reuse the locked wallet, got %v", err)
	}
}
