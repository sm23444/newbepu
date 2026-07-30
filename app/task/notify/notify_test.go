package notify

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	applog "github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
)

func newNotifyTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "notify-test.db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?cache=shared&mode=rwc&_pragma=journal_mode(WAL)&_pragma=busy_timeout(1000)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close test db: %v", err)
		}
	})

	if err := db.AutoMigrate(&model.Order{}, &model.Conf{}); err != nil {
		t.Fatalf("auto migrate order: %v", err)
	}
	if err := db.Create(&model.Conf{K: model.ApiAuthToken, V: "test-auth-token"}).Error; err != nil {
		t.Fatalf("seed auth token: %v", err)
	}

	oldDB := model.Db
	model.Db = db
	t.Cleanup(func() {
		model.Db = oldDB
	})

	return db
}

func initNotifyTestLog(t *testing.T) {
	t.Helper()

	if err := applog.Init(filepath.Join(t.TempDir(), "logs")); err != nil {
		t.Fatalf("init log: %v", err)
	}
	t.Cleanup(func() {
		applog.Close()
	})
}

func newWaitingOrder(notifyURL string) model.Order {
	now := time.Now()
	confirmedAt := now

	return model.Order{
		OrderId:     "merchant-order-1",
		TradeId:     "trade-order-1",
		TradeType:   model.UsdtTrc20,
		Fiat:        "CNY",
		Crypto:      "USDT",
		Rate:        "7.00",
		Amount:      "1.00",
		Money:       "7.00",
		Address:     "TTestAddress1234567890",
		Status:      model.OrderStatusWaiting,
		ApiType:     model.OrderApiTypeEpusdt,
		NotifyUrl:   notifyURL,
		ExpiredAt:   now.Add(10 * time.Minute),
		ConfirmedAt: &confirmedAt,
		AutoTimeAt:  model.AutoTimeAt{CreatedAt: (*model.Datetime)(&now), UpdatedAt: (*model.Datetime)(&now)},
	}
}

func TestDeliverBepusdtStatusUpdateDoesNotHoldDBWhileHTTPIsPending(t *testing.T) {
	db := newNotifyTestDB(t)
	initNotifyTestLog(t)

	requestStarted := make(chan struct{}, 1)
	releaseResponse := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestStarted <- struct{}{}
		<-releaseResponse
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 2 * time.Second

	order := newWaitingOrder(server.URL)
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- deliverBepusdtStatusUpdate(db, client, "test-auth-token", order)
	}()

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("notification request never reached test server")
	}

	queryDone := make(chan error, 1)
	go func() {
		var count int64
		queryDone <- db.Model(&model.Order{}).Where("status = ?", model.OrderStatusWaiting).Count(&count).Error
	}()

	select {
	case err := <-queryDone:
		if err != nil {
			t.Fatalf("concurrent query failed: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		close(releaseResponse)
		<-errCh
		t.Fatal("database query was blocked while notification HTTP request was in flight")
	}

	close(releaseResponse)
	if err := <-errCh; err != nil {
		t.Fatalf("deliver notification: %v", err)
	}
}

func TestDeliverBepusdtStatusUpdateRejectsOversizedResponse(t *testing.T) {
	db := newNotifyTestDB(t)
	initNotifyTestLog(t)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("x", maxCallbackResponseBytes+1)))
	}))
	defer server.Close()

	order := newWaitingOrder(server.URL)
	order.TradeId = "status-update-oversized-response"
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}

	client := server.Client()
	client.Timeout = 2 * time.Second
	err := deliverBepusdtStatusUpdate(db, client, "test-auth-token", order)
	if err == nil || !strings.Contains(err.Error(), "超过 4096 字节") {
		t.Fatalf("oversized status response error = %v, want bounded-response rejection", err)
	}
}

func TestHandleRejectsHTTPNotifyURL(t *testing.T) {
	db := newNotifyTestDB(t)
	initNotifyTestLog(t)
	order := newWaitingOrder("http://merchant.example/notify")
	order.Status = model.OrderStatusSuccess
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}

	err := Handle(order)
	if err == nil || err.Error() != "notify_url must use HTTPS" {
		t.Fatalf("Handle() error = %v, want HTTPS rejection", err)
	}

	var persisted model.Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if persisted.NotifyNum != 1 {
		t.Fatalf("notify_num = %d, want 1", persisted.NotifyNum)
	}
	if persisted.NotifyClaimToken != "" || persisted.NotifyClaimUntil != nil {
		t.Fatal("failed URL validation left the notification claimed")
	}
}

func TestConcurrentHandleDeliversOnce(t *testing.T) {
	db := newNotifyTestDB(t)
	initNotifyTestLog(t)

	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	var startOnce sync.Once
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		startOnce.Do(func() { close(requestStarted) })
		<-releaseResponse
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	order := newWaitingOrder(server.URL)
	order.Status = model.OrderStatusSuccess
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}

	client := server.Client()
	client.Timeout = 3 * time.Second
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func(snapshot model.Order) {
			<-start
			results <- handleWithClient(snapshot, false, client)
		}(order)
	}
	close(start)

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("callback request never reached test server")
	}

	var firstResult error
	select {
	case firstResult = <-results:
		if firstResult == nil {
			close(releaseResponse)
			t.Fatal("a concurrent handler completed successfully while the first delivery was blocked")
		}
	case <-time.After(time.Second):
		close(releaseResponse)
		<-results
		<-results
		t.Fatal("concurrent handler did not reject the active notification claim")
	}

	close(releaseResponse)
	secondResult := <-results
	if secondResult != nil {
		t.Fatalf("winning callback failed: %v (other result: %v)", secondResult, firstResult)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("callback requests = %d, want 1", got)
	}

	var persisted model.Order
	if err := db.First(&persisted, order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if persisted.NotifyNum != 1 {
		t.Fatalf("notify_num = %d, want 1", persisted.NotifyNum)
	}
	if persisted.NotifyState != model.OrderNotifyStateSucc {
		t.Fatalf("notify_state = %d, want success", persisted.NotifyState)
	}
}
