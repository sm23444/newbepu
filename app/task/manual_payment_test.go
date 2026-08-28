package task

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/v03413/bepusdt/app/conf"
	"github.com/v03413/bepusdt/app/model"
	"gorm.io/gorm"
)

func setupManualPaymentTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "manual-payment-test.db")
	db, err := gorm.Open(sqlite.Open(dbPath+"?cache=shared&mode=rwc&_pragma=journal_mode(WAL)&_pragma=busy_timeout(1000)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.Order{}, &model.ManualPaymentClaim{}, &model.PaymentTransactionClaim{}, &model.Conf{}); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	oldDB := model.Db
	oldPolygonEndpoint := model.GetC(model.RpcEndpointPolygon)
	oldPaymentMatchMode := model.GetC(model.PaymentMatchMode)
	model.Db = db
	model.SetK(model.RpcEndpointPolygon, "")
	model.SetK(model.PaymentMatchMode, string(model.Classic))

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	t.Cleanup(func() {
		model.SetK(model.RpcEndpointPolygon, oldPolygonEndpoint)
		model.SetK(model.PaymentMatchMode, oldPaymentMatchMode)
		model.Db = oldDB
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close test db: %v", err)
		}
	})

	return db
}

func newManualPaymentTestOrder(tradeID string, tradeType model.TradeType) model.Order {
	createdAt := time.Now().Add(-time.Minute)
	confirmedAt := time.Unix(0, 0)

	return model.Order{
		Id:           model.Id{ID: 1},
		OrderId:      "merchant-" + tradeID,
		TradeId:      tradeID,
		TradeType:    tradeType,
		Fiat:         model.CNY,
		Crypto:       model.USDT,
		Rate:         "7",
		Amount:       "10",
		Money:        "70",
		Address:      "0x1111111111111111111111111111111111111111",
		MatchAddress: "0x1111111111111111111111111111111111111111",
		Status:       model.OrderStatusWaiting,
		RefHash:      tradeID,
		ExpiredAt:    createdAt.Add(20 * time.Minute),
		ConfirmedAt:  &confirmedAt,
		AutoTimeAt: model.AutoTimeAt{
			CreatedAt: (*model.Datetime)(&createdAt),
			UpdatedAt: (*model.Datetime)(&createdAt),
		},
	}
}

func TestManualPaymentDoesNotCallVerifierWhenNetworkEndpointIsEmpty(t *testing.T) {
	setupManualPaymentTestDB(t)
	order := newManualPaymentTestOrder("disabled-network", model.UsdtPolygon)
	calls := 0
	verifiers := map[model.Network]manualPaymentVerifier{
		conf.Polygon: func(context.Context, *model.Order, string) (transfer, error) {
			calls++
			return transfer{}, nil
		},
	}

	_, err := submitManualPaymentWithVerifiers(context.Background(), &order, "0x"+string(make([]byte, 64)), verifiers)
	if !errors.Is(err, ErrManualPaymentNetworkDisabled) {
		t.Fatalf("expected disabled network error, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("disabled network verifier called %d times", calls)
	}
	if ManualPaymentAvailable(&order) {
		t.Fatal("manual submission must be hidden when the order network endpoint is empty")
	}
}

func TestManualPaymentCallsOnlyOrderNetworkVerifier(t *testing.T) {
	db := setupManualPaymentTestDB(t)
	model.SetK(model.RpcEndpointPolygon, "http://polygon.test.invalid")

	order := newManualPaymentTestOrder("polygon-order", model.UsdtPolygon)
	order.ID = 0
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}

	txHash := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	polygonCalls := 0
	bscCalls := 0
	verifiers := map[model.Network]manualPaymentVerifier{
		conf.Polygon: func(context.Context, *model.Order, string) (transfer, error) {
			polygonCalls++
			return transfer{
				Network:     string(conf.Polygon),
				TxHash:      txHash,
				Amount:      decimal.NewFromInt(10),
				FromAddress: "0x2222222222222222222222222222222222222222",
				RecvAddress: order.Address,
				Timestamp:   time.Now(),
				TradeType:   model.UsdtPolygon,
				BlockNum:    123,
			}, nil
		},
		conf.Bsc: func(context.Context, *model.Order, string) (transfer, error) {
			bscCalls++
			return transfer{}, nil
		},
	}

	result, err := submitManualPaymentWithVerifiers(context.Background(), &order, txHash, verifiers)
	if err != nil {
		t.Fatalf("submit manual payment: %v", err)
	}
	if polygonCalls != 1 || bscCalls != 0 {
		t.Fatalf("unexpected verifier calls: polygon=%d bsc=%d", polygonCalls, bscCalls)
	}
	if result.Status != model.OrderStatusConfirming || order.Status != model.OrderStatusConfirming {
		t.Fatalf("order was not moved to confirming: result=%d order=%d", result.Status, order.Status)
	}
	var transactionClaim model.PaymentTransactionClaim
	if err := db.Where("network = ? AND tx_hash = ?", conf.Polygon, txHash).First(&transactionClaim).Error; err != nil {
		t.Fatalf("load shared transaction claim: %v", err)
	}
	if transactionClaim.OrderID != order.ID {
		t.Fatalf("transaction claim order = %d, want %d", transactionClaim.OrderID, order.ID)
	}
}

func TestManualPaymentRejectsOrderReselectedDuringVerification(t *testing.T) {
	db := setupManualPaymentTestDB(t)
	model.SetK(model.RpcEndpointPolygon, "http://polygon.test.invalid")

	order := newManualPaymentTestOrder("reselected-order", model.UsdtPolygon)
	order.ApiType = model.OrderApiTypeEpusdtOrder
	order.ID = 0
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}

	txHash := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	verificationStarted := make(chan struct{})
	releaseVerification := make(chan struct{})
	verifiers := map[model.Network]manualPaymentVerifier{
		conf.Polygon: func(ctx context.Context, snapshot *model.Order, _ string) (transfer, error) {
			close(verificationStarted)
			select {
			case <-releaseVerification:
			case <-ctx.Done():
				return transfer{}, ctx.Err()
			}
			return transfer{
				Network:     string(conf.Polygon),
				TxHash:      txHash,
				Amount:      decimal.NewFromInt(10),
				FromAddress: "0x2222222222222222222222222222222222222222",
				RecvAddress: snapshot.Address,
				Timestamp:   time.Now(),
				TradeType:   snapshot.TradeType,
				BlockNum:    456,
			}, nil
		},
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := submitManualPaymentWithVerifiers(context.Background(), &order, txHash, verifiers)
		errCh <- err
	}()

	select {
	case <-verificationStarted:
	case <-time.After(time.Second):
		t.Fatal("manual payment verifier did not start")
	}
	if err := db.Model(&model.Order{}).
		Where("id = ? and status = ?", order.ID, model.OrderStatusWaiting).
		Updates(map[string]any{
			"trade_type": model.UsdcPolygon,
			"crypto":     model.USDC,
		}).Error; err != nil {
		t.Fatalf("reselect order: %v", err)
	}
	close(releaseVerification)

	var submitErr error
	select {
	case submitErr = <-errCh:
	case <-time.After(time.Second):
		t.Fatal("manual payment submission did not finish")
	}
	if !errors.Is(submitErr, ErrManualPaymentNotEligible) {
		t.Fatalf("expected reselected order rejection, got %v", submitErr)
	}

	var current model.Order
	if err := db.Where("id = ?", order.ID).First(&current).Error; err != nil {
		t.Fatalf("load current order: %v", err)
	}
	if current.TradeType != model.UsdcPolygon || current.Status != model.OrderStatusWaiting || current.RefHash != order.RefHash {
		t.Fatalf("reselected order was modified by stale verification: trade_type=%s status=%d ref_hash=%s", current.TradeType, current.Status, current.RefHash)
	}

	var claimCount int64
	if err := db.Model(&model.ManualPaymentClaim{}).Where("trade_id = ?", order.TradeId).Count(&claimCount).Error; err != nil {
		t.Fatalf("count manual payment claims: %v", err)
	}
	if claimCount != 0 {
		t.Fatalf("stale verification left %d manual payment claim rows", claimCount)
	}
	if err := db.Model(&model.PaymentTransactionClaim{}).Where("order_id = ?", order.ID).Count(&claimCount).Error; err != nil {
		t.Fatalf("count shared transaction claims: %v", err)
	}
	if claimCount != 0 {
		t.Fatalf("stale verification left %d shared transaction claim rows", claimCount)
	}
}

func TestManualPaymentRejectsExchangeOrderBeforeVerification(t *testing.T) {
	createdAt := time.Now().Add(-time.Minute)
	order := newManualPaymentTestOrder("exchange-order", model.UsdtBinance)
	order.CreatedAt = (*model.Datetime)(&createdAt)
	calls := 0
	verifiers := map[model.Network]manualPaymentVerifier{
		conf.Polygon: func(context.Context, *model.Order, string) (transfer, error) {
			calls++
			return transfer{}, nil
		},
	}

	_, err := submitManualPaymentWithVerifiers(context.Background(), &order, "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", verifiers)
	if !errors.Is(err, ErrManualPaymentExchangeOrder) {
		t.Fatalf("expected exchange order rejection, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("verifier called %d times for exchange order", calls)
	}
}
