package task

import (
	"testing"

	"github.com/v03413/bepusdt/app/model"
)

func TestExchangeTradeTypeIncludesUSDTAndUSDC(t *testing.T) {
	for _, tc := range []struct {
		provider string
		asset    string
		want     model.TradeType
	}{
		{provider: "binance", asset: "USDT", want: model.UsdtBinance},
		{provider: "binance", asset: "usdc", want: model.UsdcBinance},
		{provider: "OKX", asset: "USDT", want: model.UsdtOKX},
		{provider: "okx", asset: "USDC", want: model.UsdcOKX},
	} {
		if got := exchangeTradeType(tc.provider, tc.asset); got != tc.want {
			t.Fatalf("exchangeTradeType(%q, %q) = %q, want %q", tc.provider, tc.asset, got, tc.want)
		}
	}
	if got := exchangeTradeType("binance", "BTC"); got != "" {
		t.Fatalf("unsupported asset mapped to %q", got)
	}
}

func TestExchangeCursorKeySeparatesAssets(t *testing.T) {
	usdt := exchangeCursorKey(" OKX ", "usdt")
	usdc := exchangeCursorKey("okx", "USDC")
	if usdt != "okx:USDT" || usdc != "okx:USDC" || usdt == usdc {
		t.Fatalf("unexpected exchange cursor keys: %q, %q", usdt, usdc)
	}
}
