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
}
