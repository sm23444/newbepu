package task

import "testing"

func TestAptosRetryVersionStopsAfterMaximumAttempts(t *testing.T) {
	setEVMTestTaskLogger(t)
	a := newAptos()
	v := version{Start: 100, Limit: 25}
	a.retryAttempts[v] = aptosVersionRetryMaxAttempts

	a.retryVersion(v, "temporary RPC failure")

	if got := a.versionQueue.Len(); got != 0 {
		t.Fatalf("queued retries = %d, want 0 after reaching the maximum", got)
	}
	if _, ok := a.retryAttempts[v]; ok {
		t.Fatal("retry state was not cleared after reaching the maximum")
	}
}
