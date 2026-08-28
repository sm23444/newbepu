package model

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ScanCursor stores the last source position whose discovered payments have
// finished passing through the local order matcher.
type ScanCursor struct {
	Key       string    `gorm:"column:key;type:varchar(128);primaryKey;not null" json:"key"`
	Position  int64     `gorm:"column:position;not null" json:"position"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null;autoUpdateTime" json:"updated_at"`
}

func (ScanCursor) TableName() string {
	return "bep_scan_cursor"
}

func LoadScanCursor(key string) (int64, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, false, fmt.Errorf("scan cursor key is empty")
	}

	var cursor ScanCursor
	err := Db.Where("key = ?", key).Limit(1).First(&cursor).Error
	if err == nil {
		return cursor.Position, true, nil
	}
	if err == gorm.ErrRecordNotFound {
		return 0, false, nil
	}
	return 0, false, err
}

func SaveScanCursor(key string, position int64) error {
	key = strings.TrimSpace(key)
	if key == "" || position < 0 {
		return fmt.Errorf("invalid scan cursor %q at %d", key, position)
	}

	cursor := ScanCursor{Key: key, Position: position, UpdatedAt: time.Now()}
	return Db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"position", "updated_at"}),
	}).Create(&cursor).Error
}
