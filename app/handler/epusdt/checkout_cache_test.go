package epusdt

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
)

func TestCheckoutDisablesBrowserCaching(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "checkout-cache.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.Order{}); err != nil {
		t.Fatalf("migrate order: %v", err)
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
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/pay/checkout-counter/missing", nil)
	ctx.Params = gin.Params{{Key: "trade_id", Value: "missing"}}

	(Epusdt{}).Checkout(ctx)

	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}
