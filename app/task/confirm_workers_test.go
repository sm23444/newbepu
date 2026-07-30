package task

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v03413/bepusdt/app/model"
)

func TestRunOrderConfirmationsIsBounded(t *testing.T) {
	orders := make([]model.Order, 32)
	var active atomic.Int32
	var maxActive atomic.Int32

	runOrderConfirmations(context.Background(), orders, func(model.Order) {
		current := active.Add(1)
		for {
			max := maxActive.Load()
			if current <= max || maxActive.CompareAndSwap(max, current) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		active.Add(-1)
	})

	if got := maxActive.Load(); got > confirmWorkerLimit {
		t.Fatalf("maximum confirmation concurrency = %d, want <= %d", got, confirmWorkerLimit)
	}
}
