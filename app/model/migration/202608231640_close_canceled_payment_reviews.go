package migration

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// 202608231640 - close pending reviews whose orders were already canceled.
func m202608231640CloseCanceledPaymentReviews() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "202608231640_close_canceled_payment_reviews",
		Migrate: func(tx *gorm.DB) error {
			if !tx.Migrator().HasTable("bep_order") || !tx.Migrator().HasTable("bep_payment_review") {
				return nil
			}
			return tx.Exec(`UPDATE bep_payment_review
                SET status = 'rejected',
                    resolution_note = '订单已取消，系统自动关闭复核',
                    reviewed_by = 'system',
                    reviewed_at = CURRENT_TIMESTAMP,
                    updated_at = CURRENT_TIMESTAMP
                WHERE status = 'pending'
                  AND trade_id IN (SELECT trade_id FROM bep_order WHERE status = 4)`).Error
		},
		Rollback: func(tx *gorm.DB) error { return nil },
	}
}
