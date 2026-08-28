package model

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestCalcTradeAmountSkipsWalletWithExclusiveOrder(t *testing.T) {
	db := newBuildPendingOrderTestDB(t)
	if err := db.AutoMigrate(&Conf{}); err != nil {
		t.Fatalf("migrate configuration table: %v", err)
	}
	if err := db.Create(&Conf{K: AtomUSDT, V: "0.01"}).Error; err != nil {
		t.Fatalf("seed atomicity: %v", err)
	}

	wallets := []Wallet{
		{Address: "0x1111111111111111111111111111111111111111", MatchAddr: "0x1111111111111111111111111111111111111111", TradeType: string(UsdtPolygon)},
		{Address: "0x2222222222222222222222222222222222222222", MatchAddr: "0x2222222222222222222222222222222222222222", TradeType: string(UsdtPolygon)},
	}
	zero := time.Unix(0, 0)
	if err := db.Create(&Order{
		OrderId:       "exclusive-order",
		TradeId:       "exclusive-trade",
		RefHash:       "exclusive-trade",
		TradeType:     UsdtPolygon,
		MatchAddress:  wallets[0].MatchAddr,
		Address:       wallets[0].Address,
		AddressLocked: true,
		Status:        OrderStatusWaiting,
		Amount:        "0",
		ExpiredAt:     time.Now().Add(time.Hour),
		ConfirmedAt:   &zero,
	}).Error; err != nil {
		t.Fatalf("seed exclusive order: %v", err)
	}

	allocated, _, err := CalcTradeAmount(wallets, decimal.NewFromInt(7), OrderParams{
		TradeType: UsdtPolygon,
		Money:     decimal.NewFromInt(7),
	})
	if err != nil {
		t.Fatalf("calculate trade amount: %v", err)
	}
	if allocated.MatchAddr != wallets[1].MatchAddr {
		t.Fatalf("shared order reused exclusive wallet %q, want %q", allocated.MatchAddr, wallets[1].MatchAddr)
	}
}

func TestLockTradeAddressSkipsWalletWithSharedOrder(t *testing.T) {
	db := newBuildPendingOrderTestDB(t)
	wallets := []Wallet{
		{Address: "0x1111111111111111111111111111111111111111", MatchAddr: "0x1111111111111111111111111111111111111111", TradeType: string(UsdtPolygon)},
		{Address: "0x2222222222222222222222222222222222222222", MatchAddr: "0x2222222222222222222222222222222222222222", TradeType: string(UsdtPolygon)},
	}
	zero := time.Unix(0, 0)
	if err := db.Create(&Order{
		OrderId:      "shared-order",
		TradeId:      "shared-trade",
		RefHash:      "shared-trade",
		TradeType:    UsdtPolygon,
		MatchAddress: wallets[0].MatchAddr,
		Address:      wallets[0].Address,
		Status:       OrderStatusWaiting,
		Amount:       "1",
		ExpiredAt:    time.Now().Add(time.Hour),
		ConfirmedAt:  &zero,
	}).Error; err != nil {
		t.Fatalf("seed shared order: %v", err)
	}

	allocated, amount, err := LockTradeAddress(wallets, UsdtPolygon)
	if err != nil {
		t.Fatalf("lock trade address: %v", err)
	}
	if allocated.MatchAddr != wallets[1].MatchAddr || amount != "0" {
		t.Fatalf("exclusive order reused shared wallet: address=%q amount=%q, want %q/0", allocated.MatchAddr, amount, wallets[1].MatchAddr)
	}
}

func TestCalcTradeAmountKeepsSharedAddressForDifferentAmount(t *testing.T) {
	db := newBuildPendingOrderTestDB(t)
	if err := db.AutoMigrate(&Conf{}); err != nil {
		t.Fatalf("migrate configuration table: %v", err)
	}
	if err := db.Create(&Conf{K: AtomUSDT, V: "0.01"}).Error; err != nil {
		t.Fatalf("seed atomicity: %v", err)
	}

	wallet := Wallet{
		Address:   "0x1111111111111111111111111111111111111111",
		MatchAddr: "0x1111111111111111111111111111111111111111",
		TradeType: string(UsdtPolygon),
	}
	zero := time.Unix(0, 0)
	if err := db.Create(&Order{
		OrderId:      "shared-different-amount-order",
		TradeId:      "shared-different-amount-trade",
		RefHash:      "shared-different-amount-trade",
		TradeType:    UsdtPolygon,
		MatchAddress: wallet.MatchAddr,
		Address:      wallet.Address,
		Status:       OrderStatusWaiting,
		Amount:       "1",
		ExpiredAt:    time.Now().Add(time.Hour),
		ConfirmedAt:  &zero,
	}).Error; err != nil {
		t.Fatalf("seed shared order: %v", err)
	}

	allocated, amount, err := CalcTradeAmount([]Wallet{wallet}, decimal.NewFromInt(1), OrderParams{
		TradeType: UsdtPolygon,
		Money:     decimal.NewFromInt(2),
	})
	if err != nil {
		t.Fatalf("calculate trade amount: %v", err)
	}
	if allocated.MatchAddr != wallet.MatchAddr || amount != "2" {
		t.Fatalf("different shared amount changed wallet allocation: address=%q amount=%q", allocated.MatchAddr, amount)
	}
}

func TestCalcTradeAmountIncludesExpiredOrdersWithinLookback(t *testing.T) {
	db := newBuildPendingOrderTestDB(t)
	if err := db.AutoMigrate(&Conf{}); err != nil {
		t.Fatalf("migrate configuration table: %v", err)
	}
	if err := db.Create(&Conf{K: AtomUSDT, V: "0.01"}).Error; err != nil {
		t.Fatalf("seed atomicity: %v", err)
	}

	oldSnapshot := snapshotConfCache()
	updated := snapshotConfCache()
	updated[PaymentLookbackHour] = "3"
	restoreConfCache(updated)
	t.Cleanup(func() { restoreConfCache(oldSnapshot) })

	wallet := Wallet{
		Address:   "0x1111111111111111111111111111111111111111",
		MatchAddr: "0x1111111111111111111111111111111111111111",
		TradeType: string(UsdtPolygon),
	}
	zero := time.Unix(0, 0)
	if err := db.Create(&Order{
		OrderId:      "expired-within-lookback-order",
		TradeId:      "expired-within-lookback-trade",
		RefHash:      "expired-within-lookback-trade",
		TradeType:    UsdtPolygon,
		MatchAddress: wallet.MatchAddr,
		Address:      wallet.Address,
		Status:       OrderStatusExpired,
		Amount:       "1",
		ExpiredAt:    time.Now().Add(-time.Hour),
		ConfirmedAt:  &zero,
	}).Error; err != nil {
		t.Fatalf("seed expired order: %v", err)
	}

	allocated, amount, err := CalcTradeAmount([]Wallet{wallet}, decimal.NewFromInt(1), OrderParams{
		TradeType: UsdtPolygon,
		Money:     decimal.NewFromInt(1),
	})
	if err != nil {
		t.Fatalf("calculate trade amount: %v", err)
	}
	if allocated.MatchAddr != wallet.MatchAddr {
		t.Fatalf("expired order changed wallet allocation: got %q, want %q", allocated.MatchAddr, wallet.MatchAddr)
	}
	if amount != "1.01" {
		t.Fatalf("expired order within lookback did not reserve amount: got %q, want 1.01", amount)
	}
}

func TestCalcTradeAmountReleasesExpiredOrdersOutsideLookback(t *testing.T) {
	db := newBuildPendingOrderTestDB(t)
	if err := db.AutoMigrate(&Conf{}); err != nil {
		t.Fatalf("migrate configuration table: %v", err)
	}
	if err := db.Create(&Conf{K: AtomUSDT, V: "0.01"}).Error; err != nil {
		t.Fatalf("seed atomicity: %v", err)
	}

	oldSnapshot := snapshotConfCache()
	updated := snapshotConfCache()
	updated[PaymentLookbackHour] = "3"
	restoreConfCache(updated)
	t.Cleanup(func() { restoreConfCache(oldSnapshot) })

	wallet := Wallet{
		Address:   "0x2222222222222222222222222222222222222222",
		MatchAddr: "0x2222222222222222222222222222222222222222",
		TradeType: string(UsdtPolygon),
	}
	zero := time.Unix(0, 0)
	if err := db.Create(&Order{
		OrderId:      "expired-outside-lookback-order",
		TradeId:      "expired-outside-lookback-trade",
		RefHash:      "expired-outside-lookback-trade",
		TradeType:    UsdtPolygon,
		MatchAddress: wallet.MatchAddr,
		Address:      wallet.Address,
		Status:       OrderStatusExpired,
		Amount:       "1",
		ExpiredAt:    time.Now().Add(-4 * time.Hour),
		ConfirmedAt:  &zero,
	}).Error; err != nil {
		t.Fatalf("seed expired order: %v", err)
	}

	allocated, amount, err := CalcTradeAmount([]Wallet{wallet}, decimal.NewFromInt(1), OrderParams{
		TradeType: UsdtPolygon,
		Money:     decimal.NewFromInt(1),
	})
	if err != nil {
		t.Fatalf("calculate trade amount: %v", err)
	}
	if allocated.MatchAddr != wallet.MatchAddr || amount != "1" {
		t.Fatalf("expired order outside lookback remained reserved: address=%q amount=%q", allocated.MatchAddr, amount)
	}
}

func TestLockTradeAddressIncludesExpiredOrdersWithinLookback(t *testing.T) {
	db := newBuildPendingOrderTestDB(t)

	oldSnapshot := snapshotConfCache()
	updated := snapshotConfCache()
	updated[PaymentLookbackHour] = "3"
	restoreConfCache(updated)
	t.Cleanup(func() { restoreConfCache(oldSnapshot) })

	wallets := []Wallet{
		{Address: "0x3333333333333333333333333333333333333333", MatchAddr: "0x3333333333333333333333333333333333333333", TradeType: string(UsdtPolygon)},
		{Address: "0x4444444444444444444444444444444444444444", MatchAddr: "0x4444444444444444444444444444444444444444", TradeType: string(UsdtPolygon)},
	}
	zero := time.Unix(0, 0)
	if err := db.Create(&Order{
		OrderId:       "expired-exclusive-order",
		TradeId:       "expired-exclusive-trade",
		RefHash:       "expired-exclusive-trade",
		TradeType:     UsdtPolygon,
		MatchAddress:  wallets[0].MatchAddr,
		Address:       wallets[0].Address,
		AddressLocked: true,
		Status:        OrderStatusExpired,
		ExpiredAt:     time.Now().Add(-time.Hour),
		ConfirmedAt:   &zero,
	}).Error; err != nil {
		t.Fatalf("seed expired exclusive order: %v", err)
	}

	allocated, amount, err := LockTradeAddress(wallets, UsdtPolygon)
	if err != nil {
		t.Fatalf("lock trade address: %v", err)
	}
	if allocated.MatchAddr != wallets[1].MatchAddr || amount != "0" {
		t.Fatalf("expired exclusive order did not reserve address: address=%q amount=%q", allocated.MatchAddr, amount)
	}
}

func TestLockTradeAddressReleasesExpiredOrdersOutsideLookback(t *testing.T) {
	db := newBuildPendingOrderTestDB(t)

	oldSnapshot := snapshotConfCache()
	updated := snapshotConfCache()
	updated[PaymentLookbackHour] = "3"
	restoreConfCache(updated)
	t.Cleanup(func() { restoreConfCache(oldSnapshot) })

	wallets := []Wallet{
		{Address: "0x5555555555555555555555555555555555555555", MatchAddr: "0x5555555555555555555555555555555555555555", TradeType: string(UsdtPolygon)},
		{Address: "0x6666666666666666666666666666666666666666", MatchAddr: "0x6666666666666666666666666666666666666666", TradeType: string(UsdtPolygon)},
	}
	zero := time.Unix(0, 0)
	if err := db.Create(&Order{
		OrderId:       "old-exclusive-order",
		TradeId:       "old-exclusive-trade",
		RefHash:       "old-exclusive-trade",
		TradeType:     UsdtPolygon,
		MatchAddress:  wallets[0].MatchAddr,
		Address:       wallets[0].Address,
		AddressLocked: true,
		Status:        OrderStatusExpired,
		ExpiredAt:     time.Now().Add(-4 * time.Hour),
		ConfirmedAt:   &zero,
	}).Error; err != nil {
		t.Fatalf("seed old exclusive order: %v", err)
	}

	allocated, amount, err := LockTradeAddress(wallets, UsdtPolygon)
	if err != nil {
		t.Fatalf("lock trade address: %v", err)
	}
	if allocated.MatchAddr != wallets[0].MatchAddr || amount != "0" {
		t.Fatalf("expired exclusive order outside lookback remained reserved: address=%q amount=%q", allocated.MatchAddr, amount)
	}
}
