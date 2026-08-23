package migration

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// 202608231730 - persist case-insensitive manual payment references canonically.
func m202608231730NormalizeManualPaymentClaims() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "202608231730_normalize_manual_payment_claims",
		Migrate: func(tx *gorm.DB) error {
			if !tx.Migrator().HasTable("bep_manual_payment_claim") {
				return nil
			}
			// EVM and other hex references are case-insensitive. Solana signatures
			// are base58 and must retain their original case.
			return tx.Exec(`UPDATE bep_manual_payment_claim
                SET tx_hash = lower(tx_hash)
                WHERE lower(network) <> 'solana'`).Error
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
