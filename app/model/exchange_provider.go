package model

import (
	"os"
	"strings"
)

type exchangeSetting struct {
	confKey  ConfKey
	envKey   string
	required bool
	uid      bool
}

type exchangeProviderConf struct {
	enabledKey ConfKey
	settings   []exchangeSetting
}

var exchangeProviderRegistry = map[string]exchangeProviderConf{
	"binance": {
		enabledKey: ExchangeBinanceEnabled,
		settings: []exchangeSetting{
			{confKey: ExchangeBinanceAPIKey, envKey: "BEPUSDT_BINANCE_API_KEY", required: true},
			{confKey: ExchangeBinanceSecretKey, envKey: "BEPUSDT_BINANCE_SECRET_KEY", required: true},
			{confKey: ExchangeBinanceUID, envKey: "BEPUSDT_BINANCE_UID", required: true, uid: true},
			{confKey: ExchangeBinanceAPIURL, envKey: "BEPUSDT_BINANCE_API_URL"},
		},
	},
	"okx": {
		enabledKey: ExchangeOKXEnabled,
		settings: []exchangeSetting{
			{confKey: ExchangeOKXAPIKey, envKey: "BEPUSDT_OKX_API_KEY", required: true},
			{confKey: ExchangeOKXSecretKey, envKey: "BEPUSDT_OKX_SECRET_KEY", required: true},
			{confKey: ExchangeOKXPassphrase, envKey: "BEPUSDT_OKX_PASSPHRASE", required: true},
			{confKey: ExchangeOKXUID, envKey: "BEPUSDT_OKX_UID", required: true, uid: true},
			{confKey: ExchangeOKXAPIURL, envKey: "BEPUSDT_OKX_API_URL"},
		},
	},
}

var exchangeRuntimeSettings = map[string]ConfKey{
	"BEPUSDT_EXCHANGE_POLL_INTERVAL": ExchangePollInterval,
	"BEPUSDT_EXCHANGE_TIMEOUT":       ExchangeTimeout,
}

func configuredExchangeValue(setting exchangeSetting) string {
	if value := strings.TrimSpace(GetC(setting.confKey)); value != "" {
		return value
	}

	return strings.TrimSpace(os.Getenv(setting.envKey))
}

func exchangeTradeConfig(t TradeType) (TradeTypeConf, bool) {
	trade, ok := registry[t]
	return trade, ok && trade.ExchangeProvider != ""
}

func IsExchangeTradeType(t TradeType) bool {
	_, ok := exchangeTradeConfig(t)
	return ok
}

func IsExchangeProviderEnabled(t TradeType) bool {
	trade, ok := exchangeTradeConfig(t)
	if !ok {
		return true
	}
	provider, ok := exchangeProviderRegistry[trade.ExchangeProvider]
	if !ok || strings.TrimSpace(GetC(provider.enabledKey)) == "0" {
		return false
	}
	uid := ""
	for _, setting := range provider.settings {
		if !setting.required && !setting.uid {
			continue
		}
		value := configuredExchangeValue(setting)
		if setting.required && value == "" {
			return false
		}
		if setting.uid {
			uid = value
		}
	}

	return exchangeUIDPattern.MatchString(uid)
}

func GetConfiguredExchangeUID(t TradeType) string {
	trade, ok := exchangeTradeConfig(t)
	if !ok {
		return ""
	}
	provider, ok := exchangeProviderRegistry[trade.ExchangeProvider]
	if !ok {
		return ""
	}

	for _, setting := range provider.settings {
		if setting.uid {
			return configuredExchangeValue(setting)
		}
	}

	return ""
}

func GetExchangeRuntimeConfigValue(key string) string {
	if confKey, ok := exchangeRuntimeSettings[key]; ok {
		if value := strings.TrimSpace(GetC(confKey)); value != "" {
			return value
		}
		return strings.TrimSpace(os.Getenv(key))
	}
	for _, provider := range exchangeProviderRegistry {
		for _, setting := range provider.settings {
			if setting.envKey != key {
				continue
			}
			if strings.TrimSpace(GetC(provider.enabledKey)) == "0" {
				return ""
			}
			return configuredExchangeValue(setting)
		}
	}

	return strings.TrimSpace(os.Getenv(key))
}

func paymentKind(t TradeType) string {
	if IsExchangeTradeType(t) {
		return "exchange"
	}
	return "chain"
}

func exchangeProvider(t TradeType) string {
	if trade, ok := exchangeTradeConfig(t); ok {
		return trade.ExchangeProvider
	}
	return ""
}

func receiverLabel(t TradeType) string {
	if trade, ok := exchangeTradeConfig(t); ok && trade.ReceiverLabel != "" {
		return trade.ReceiverLabel
	}
	return "收款地址"
}
