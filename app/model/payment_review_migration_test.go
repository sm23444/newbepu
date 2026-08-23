package model

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/v03413/bepusdt/app/model/migration"
	"gorm.io/gorm"
)

func TestPaymentReviewMigrationAllowsRejectedResubmission(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "payment-review-migration.db")+"?cache=shared&mode=rwc"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&PaymentReview{}); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := db.Exec("CREATE UNIQUE INDEX idx_bep_payment_review_trade_id ON bep_payment_review (trade_id)").Error; err != nil {
		t.Fatalf("create legacy unique index: %v", err)
	}
	if !db.Migrator().HasIndex("bep_payment_review", "idx_bep_payment_review_trade_id") {
		t.Fatal("legacy unique trade_id index was not created")
	}
	if err := migration.Run(db, []any{&PaymentReview{}}); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	if db.Migrator().HasIndex("bep_payment_review", "idx_bep_payment_review_trade_id") {
		t.Fatal("legacy unique trade_id index still exists")
	}
	if !db.Migrator().HasIndex("bep_payment_review", "idx_payment_review_pending_trade_id") {
		t.Fatal("pending trade_id unique index was not created")
	}

	base := PaymentReview{TradeID: "same-trade", Status: PaymentReviewRejected, Description: "rejected", EvidencePath: "x", EvidenceType: "image/png", EvidenceSize: 1, EvidenceSHA256: "hash"}
	if err := db.Create(&base).Error; err != nil {
		t.Fatalf("create rejected review: %v", err)
	}
	pending := base
	pending.ID = 0
	pending.Status = PaymentReviewPending
	if err := db.Create(&pending).Error; err != nil {
		t.Fatalf("create pending resubmission: %v", err)
	}
	duplicate := pending
	duplicate.ID = 0
	if err := db.Create(&duplicate).Error; err == nil {
		t.Fatal("duplicate pending review was accepted")
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		_ = sqlDB.Close()
	}
}
