package task

// boundedForwardCursor keeps a resumed scanner close to the chain head after a
// long idle period. Order lookback handles the relevant payment-time window.
func boundedForwardCursor(head, previous, maxGap, recentSpan int64) int64 {
	if head-previous <= maxGap {
		return previous
	}
	if recentSpan < 1 {
		recentSpan = 1
	}
	previous = head - recentSpan
	if previous < 0 {
		return 0
	}
	return previous
}
