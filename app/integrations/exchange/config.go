package exchange

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultBinanceAPIURL = "https://api-gcp.binance.com"
	defaultOKXAPIURL     = "https://www.okx.com"
)

type RuntimeConfig struct {
	PollInterval time.Duration
	Timeout      time.Duration
	Binance      *BinanceConfig
	OKX          *OKXConfig
}

func LoadRuntimeConfig() (RuntimeConfig, error) {
	return LoadRuntimeConfigFrom(os.Getenv)
}

// LoadRuntimeConfigFrom loads exchange settings from the supplied lookup.
// It allows the application to combine database settings with environment
// variables while keeping configuration validation in one place.
func LoadRuntimeConfigFrom(lookup func(string) string) (RuntimeConfig, error) {
	if lookup == nil {
		lookup = os.Getenv
	}

	pollInterval, err := durationValue(lookup("BEPUSDT_EXCHANGE_POLL_INTERVAL"), "BEPUSDT_EXCHANGE_POLL_INTERVAL", 10*time.Second)
	if err != nil {
		return RuntimeConfig{}, err
	}
	if pollInterval < 5*time.Second || pollInterval > 10*time.Minute {
		return RuntimeConfig{}, fmt.Errorf("BEPUSDT_EXCHANGE_POLL_INTERVAL must be between 5s and 10m")
	}

	timeout, err := durationValue(lookup("BEPUSDT_EXCHANGE_TIMEOUT"), "BEPUSDT_EXCHANGE_TIMEOUT", 8*time.Second)
	if err != nil {
		return RuntimeConfig{}, err
	}
	if timeout < time.Second || timeout > 30*time.Second {
		return RuntimeConfig{}, fmt.Errorf("BEPUSDT_EXCHANGE_TIMEOUT must be between 1s and 30s")
	}

	binance, err := loadBinanceConfig(lookup, timeout)
	if err != nil {
		return RuntimeConfig{}, err
	}
	okx, err := loadOKXConfig(lookup, timeout)
	if err != nil {
		return RuntimeConfig{}, err
	}

	return RuntimeConfig{
		PollInterval: pollInterval,
		Timeout:      timeout,
		Binance:      binance,
		OKX:          okx,
	}, nil
}

func loadBinanceConfig(lookup func(string) string, timeout time.Duration) (*BinanceConfig, error) {
	apiKey := strings.TrimSpace(lookup("BEPUSDT_BINANCE_API_KEY"))
	secretKey := strings.TrimSpace(lookup("BEPUSDT_BINANCE_SECRET_KEY"))
	receiverUID := strings.TrimSpace(lookup("BEPUSDT_BINANCE_UID"))
	if apiKey == "" && secretKey == "" && receiverUID == "" {
		return nil, nil
	}
	if apiKey == "" || secretKey == "" || receiverUID == "" {
		return nil, fmt.Errorf("Binance exchange payments require API key, secret key, and UID")
	}

	apiURL := strings.TrimRight(strings.TrimSpace(lookup("BEPUSDT_BINANCE_API_URL")), "/")
	if apiURL == "" {
		apiURL = defaultBinanceAPIURL
	}
	apiURL, err := validateExchangeAPIURL(apiURL, "BEPUSDT_BINANCE_API_URL")
	if err != nil {
		return nil, err
	}

	return &BinanceConfig{
		APIKey:      apiKey,
		SecretKey:   secretKey,
		ReceiverUID: receiverUID,
		APIURL:      apiURL,
		Timeout:     timeout,
	}, nil
}

func loadOKXConfig(lookup func(string) string, timeout time.Duration) (*OKXConfig, error) {
	apiKey := strings.TrimSpace(lookup("BEPUSDT_OKX_API_KEY"))
	secretKey := strings.TrimSpace(lookup("BEPUSDT_OKX_SECRET_KEY"))
	passphrase := strings.TrimSpace(lookup("BEPUSDT_OKX_PASSPHRASE"))
	accountUID := strings.TrimSpace(lookup("BEPUSDT_OKX_UID"))
	if apiKey == "" && secretKey == "" && passphrase == "" && accountUID == "" {
		return nil, nil
	}
	if apiKey == "" || secretKey == "" || passphrase == "" || accountUID == "" {
		return nil, fmt.Errorf("OKX exchange payments require API key, secret key, passphrase, and UID")
	}

	apiURL := strings.TrimRight(strings.TrimSpace(lookup("BEPUSDT_OKX_API_URL")), "/")
	if apiURL == "" {
		apiURL = defaultOKXAPIURL
	}
	apiURL, err := validateExchangeAPIURL(apiURL, "BEPUSDT_OKX_API_URL")
	if err != nil {
		return nil, err
	}

	return &OKXConfig{
		APIKey:     apiKey,
		SecretKey:  secretKey,
		Passphrase: passphrase,
		AccountUID: accountUID,
		APIURL:     apiURL,
		Timeout:    timeout,
	}, nil
}

func validateExchangeAPIURL(raw, key string) (string, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("%s must be an absolute HTTPS URL", key)
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("%s must use HTTPS", key)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%s must not include credentials, a query, or a fragment", key)
	}

	return strings.TrimRight(raw, "/"), nil
}

func durationValue(raw, key string, fallback time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return value, nil
}
