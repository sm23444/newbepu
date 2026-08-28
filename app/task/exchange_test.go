package task

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/v03413/bepusdt/app/integrations/exchange"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
)

type recordingExchangeClient struct {
	windows []struct{ start, end time.Time }
}

func (c *recordingExchangeClient) Provider() string { return "okx" }

func (c *recordingExchangeClient) ListIncoming(_ context.Context, _ string, start, end time.Time) ([]exchange.Transaction, error) {
	c.windows = append(c.windows, struct{ start, end time.Time }{start: start, end: end})
	return nil, nil
}

func setupExchangeCursorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "exchange-cursor.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.Order{}, &model.ExchangeTransaction{}, &model.ScanCursor{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	previous := model.Db
	model.Db = db
	t.Cleanup(func() {
		model.Db = previous
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestExchangeCursorKeySeparatesAssets(t *testing.T) {
	usdt := exchangeCursorKey(" OKX ", "usdt")
	usdc := exchangeCursorKey("okx", "USDC")
	if usdt != "okx:USDT" || usdc != "okx:USDC" || usdt == usdc {
		t.Fatalf("unexpected exchange cursor keys: %q, %q", usdt, usdc)
	}
}

func TestPollExchangeResumesPersistedHistoryWindow(t *testing.T) {
	db := setupExchangeCursorTestDB(t)
	now := time.Now().Truncate(time.Second)
	start := now.Add(-48 * time.Hour)
	createdAt := model.Datetime(start.Add(10 * time.Minute))
	updatedAt := createdAt
	confirmedAt := start.Add(10 * time.Minute)
	order := model.Order{
		OrderId:      "exchange-recovery-order",
		TradeId:      "exchange-recovery-trade",
		TradeType:    model.UsdtOKX,
		Fiat:         model.CNY,
		Crypto:       model.USDT,
		Rate:         "1",
		Amount:       "10",
		Money:        "10",
		Address:      "123456789",
		MatchAddress: "123456789",
		Status:       model.OrderStatusExpired,
		RefHash:      "exchange-recovery-trade",
		ApiType:      model.OrderApiTypeEpusdtOrder,
		ExpiredAt:    start.Add(30 * time.Minute),
		ConfirmedAt:  &confirmedAt,
		AutoTimeAt:   model.AutoTimeAt{CreatedAt: &createdAt, UpdatedAt: &updatedAt},
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create historical order: %v", err)
	}
	if err := model.SaveScanCursor("exchange:okx:USDT", start.UnixMilli()); err != nil {
		t.Fatalf("save initial cursor: %v", err)
	}
	hasOrders, err := hasReceivableOrdersInWindow([]model.TradeType{model.UsdtOKX}, start, start.Add(exchangeRecoveryChunk))
	if err != nil || !hasOrders {
		var stored model.Order
		_ = db.First(&stored, order.ID).Error
		t.Fatalf("historical order window = %v/%v; stored created=%v expired=%v status=%d", hasOrders, err, stored.CreatedAt, stored.ExpiredAt, stored.Status)
	}

	client := &recordingExchangeClient{}
	if tradeType := model.GetExchangeTradeType(client.Provider(), "USDT"); tradeType != model.UsdtOKX {
		t.Fatalf("resolved trade type = %q, want %q", tradeType, model.UsdtOKX)
	}
	pollExchange(context.Background(), client, "USDT", 0)
	if len(client.windows) != 1 {
		position, _, _ := model.LoadScanCursor("exchange:okx:USDT")
		t.Fatalf("history requests = %d, want 1; cursor=%d start=%d", len(client.windows), position, start.UnixMilli())
	}
	if !client.windows[0].start.Equal(start.Add(-exchangeScanOverlap)) || !client.windows[0].end.Equal(start.Add(exchangeRecoveryChunk)) {
		t.Fatalf("history window = %s..%s, want %s..%s", client.windows[0].start, client.windows[0].end, start.Add(-exchangeScanOverlap), start.Add(exchangeRecoveryChunk))
	}
	position, found, err := model.LoadScanCursor("exchange:okx:USDT")
	if err != nil || !found || position != start.Add(exchangeRecoveryChunk).UnixMilli() {
		t.Fatalf("persisted cursor = %d/%v/%v", position, found, err)
	}
}

func TestGetReceivableOrdersUsesHistoricalTransferTime(t *testing.T) {
	db := setupExchangeCursorTestDB(t)
	now := time.Now().Truncate(time.Second)
	transferAt := now.Add(-8 * time.Hour)
	createdAt := model.Datetime(transferAt.Add(-time.Minute))
	updatedAt := createdAt
	confirmedAt := transferAt
	order := model.Order{
		OrderId:      "historical-order",
		TradeId:      "historical-trade",
		TradeType:    model.UsdtOKX,
		Fiat:         model.CNY,
		Crypto:       model.USDT,
		Rate:         "1",
		Amount:       "10",
		Money:        "10",
		Address:      "987654321",
		MatchAddress: "987654321",
		Status:       model.OrderStatusExpired,
		RefHash:      "historical-trade",
		ApiType:      model.OrderApiTypeEpusdtOrder,
		ExpiredAt:    transferAt.Add(time.Minute),
		ConfirmedAt:  &confirmedAt,
		AutoTimeAt:   model.AutoTimeAt{CreatedAt: &createdAt, UpdatedAt: &updatedAt},
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create historical order: %v", err)
	}

	orders, err := getReceivableOrders([]transfer{{
		Timestamp:   transferAt,
		TradeType:   model.UsdtOKX,
		RecvAddress: order.MatchAddress,
	}})
	if err != nil {
		t.Fatalf("load historical candidates: %v", err)
	}
	key := order.MatchAddress + string(order.TradeType)
	if len(orders[key]) != 1 || orders[key][0].ID != order.ID {
		t.Fatalf("historical candidates = %#v", orders[key])
	}
}
