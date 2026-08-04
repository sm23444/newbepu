package model

import (
	"encoding/json"
	"testing"
)

func TestExchangeOrderCheckoutMetadata(t *testing.T) {
	order := Order{TradeType: UsdtBinance}
	if order.PaymentKind() != "exchange" || order.PaymentProvider() != "binance" {
		t.Fatalf("unexpected Binance payment metadata")
	}
	if order.ReceiverLabel() != "币安ID" {
		t.Fatalf("unexpected receiver label: %q", order.ReceiverLabel())
	}

	payload, err := json.Marshal(order.Network())
	if err != nil {
		t.Fatalf("marshal network: %v", err)
	}
	var network map[string]any
	if err := json.Unmarshal(payload, &network); err != nil {
		t.Fatalf("decode network: %v", err)
	}
	if network["name"] != "币安交易所" || network["payment_kind"] != "exchange" || network["provider"] != "binance" {
		t.Fatalf("unexpected checkout network payload: %s", payload)
	}

	okxOrder := Order{TradeType: UsdtOKX}
	if okxOrder.ReceiverLabel() != "欧易UID" {
		t.Fatalf("unexpected OKX receiver label: %q", okxOrder.ReceiverLabel())
	}
	okxPayload, err := json.Marshal(okxOrder.Network())
	if err != nil {
		t.Fatalf("marshal OKX network: %v", err)
	}
	var okxNetwork map[string]any
	if err := json.Unmarshal(okxPayload, &okxNetwork); err != nil {
		t.Fatalf("decode OKX network: %v", err)
	}
	if okxNetwork["name"] != "欧易交易所" || okxNetwork["provider"] != "okx" {
		t.Fatalf("unexpected OKX checkout network payload: %s", okxPayload)
	}

	for _, tc := range []struct {
		tradeType TradeType
		provider  string
	}{
		{tradeType: UsdcBinance, provider: "binance"},
		{tradeType: UsdcOKX, provider: "okx"},
	} {
		crypto, err := GetCrypto(tc.tradeType)
		if err != nil {
			t.Fatalf("get %s crypto: %v", tc.tradeType, err)
		}
		if crypto != USDC {
			t.Fatalf("%s crypto = %s, want USDC", tc.tradeType, crypto)
		}
		order := Order{TradeType: tc.tradeType}
		if order.PaymentKind() != "exchange" || order.PaymentProvider() != tc.provider {
			t.Fatalf("unexpected %s payment metadata", tc.tradeType)
		}
		atomKey, ok := GetTradeAtomKey(tc.tradeType)
		if !ok || atomKey != AtomExchangeUSDC {
			t.Fatalf("%s atom key = %s/%t, want %s/true", tc.tradeType, atomKey, ok, AtomExchangeUSDC)
		}
	}
}

func TestUSDCCheckoutListsBinanceAndOKX(t *testing.T) {
	db := newExchangeReselectionTestDB(t)
	setExchangeReselectionTestConf(t, ExchangeBinanceEnabled, "1")
	setExchangeReselectionTestConf(t, ExchangeBinanceUID, "123456")
	setExchangeReselectionTestConf(t, ExchangeOKXEnabled, "1")
	setExchangeReselectionTestConf(t, ExchangeOKXUID, "654321")

	if err := db.Create(&Conf{K: AtomExchangeUSDC, V: "0.0001"}).Error; err != nil {
		t.Fatalf("seed USDC exchange atomicity: %v", err)
	}
	if err := db.Create(&Rate{Rate: "7", RawRate: 7, Fiat: string(CNY), Crypto: string(USDC)}).Error; err != nil {
		t.Fatalf("seed USDC rate: %v", err)
	}
	wallets := []Wallet{
		{Name: "Binance Pay USDC", Status: WaStatusEnable, Address: "123456", MatchAddr: "123456", TradeType: string(UsdcBinance)},
		{Name: "OKX Pay USDC", Status: WaStatusEnable, Address: "654321", MatchAddr: "654321", TradeType: string(UsdcOKX)},
	}
	if err := db.Create(&wallets).Error; err != nil {
		t.Fatalf("seed USDC exchange wallets: %v", err)
	}

	order := Order{Money: "70", Fiat: CNY, Status: OrderStatusWaiting, ApiType: OrderApiTypeEpusdtOrder}
	methods := order.GetMethods(USDC)
	if len(methods) != 2 {
		t.Fatalf("USDC exchange method count = %d, want 2: %#v", len(methods), methods)
	}
	providers := map[string]bool{}
	for _, method := range methods {
		if method.Currency != string(USDC) || method.PaymentKind != "exchange" {
			t.Fatalf("unexpected USDC method: %+v", method)
		}
		providers[method.Provider] = true
	}
	if !providers["binance"] || !providers["okx"] {
		t.Fatalf("USDC providers = %#v, want Binance and OKX", providers)
	}
}
