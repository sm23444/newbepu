package admin

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	applog "github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
)

func TestWalletDeleteReturnsDatabaseError(t *testing.T) {
	if err := applog.Init(t.TempDir()); err != nil {
		t.Fatalf("init test log: %v", err)
	}
	t.Cleanup(applog.Close)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wallet-delete.db")+"?cache=shared&mode=rwc"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Wallet{}); err != nil {
		t.Fatalf("migrate wallet: %v", err)
	}
	if err := db.Create(&model.Wallet{Address: "Ttest", MatchAddr: "Ttest", TradeType: string(model.UsdtTrc20)}).Error; err != nil {
		t.Fatalf("create wallet: %v", err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_wallet_delete
        BEFORE DELETE ON bep_wallet
        BEGIN SELECT RAISE(ABORT, 'forced wallet delete failure'); END`).Error; err != nil {
		t.Fatalf("create delete trigger: %v", err)
	}
	previousDB := model.Db
	model.Db = db
	t.Cleanup(func() {
		model.Db = previousDB
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest("POST", "/api/wallet/del", strings.NewReader(`{"id":1}`))
	request.Header.Set("Content-Type", "application/json")
	writer := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(writer)
	ctx.Request = request
	(Wallet{}).Del(ctx)

	if !strings.Contains(writer.Body.String(), `"code":500`) {
		t.Fatalf("delete response = %q, want code 500", writer.Body.String())
	}
}
