package exchange

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestFormatOKXTimestampUsesMillisecondPrecision(t *testing.T) {
	value := time.Date(2026, time.July, 24, 5, 6, 7, 123456789, time.FixedZone("UTC+8", 8*60*60))
	if got, want := formatOKXTimestamp(value), "2026-07-23T21:06:07.123Z"; got != want {
		t.Fatalf("unexpected OKX timestamp: got %q, want %q", got, want)
	}
}

func TestOKXListIncomingPaginatesWithLastBillTimestamp(t *testing.T) {
	const firstPageLastTimestamp = int64(1_700_000_000_001)
	firstPage := make([]okxBill, 100)
	for i := range firstPage {
		firstPage[i] = testOKXBill(
			"bill-"+strconv.Itoa(101-i),
			firstPageLastTimestamp+int64(99-i),
		)
	}
	secondPage := []okxBill{testOKXBill("bill-1", firstPageLastTimestamp-1)}

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v5/asset/bills" {
			t.Errorf("unexpected OKX path: %s", request.URL.Path)
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("type") == "72" {
			requests.Add(1)
			_ = json.NewEncoder(writer).Encode(okxBillsResponse{Code: "0"})
			return
		}
		switch requests.Add(1) {
		case 1:
			if got := request.URL.Query().Get("after"); got != "" {
				t.Errorf("first page unexpectedly has after cursor %q", got)
			}
			_ = json.NewEncoder(writer).Encode(okxBillsResponse{Code: "0", Data: firstPage})
		case 2:
			if got, want := request.URL.Query().Get("after"), strconv.FormatInt(firstPageLastTimestamp, 10); got != want {
				t.Errorf("second page cursor: got %q, want last bill timestamp %q", got, want)
			}
			_ = json.NewEncoder(writer).Encode(okxBillsResponse{Code: "0", Data: secondPage})
		default:
			t.Errorf("unexpected extra OKX bills request")
			_ = json.NewEncoder(writer).Encode(okxBillsResponse{Code: "0"})
		}
	}))
	defer server.Close()

	client, err := NewOKXClient(OKXConfig{
		APIKey:     "api-key",
		SecretKey:  "secret-key",
		Passphrase: "passphrase",
		AccountUID: "123456",
		APIURL:     server.URL,
	})
	if err != nil {
		t.Fatalf("create OKX client: %v", err)
	}
	client.validated = true
	start := time.UnixMilli(firstPageLastTimestamp - 1)
	end := time.UnixMilli(firstPageLastTimestamp + 100)
	transactions, err := client.ListIncoming(context.Background(), "USDT", start, end)
	if err != nil {
		t.Fatalf("list incoming OKX bills: %v", err)
	}
	if got, want := len(transactions), 101; got != want {
		t.Fatalf("unexpected transaction count: got %d, want %d", got, want)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("unexpected request count: got %d, want 3", got)
	}
}

func TestOKXListIncomingAcceptsCurrentAndLegacyDepositBillTypes(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		bill := testOKXBill("bill-"+request.URL.Query().Get("type"), now.UnixMilli())
		bill.Type = json.RawMessage(strconv.Quote(request.URL.Query().Get("type")))
		_ = json.NewEncoder(writer).Encode(okxBillsResponse{Code: "0", Data: []okxBill{bill}})
	}))
	defer server.Close()

	client, err := NewOKXClient(OKXConfig{
		APIKey: "api-key", SecretKey: "secret-key", Passphrase: "passphrase",
		AccountUID: "123456", APIURL: server.URL,
	})
	if err != nil {
		t.Fatalf("create OKX client: %v", err)
	}
	client.validated = true
	transactions, err := client.ListIncoming(context.Background(), "USDT", now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("list incoming OKX bills: %v", err)
	}
	if got, want := len(transactions), 2; got != want {
		t.Fatalf("unexpected transaction count: got %d, want %d", got, want)
	}
}

func testOKXBill(id string, timestamp int64) okxBill {
	return okxBill{
		BalanceChange: "1.0000",
		BillID:        json.RawMessage(strconv.Quote(id)),
		Currency:      "USDT",
		Timestamp:     json.RawMessage(strconv.FormatInt(timestamp, 10)),
		Type:          json.RawMessage(`"1"`),
	}
}
