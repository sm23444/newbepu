package admin

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/v03413/bepusdt/app/handler/base"
	"github.com/v03413/bepusdt/app/integrations/exchange"
	"github.com/v03413/bepusdt/app/model"
	"github.com/v03413/bepusdt/app/task"
	"gorm.io/gorm"
)

type Exchange struct{}

type exchangeProviderConfig struct {
	Enabled    bool   `json:"enabled"`
	APIURL     string `json:"api_url"`
	APIKey     string `json:"api_key"`
	SecretKey  string `json:"secret_key"`
	Passphrase string `json:"passphrase"`
	UID        string `json:"uid"`
}

type exchangeSaveReq struct {
	PollInterval string                 `json:"poll_interval"`
	Timeout      string                 `json:"timeout"`
	Binance      exchangeProviderConfig `json:"binance"`
	OKX          exchangeProviderConfig `json:"okx"`
}

type exchangeTestReq struct {
	Provider string `json:"provider" binding:"required"`
}

type exchangeProviderView struct {
	Enabled              bool   `json:"enabled"`
	APIURL               string `json:"api_url"`
	UID                  string `json:"uid"`
	APIKeyConfigured     bool   `json:"api_key_configured"`
	SecretKeyConfigured  bool   `json:"secret_key_configured"`
	PassphraseConfigured bool   `json:"passphrase_configured"`
	Source               string `json:"source"`
}

var exchangeEnvKeys = map[model.ConfKey]string{
	model.ExchangePollInterval:     "BEPUSDT_EXCHANGE_POLL_INTERVAL",
	model.ExchangeTimeout:          "BEPUSDT_EXCHANGE_TIMEOUT",
	model.ExchangeBinanceAPIKey:    "BEPUSDT_BINANCE_API_KEY",
	model.ExchangeBinanceSecretKey: "BEPUSDT_BINANCE_SECRET_KEY",
	model.ExchangeBinanceUID:       "BEPUSDT_BINANCE_UID",
	model.ExchangeBinanceAPIURL:    "BEPUSDT_BINANCE_API_URL",
	model.ExchangeOKXAPIKey:        "BEPUSDT_OKX_API_KEY",
	model.ExchangeOKXSecretKey:     "BEPUSDT_OKX_SECRET_KEY",
	model.ExchangeOKXPassphrase:    "BEPUSDT_OKX_PASSPHRASE",
	model.ExchangeOKXUID:           "BEPUSDT_OKX_UID",
	model.ExchangeOKXAPIURL:        "BEPUSDT_OKX_API_URL",
}

func (Exchange) Config(ctx *gin.Context) {
	binanceAPIKey := effectiveExchangeValue(model.ExchangeBinanceAPIKey)
	binanceSecret := effectiveExchangeValue(model.ExchangeBinanceSecretKey)
	binanceUID := effectiveExchangeValue(model.ExchangeBinanceUID)
	okxAPIKey := effectiveExchangeValue(model.ExchangeOKXAPIKey)
	okxSecret := effectiveExchangeValue(model.ExchangeOKXSecretKey)
	okxPassphrase := effectiveExchangeValue(model.ExchangeOKXPassphrase)
	okxUID := effectiveExchangeValue(model.ExchangeOKXUID)

	base.Ok(ctx, gin.H{
		"poll_interval": valueOrDefault(effectiveExchangeValue(model.ExchangePollInterval), "10s"),
		"timeout":       valueOrDefault(effectiveExchangeValue(model.ExchangeTimeout), "8s"),
		"binance": exchangeProviderView{
			Enabled:             providerEnabled(model.ExchangeBinanceEnabled, binanceAPIKey, binanceSecret, binanceUID),
			APIURL:              valueOrDefault(effectiveExchangeValue(model.ExchangeBinanceAPIURL), "https://api-gcp.binance.com"),
			UID:                 binanceUID,
			APIKeyConfigured:    binanceAPIKey != "",
			SecretKeyConfigured: binanceSecret != "",
			Source:              providerSource(model.ExchangeBinanceAPIKey, model.ExchangeBinanceSecretKey),
		},
		"okx": exchangeProviderView{
			Enabled:              providerEnabled(model.ExchangeOKXEnabled, okxAPIKey, okxSecret, okxPassphrase, okxUID),
			APIURL:               valueOrDefault(effectiveExchangeValue(model.ExchangeOKXAPIURL), "https://www.okx.com"),
			UID:                  okxUID,
			APIKeyConfigured:     okxAPIKey != "",
			SecretKeyConfigured:  okxSecret != "",
			PassphraseConfigured: okxPassphrase != "",
			Source:               providerSource(model.ExchangeOKXAPIKey, model.ExchangeOKXSecretKey, model.ExchangeOKXPassphrase),
		},
	})
}

func (Exchange) Save(ctx *gin.Context) {
	var req exchangeSaveReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		base.BadRequest(ctx, err.Error())
		return
	}

	values := exchangeSaveValues(req)
	if err := validateExchangeValues(values); err != nil {
		base.BadRequest(ctx, err.Error())
		return
	}

	if err := model.Db.Transaction(func(tx *gorm.DB) error {
		keys := make([]model.ConfKey, 0, len(values))
		rows := make([]model.Conf, 0, len(values))
		for key, value := range values {
			keys = append(keys, key)
			rows = append(rows, model.Conf{K: key, V: value})
		}
		if err := tx.Where("k IN ?", keys).Delete(&model.Conf{}).Error; err != nil {
			return err
		}
		return tx.Create(&rows).Error
	}); err != nil {
		base.Error(ctx, err)
		return
	}

	model.RefreshC()
	if err := task.ReloadExchangePayments(); err != nil {
		base.Error(ctx, err)
		return
	}
	base.Ok(ctx, "交易所支付配置已保存并生效")
}

func (Exchange) Test(ctx *gin.Context) {
	var req exchangeTestReq
	if err := ctx.ShouldBindJSON(&req); err != nil {
		base.BadRequest(ctx, err.Error())
		return
	}
	testCtx, cancel := context.WithTimeout(ctx.Request.Context(), 30*time.Second)
	defer cancel()
	count, err := task.TestExchangePayment(testCtx, req.Provider)
	if err != nil {
		base.BadRequest(ctx, err.Error())
		return
	}
	base.Ok(ctx, gin.H{"provider": strings.ToLower(req.Provider), "transactions_24h": count})
}

func exchangeSaveValues(req exchangeSaveReq) map[model.ConfKey]string {
	values := map[model.ConfKey]string{
		model.ExchangePollInterval:   strings.TrimSpace(req.PollInterval),
		model.ExchangeTimeout:        strings.TrimSpace(req.Timeout),
		model.ExchangeBinanceEnabled: boolString(req.Binance.Enabled),
		model.ExchangeBinanceAPIURL:  strings.TrimRight(strings.TrimSpace(req.Binance.APIURL), "/"),
		model.ExchangeBinanceUID:     strings.TrimSpace(req.Binance.UID),
		model.ExchangeOKXEnabled:     boolString(req.OKX.Enabled),
		model.ExchangeOKXAPIURL:      strings.TrimRight(strings.TrimSpace(req.OKX.APIURL), "/"),
		model.ExchangeOKXUID:         strings.TrimSpace(req.OKX.UID),
	}
	setSecretValue(values, model.ExchangeBinanceAPIKey, req.Binance.APIKey)
	setSecretValue(values, model.ExchangeBinanceSecretKey, req.Binance.SecretKey)
	setSecretValue(values, model.ExchangeOKXAPIKey, req.OKX.APIKey)
	setSecretValue(values, model.ExchangeOKXSecretKey, req.OKX.SecretKey)
	setSecretValue(values, model.ExchangeOKXPassphrase, req.OKX.Passphrase)
	return values
}

func setSecretValue(values map[model.ConfKey]string, key model.ConfKey, submitted string) {
	if value := strings.TrimSpace(submitted); value != "" {
		values[key] = value
		return
	}
	if current := strings.TrimSpace(model.GetC(key)); current != "" {
		values[key] = current
	}
}

func validateExchangeValues(values map[model.ConfKey]string) error {
	lookup := func(envKey string) string {
		for confKey, key := range exchangeEnvKeys {
			if key != envKey {
				continue
			}
			if value, ok := values[confKey]; ok {
				return value
			}
			return effectiveExchangeValue(confKey)
		}
		return ""
	}
	if values[model.ExchangeBinanceEnabled] == "0" {
		previous := lookup
		lookup = func(key string) string {
			if strings.HasPrefix(key, "BEPUSDT_BINANCE_") {
				return ""
			}
			return previous(key)
		}
	}
	if values[model.ExchangeOKXEnabled] == "0" {
		previous := lookup
		lookup = func(key string) string {
			if strings.HasPrefix(key, "BEPUSDT_OKX_") {
				return ""
			}
			return previous(key)
		}
	}
	runtime, err := exchange.LoadRuntimeConfigFrom(lookup)
	if err != nil {
		return err
	}
	if runtime.Binance != nil {
		if _, err := exchange.NewBinanceClient(*runtime.Binance); err != nil {
			return err
		}
	}
	if runtime.OKX != nil {
		if _, err := exchange.NewOKXClient(*runtime.OKX); err != nil {
			return err
		}
	}
	return nil
}

func effectiveExchangeValue(key model.ConfKey) string {
	if value := strings.TrimSpace(model.GetC(key)); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv(exchangeEnvKeys[key]))
}

func providerEnabled(key model.ConfKey, required ...string) bool {
	setting := model.GetC(key)
	if setting == "0" {
		return false
	}
	if setting == "1" {
		return true
	}
	for _, value := range required {
		if value == "" {
			return false
		}
	}
	return true
}

func providerSource(keys ...model.ConfKey) string {
	for _, key := range keys {
		if strings.TrimSpace(model.GetC(key)) != "" {
			return "database"
		}
	}
	for _, key := range keys {
		if strings.TrimSpace(os.Getenv(exchangeEnvKeys[key])) != "" {
			return "environment"
		}
	}
	return "none"
}

func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
