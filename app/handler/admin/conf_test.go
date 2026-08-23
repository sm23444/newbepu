package admin

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
)

func TestValidPublicURLSetting(t *testing.T) {
	tests := []struct {
		name  string
		key   model.ConfKey
		value string
		want  bool
	}{
		{name: "empty support URL", key: model.PaymentSupportUrl, value: "", want: true},
		{name: "HTTPS support URL", key: model.PaymentSupportUrl, value: "https://support.example/help", want: true},
		{name: "HTTP support URL", key: model.PaymentSupportUrl, value: "http://support.example/help", want: false},
		{name: "javascript support URL", key: model.PaymentSupportUrl, value: "javascript:alert(1)", want: false},
		{name: "unrelated setting", key: model.PaymentCheckout, value: "sm", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validPublicURLSetting(tt.key, tt.value); got != tt.want {
				t.Fatalf("validPublicURLSetting(%q, %q) = %v, want %v", tt.key, tt.value, got, tt.want)
			}
		})
	}
}

func TestSensitiveConfigurationValuesAreMasked(t *testing.T) {
	tests := []struct {
		key      model.ConfKey
		value    string
		wantSafe string
	}{
		{key: model.ApiAuthToken, value: "token-value", wantSafe: maskedConfValue},
		{key: model.ExchangeOKXSecretKey, value: "secret-value", wantSafe: maskedConfValue},
		{key: model.RpcEndpointTronGridApiKey, value: "rpc-key", wantSafe: maskedConfValue},
		{key: model.AdminSecure, value: "/private-entry", wantSafe: "/private-entry"},
		{key: model.PaymentTimeout, value: "1200", wantSafe: "1200"},
	}
	for _, tt := range tests {
		t.Run(string(tt.key), func(t *testing.T) {
			if got := safeConfValue(tt.key, tt.value); got != tt.wantSafe {
				t.Fatalf("safeConfValue(%q) = %q, want %q", tt.key, got, tt.wantSafe)
			}
		})
	}
}

func TestConfGetsMasksSecretsWithoutMaskingSecureEntry(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "conf-api.db")+"?cache=shared&mode=rwc"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Conf{}); err != nil {
		t.Fatalf("migrate conf: %v", err)
	}
	if err := db.Create(&[]model.Conf{
		{K: model.ApiAuthToken, V: "secret-token"},
		{K: model.AdminSecure, V: "/private-entry"},
		{K: model.AdminUsername, V: "admin"},
	}).Error; err != nil {
		t.Fatal(err)
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
	request := httptest.NewRequest("POST", "/api/conf/gets", strings.NewReader(`{"keys":["api_auth_token","admin_secure","admin_username"]}`))
	request.Header.Set("Content-Type", "application/json")
	writer := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(writer)
	ctx.Request = request
	(Conf{}).Gets(ctx)

	var response struct {
		Code int               `json:"code"`
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode conf response %q: %v", writer.Body.String(), err)
	}
	if response.Code != 200 || response.Data["api_auth_token"] != maskedConfValue || response.Data["admin_secure"] != "/private-entry" {
		t.Fatalf("conf response = %+v", response)
	}

	request = httptest.NewRequest("POST", "/api/conf/sets", strings.NewReader(`[{"key":"admin_username","value":"owner"},{"key":"admin_secure","value":"/private-entry"}]`))
	request.Header.Set("Content-Type", "application/json")
	writer = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(writer)
	ctx.Request = request
	(Conf{}).Sets(ctx)
	var secure model.Conf
	if err := db.Where("k = ?", model.AdminSecure).First(&secure).Error; err != nil {
		t.Fatal(err)
	}
	if secure.V != "/private-entry" {
		t.Fatalf("secure entry after save = %q", secure.V)
	}
}
