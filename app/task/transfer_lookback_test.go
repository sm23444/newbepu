package task

import (
	"testing"
	"time"

	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/model"
)

func TestLookbackWindowCoversMixedExpiredAndWaitingOrders(t *testing.T) {
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

	window, ok, err := getLookbackWindow(conf.Polygon)
	if err != nil {
		t.Fatalf("get lookback window: %v", err)
	}
	if !ok {
		t.Fatal("expected a lookback window")
	}
	if window.startAt != oldCreated.Unix() {
		t.Fatalf("startAt=%d, want %d", window.startAt, oldCreated.Unix())
	}
	if window.endAt < now.Add(-time.Second).Unix() {
		t.Fatalf("mixed-order lookback ended too early: endAt=%d now=%d", window.endAt, now.Unix())
	}
	if len(window.orders) != 2 {
		t.Fatalf("lookback order count=%d, want 2", len(window.orders))
	}
	for _, order := range orders {
		if _, marked := lookbackDone.Load(order.ID); marked {
			t.Fatalf("order %d was marked done before queueing completed", order.ID)
		}
	}

	markLookbackDone(window)
	for _, order := range orders {
		if _, marked := lookbackDone.Load(order.ID); !marked {
			t.Fatalf("order %d was not marked done", order.ID)
		}
	}

	_, ok, err = getLookbackWindow(conf.Polygon)
	if err != nil {
		t.Fatalf("get second lookback window: %v", err)
	}
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
