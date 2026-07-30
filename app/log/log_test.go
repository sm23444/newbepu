package log

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInitUsesPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}

	dir := filepath.Join(t.TempDir(), "logs")
	if err := Init(dir); err != nil {
		t.Fatalf("initialize logs: %v", err)
	}
	Close()

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat log directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("log directory mode=%#o, want 0700", got)
	}
	for _, name := range []string{"bepusdt.log", "task.log"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode=%#o, want 0600", name, got)
		}
	}
}
