package epusdt

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
)

func setupPaymentReviewHandlerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "review-handler.db")+"?cache=shared&mode=rwc"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Order{}, &model.PaymentReview{}); err != nil {
		t.Fatalf("migrate db: %v", err)
	}
	previous := model.Db
	model.Db = db
	t.Cleanup(func() {
		model.Db = previous
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func TestAdminListBindsJSONPaginationAndStatus(t *testing.T) {
	db := setupPaymentReviewHandlerDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	zero := time.Unix(0, 0)
	for i := 0; i < 25; i++ {
		tradeID := "pending-" + string(rune('a'+i))
		order := model.Order{
			OrderId: "order-" + tradeID, TradeId: tradeID, TradeType: model.UsdtTrc20,
			Fiat: model.CNY, Crypto: model.USDT, Amount: "1", Money: "7", Rate: "7",
			Address: "TQm8xM9fS2x3a7WQzJzVJb5iP8h9Q1yXkL", MatchAddress: "TQm8xM9fS2x3a7WQzJzVJb5iP8h9Q1yXkL",
			Status: model.OrderStatusWaiting, RefHash: tradeID, ExpiredAt: now.Add(time.Hour), ConfirmedAt: &zero,
			AutoTimeAt: model.AutoTimeAt{CreatedAt: (*model.Datetime)(&now), UpdatedAt: (*model.Datetime)(&now)},
		}
		if err := db.Create(&order).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.PaymentReview{
			TradeID: tradeID, Status: model.PaymentReviewPending, Description: "pending review description",
			EvidencePath: "/tmp/evidence", EvidenceType: "image/png", EvidenceSize: 10, EvidenceSHA256: strings.Repeat("a", 64),
			AutoTimeAt: model.AutoTimeAt{CreatedAt: (*model.Datetime)(&now), UpdatedAt: (*model.Datetime)(&now)},
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		tradeID := "rejected-" + string(rune('a'+i))
		if err := db.Create(&model.PaymentReview{
			TradeID: tradeID, Status: model.PaymentReviewRejected, Description: "rejected review description",
			EvidencePath: "/tmp/evidence", EvidenceType: "image/png", EvidenceSize: 10, EvidenceSHA256: strings.Repeat("b", 64),
			AutoTimeAt: model.AutoTimeAt{CreatedAt: (*model.Datetime)(&now), UpdatedAt: (*model.Datetime)(&now)},
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest("POST", "/api/review/list", strings.NewReader(`{"page":2,"size":10,"status":"pending"}`))
	request.Header.Set("Content-Type", "application/json")
	writer := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(writer)
	ctx.Request = request
	(PaymentReview{}).AdminList(ctx)

	var response struct {
		Code  int              `json:"code"`
		Total int64            `json:"total"`
		Data  []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response %q: %v", writer.Body.String(), err)
	}
	if response.Code != 200 || response.Total != 25 || len(response.Data) != 10 {
		t.Fatalf("response = code %d total %d data %d, want 200/25/10", response.Code, response.Total, len(response.Data))
	}
	for _, item := range response.Data {
		if item["status"] != model.PaymentReviewPending {
			t.Fatalf("non-pending row returned: %#v", item)
		}
	}
}
