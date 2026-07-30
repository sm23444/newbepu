package exchange

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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

type OKXConfig struct {
	APIKey     string
	SecretKey  string
	Passphrase string
	AccountUID string
	APIURL     string
	Timeout    time.Duration
}

type OKXClient struct {
	config        OKXConfig
	http          *http.Client
	clockOffsetMS int64
	validated     bool
}

const okxTimestampLayout = "2006-01-02T15:04:05.000Z"

type okxBill struct {
	BalanceChange string          `json:"balChg"`
	BillID        json.RawMessage `json:"billId"`
	Currency      string          `json:"ccy"`
	Timestamp     json.RawMessage `json:"ts"`
	Type          json.RawMessage `json:"type"`
}

type okxBillsResponse struct {
	Code string    `json:"code"`
	Data []okxBill `json:"data"`
}

func NewOKXClient(config OKXConfig) (*OKXClient, error) {
	if config.APIKey == "" || config.SecretKey == "" || config.Passphrase == "" || !validUID(config.AccountUID) {
		return nil, fmt.Errorf("invalid OKX exchange payment configuration")
	}
	if _, err := url.ParseRequestURI(config.APIURL); err != nil {
		return nil, fmt.Errorf("invalid OKX API URL: %w", err)
	}
	if config.Timeout <= 0 {
		config.Timeout = 8 * time.Second
	}
	return &OKXClient{
		config: config,
		http:   &http.Client{Timeout: config.Timeout},
	}, nil
}

func (c *OKXClient) Provider() string {
	return "okx"
}

func (c *OKXClient) ListIncoming(ctx context.Context, asset string, start, end time.Time) ([]Transaction, error) {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if asset == "" || !start.Before(end) {
		return nil, fmt.Errorf("invalid OKX history range")
	}
	if !c.validated {
		if err := c.validateAccount(ctx); err != nil {
			return nil, err
		}
		c.validated = true
	}

	rows := make([]okxBill, 0, 200)
	for _, billType := range []string{"1", "72"} {
		after := ""
		for page := 0; page < 500; page++ {
			query := url.Values{
				"ccy":   {asset},
				"limit": {"100"},
				"type":  {billType},
			}
			if after != "" {
				query.Set("after", after)
			}
			var payload okxBillsResponse
			if err := c.signedGet(ctx, "/api/v5/asset/bills", query, &payload, true); err != nil {
				return nil, err
			}
			if payload.Code != "0" {
				return nil, fmt.Errorf("OKX funding bill request rejected")
			}
			rows = append(rows, payload.Data...)
			if len(payload.Data) < 100 {
				break
			}
			lastTimestamp, err := scalarInt64(payload.Data[len(payload.Data)-1].Timestamp)
			if err != nil {
				return nil, fmt.Errorf("OKX funding bill cursor is invalid")
			}
			nextAfter := strconv.FormatInt(normalizeMillis(lastTimestamp), 10)
			if nextAfter == after {
				return nil, fmt.Errorf("OKX funding bill cursor did not advance")
			}
			after = nextAfter
			belowStart := false
			for _, row := range payload.Data {
				timestamp, err := scalarInt64(row.Timestamp)
				if err == nil && normalizeMillis(timestamp) < start.UnixMilli() {
					belowStart = true
					break
				}
			}
			if belowStart {
				break
			}
		}
	}

	transactions := make([]Transaction, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.Currency == "" || strings.ToUpper(row.Currency) != asset ||
			(!scalarEquals(row.Type, "1") && !scalarEquals(row.Type, "72")) {
			continue
		}
		amount, err := decimal.NewFromString(row.BalanceChange)
		if err != nil || !amount.IsPositive() {
			continue
		}
		id, err := scalarString(row.BillID)
		if err != nil || id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		timestamp, err := scalarInt64(row.Timestamp)
		if err != nil {
			continue
		}
		occurredAt := time.UnixMilli(normalizeMillis(timestamp))
		if occurredAt.Before(start) || occurredAt.After(end) {
			continue
		}
		transactions = append(transactions, Transaction{
			Provider:      c.Provider(),
			TransactionID: id,
			Asset:         asset,
			Amount:        amount,
			ReceiverUID:   c.config.AccountUID,
			OccurredAt:    occurredAt,
		})
	}
	sort.Slice(transactions, func(i, j int) bool {
		return transactions[i].OccurredAt.Before(transactions[j].OccurredAt)
	})
	return transactions, nil
}

func (c *OKXClient) validateAccount(ctx context.Context) error {
	var payload struct {
		Code string `json:"code"`
		Data []struct {
			UID json.RawMessage `json:"uid"`
		} `json:"data"`
	}
	if err := c.signedGet(ctx, "/api/v5/account/config", nil, &payload, true); err != nil {
		return err
	}
	if payload.Code != "0" || len(payload.Data) == 0 {
		return fmt.Errorf("OKX account validation failed")
	}
	uid, err := scalarString(payload.Data[0].UID)
	if err != nil || uid != c.config.AccountUID {
		return fmt.Errorf("OKX API credentials do not match configured UID")
	}
	return nil
}

func (c *OKXClient) signedGet(ctx context.Context, path string, query url.Values, target any, allowClockRetry bool) error {
	if query == nil {
		query = url.Values{}
	}
	requestPath := path
	if encoded := query.Encode(); encoded != "" {
		requestPath += "?" + encoded
	}
	timestamp := formatOKXTimestamp(time.Now().Add(time.Duration(c.clockOffsetMS) * time.Millisecond))
	mac := hmac.New(sha256.New, []byte(c.config.SecretKey))
	_, _ = mac.Write([]byte(timestamp + http.MethodGet + requestPath))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.APIURL+requestPath, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("OK-ACCESS-KEY", c.config.APIKey)
	request.Header.Set("OK-ACCESS-PASSPHRASE", c.config.Passphrase)
	request.Header.Set("OK-ACCESS-SIGN", signature)
	request.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("OKX request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("OKX response read failed: %w", err)
	}
	var envelope struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &envelope)
	if envelope.Code == "50102" && allowClockRetry {
		if err := c.synchronizeClock(ctx); err != nil {
			return err
		}
		return c.signedGet(ctx, path, query, target, false)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || (envelope.Code != "" && envelope.Code != "0") {
		return &providerHTTPError{provider: c.Provider(), status: response.StatusCode, code: envelope.Code}
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("OKX returned invalid JSON: %w", err)
	}
	return nil
}

func formatOKXTimestamp(value time.Time) string {
	return value.UTC().Format(okxTimestampLayout)
}

func (c *OKXClient) synchronizeClock(ctx context.Context) error {
	started := time.Now()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.APIURL+"/api/v5/public/time", nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("OKX time request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &providerHTTPError{provider: c.Provider(), status: response.StatusCode}
	}
	var payload struct {
		Code string `json:"code"`
		Data []struct {
			Timestamp string `json:"ts"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return fmt.Errorf("OKX time response invalid: %w", err)
	}
	if payload.Code != "0" || len(payload.Data) == 0 {
		return fmt.Errorf("OKX time response rejected")
	}
	serverMS, err := strconv.ParseInt(payload.Data[0].Timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("OKX time response timestamp invalid: %w", err)
	}
	completed := time.Now()
	localMidpoint := started.UnixMilli() + completed.Sub(started).Milliseconds()/2
	c.clockOffsetMS = serverMS - localMidpoint
	return nil
}

func normalizeMillis(value int64) int64 {
	if value < 10_000_000_000 {
		return value * 1000
	}
	return value
}

func scalarEquals(raw json.RawMessage, expected string) bool {
	value, err := scalarString(raw)
	return err == nil && value == expected
}
