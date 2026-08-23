package migration

import (
	"fmt"
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
	"strings"
)

// 202608230900 - allow a rejected review to be resubmitted for the same order.
func m202608230900PaymentReviewTradeIDNotUnique() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "202608230900_payment_review_trade_id_not_unique",
		Migrate: func(tx *gorm.DB) error {
			if !tx.Migrator().HasTable("bep_payment_review") {
				return nil
			}
			if tx.Dialector.Name() == "sqlite" {
				return rebuildSQLitePaymentReview(tx)
			}
			// Older GORM versions may have created the column unique constraint
			// under either of these names. SQLite's GetIndexes skips origin=u
			// indexes, so remove the known names explicitly as well.
			for _, name := range []string{"idx_bep_payment_review_trade_id", "idx_payment_review_trade_id"} {
				if tx.Migrator().HasIndex("bep_payment_review", name) {
					if err := tx.Migrator().DropIndex("bep_payment_review", name); err != nil {
						return err
					}
				}
			}
			indexes, err := tx.Migrator().GetIndexes("bep_payment_review")
			if err != nil {
				return err
			}
			for _, index := range indexes {
				unique, ok := index.Unique()
				if ok && unique && len(index.Columns()) == 1 && strings.EqualFold(index.Columns()[0], "trade_id") {
					if err := tx.Migrator().DropIndex("bep_payment_review", index.Name()); err != nil {
						return err
					}
				}
			}
			indexName := "idx_payment_review_pending_trade_id"
			return tx.Exec(fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON bep_payment_review (trade_id) WHERE status = 'pending'", indexName)).Error
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}

func rebuildSQLitePaymentReview(tx *gorm.DB) error {
	const oldTable = "bep_payment_review"
	const newTable = "bep_payment_review_migration_new"
	if err := tx.Exec("DROP TABLE IF EXISTS " + newTable).Error; err != nil {
		return err
	}
	if err := tx.Exec("CREATE TABLE " + newTable + ` (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        trade_id VARCHAR(128) NOT NULL,
        status VARCHAR(16) NOT NULL,
        transaction_hash VARCHAR(256) NOT NULL DEFAULT '',
        description VARCHAR(1000) NOT NULL,
        evidence_path VARCHAR(512) NOT NULL,
        evidence_type VARCHAR(64) NOT NULL,
        evidence_size INTEGER NOT NULL,
        evidence_sha256 CHAR(64) NOT NULL,
        resolution_note VARCHAR(1000) NOT NULL DEFAULT '',
        reviewed_by VARCHAR(128) NOT NULL DEFAULT '',
        reviewed_at DATETIME,
        created_at DATETIME NOT NULL,
        updated_at DATETIME NOT NULL
    )`).Error; err != nil {
		return err
	}
	columns := "id, trade_id, status, transaction_hash, description, evidence_path, evidence_type, evidence_size, evidence_sha256, resolution_note, reviewed_by, reviewed_at, created_at, updated_at"
	if err := tx.Exec(fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s", newTable, columns, columns, oldTable)).Error; err != nil {
		return err
	}
	if err := tx.Exec("DROP TABLE " + oldTable).Error; err != nil {
		return err
	}
	if err := tx.Exec("ALTER TABLE " + newTable + " RENAME TO " + oldTable).Error; err != nil {
		return err
	}
	for _, statement := range []string{
		"CREATE INDEX IF NOT EXISTS idx_payment_review_trade_id ON bep_payment_review (trade_id)",
		"CREATE INDEX IF NOT EXISTS idx_bep_payment_review_status ON bep_payment_review (status)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_review_pending_trade_id ON bep_payment_review (trade_id) WHERE status = 'pending'",
	} {
		if err := tx.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
