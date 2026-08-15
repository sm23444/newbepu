package model

import "testing"

func TestGetExchangeRuntimeConfigValueResolution(t *testing.T) {
	setExchangeReselectionTestConf(t, ExchangeBinanceEnabled, "1")
	setExchangeReselectionTestConf(t, ExchangeBinanceAPIKey, " database-key ")
	setExchangeReselectionTestConf(t, ExchangeBinanceSecretKey, "")
	setExchangeReselectionTestConf(t, ExchangePollInterval, " 15s ")
	setExchangeReselectionTestConf(t, ExchangeTimeout, "")
	t.Setenv("BEPUSDT_BINANCE_API_KEY", "environment-key")
	t.Setenv("BEPUSDT_BINANCE_SECRET_KEY", " environment-secret ")
	t.Setenv("BEPUSDT_EXCHANGE_POLL_INTERVAL", "30s")
	t.Setenv("BEPUSDT_EXCHANGE_TIMEOUT", " 5s ")

	tests := []struct {
		name string
		key  string
		want string
	}{
		{name: "provider database value wins", key: "BEPUSDT_BINANCE_API_KEY", want: "database-key"},
		{name: "provider environment fallback", key: "BEPUSDT_BINANCE_SECRET_KEY", want: "environment-secret"},
		{name: "runtime database value wins", key: "BEPUSDT_EXCHANGE_POLL_INTERVAL", want: "15s"},
		{name: "runtime environment fallback", key: "BEPUSDT_EXCHANGE_TIMEOUT", want: "5s"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := GetExchangeRuntimeConfigValue(test.key); got != test.want {
				t.Fatalf("GetExchangeRuntimeConfigValue(%q) = %q, want %q", test.key, got, test.want)
			}
		})
	}
}

func TestGetExchangeRuntimeConfigValueHonorsDisabledProvider(t *testing.T) {
	setExchangeReselectionTestConf(t, ExchangeBinanceEnabled, "0")
	setExchangeReselectionTestConf(t, ExchangeBinanceAPIKey, "database-key")
	setExchangeReselectionTestConf(t, ExchangeBinanceAPIURL, "https://database.example")
	t.Setenv("BEPUSDT_BINANCE_API_KEY", "environment-key")
	t.Setenv("BEPUSDT_BINANCE_API_URL", "https://environment.example")

	for _, key := range []string{"BEPUSDT_BINANCE_API_KEY", "BEPUSDT_BINANCE_API_URL"} {
		if got := GetExchangeRuntimeConfigValue(key); got != "" {
			t.Fatalf("disabled provider setting %q = %q, want empty", key, got)
		}
	}
}

func TestIsExchangeProviderEnabledRequiresCompleteCredentials(t *testing.T) {
	setExchangeReselectionTestConf(t, ExchangeOKXEnabled, "1")
	setExchangeReselectionTestConf(t, ExchangeOKXAPIKey, "okx-key")
	setExchangeReselectionTestConf(t, ExchangeOKXSecretKey, "okx-secret")
	setExchangeReselectionTestConf(t, ExchangeOKXPassphrase, "okx-passphrase")
	setExchangeReselectionTestConf(t, ExchangeOKXUID, "654321")

	if !IsExchangeProviderEnabled(UsdtOKX) {
		t.Fatal("complete OKX credentials were treated as disabled")
	}
	if got := GetConfiguredExchangeUID(UsdcOKX); got != "654321" {
		t.Fatalf("configured OKX UID = %q, want 654321", got)
	}

	confCache.Store(ExchangeOKXPassphrase, "")
	t.Setenv("BEPUSDT_OKX_PASSPHRASE", "")
	if IsExchangeProviderEnabled(UsdtOKX) {
		t.Fatal("OKX provider remained enabled without its required passphrase")
	}
}

func TestExchangeProviderRegistryDeclaresEachSettingOnce(t *testing.T) {
	seenConfKeys := make(map[ConfKey]ExchangeProvider)
	seenEnvKeys := make(map[string]ExchangeProvider)
	for name, provider := range exchangeProviderRegistry {
		if provider.enabledKey == "" {
			t.Fatalf("provider %s has no enabled key", name)
		}
		uidCount := 0
		for _, setting := range provider.settings {
			if setting.confKey == "" || setting.envKey == "" {
				t.Fatalf("provider %s contains an incomplete setting: %+v", name, setting)
			}
			if owner, exists := seenConfKeys[setting.confKey]; exists {
				t.Fatalf("providers %s and %s repeat configuration key %s", owner, name, setting.confKey)
			}
			seenConfKeys[setting.confKey] = name
			if owner, exists := seenEnvKeys[setting.envKey]; exists {
				t.Fatalf("providers %s and %s repeat environment key %s", owner, name, setting.envKey)
			}
			seenEnvKeys[setting.envKey] = name
			if setting.uid {
				uidCount++
				if !setting.required {
					t.Fatalf("provider %s UID setting must be required", name)
				}
			}
		}
		if uidCount != 1 {
			t.Fatalf("provider %s UID setting count = %d, want 1", name, uidCount)
		}
	}
	for tradeType, trade := range registry {
		if trade.ExchangeProvider == "" {
			continue
		}
		if _, exists := exchangeProviderRegistry[trade.ExchangeProvider]; !exists {
			t.Fatalf("trade type %s references unknown provider %q", tradeType, trade.ExchangeProvider)
		}
	}
}

func TestGetExchangeTradeTypeIncludesUSDTAndUSDC(t *testing.T) {
	for _, test := range []struct {
		provider string
		asset    string
		want     TradeType
	}{
		{provider: "binance", asset: "USDT", want: UsdtBinance},
		{provider: "binance", asset: "usdc", want: UsdcBinance},
		{provider: "OKX", asset: "USDT", want: UsdtOKX},
		{provider: "okx", asset: "USDC", want: UsdcOKX},
	} {
		if got := GetExchangeTradeType(test.provider, test.asset); got != test.want {
			t.Fatalf("GetExchangeTradeType(%q, %q) = %q, want %q", test.provider, test.asset, got, test.want)
		}
	}
	for _, test := range []struct {
		provider string
		asset    string
	}{
		{provider: "binanec", asset: "USDT"},
		{provider: "binance", asset: "BTC"},
	} {
		if got := GetExchangeTradeType(test.provider, test.asset); got != "" {
			t.Fatalf("unsupported provider/asset %q/%q mapped to %q", test.provider, test.asset, got)
		}
	}
}
