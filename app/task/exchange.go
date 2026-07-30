package task

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"github.com/v03413/bepusdt/app/integrations/exchange"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
)

var (
	exchangePoller *exchangeRuntime
	exchangeMu     sync.RWMutex
)

type exchangeRuntime struct {
	config  exchange.RuntimeConfig
	clients []exchange.Client
	cleaned time.Time
}

func init() {
	Register(Task{Callback: exchangePollingLoop})
}

func exchangeInit() error {
	return ReloadExchangePayments()
}

func loadExchangeRuntimeConfig() (exchange.RuntimeConfig, error) {
	return exchange.LoadRuntimeConfigFrom(func(key string) string {
		provider := ""
		if strings.HasPrefix(key, "BEPUSDT_BINANCE_") {
			provider = model.GetC(model.ExchangeBinanceEnabled)
		}
		if strings.HasPrefix(key, "BEPUSDT_OKX_") {
			provider = model.GetC(model.ExchangeOKXEnabled)
		}
		if provider == "0" {
			return ""
		}

		if confKey, ok := exchangeConfigKeys[key]; ok {
			if value := strings.TrimSpace(model.GetC(confKey)); value != "" {
				return value
			}
		}
		return os.Getenv(key)
	})
}

var exchangeConfigKeys = map[string]model.ConfKey{
	"BEPUSDT_EXCHANGE_POLL_INTERVAL": model.ExchangePollInterval,
	"BEPUSDT_EXCHANGE_TIMEOUT":       model.ExchangeTimeout,
	"BEPUSDT_BINANCE_API_KEY":        model.ExchangeBinanceAPIKey,
	"BEPUSDT_BINANCE_SECRET_KEY":     model.ExchangeBinanceSecretKey,
	"BEPUSDT_BINANCE_UID":            model.ExchangeBinanceUID,
	"BEPUSDT_BINANCE_API_URL":        model.ExchangeBinanceAPIURL,
	"BEPUSDT_OKX_API_KEY":            model.ExchangeOKXAPIKey,
	"BEPUSDT_OKX_SECRET_KEY":         model.ExchangeOKXSecretKey,
	"BEPUSDT_OKX_PASSPHRASE":         model.ExchangeOKXPassphrase,
	"BEPUSDT_OKX_UID":                model.ExchangeOKXUID,
	"BEPUSDT_OKX_API_URL":            model.ExchangeOKXAPIURL,
}

// ReloadExchangePayments applies database and environment settings without
// restarting the service.
func ReloadExchangePayments() error {
	config, err := loadExchangeRuntimeConfig()
	if err != nil {
		return err
	}
	clients := make([]exchange.Client, 0, 2)
	if config.Binance != nil {
		client, err := exchange.NewBinanceClient(*config.Binance)
		if err != nil {
			return err
		}
		if err := model.EnsureExchangeWallet(model.UsdtBinance, config.Binance.ReceiverUID, "Binance Pay"); err != nil {
			return err
		}
		clients = append(clients, client)
	}
	if config.OKX != nil {
		client, err := exchange.NewOKXClient(*config.OKX)
		if err != nil {
			return err
		}
		if err := model.EnsureExchangeWallet(model.UsdtOKX, config.OKX.AccountUID, "OKX Pay"); err != nil {
			return err
		}
		clients = append(clients, client)
	}
	if len(clients) == 0 {
		exchangeMu.Lock()
		exchangePoller = nil
		exchangeMu.Unlock()
		log.Task.Info("exchange payment polling disabled: no Binance or OKX credentials configured")
		return nil
	}
	exchangeMu.Lock()
	exchangePoller = &exchangeRuntime{config: config, clients: clients}
	exchangeMu.Unlock()
	providers := make([]string, 0, len(clients))
	for _, client := range clients {
		providers = append(providers, client.Provider())
	}
	log.Task.Info("exchange payment polling enabled: ", strings.Join(providers, ", "))
	return nil
}

func exchangePollingLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var active *exchangeRuntime
	var nextPoll time.Time
	poll := func(runtime *exchangeRuntime) {
		for _, client := range runtime.clients {
			pollExchange(ctx, client)
		}
		if runtime.cleaned.IsZero() || time.Since(runtime.cleaned) >= time.Hour {
			if err := model.DeleteExchangeTransactionsBefore(time.Now().Add(-7 * 24 * time.Hour)); err != nil {
				log.Task.Warn("exchange transaction cleanup failed:", err)
			}
			runtime.cleaned = time.Now()
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			exchangeMu.RLock()
			runtime := exchangePoller
			exchangeMu.RUnlock()
			if runtime == nil {
				active = nil
				continue
			}
			if runtime != active {
				active = runtime
				nextPoll = time.Time{}
			}
			if time.Now().Before(nextPoll) {
				continue
			}
			poll(runtime)
			nextPoll = time.Now().Add(runtime.config.PollInterval)
		}
	}
}

// TestExchangePayment checks the configured account and history permission.
func TestExchangePayment(ctx context.Context, provider string) (int, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	config, err := loadExchangeRuntimeConfig()
	if err != nil {
		return 0, err
	}
	var client exchange.Client
	switch provider {
	case "binance":
		if config.Binance == nil {
			return 0, fmt.Errorf("Binance credentials are not configured")
		}
		client, err = exchange.NewBinanceClient(*config.Binance)
	case "okx":
		if config.OKX == nil {
			return 0, fmt.Errorf("OKX credentials are not configured")
		}
		client, err = exchange.NewOKXClient(*config.OKX)
	default:
		return 0, fmt.Errorf("unsupported exchange provider")
	}
	if err != nil {
		return 0, err
	}
	transactions, err := client.ListIncoming(ctx, "USDT", time.Now().Add(-24*time.Hour), time.Now())
	if err != nil {
		return 0, err
	}
	return len(transactions), nil
}

func pollExchange(ctx context.Context, client exchange.Client) {
	tradeType := exchangeTradeType(client.Provider())
	if tradeType == "" || !hasLookbackOrders([]model.TradeType{tradeType}) {
		return
	}

	now := time.Now()
	start := now.Add(model.GetLookbackHour())
	transactions, err := client.ListIncoming(ctx, "USDT", start, now)
	if err != nil {
		log.Task.Warn(client.Provider(), " exchange payment scan failed:", err)
		return
	}
	rows := make([]model.ExchangeTransaction, 0, len(transactions))
	for _, transaction := range transactions {
		rows = append(rows, model.ExchangeTransaction{
			Provider:      transaction.Provider,
			TransactionID: transaction.TransactionID,
			TradeType:     tradeType,
			Asset:         transaction.Asset,
			Amount:        transaction.Amount.String(),
			ReceiverUID:   transaction.ReceiverUID,
			OccurredAt:    transaction.OccurredAt,
		})
	}
	if err := model.StoreExchangeTransactions(rows); err != nil {
		log.Task.Warn(client.Provider(), " exchange transaction store failed:", err)
		return
	}
	pending, err := model.PendingExchangeTransactions(client.Provider(), start, 500)
	if err != nil {
		log.Task.Warn(client.Provider(), " exchange pending transaction query failed:", err)
		return
	}
	transfers := make([]transfer, 0, len(pending))
	for _, row := range pending {
		amount, err := decimal.NewFromString(row.Amount)
		if err != nil {
			log.Task.Warn(client.Provider(), " exchange transaction has invalid amount:", err)
			continue
		}
		transfers = append(transfers, transfer{
			Network:     client.Provider(),
			TxHash:      row.TransactionID,
			Amount:      amount,
			FromAddress: client.Provider() + "-pay",
			RecvAddress: row.ReceiverUID,
			Timestamp:   row.OccurredAt,
			TradeType:   row.TradeType,
			BlockNum:    int(row.OccurredAt.Unix()),
			Final:       true,
			Source:      row.Provider,
			SourceID:    row.TransactionID,
		})
	}
	if len(transfers) > 0 {
		transferQueue.In <- transfers
	}
}

func exchangeTradeType(provider string) model.TradeType {
	switch provider {
	case "binance":
		return model.UsdtBinance
	case "okx":
		return model.UsdtOKX
	default:
		return ""
	}
}
