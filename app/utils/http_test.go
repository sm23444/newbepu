package utils

import (
	"net"
	"net/http"
	"testing"
)

func TestCallbackClientBlocksRedirects(t *testing.T) {
	client := WithCallbackRedirectPolicy(&http.Client{})
	if err := client.CheckRedirect(&http.Request{}, []*http.Request{{}}); err != http.ErrUseLastResponse {
		t.Fatalf("redirect error = %v, want ErrUseLastResponse", err)
	}
}

func TestIsBlockedCallbackIP(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "169.254.1.1", "100.64.0.1", "::1"} {
		if !isBlockedCallbackIP(net.ParseIP(raw)) {
			t.Fatalf("isBlockedCallbackIP(%q) = false, want true", raw)
		}
	}
	if isBlockedCallbackIP(net.ParseIP("1.1.1.1")) {
		t.Fatal("isBlockedCallbackIP(1.1.1.1) = true, want false")
	}
}
