package exchange

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestBinanceListIncomingFiltersUSDCFunds(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/sapi/v1/pay/transactions" {
			t.Errorf("unexpected Binance path: %s", request.URL.Path)
			http.NotFound(writer, request)
			return
		}
		response := binanceHistoryResponse{
			Code: json.RawMessage(`"000000"`),
			Data: []binanceHistoryRow{
				{
					FundsDetail: []binanceFund{
						{Amount: "10.0001", Currency: "USDT"},
						{Amount: "20.0001", Currency: "USDC"},
					},
					ReceiverInfo:    binanceReceiver{BinanceID: json.RawMessage(`"123456"`)},
					TransactionID:   json.RawMessage(`"bill-usdc"`),
					TransactionTime: json.RawMessage(strconv.FormatInt(now.UnixMilli(), 10)),
				},
			},
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer server.Close()

	client, err := NewBinanceClient(BinanceConfig{
		APIKey: "api-key", SecretKey: "secret-key", ReceiverUID: "123456", APIURL: server.URL,
	})
	if err != nil {
		t.Fatalf("create Binance client: %v", err)
	}
	transactions, err := client.ListIncoming(context.Background(), "usdc", now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("list Binance USDC transactions: %v", err)
	}
	if len(transactions) != 1 {
		t.Fatalf("transaction count = %d, want 1", len(transactions))
	}
	if transactions[0].Asset != "USDC" || transactions[0].Amount.String() != "20.0001" {
		t.Fatalf("unexpected USDC transaction: %+v", transactions[0])
	}
}
