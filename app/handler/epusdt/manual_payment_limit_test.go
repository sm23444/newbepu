package epusdt

import (
	"strconv"
	"testing"
	"time"
)

func TestManualPaymentAttemptLimitIsPerOrder(t *testing.T) {
	resetManualPaymentAttempts()
	t.Cleanup(resetManualPaymentAttempts)

	now := time.Now()
	for attempt := 1; attempt <= manualPaymentAttemptLimit; attempt++ {
		if !allowManualPaymentAttempt("trade-1", now) {
			t.Fatalf("attempt %d was unexpectedly rejected", attempt)
		}
	}
	if allowManualPaymentAttempt("trade-1", now) {
		t.Fatal("attempt above the per-order limit was accepted")
	}
	if !allowManualPaymentAttempt("trade-1", now.Add(manualPaymentAttemptWindow)) {
		t.Fatal("attempt window did not reset")
	}
}

func TestManualPaymentAttemptMapHasHardLimit(t *testing.T) {
	resetManualPaymentAttempts()
	t.Cleanup(resetManualPaymentAttempts)

	now := time.Now()
	manualPaymentAttempts.Lock()
	for i := 0; i < manualPaymentAttemptMaxEntries; i++ {
		manualPaymentAttempts.Items[strconv.Itoa(i)] = manualPaymentAttempt{StartedAt: now, Count: 1}
	}
	manualPaymentAttempts.Unlock()

	if allowManualPaymentAttempt("overflow", now) {
		t.Fatal("new key was accepted after the hard limit")
	}
	manualPaymentAttempts.Lock()
	count := len(manualPaymentAttempts.Items)
	manualPaymentAttempts.Unlock()
	if count != manualPaymentAttemptMaxEntries {
		t.Fatalf("attempt map size=%d, want %d", count, manualPaymentAttemptMaxEntries)
	}
}

func resetManualPaymentAttempts() {
	manualPaymentAttempts.Lock()
	clear(manualPaymentAttempts.Items)
	manualPaymentAttempts.Unlock()
}
