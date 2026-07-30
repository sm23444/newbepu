package model

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInitSqliteUsesPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}

	dir := filepath.Join(t.TempDir(), "data")
	dbPath := filepath.Join(dir, "sqlite.db")
	previous := Db
	if err := initSqlite(dbPath); err != nil {
		t.Fatalf("initialize SQLite: %v", err)
	}
	t.Cleanup(func() {
		if Db != nil {
			if sqlDB, err := Db.DB(); err == nil {
				_ = sqlDB.Close()
			}
		}
		Db = previous
	})

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat data directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("data directory mode=%#o, want 0700", got)
	}
	dbInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat database: %v", err)
	}
	if got := dbInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("database mode=%#o, want 0600", got)
	}
}
