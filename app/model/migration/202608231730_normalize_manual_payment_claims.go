package migration

import (
	"strings"

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
			var rows []struct {
				ID      int64
				Network string
				TxHash  string
			}
			if err := tx.Table("bep_manual_payment_claim").Select("id, network, tx_hash").Find(&rows).Error; err != nil {
				return err
			}
			// EVM and other hex references are case-insensitive. Solana signatures
			// are base58 and must retain their original case. If old data already
			// contains a case-only duplicate, leave that group intact so migration
			// remains lossless and future canonical writes can still detect it.
			counts := make(map[string]int)
			for _, row := range rows {
				if strings.EqualFold(row.Network, "solana") {
					continue
				}
				key := strings.ToLower(strings.TrimSpace(row.Network)) + "\x00" + strings.ToLower(strings.TrimSpace(row.TxHash))
				counts[key]++
			}
			for _, row := range rows {
				if strings.EqualFold(row.Network, "solana") {
					continue
				}
				canonical := strings.ToLower(strings.TrimSpace(row.TxHash))
				key := strings.ToLower(strings.TrimSpace(row.Network)) + "\x00" + canonical
				if counts[key] != 1 || row.TxHash == canonical {
					continue
				}
				if err := tx.Table("bep_manual_payment_claim").Where("id = ?", row.ID).Update("tx_hash", canonical).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
