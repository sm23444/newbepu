package model

import (
	"net/url"
	"strings"
	"testing"
)

func TestOrderRedirectURLRequiresHTTPS(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "HTTPS", url: "https://merchant.example/return", want: "https://merchant.example/return"},
		{name: "HTTP", url: "http://merchant.example/return", want: ""},
		{name: "attribute injection", url: "https://merchant.example/\"><img src=x onerror=alert(1)>", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			order := Order{ReturnUrl: tt.url, Status: OrderStatusWaiting}
			if got := order.RedirectUrl(); got != tt.want {
				t.Fatalf("RedirectUrl() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOrderRedirectURLAppendsEpayParamsBeforeFragment(t *testing.T) {
	db := newTestDB(t, "order-url")
	if err := db.AutoMigrate(&Conf{}); err != nil {
		t.Fatalf("migrate configuration table: %v", err)
	}
	order := Order{
		ReturnUrl: "https://merchant.example/return?existing=1#paid",
		Status:    OrderStatusSuccess,
		ApiType:   OrderApiTypeEpay,
		OrderId:   "merchant-order",
		TradeId:   "trade-id",
		TradeType: UsdtTrc20,
		Money:     "7.00",
		Name:      "Test product",
	}

	redirect, err := url.Parse(order.RedirectUrl())
	if err != nil {
		t.Fatalf("parse redirect URL: %v", err)
	}
	if redirect.Fragment != "paid" {
		t.Fatalf("redirect fragment = %q, want paid", redirect.Fragment)
	}
	if !strings.Contains(redirect.RawQuery, "existing=1") || !strings.Contains(redirect.RawQuery, "trade_status=TRADE_SUCCESS") {
		t.Fatalf("redirect query did not preserve existing and EPay parameters: %q", redirect.RawQuery)
	}
}
