package admin

import (
	"testing"

	"github.com/v03413/bepusdt/app/model"
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
