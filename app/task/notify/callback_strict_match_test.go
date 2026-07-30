package notify

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/v03413/bepusdt/app/model"
)

func TestCallbackSuccessRequiresExactMatch(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantFail bool
	}{
		{name: "success lowercase", body: "success", wantFail: false},
		{name: "ok lowercase", body: "ok", wantFail: false},
		{name: "SUCCESS uppercase", body: "SUCCESS", wantFail: false},
		{name: "OK uppercase", body: "OK", wantFail: false},
		{name: "token error contains ok", body: "token error", wantFail: true},
		{name: "broken contains ok", body: "broken", wantFail: true},
		{name: "Not OK", body: "Not OK", wantFail: true},
		{name: "Cookie contains ok", body: "Set-Cookie: session=abc", wantFail: true},
		{name: "success with trailing", body: "success!", wantFail: true},
		{name: "leading ok", body: "ok response", wantFail: true},
		{name: "response too large", body: strings.Repeat("a", 4097), wantFail: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newNotifyTestDB(t)
			initNotifyTestLog(t)
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(200)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			order := newWaitingOrder(srv.URL)
			order.Status = model.OrderStatusSuccess
			if err := db.Create(&order).Error; err != nil {
				t.Fatalf("seed order: %v", err)
			}

			client := srv.Client()
			client.Timeout = 2 * time.Second
			err := handleWithClient(order, false, client)
			if tt.wantFail && err == nil {
				t.Fatalf("expected failure for body %q but got success", tt.body)
			}
			if !tt.wantFail && err != nil {
				t.Fatalf("expected success for body %q but got error: %v", tt.body, err)
			}

			var persisted model.Order
			if err := db.First(&persisted, order.ID).Error; err != nil {
				t.Fatalf("reload order: %v", err)
			}
			if persisted.NotifyNum != 1 {
				t.Fatalf("notify_num = %d, want 1", persisted.NotifyNum)
			}
			wantState := model.OrderNotifyStateSucc
			if tt.wantFail {
				wantState = model.OrderNotifyStateFail
			}
			if persisted.NotifyState != wantState {
				t.Fatalf("notify_state = %d, want %d", persisted.NotifyState, wantState)
			}
			if persisted.NotifyClaimToken != "" || persisted.NotifyClaimUntil != nil {
				t.Fatal("callback completion left the notification claimed")
			}
		})
	}
}
