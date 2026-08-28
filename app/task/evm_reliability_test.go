package task

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/sirupsen/logrus"
	"github.com/smallnest/chanx"
	"github.com/tidwall/gjson"
	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
)

const (
	evmTestTxHash    = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	evmTestBlockHash = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func setupEVMReliabilityTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "evm-reliability.db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?cache=shared&mode=rwc&_pragma=busy_timeout(1000)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&model.Order{}, &model.PaymentTransactionClaim{}, &model.Conf{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	previousDB := model.Db
	model.Db = db
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get SQL database: %v", err)
	}
	t.Cleanup(func() {
		model.Db = previousDB
		chainBlockNum.Delete(conf.Polygon)
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	})

	return db
}

func setEVMReliabilityTestConf(t *testing.T, key model.ConfKey, value string) {
	t.Helper()

	previous := model.GetC(key)
	if err := model.SetK(key, value); err != nil {
		t.Fatalf("set test configuration %s: %v", key, err)
	}
	t.Cleanup(func() {
		if err := model.SetK(key, previous); err != nil {
			t.Errorf("restore test configuration %s: %v", key, err)
		}
	})
}

func setEVMTestTaskLogger(t *testing.T) {
	t.Helper()

	previous := log.Task
	log.Task = logrus.New()
	t.Cleanup(func() { log.Task = previous })
}

func TestBuildEVMLogRequestFiltersByNetworkContracts(t *testing.T) {
	post, err := buildEVMLogRequest(conf.Polygon, evmBlock{From: 10, To: 12})
	if err != nil {
		t.Fatalf("build eth_getLogs request: %v", err)
	}
	if post == nil {
		t.Fatal("eth_getLogs request was empty for a registered token network")
	}

	var request evmRPCRequest
	if err := json.Unmarshal(post, &request); err != nil {
		t.Fatalf("decode eth_getLogs request: %v", err)
	}
	if request.Method != "eth_getLogs" {
		t.Fatalf("RPC method = %q, want eth_getLogs", request.Method)
	}
	encoded, err := json.Marshal(request.Params)
	if err != nil {
		t.Fatalf("encode captured params: %v", err)
	}
	filter := gjson.ParseBytes(encoded).Get("0")
	if filter.Get("fromBlock").String() != "0xa" || filter.Get("toBlock").String() != "0xc" {
		t.Fatalf("unexpected block filter: %s", filter.Raw)
	}
	gotAddresses := make([]string, 0)
	for _, address := range filter.Get("address").Array() {
		gotAddresses = append(gotAddresses, address.String())
	}
	wantAddresses := model.GetNetworkContractAddresses(conf.Polygon)
	if !reflect.DeepEqual(gotAddresses, wantAddresses) {
		t.Fatalf("contract filter = %#v, want %#v", gotAddresses, wantAddresses)
	}
	if len(gotAddresses) == 0 {
		t.Fatal("eth_getLogs request did not contain a contract address filter")
	}
	for _, address := range model.GetNetworkContractAddresses(conf.Ethereum) {
		for _, got := range gotAddresses {
			if got == address {
				t.Fatalf("Polygon filter contains Ethereum-only contract %s", address)
			}
		}
	}
}

func TestGetBlockByNumberSkipsZeroAmountTransferWithoutRetry(t *testing.T) {
	setupEVMReliabilityTestDB(t)
	setEVMTestTaskLogger(t)

	contracts := model.GetNetworkContractAddresses(conf.Polygon)
	if len(contracts) == 0 {
		t.Fatal("Polygon has no registered token contracts")
	}
	contract := contracts[0]
	fromTopic := "0x0000000000000000000000001111111111111111111111111111111111111111"
	recvTopic := "0x0000000000000000000000002222222222222222222222222222222222222222"
	zeroAmount := "0x0000000000000000000000000000000000000000000000000000000000000000"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode RPC request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch request.(type) {
		case []any:
			_, _ = fmt.Fprint(w, `[{"jsonrpc":"2.0","result":{"number":"0xa","timestamp":"0x64","transactions":[]},"id":10}]`)
		case map[string]any:
			_, _ = fmt.Fprintf(
				w,
				`{"jsonrpc":"2.0","result":[{"address":%q,"topics":[%q,%q,%q],"data":%q,"blockNumber":"0xa","transactionHash":%q,"removed":false}],"id":1}`,
				contract,
				evmTransferEvent,
				fromTopic,
				recvTopic,
				zeroAmount,
				evmTestTxHash,
			)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	setEVMReliabilityTestConf(t, model.RpcEndpointPolygon, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := chanx.NewUnboundedChan[evmBlock](ctx, 1)
	retries := 0
	e := evm{
		Network:        conf.Polygon,
		Client:         server.Client(),
		blockScanQueue: queue,
		blockRetryAfter: func(_ time.Duration, retry func()) {
			retries++
			retry()
		},
	}

	transfers, err := e.parseEventTransfer(
		evmBlock{From: 10, To: 10},
		map[int64]time.Time{10: time.Unix(100, 0)},
	)
	if err != nil {
		t.Fatalf("parse zero-amount transfer: %v", err)
	}
	if len(transfers) != 0 {
		t.Fatalf("zero-amount transfer produced %d transfers", len(transfers))
	}

	e.getBlockByNumber(evmBlock{From: 10, To: 10})

	if retries != 0 {
		t.Fatalf("zero-amount transfer triggered %d block retries", retries)
	}
	if queue.Len() != 0 {
		t.Fatalf("zero-amount transfer requeued %d block ranges", queue.Len())
	}
}

func TestParseEventTransferSkipsMalformedLogAndKeepsValidTransfer(t *testing.T) {
	setupEVMReliabilityTestDB(t)

	contracts := model.GetNetworkContractAddresses(conf.Polygon)
	if len(contracts) == 0 {
		t.Fatal("Polygon has no registered token contracts")
	}
	contract := contracts[0]
	fromTopic := "0x0000000000000000000000001111111111111111111111111111111111111111"
	recvTopic := "0x0000000000000000000000002222222222222222222222222222222222222222"
	amount := "0x0000000000000000000000000000000000000000000000000000000000000001"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(
			w,
			`{"jsonrpc":"2.0","result":[{"address":%q,"topics":[%q],"data":%q,"blockNumber":"0xa","transactionHash":%q},{"address":%q,"topics":[%q,%q,%q],"data":%q,"blockNumber":"0xa","transactionHash":%q}],"id":1}`,
			contract,
			evmTransferEvent,
			amount,
			"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			contract,
			evmTransferEvent,
			fromTopic,
			recvTopic,
			amount,
			evmTestTxHash,
		)
	}))
	defer server.Close()
	setEVMReliabilityTestConf(t, model.RpcEndpointPolygon, server.URL)

	e := evm{Network: conf.Polygon, Client: server.Client()}
	transfers, err := e.parseEventTransfer(
		evmBlock{From: 10, To: 10},
		map[int64]time.Time{10: time.Unix(100, 0)},
	)
	if err != nil {
		t.Fatalf("parse event transfers: %v", err)
	}
	if len(transfers) != 1 || transfers[0].TxHash != evmTestTxHash {
		t.Fatalf("transfers = %#v, want the valid log only", transfers)
	}
}

func TestRetryBlockBacksOffAndSplitsWideRanges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := chanx.NewUnboundedChan[evmBlock](ctx, 4)
	setEVMTestTaskLogger(t)
	var delay time.Duration
	e := evm{
		Network:        conf.Polygon,
		blockScanQueue: queue,
		blockRetryAfter: func(got time.Duration, retry func()) {
			delay = got
			retry()
		},
	}
	e.retryBlock(evmBlock{From: 10, To: 19, Attempt: 2}, "query returned more than provider limit")

	if delay != 2*time.Second {
		t.Fatalf("retry delay = %s, want 2s", delay)
	}
	first := <-queue.Out
	second := <-queue.Out
	if first != (evmBlock{From: 10, To: 14, Attempt: 3}) || second != (evmBlock{From: 15, To: 19, Attempt: 3}) {
		t.Fatalf("split retry ranges = %#v, %#v", first, second)
	}
}

func TestRetryBlockStopsAfterMaximumAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue := chanx.NewUnboundedChan[evmBlock](ctx, 4)
	setEVMTestTaskLogger(t)
	retries := 0
	e := evm{
		Network:        conf.Polygon,
		blockScanQueue: queue,
		blockRetryAfter: func(_ time.Duration, retry func()) {
			retries++
			retry()
		},
	}
	e.retryBlock(evmBlock{From: 10, To: 10, Attempt: evmBlockRetryMaxAttempts}, "temporary RPC failure")

	if retries != 0 {
		t.Fatalf("retry callbacks = %d, want 0 after maximum attempts", retries)
	}
	if queue.Len() != 0 {
		t.Fatalf("queued retries = %d, want 0 after maximum attempts", queue.Len())
	}
}

func TestInspectEVMReceiptAndCanonicalBlock(t *testing.T) {
	if got := inspectEVMReceipt(gjson.Parse("null"), evmTestTxHash); got.Kind != evmReceiptMissing {
		t.Fatalf("null receipt kind = %d, want missing", got.Kind)
	}

	failed := gjson.Parse(fmt.Sprintf(
		"{\"transactionHash\":%q,\"blockNumber\":\"0x64\",\"blockHash\":%q,\"status\":\"0x0\"}",
		evmTestTxHash,
		evmTestBlockHash,
	))
	got := inspectEVMReceipt(failed, evmTestTxHash)
	if got.Kind != evmReceiptFailed || got.BlockNumber != 100 || got.BlockHash != evmTestBlockHash {
		t.Fatalf("failed receipt = %#v", got)
	}

	canonical := gjson.Parse(fmt.Sprintf("{\"number\":\"0x64\",\"hash\":%q}", evmTestBlockHash))
	if reason := inspectCanonicalEVMBlock(canonical, 100, evmTestBlockHash); reason != "" {
		t.Fatalf("valid canonical block rejected: %s", reason)
	}
	otherHash := "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if reason := inspectCanonicalEVMBlock(canonical, 100, otherHash); reason == "" {
		t.Fatal("canonical block hash mismatch was accepted")
	}
}

func TestConfirmOrderFailedReceiptSetsFailed(t *testing.T) {
	db := setupEVMReliabilityTestDB(t)
	setEVMTestTaskLogger(t)
	setEVMReliabilityTestConf(t, model.BlockOffsetConfirm, "0")

	calls := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request evmRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode RPC request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		calls = append(calls, request.Method)
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "eth_getTransactionReceipt":
			_, _ = fmt.Fprintf(
				w,
				"{\"jsonrpc\":\"2.0\",\"result\":{\"transactionHash\":%q,\"blockNumber\":\"0x64\",\"blockHash\":%q,\"status\":\"0x0\"},\"id\":1}",
				evmTestTxHash,
				evmTestBlockHash,
			)
		case "eth_getBlockByNumber":
			_, _ = fmt.Fprintf(
				w,
				"{\"jsonrpc\":\"2.0\",\"result\":{\"number\":\"0x64\",\"hash\":%q},\"id\":1}",
				evmTestBlockHash,
			)
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	setEVMReliabilityTestConf(t, model.RpcEndpointPolygon, server.URL)

	now := time.Now().UTC().Truncate(time.Second)
	createdAt := model.Datetime(now.Add(-10 * time.Minute))
	updatedAt := model.Datetime(now.Add(-5 * time.Minute))
	confirmedAt := now.Add(-5 * time.Minute)
	order := model.Order{
		OrderId:      "merchant-evm-failed",
		TradeId:      "trade-evm-failed",
		TradeType:    model.UsdtPolygon,
		Fiat:         model.CNY,
		Crypto:       model.USDT,
		Rate:         "7",
		Amount:       "10",
		Money:        "70",
		Address:      "0x1111111111111111111111111111111111111111",
		MatchAddress: "0x1111111111111111111111111111111111111111",
		Status:       model.OrderStatusConfirming,
		RefHash:      evmTestTxHash,
		RefBlockNum:  100,
		ApiType:      model.OrderApiTypeAdmin,
		ExpiredAt:    now.Add(10 * time.Minute),
		ConfirmedAt:  &confirmedAt,
		AutoTimeAt: model.AutoTimeAt{
			CreatedAt: &createdAt,
			UpdatedAt: &updatedAt,
		},
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create confirming order: %v", err)
	}
	chainBlockNum.Store(conf.Polygon, int64(200))

	e := evm{Network: conf.Polygon, Block: block{ConfirmedOffset: 40}, Client: server.Client()}
	e.confirmOrder(context.Background(), order)

	var persisted model.Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if persisted.Status != model.OrderStatusFailed {
		t.Fatalf("order status = %d, want failed", persisted.Status)
	}
	if !reflect.DeepEqual(calls, []string{"eth_getTransactionReceipt", "eth_getBlockByNumber"}) {
		t.Fatalf("RPC calls = %#v", calls)
	}
}

func TestReceiptAnomaliesEventuallyFailConfirmingOrder(t *testing.T) {
	db := setupEVMReliabilityTestDB(t)
	setEVMTestTaskLogger(t)
	now := time.Now().UTC().Truncate(time.Second)
	createdAt := model.Datetime(now.Add(-10 * time.Minute))
	updatedAt := model.Datetime(now.Add(-5 * time.Minute))
	confirmedAt := now.Add(-5 * time.Minute)
	order := model.Order{
		OrderId:      "merchant-evm-anomaly",
		TradeId:      "trade-evm-anomaly",
		TradeType:    model.UsdtPolygon,
		Fiat:         model.CNY,
		Crypto:       model.USDT,
		Rate:         "7",
		Amount:       "10",
		Money:        "70",
		Address:      "0x1111111111111111111111111111111111111111",
		MatchAddress: "0x1111111111111111111111111111111111111111",
		Status:       model.OrderStatusConfirming,
		RefHash:      evmTestTxHash,
		RefBlockNum:  100,
		ApiType:      model.OrderApiTypeAdmin,
		ExpiredAt:    now.Add(10 * time.Minute),
		ConfirmedAt:  &confirmedAt,
		AutoTimeAt: model.AutoTimeAt{
			CreatedAt: &createdAt,
			UpdatedAt: &updatedAt,
		},
	}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("create confirming order: %v", err)
	}
	chainBlockNum.Store(conf.Polygon, int64(200))

	e := evm{
		Network: conf.Polygon,
		Block:   block{ConfirmedOffset: 40},
		receiptAnomalies: map[evmReceiptKey]evmReceiptAnomaly{
			{OrderID: order.ID, RefHash: evmTestTxHash}: {
				Count:     evmReceiptAnomalyThreshold - 1,
				FirstSeen: now.Add(-evmReceiptAnomalyObservationTime),
			},
		},
	}
	e.handleReceiptAnomaly(order, 100, "transaction receipt is null")

	var persisted model.Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if persisted.Status != model.OrderStatusFailed {
		t.Fatalf("order status = %d, want failed", persisted.Status)
	}
}
