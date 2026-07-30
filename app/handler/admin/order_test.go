package admin

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
)

func TestUpdateOrderURLsPreservesConcurrentPaymentAndNotifyClaim(t *testing.T) {
	db := newAdminOrderTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	zero := time.Unix(0, 0)
	order := model.Order{
		OrderId:      "admin-create-stale-snapshot",
		TradeId:      "admin-create-stale-trade",
		TradeType:    model.UsdtOKX,
		Rate:         "7",
		Amount:       "1",
		Money:        "7",
		Address:      "123456789",
		MatchAddress: "123456789",
		Status:       model.OrderStatusWaiting,
		ApiType:      model.OrderApiTypeAdmin,
		NotifyUrl:    "https://old.example/notify",
		ReturnUrl:    "https://old.example/return",
		ExpiredAt:    now.Add(time.Hour),
		ConfirmedAt:  &zero,
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create order: %v", err)
	}

	stale := order
	claimUntil := now.Add(30 * time.Second)
	if err := db.Model(&model.Order{}).Where("id = ?", order.ID).Updates(map[string]any{
		"status":             model.OrderStatusSuccess,
		"ref_hash":           "okx-payment-id",
		"confirmed_at":       now,
		"notify_num":         1,
		"notify_claim_token": "active-notify-claim",
		"notify_claim_until": claimUntil,
	}).Error; err != nil {
		t.Fatalf("simulate concurrent payment and notify claim: %v", err)
	}

	stale.NotifyUrl = "https://new.example/notify"
	stale.ReturnUrl = "https://new.example/return"
	if err := updateOrderURLs(&stale); err != nil {
		t.Fatalf("update order URLs: %v", err)
	}

	var persisted model.Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if persisted.NotifyUrl != stale.NotifyUrl || persisted.ReturnUrl != stale.ReturnUrl {
		t.Fatalf("URLs = %q/%q, want %q/%q", persisted.NotifyUrl, persisted.ReturnUrl, stale.NotifyUrl, stale.ReturnUrl)
	}
	if persisted.Status != model.OrderStatusSuccess || persisted.RefHash != "okx-payment-id" {
		t.Fatalf("payment state was overwritten: status/ref = %d/%q", persisted.Status, persisted.RefHash)
	}
	if persisted.NotifyNum != 1 || persisted.NotifyClaimToken != "active-notify-claim" {
		t.Fatalf("notification claim was overwritten: num/token = %d/%q", persisted.NotifyNum, persisted.NotifyClaimToken)
	}
	if persisted.NotifyClaimUntil == nil || !persisted.NotifyClaimUntil.Equal(claimUntil) {
		t.Fatalf("notification lease was overwritten: got %v, want %v", persisted.NotifyClaimUntil, claimUntil)
	}
}

func TestMarkOrderPaidPreservesConcurrentStateTransition(t *testing.T) {
	tests := []struct {
		name        string
		concurrent  map[string]any
		wantUpdated bool
		wantStatus  int
		wantRefHash string
	}{
		{
			name:        "unchanged waiting order",
			wantUpdated: true,
			wantStatus:  model.OrderStatusSuccess,
			wantRefHash: "manual-transaction",
		},
		{
			name: "canceled order",
			concurrent: map[string]any{
				"status": model.OrderStatusCanceled,
			},
			wantStatus: model.OrderStatusCanceled,
		},
		{
			name: "exchange payment success",
			concurrent: map[string]any{
				"status":   model.OrderStatusSuccess,
				"ref_hash": "real-exchange-transaction",
			},
			wantStatus:  model.OrderStatusSuccess,
			wantRefHash: "real-exchange-transaction",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newAdminOrderTestDB(t)
			now := time.Now().UTC().Truncate(time.Second)
			zero := time.Unix(0, 0)
			order := model.Order{
				OrderId:      "admin-paid-stale-snapshot",
				TradeId:      "admin-paid-stale-trade",
				TradeType:    model.UsdtOKX,
				Rate:         "7",
				Amount:       "1",
				Money:        "7",
				Address:      "123456789",
				MatchAddress: "123456789",
				Status:       model.OrderStatusWaiting,
				ApiType:      model.OrderApiTypeAdmin,
				ExpiredAt:    now.Add(time.Hour),
				ConfirmedAt:  &zero,
			}
			if err := db.Create(&order).Error; err != nil {
				t.Fatalf("create order: %v", err)
			}

			stale := order
			if tt.concurrent != nil {
				if err := db.Model(&model.Order{}).Where("id = ?", order.ID).Updates(tt.concurrent).Error; err != nil {
					t.Fatalf("simulate concurrent state transition: %v", err)
				}
			}

			updated, err := markOrderPaid(&stale, "manual-transaction", now)
			if err != nil {
				t.Fatalf("mark order paid: %v", err)
			}
			if updated != tt.wantUpdated {
				t.Fatalf("updated = %t, want %t", updated, tt.wantUpdated)
			}

			var persisted model.Order
			if err := db.First(&persisted, order.ID).Error; err != nil {
				t.Fatalf("reload order: %v", err)
			}
			if persisted.Status != tt.wantStatus || persisted.RefHash != tt.wantRefHash {
				t.Fatalf("state/ref_hash = %d/%q, want %d/%q", persisted.Status, persisted.RefHash, tt.wantStatus, tt.wantRefHash)
			}
		})
	}
}

func newAdminOrderTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "admin-order-test.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&model.Order{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	oldDB := model.Db
	model.Db = db
	t.Cleanup(func() {
		model.Db = oldDB
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	return db
}
