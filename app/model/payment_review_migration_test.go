package model

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/v03413/bepusdt/app/model/migration"
	"gorm.io/gorm"
)

func TestPaymentReviewMigrationAllowsRejectedResubmission(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "payment-review-migration.db")+"?cache=shared&mode=rwc"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
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
	historical := PaymentReview{
		TradeID: "historical-review", Status: PaymentReviewRejected, TransactionHash: "historical-hash",
		Description: "historical review", EvidencePath: "historical.png", EvidenceType: "image/png",
		EvidenceSize: 128, EvidenceSHA256: "historical-sha", ResolutionNote: "historical note", ReviewedBy: "admin",
	}
	if err := db.Create(&historical).Error; err != nil {
		t.Fatalf("create historical review: %v", err)
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
	var migrated PaymentReview
	if err := db.Where("trade_id = ?", historical.TradeID).First(&migrated).Error; err != nil {
		t.Fatalf("historical review was not preserved: %v", err)
	}
	if migrated.TransactionHash != historical.TransactionHash || migrated.Description != historical.Description || migrated.ResolutionNote != historical.ResolutionNote {
		t.Fatalf("historical review changed during migration: %+v", migrated)
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
}

func TestPaymentReviewMigrationRecoversPopulatedTemporaryTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "payment-review-recovery.db")+"?cache=shared&mode=rwc"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(&PaymentReview{}); err != nil {
		t.Fatalf("create empty replacement table: %v", err)
	}
	if err := db.Exec(`CREATE TABLE bep_payment_review_migration_new AS SELECT * FROM bep_payment_review WHERE 0`).Error; err != nil {
		t.Fatalf("create interrupted migration table: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.Exec(`INSERT INTO bep_payment_review_migration_new
        (id, trade_id, status, transaction_hash, description, evidence_path, evidence_type, evidence_size, evidence_sha256, resolution_note, reviewed_by, reviewed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`,
		41, "recovered-review", PaymentReviewRejected, "recovered-hash", "recovered description", "recovered.png", "image/png", 256, "recovered-sha", "recovered note", "admin", now, now).Error; err != nil {
		t.Fatalf("seed interrupted migration table: %v", err)
	}

	if err := migration.Run(db, []any{&PaymentReview{}}); err != nil {
		t.Fatalf("recover interrupted migration: %v", err)
	}
	var recovered struct {
		ID              int64
		TransactionHash string
		ResolutionNote  string
	}
	if err := db.Table("bep_payment_review").
		Select("id, transaction_hash, resolution_note").
		Where("trade_id = ?", "recovered-review").
		Take(&recovered).Error; err != nil {
		t.Fatalf("recovered review missing: %v", err)
	}
	if recovered.ID != 41 || recovered.TransactionHash != "recovered-hash" || recovered.ResolutionNote != "recovered note" {
		t.Fatalf("unexpected recovered review: %+v", recovered)
	}
	if db.Migrator().HasTable("bep_payment_review_migration_new") {
		t.Fatal("temporary migration table still exists after recovery")
	}
}

func TestPaymentReviewMigrationRollsBackDDLFailure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "payment-review-rollback.db")+"?cache=shared&mode=rwc"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(&PaymentReview{}); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	rows := []PaymentReview{
		{TradeID: "duplicate-pending", Status: PaymentReviewPending, TransactionHash: "hash-a", Description: "first pending", EvidencePath: "a.png", EvidenceType: "image/png", EvidenceSize: 1, EvidenceSHA256: "sha-a", AutoTimeAt: AutoTimeAt{CreatedAt: (*Datetime)(&now), UpdatedAt: (*Datetime)(&now)}},
		{TradeID: "duplicate-pending", Status: PaymentReviewPending, TransactionHash: "hash-b", Description: "second pending", EvidencePath: "b.png", EvidenceType: "image/png", EvidenceSize: 1, EvidenceSHA256: "sha-b", AutoTimeAt: AutoTimeAt{CreatedAt: (*Datetime)(&now), UpdatedAt: (*Datetime)(&now)}},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed legacy rows: %v", err)
	}

	if err := migration.Run(db, []any{&PaymentReview{}}); err == nil {
		t.Fatal("expected partial unique index creation to fail")
	}
	var count int64
	if err := db.Model(&PaymentReview{}).Where("trade_id = ?", "duplicate-pending").Count(&count).Error; err != nil {
		t.Fatalf("count rolled-back rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("historical rows after rollback = %d, want 2", count)
	}
	if db.Migrator().HasTable("bep_payment_review_migration_new") {
		t.Fatal("temporary migration table survived transaction rollback")
	}
	var migrationCount int64
	if err := db.Table(migration.TableName).
		Where("id = ?", "202608230900_payment_review_trade_id_not_unique").
		Count(&migrationCount).Error; err != nil {
		t.Fatalf("count rolled-back migration record: %v", err)
	}
	if migrationCount != 0 {
		t.Fatal("failed migration was incorrectly recorded as completed")
	}
}

func TestPaymentReviewMigrationClosesCanceledOrderReviews(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "canceled-review-migration.db")+"?cache=shared&mode=rwc"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(&Order{}, &PaymentReview{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	zero := time.Unix(0, 0)
	orders := []Order{
		{OrderId: "canceled-order", TradeId: "canceled-trade", TradeType: UsdtTrc20, Status: OrderStatusCanceled, Rate: "7", Amount: "1", Money: "7", Address: "a", MatchAddress: "a", ExpiredAt: now, ConfirmedAt: &zero},
		{OrderId: "waiting-order", TradeId: "waiting-trade", TradeType: UsdtTrc20, Status: OrderStatusWaiting, Rate: "7", Amount: "1", Money: "7", Address: "b", MatchAddress: "b", ExpiredAt: now.Add(time.Hour), ConfirmedAt: &zero},
	}
	if err := db.Create(&orders).Error; err != nil {
		t.Fatal(err)
	}
	reviews := []PaymentReview{
		{TradeID: "canceled-trade", Status: PaymentReviewPending, TransactionHash: "canceled-hash", Description: "canceled review", EvidencePath: "a.png", EvidenceType: "image/png", EvidenceSize: 1, EvidenceSHA256: "sha-a"},
		{TradeID: "waiting-trade", Status: PaymentReviewPending, TransactionHash: "waiting-hash", Description: "waiting review", EvidencePath: "b.png", EvidenceType: "image/png", EvidenceSize: 1, EvidenceSHA256: "sha-b"},
	}
	if err := db.Create(&reviews).Error; err != nil {
		t.Fatal(err)
	}

	if err := migration.Run(db, []any{&Order{}, &PaymentReview{}}); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	var canceled, waiting PaymentReview
	if err := db.First(&canceled, reviews[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&waiting, reviews[1].ID).Error; err != nil {
		t.Fatal(err)
	}
	if canceled.Status != PaymentReviewRejected || canceled.ReviewedBy != "system" || canceled.ReviewedAt == nil {
		t.Fatalf("canceled review = %+v", canceled)
	}
	if waiting.Status != PaymentReviewPending || waiting.ReviewedAt != nil {
		t.Fatalf("waiting review changed: %+v", waiting)
	}
}
