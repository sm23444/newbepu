package exchange

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

type BinanceConfig struct {
	APIKey      string
	SecretKey   string
	ReceiverUID string
	APIURL      string
	Timeout     time.Duration
}

type BinanceClient struct {
	config        BinanceConfig
	http          *http.Client
	clockOffsetMS int64
}

type binanceFund struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type binanceHistoryRow struct {
	Amount          string          `json:"amount"`
	Currency        string          `json:"currency"`
	OrderType       string          `json:"orderType"`
	FundsDetail     []binanceFund   `json:"fundsDetail"`
	ReceiverInfo    binanceReceiver `json:"receiverInfo"`
	TransactionID   json.RawMessage `json:"transactionId"`
	TransactionTime json.RawMessage `json:"transactionTime"`
}

type binanceReceiver struct {
	BinanceID json.RawMessage `json:"binanceId"`
}

type binanceHistoryResponse struct {
	Code    json.RawMessage     `json:"code"`
	Success *bool               `json:"success"`
	Data    []binanceHistoryRow `json:"data"`
}

func NewBinanceClient(config BinanceConfig) (*BinanceClient, error) {
	if config.APIKey == "" || config.SecretKey == "" || !validUID(config.ReceiverUID) {
		return nil, fmt.Errorf("invalid Binance exchange payment configuration")
	}
	if _, err := url.ParseRequestURI(config.APIURL); err != nil {
		return nil, fmt.Errorf("invalid Binance API URL: %w", err)
	}
	if config.Timeout <= 0 {
		config.Timeout = 8 * time.Second
	}
	return &BinanceClient{
		config: config,
		http:   &http.Client{Timeout: config.Timeout},
	}, nil
}

func (c *BinanceClient) Provider() string {
	return "binance"
}

func (c *BinanceClient) ListIncoming(ctx context.Context, asset string, start, end time.Time) ([]Transaction, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if asset == "" || !start.Before(end) {
		return nil, fmt.Errorf("invalid Binance history range")
	}

	type window struct{ start, end int64 }
	windows := []window{{start: start.UnixMilli(), end: end.UnixMilli()}}
	rows := make(map[string]binanceHistoryRow)
	for requests := 0; len(windows) > 0; requests++ {
		if requests >= 100 {
			return nil, fmt.Errorf("Binance history exceeded request limit")
		}
		last := len(windows) - 1
		current := windows[last]
		windows = windows[:last]
		batch, err := c.history(ctx, current.start, current.end)
		if err != nil {
			return nil, err
		}
		if len(batch) == 100 {
			if current.end <= current.start {
				return nil, fmt.Errorf("Binance history window cannot be split")
			}
			midpoint := current.start + (current.end-current.start)/2
			windows = append(windows,
				window{start: midpoint + 1, end: current.end},
				window{start: current.start, end: midpoint},
			)
			continue
		}
		for _, row := range batch {
			id, err := scalarString(row.TransactionID)
			if err != nil || id == "" {
				continue
			}
			rows[id] = row
		}
	}

	transactions := make([]Transaction, 0, len(rows))
	for id, row := range rows {
		receiverUID, err := scalarString(row.ReceiverInfo.BinanceID)
		if err != nil || receiverUID != c.config.ReceiverUID {
			continue
		}
		if !isSupportedBinanceOrderType(row.OrderType) {
			continue
		}
		// Top-level fields describe the received transaction; fundsDetail only
		// describes the assets used to fund a combination payment.
		if strings.ToUpper(strings.TrimSpace(row.Currency)) != asset {
			continue
		}
		amount, err := decimal.NewFromString(strings.TrimSpace(row.Amount))
		if err != nil || !amount.IsPositive() {
			continue
		}
		timestamp, err := scalarInt64(row.TransactionTime)
		if err != nil {
			continue
		}
		if timestamp < 10_000_000_000 {
			timestamp *= 1000
		}
		occurredAt := time.UnixMilli(timestamp)
		if occurredAt.Before(start) || occurredAt.After(end) {
			continue
		}
		transactions = append(transactions, Transaction{
			Provider:      c.Provider(),
			TransactionID: id,
			Asset:         asset,
			Amount:        amount,
			ReceiverUID:   receiverUID,
			OccurredAt:    occurredAt,
		})
	}
	sort.Slice(transactions, func(i, j int) bool {
		return transactions[i].OccurredAt.Before(transactions[j].OccurredAt)
	})
	return transactions, nil
}

// Positive amount alone is not enough: only direct UID transfers and merchant
// payments can represent a customer payment for this gateway.
func isSupportedBinanceOrderType(orderType string) bool {
	switch strings.ToUpper(strings.TrimSpace(orderType)) {
	case "C2C", "PAY":
		return true
	default:
		return false
	}
}

func (c *BinanceClient) history(ctx context.Context, startMS, endMS int64) ([]binanceHistoryRow, error) {
	var payload binanceHistoryResponse
	err := c.signedGet(ctx, "/sapi/v1/pay/transactions", url.Values{
		"startTime": {strconv.FormatInt(startMS, 10)},
		"endTime":   {strconv.FormatInt(endMS, 10)},
		"limit":     {"100"},
	}, &payload, true)
	if err != nil {
		return nil, err
	}
	code, _ := scalarString(payload.Code)
	if (payload.Success != nil && !*payload.Success) || (code != "" && code != "000000") {
		return nil, fmt.Errorf("Binance Pay history rejected the request")
	}
	return payload.Data, nil
}

func (c *BinanceClient) signedGet(ctx context.Context, path string, values url.Values, target any, allowClockRetry bool) error {
	values.Set("recvWindow", "5000")
	values.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli()+c.clockOffsetMS, 10))
	query := values.Encode()
	mac := hmac.New(sha256.New, []byte(c.config.SecretKey))
	_, _ = mac.Write([]byte(query))
	values.Set("signature", hex.EncodeToString(mac.Sum(nil)))

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.APIURL+path+"?"+values.Encode(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-MBX-APIKEY", c.config.APIKey)
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("Binance request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("Binance response read failed: %w", err)
	}
	var envelope struct {
		Code json.RawMessage `json:"code"`
	}
	_ = json.Unmarshal(body, &envelope)
	code, _ := scalarString(envelope.Code)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if code == "-1021" && allowClockRetry {
			if err := c.synchronizeClock(ctx); err != nil {
				return err
			}
			return c.signedGet(ctx, path, valuesWithoutSignature(values), target, false)
		}
		return &providerHTTPError{provider: c.Provider(), status: response.StatusCode, code: code}
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("Binance returned invalid JSON: %w", err)
	}
	return nil
}

func (c *BinanceClient) synchronizeClock(ctx context.Context) error {
	started := time.Now()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.APIURL+"/api/v3/time", nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("Binance time request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &providerHTTPError{provider: c.Provider(), status: response.StatusCode}
	}
	var payload struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return fmt.Errorf("Binance time response invalid: %w", err)
	}
	completed := time.Now()
	localMidpoint := started.UnixMilli() + completed.Sub(started).Milliseconds()/2
	c.clockOffsetMS = payload.ServerTime - localMidpoint
	return nil
}

func valuesWithoutSignature(values url.Values) url.Values {
	copy := make(url.Values, len(values))
	for key, entries := range values {
		if key == "signature" || key == "timestamp" || key == "recvWindow" {
			continue
		}
		copy[key] = append([]string(nil), entries...)
	}
	return copy
}

func scalarString(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", err
		}
		return text, nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var number json.Number
	if err := decoder.Decode(&number); err != nil {
		return "", err
	}
	return number.String(), nil
}

func scalarInt64(raw json.RawMessage) (int64, error) {
	value, err := scalarString(raw)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(value, 10, 64)
}

func validUID(value string) bool {
	if value == "" || len(value) > 32 || value[0] == '0' {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

type providerHTTPError struct {
	provider string
	status   int
	code     string
}

func (e *providerHTTPError) Error() string {
	if e.code != "" {
		return fmt.Sprintf("%s returned HTTP %d (code %s)", e.provider, e.status, e.code)
	}
	return fmt.Sprintf("%s returned HTTP %d", e.provider, e.status)
}
