package model

import "testing"

func TestUSDCExchangeWalletsReuseConfiguredProviderUIDs(t *testing.T) {
	setExchangeReselectionTestConf(t, ExchangeBinanceUID, "123456")
	setExchangeReselectionTestConf(t, ExchangeOKXUID, "654321")

	for _, tc := range []struct {
		tradeType TradeType
		uid       string
	}{
		{tradeType: UsdcBinance, uid: "123456"},
		{tradeType: UsdcOKX, uid: "654321"},
	} {
		wallet, err := NewWallet(tc.uid, tc.tradeType)
		if err != nil {
			t.Fatalf("create %s wallet: %v", tc.tradeType, err)
		}
		if wallet.MatchAddr != tc.uid {
			t.Fatalf("%s match address = %q, want %q", tc.tradeType, wallet.MatchAddr, tc.uid)
		}
		if got := GetConfiguredExchangeUID(tc.tradeType); got != tc.uid {
			t.Fatalf("%s configured UID = %q, want %q", tc.tradeType, got, tc.uid)
		}
	}
}
