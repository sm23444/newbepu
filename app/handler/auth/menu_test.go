package auth

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPaymentReviewMenuIsBetweenOrderAndRateHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	writer := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(writer)
	(Auth{}).Menu(ctx)

	var response struct {
		Code int `json:"code"`
		Data []struct {
			Path      string `json:"path"`
			Name      string `json:"name"`
			Component string `json:"component"`
			Meta      struct {
				Title   string `json:"title"`
				SvgIcon string `json:"svgIcon"`
			} `json:"meta"`
		} `json:"data"`
	}
	if err := json.Unmarshal(writer.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode menu response %q: %v", writer.Body.String(), err)
	}
	if response.Code != 200 {
		t.Fatalf("menu response code = %d, want 200", response.Code)
	}

	var orderIndex, reviewIndex, rateIndex = -1, -1, -1
	for index, item := range response.Data {
		switch item.Name {
		case "order":
			orderIndex = index
		case "payment-review":
			reviewIndex = index
			if item.Path != "/payment-review" || item.Component != "review/review" || item.Meta.Title != "payment-review" || item.Meta.SvgIcon != "add-voucher" {
				t.Fatalf("payment review menu = %+v", item)
			}
		case "rate-list":
			rateIndex = index
		}
	}
	if orderIndex < 0 || reviewIndex != orderIndex+1 || rateIndex != reviewIndex+1 {
		t.Fatalf("menu indexes order/review/rate = %d/%d/%d", orderIndex, reviewIndex, rateIndex)
	}
}
