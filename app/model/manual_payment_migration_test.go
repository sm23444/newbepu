package model

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/v03413/bepusdt/app/model/migration"
	"gorm.io/gorm"
)

func TestManualPaymentClaimMigrationNormalizesNonSolanaReferences(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "manual-payment-claims.db")+"?cache=shared&mode=rwc"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(&ManualPaymentClaim{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]ManualPaymentClaim{
		{Network: "polygon", TxHash: "0xAbCd", TradeID: "polygon-trade"},
		{Network: "solana", TxHash: "AbCd", TradeID: "solana-trade"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := migration.Run(db, []any{&ManualPaymentClaim{}}); err != nil {
		t.Fatalf("run migration: %v", err)
	}
	var polygon, solana ManualPaymentClaim
	if err := db.Where("trade_id = ?", "polygon-trade").First(&polygon).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("trade_id = ?", "solana-trade").First(&solana).Error; err != nil {
		t.Fatal(err)
	}
	if polygon.TxHash != "0xabcd" {
		t.Fatalf("polygon reference = %q, want lowercase", polygon.TxHash)
	}
	if solana.TxHash != "AbCd" {
		t.Fatalf("Solana reference = %q, want original case", solana.TxHash)
	}
}

func TestManualPaymentClaimMigrationKeepsCaseConflictWithoutFailing(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "manual-payment-conflict.db")+"?cache=shared&mode=rwc"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if sqlDB, dbErr := db.DB(); dbErr == nil {
		t.Cleanup(func() { _ = sqlDB.Close() })
	}
	if err := db.AutoMigrate(&ManualPaymentClaim{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]ManualPaymentClaim{
		{Network: "polygon", TxHash: "0xAbCd", TradeID: "conflict-a"},
		{Network: "polygon", TxHash: "0xabcd", TradeID: "conflict-b"},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := migration.Run(db, []any{&ManualPaymentClaim{}}); err != nil {
		t.Fatalf("migration rejected historical case conflict: %v", err)
	}
	var count int64
	if err := db.Model(&ManualPaymentClaim{}).Where("network = ?", "polygon").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("conflicting claims count = %d, want 2", count)
	}
}
