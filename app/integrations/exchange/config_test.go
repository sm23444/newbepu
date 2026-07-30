package exchange

import (
	"strings"
	"testing"
	"time"
)

func TestLoadRuntimeConfigFrom(t *testing.T) {
	values := map[string]string{
		"BEPUSDT_EXCHANGE_POLL_INTERVAL": "15s",
		"BEPUSDT_EXCHANGE_TIMEOUT":       "3s",
		"BEPUSDT_BINANCE_API_KEY":        "binance-key",
		"BEPUSDT_BINANCE_SECRET_KEY":     "binance-secret",
		"BEPUSDT_BINANCE_UID":            "123456",
		"BEPUSDT_BINANCE_API_URL":        "https://binance.example/",
		"BEPUSDT_OKX_API_KEY":            "okx-key",
		"BEPUSDT_OKX_SECRET_KEY":         "okx-secret",
		"BEPUSDT_OKX_PASSPHRASE":         "okx-pass",
		"BEPUSDT_OKX_UID":                "654321",
		"BEPUSDT_OKX_API_URL":            "https://okx.example/",
	}
	config, err := LoadRuntimeConfigFrom(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if config.PollInterval != 15*time.Second || config.Timeout != 3*time.Second {
		t.Fatalf("unexpected timing config: %#v", config)
	}
	if config.Binance == nil || config.Binance.APIURL != "https://binance.example" {
		t.Fatalf("unexpected Binance config: %#v", config.Binance)
	}
	if config.OKX == nil || config.OKX.APIURL != "https://okx.example" {
		t.Fatalf("unexpected OKX config: %#v", config.OKX)
	}
}

func TestLoadRuntimeConfigFromAllowsNoProviders(t *testing.T) {
	config, err := LoadRuntimeConfigFrom(func(string) string { return "" })
	if err != nil {
		t.Fatalf("load empty config: %v", err)
	}
	if config.Binance != nil || config.OKX != nil {
		t.Fatalf("expected no providers, got %#v", config)
	}
}

func TestLoadRuntimeConfigFromRejectsInsecureProviderURLs(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
		key    string
	}{
		{
			name: "Binance",
			key:  "BEPUSDT_BINANCE_API_URL",
			values: map[string]string{
				"BEPUSDT_BINANCE_API_KEY":    "key",
				"BEPUSDT_BINANCE_SECRET_KEY": "secret",
				"BEPUSDT_BINANCE_UID":        "123",
				"BEPUSDT_BINANCE_API_URL":    "http://binance.example",
			},
		},
		{
			name: "OKX",
			key:  "BEPUSDT_OKX_API_URL",
			values: map[string]string{
				"BEPUSDT_OKX_API_KEY":    "key",
				"BEPUSDT_OKX_SECRET_KEY": "secret",
				"BEPUSDT_OKX_PASSPHRASE": "passphrase",
				"BEPUSDT_OKX_UID":        "123",
				"BEPUSDT_OKX_API_URL":    "http://okx.example",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadRuntimeConfigFrom(func(key string) string { return tt.values[key] })
			if err == nil {
				t.Fatal("expected insecure URL rejection")
			}
			if !strings.Contains(err.Error(), tt.key) || !strings.Contains(err.Error(), "HTTPS") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
