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
