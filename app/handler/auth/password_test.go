package auth

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/v03413/bepusdt/app/model"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func TestSetPasswordRejectsValuesOverBcryptLimitWithoutChangingHash(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "password.db")+"?cache=shared&mode=rwc"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Conf{}); err != nil {
		t.Fatalf("migrate conf: %v", err)
	}
	oldHash, err := bcrypt.GenerateFromPassword([]byte("current-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Conf{K: model.AdminPassword, V: string(oldHash)}).Error; err != nil {
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

	newPassword := strings.Repeat("a", 73)
	body := `{"password":"current-password","new_password":"` + newPassword + `","confirm_password":"` + newPassword + `"}`
	request := httptest.NewRequest("POST", "/api/auth/set_password", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	writer := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(writer)
	ctx.Request = request
	(Auth{}).SetPassword(ctx)

	var response struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response %q: %v", writer.Body.String(), err)
	}
	if response.Code != 400 || !strings.Contains(response.Msg, "72") {
		t.Fatalf("response = %+v, want bcrypt limit error", response)
	}
	if got := model.GetK(model.AdminPassword); got != string(oldHash) {
		t.Fatal("rejected password update changed the stored hash")
	}
}
