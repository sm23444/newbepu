package task

import "testing"

func TestBoundedForwardCursorSkipsOnlyStaleHistory(t *testing.T) {
	tests := []struct {
		name                         string
		head, previous, maxGap, span int64
		want                         int64
	}{
		{name: "continuous scan", head: 120, previous: 100, maxGap: 1000, span: 1, want: 100},
		{name: "stale evm or tron cursor", head: 10050, previous: 100, maxGap: 1000, span: 1, want: 10049},
		{name: "stale aptos cursor", head: 10050, previous: 0, maxGap: 10000, span: 100, want: 9950},
		{name: "head below cursor", head: 99, previous: 100, maxGap: 1000, span: 1, want: 100},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := boundedForwardCursor(test.head, test.previous, test.maxGap, test.span); got != test.want {
				t.Fatalf("boundedForwardCursor(%d, %d, %d, %d) = %d, want %d", test.head, test.previous, test.maxGap, test.span, got, test.want)
			}
		})
	}
}
