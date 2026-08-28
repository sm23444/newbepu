package model

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestScanCursorPersistsAcrossReload(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cursor.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&ScanCursor{}); err != nil {
		t.Fatalf("migrate cursor: %v", err)
	}

	previous := Db
	Db = db
	t.Cleanup(func() {
		Db = previous
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})

	if _, found, err := LoadScanCursor("evm:polygon"); err != nil || found {
		t.Fatalf("missing cursor lookup = found %v, error %v", found, err)
	}
	if err := SaveScanCursor("evm:polygon", 100); err != nil {
		t.Fatalf("save cursor: %v", err)
	}
	if err := SaveScanCursor("evm:polygon", 125); err != nil {
		t.Fatalf("update cursor: %v", err)
	}

	position, found, err := LoadScanCursor("evm:polygon")
	if err != nil || !found || position != 125 {
		t.Fatalf("reloaded cursor = %d/%v/%v, want 125/true/nil", position, found, err)
	}
}
