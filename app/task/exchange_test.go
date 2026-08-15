package task

import "testing"

func TestExchangeCursorKeySeparatesAssets(t *testing.T) {
	usdt := exchangeCursorKey(" OKX ", "usdt")
	usdc := exchangeCursorKey("okx", "USDC")
	if usdt != "okx:USDT" || usdc != "okx:USDC" || usdt == usdc {
		t.Fatalf("unexpected exchange cursor keys: %q, %q", usdt, usdc)
	}
}
