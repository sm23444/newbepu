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

func TestBinanceListIncomingUsesTopLevelTransactionAsset(t *testing.T) {
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
					Amount:    "20.0001",
					Currency:  "USDC",
					OrderType: "PAY",
					FundsDetail: []binanceFund{
						{Amount: "1.5", Currency: "BNB"},
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

func TestBinanceListIncomingDoesNotTreatCombinationFundingAsReceivedAsset(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		response := binanceHistoryResponse{
			Code: json.RawMessage(`"000000"`),
			Data: []binanceHistoryRow{
				{
					Amount:    "1.5",
					Currency:  "BNB",
					OrderType: "C2C",
					FundsDetail: []binanceFund{
						{Amount: "20.0001", Currency: "USDC"},
					},
					ReceiverInfo:    binanceReceiver{BinanceID: json.RawMessage(`"123456"`)},
					TransactionID:   json.RawMessage(`"combination-payment"`),
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
	transactions, err := client.ListIncoming(context.Background(), "USDC", now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("list Binance USDC transactions: %v", err)
	}
	if len(transactions) != 0 {
		t.Fatalf("combination-payment funding returned as USDC received asset: %+v", transactions)
	}
}

func TestBinanceListIncomingFiltersUnsupportedOrderTypes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		response := binanceHistoryResponse{
			Code: json.RawMessage(`"000000"`),
			Data: []binanceHistoryRow{
				{
					Amount: "1", Currency: "USDT", OrderType: "C2C",
					ReceiverInfo:  binanceReceiver{BinanceID: json.RawMessage(`"123456"`)},
					TransactionID: json.RawMessage(`"c2c"`), TransactionTime: json.RawMessage(strconv.FormatInt(now.UnixMilli(), 10)),
				},
				{
					Amount: "2", Currency: "USDT", OrderType: "PAY",
					ReceiverInfo:  binanceReceiver{BinanceID: json.RawMessage(`"123456"`)},
					TransactionID: json.RawMessage(`"pay"`), TransactionTime: json.RawMessage(strconv.FormatInt(now.UnixMilli(), 10)),
				},
				{
					Amount: "3", Currency: "USDT", OrderType: "PAY_REFUND",
					ReceiverInfo:  binanceReceiver{BinanceID: json.RawMessage(`"123456"`)},
					TransactionID: json.RawMessage(`"refund"`), TransactionTime: json.RawMessage(strconv.FormatInt(now.UnixMilli(), 10)),
				},
				{
					Amount: "4", Currency: "USDT", OrderType: "CRYPTO_BOX_RF",
					ReceiverInfo:  binanceReceiver{BinanceID: json.RawMessage(`"123456"`)},
					TransactionID: json.RawMessage(`"box-refund"`), TransactionTime: json.RawMessage(strconv.FormatInt(now.UnixMilli(), 10)),
				},
				{
					Amount: "5", Currency: "USDT",
					ReceiverInfo:  binanceReceiver{BinanceID: json.RawMessage(`"123456"`)},
					TransactionID: json.RawMessage(`"missing-type"`), TransactionTime: json.RawMessage(strconv.FormatInt(now.UnixMilli(), 10)),
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
	transactions, err := client.ListIncoming(context.Background(), "USDT", now.Add(-time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("list Binance transactions: %v", err)
	}
	if len(transactions) != 2 {
		t.Fatalf("transaction count = %d, want 2: %+v", len(transactions), transactions)
	}
	accepted := make(map[string]bool, len(transactions))
	for _, transaction := range transactions {
		accepted[transaction.TransactionID] = true
	}
	if !accepted["c2c"] || !accepted["pay"] {
		t.Fatalf("unexpected accepted transactions: %+v", transactions)
	}
}
