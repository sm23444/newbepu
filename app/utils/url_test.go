package utils

import "testing"

func TestIsAllowedHTTPSURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{name: "https", url: "https://merchant.example/notify", want: true},
		{name: "https with port and query", url: "https://merchant.example:8443/return?id=1#paid", want: true},
		{name: "uppercase scheme", url: "HTTPS://merchant.example/return", want: true},
		{name: "http", url: "http://merchant.example/notify", want: false},
		{name: "relative", url: "/return", want: false},
		{name: "protocol relative", url: "//merchant.example/return", want: false},
		{name: "javascript", url: "javascript:alert(1)", want: false},
		{name: "userinfo", url: "https://user:pass@merchant.example/return", want: false},
		{name: "attribute injection", url: "https://merchant.example/\"><img src=x onerror=alert(1)>", want: false},
		{name: "backslash", url: "https:\\merchant.example\\return", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAllowedHTTPSURL(tt.url); got != tt.want {
				t.Fatalf("IsAllowedHTTPSURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
			if got := IsAllowedCallbackURL(tt.url); got != tt.want {
				t.Fatalf("IsAllowedCallbackURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestAppendURLQueryPreservesExistingQueryAndFragment(t *testing.T) {
	got, err := AppendURLQuery("https://merchant.example/return?existing=1#paid", "trade_status=TRADE_SUCCESS&sign=abc")
	if err != nil {
		t.Fatalf("AppendURLQuery() error = %v", err)
	}
	want := "https://merchant.example/return?existing=1&trade_status=TRADE_SUCCESS&sign=abc#paid"
	if got != want {
		t.Fatalf("AppendURLQuery() = %q, want %q", got, want)
	}
}
