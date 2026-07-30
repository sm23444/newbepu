package model

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

var installTokenPattern = regexp.MustCompile(`一次性安装令牌:\s*([0-9A-F]{64})`)

func TestSecureInstallBootstrap(t *testing.T) {
	token := initializeBootstrapTestDB(t)

	if IsInstalled() {
		t.Fatal("fresh database was marked installed before owner initialization")
	}

	initial := GetVs([]ConfKey{ApiAuthToken, AdminSecret, AdminSecure, AdminPassword, SystemInstallLock})
	if len(initial[ApiAuthToken]) != 64 {
		t.Fatalf("API token length = %d, want 64", len(initial[ApiAuthToken]))
	}
	if len(initial[AdminSecret]) != 64 {
		t.Fatalf("session secret length = %d, want 64", len(initial[AdminSecret]))
	}
	if initial[ApiAuthToken] == initial[AdminSecret] {
		t.Fatal("API token and session secret must be generated independently")
	}
	if initial[AdminSecure] == "" || initial[AdminPassword] == "" {
		t.Fatal("initial secure entrance and password hash must be populated")
	}
	if initial[SystemInstallLock] != "0" {
		t.Fatalf("initial install lock = %q, want 0", initial[SystemInstallLock])
	}

	var persistedTokenCount int64
	if err := Db.Model(&Conf{}).Where("v = ?", token).Count(&persistedTokenCount).Error; err != nil {
		t.Fatalf("check persisted install token: %v", err)
	}
	if persistedTokenCount != 0 {
		t.Fatal("one-time install token must not be persisted")
	}

	if _, err := CompleteInstall("wrong-token", "owner", "owner-password-123"); !errors.Is(err, ErrInvalidInstallToken) {
		t.Fatalf("invalid token error = %v, want ErrInvalidInstallToken", err)
	}
	if IsInstalled() || GetK(SystemInstallLock) != "0" {
		t.Fatal("invalid token changed the install state")
	}

	secure, err := CompleteInstall(token, "owner", "owner-password-123")
	if err != nil {
		t.Fatalf("complete installation: %v", err)
	}
	if secure != initial[AdminSecure] {
		t.Fatalf("secure entrance = %q, want %q", secure, initial[AdminSecure])
	}
	if !IsInstalled() || GetK(SystemInstallLock) != "1" {
		t.Fatal("successful initialization did not persist the install state")
	}
	if got := GetK(AdminUsername); got != "owner" {
		t.Fatalf("administrator username = %q, want owner", got)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(GetK(AdminPassword)), []byte("owner-password-123")); err != nil {
		t.Fatalf("owner-selected password was not persisted: %v", err)
	}
	if GetK(AdminPassword) == initial[AdminPassword] {
		t.Fatal("initial random password hash was not replaced")
	}

	installMu.Lock()
	ready := installTokenReady
	hash := installTokenHash
	installMu.Unlock()
	if ready {
		t.Fatal("install token remained enabled after successful initialization")
	}
	if hash != ([32]byte{}) {
		t.Fatal("install token hash was not cleared after successful initialization")
	}

	if _, err := CompleteInstall(token, "other", "different-password"); !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("reused token error = %v, want ErrAlreadyInstalled", err)
	}
}

func TestInstallFailureRollsBackLockAndKeepsToken(t *testing.T) {
	token := initializeBootstrapTestDB(t)

	if err := Db.Where("k = ?", AdminUsername).Delete(&Conf{}).Error; err != nil {
		t.Fatalf("remove initial administrator username: %v", err)
	}
	if _, err := CompleteInstall(token, "owner", "owner-password-123"); err == nil {
		t.Fatal("installation unexpectedly succeeded with missing initial configuration")
	}
	if got := GetK(SystemInstallLock); got != "0" {
		t.Fatalf("install lock after failed transaction = %q, want 0", got)
	}
	if IsInstalled() {
		t.Fatal("failed installation was visible as installed")
	}

	installMu.Lock()
	ready := installTokenReady
	installMu.Unlock()
	if !ready {
		t.Fatal("valid install token was destroyed after a database failure")
	}

	if err := Db.Create(&Conf{K: AdminUsername, V: "admin"}).Error; err != nil {
		t.Fatalf("restore initial administrator username: %v", err)
	}
	if _, err := CompleteInstall(token, "owner", "owner-password-123"); err != nil {
		t.Fatalf("retry installation with same token: %v", err)
	}
	if !IsInstalled() {
		t.Fatal("retried installation did not complete")
	}
}

func TestInstallTokenRotatesBeforeInstallation(t *testing.T) {
	oldToken := initializeBootstrapTestDB(t)
	newToken := capturePreparedInstallToken(t)
	if newToken == oldToken {
		t.Fatal("install token was reused across initialization cycles")
	}

	if _, err := CompleteInstall(oldToken, "owner", "owner-password-123"); !errors.Is(err, ErrInvalidInstallToken) {
		t.Fatalf("old install token error = %v, want ErrInvalidInstallToken", err)
	}
	if _, err := CompleteInstall(newToken, "owner", "owner-password-123"); err != nil {
		t.Fatalf("new install token failed: %v", err)
	}
}

func initializeBootstrapTestDB(t *testing.T) string {
	t.Helper()

	previousDB := Db
	previousCache := snapshotConfCache()
	installMu.Lock()
	previousTokenHash := installTokenHash
	previousTokenReady := installTokenReady
	installMu.Unlock()

	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	previousStdout := os.Stdout
	os.Stdout = writePipe
	initErr := Init(filepath.Join(t.TempDir(), "bootstrap.db"), "")
	os.Stdout = previousStdout
	if err := writePipe.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	output, readErr := io.ReadAll(readPipe)
	_ = readPipe.Close()
	if initErr != nil {
		t.Fatalf("initialize test database: %v", initErr)
	}
	if readErr != nil {
		t.Fatalf("read initialization output: %v", readErr)
	}

	testDB := Db
	t.Cleanup(func() {
		if sqlDB, err := testDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		Db = previousDB
		restoreConfCache(previousCache)
		installMu.Lock()
		installTokenHash = previousTokenHash
		installTokenReady = previousTokenReady
		installMu.Unlock()
	})

	match := installTokenPattern.FindStringSubmatch(string(output))
	if len(match) != 2 {
		t.Fatalf("initialization output did not contain one-time install token: %q", strings.TrimSpace(string(output)))
	}

	return match[1]
}

func capturePreparedInstallToken(t *testing.T) string {
	t.Helper()

	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	previousStdout := os.Stdout
	os.Stdout = writePipe
	prepareErr := prepareInstallToken()
	os.Stdout = previousStdout
	if err := writePipe.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	output, readErr := io.ReadAll(readPipe)
	_ = readPipe.Close()
	if prepareErr != nil {
		t.Fatalf("prepare replacement install token: %v", prepareErr)
	}
	if readErr != nil {
		t.Fatalf("read replacement install token: %v", readErr)
	}

	match := installTokenPattern.FindStringSubmatch(string(output))
	if len(match) != 2 {
		t.Fatalf("replacement initialization output did not contain install token: %q", strings.TrimSpace(string(output)))
	}

	return match[1]
}

func snapshotConfCache() map[ConfKey]string {
	values := make(map[ConfKey]string)
	confCache.Range(func(key, value any) bool {
		values[key.(ConfKey)] = value.(string)
		return true
	})

	return values
}

func restoreConfCache(values map[ConfKey]string) {
	confCache.Range(func(key, _ any) bool {
		confCache.Delete(key)
		return true
	})
	for key, value := range values {
		confCache.Store(key, value)
	}
}
