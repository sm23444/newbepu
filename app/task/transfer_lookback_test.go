package task

import (
	"testing"
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/model"
)

func TestLookbackMarksOrdersWhenWindowRequested(t *testing.T) {
	db := setupManualPaymentTestDB(t)
	oldLookback := model.GetC(model.PaymentLookbackHour)
	model.SetK(model.PaymentLookbackHour, "3")
	t.Cleanup(func() {
		model.SetK(model.PaymentLookbackHour, oldLookback)
	})

	clearLookbackDone()
	t.Cleanup(clearLookbackDone)

	now := time.Now()
	oldCreated := now.Add(-2 * time.Hour)
	oldUpdated := oldCreated
	newCreated := now.Add(-5 * time.Minute)
	newUpdated := newCreated
	confirmedAt := time.Unix(0, 0)
	orders := []model.Order{
		{
			OrderId: "old-expired", TradeId: "old-expired", TradeType: model.UsdtPolygon,
			Status: model.OrderStatusExpired, Rate: "7", Amount: "1", Money: "7",
			Address: "old-address", MatchAddress: "old-address", ExpiredAt: now.Add(-90 * time.Minute), ConfirmedAt: &confirmedAt,
			AutoTimeAt: model.AutoTimeAt{CreatedAt: (*model.Datetime)(&oldCreated), UpdatedAt: (*model.Datetime)(&oldUpdated)},
		},
		{
			OrderId: "new-waiting", TradeId: "new-waiting", TradeType: model.UsdtPolygon,
			Status: model.OrderStatusWaiting, Rate: "7", Amount: "2", Money: "14",
			Address: "new-address", MatchAddress: "new-address", ExpiredAt: now.Add(15 * time.Minute), ConfirmedAt: &confirmedAt,
			AutoTimeAt: model.AutoTimeAt{CreatedAt: (*model.Datetime)(&newCreated), UpdatedAt: (*model.Datetime)(&newUpdated)},
		},
	}
	if err := db.Create(&orders).Error; err != nil {
		t.Fatalf("create orders: %v", err)
	}

	startAt, _, ok := getLookbackUnix(conf.Polygon)
	if !ok {
		t.Fatal("expected a lookback window")
	}
	if startAt != oldCreated.Unix() {
		t.Fatalf("startAt=%d, want %d", startAt, oldCreated.Unix())
	}
	for _, order := range orders {
		if _, marked := lookbackDone.Load(order.ID); !marked {
			t.Fatalf("order %d was not marked when lookback window was requested", order.ID)
		}
	}

	_, _, ok = getLookbackUnix(conf.Polygon)
	if ok {
		t.Fatal("completed orders should not trigger another lookback")
	}
}

func clearLookbackDone() {
	lookbackDone.Range(func(key, _ any) bool {
		lookbackDone.Delete(key)
		return true
	})
}
