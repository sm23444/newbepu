package task

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/v03413/bepusdt/app/log"
	"github.com/v03413/bepusdt/app/model"
)

type scanRange struct {
	From int64
	To   int64
}

type scanProgress struct {
	key       string
	mu        sync.Mutex
	loaded    bool
	committed int64
	scheduled int64
	completed []scanRange
	load      func(string) (int64, bool, error)
	save      func(string, int64) error
}

func newScanProgress(key string) *scanProgress {
	return &scanProgress{
		key:  key,
		load: model.LoadScanCursor,
		save: model.SaveScanCursor,
	}
}

// schedule returns new forward-only ranges. A missing cursor starts one item
// before the current head; existing installations still use their order
// lookback once, while all later restarts resume durably.
func (p *scanProgress) schedule(head, chunkSize int64, maxRanges int) ([]scanRange, error) {
	if head <= 0 || chunkSize <= 0 || maxRanges <= 0 {
		return nil, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.loaded {
		position, found, err := p.load(p.key)
		if err != nil {
			return nil, err
		}
		if !found {
			position = head - 1
			if err := p.save(p.key, position); err != nil {
				return nil, err
			}
		}
		p.committed = position
		p.scheduled = position
		p.loaded = true
	}

	if head <= p.scheduled {
		return nil, nil
	}

	ranges := make([]scanRange, 0, maxRanges)
	for from := p.scheduled + 1; from <= head && len(ranges) < maxRanges; from += chunkSize {
		to := from + chunkSize - 1
		if to > head {
			to = head
		}
		ranges = append(ranges, scanRange{From: from, To: to})
		p.scheduled = to
	}
	return ranges, nil
}

func (p *scanProgress) complete(done scanRange) error {
	if done.From <= 0 || done.To < done.From {
		return fmt.Errorf("invalid completed scan range %d-%d", done.From, done.To)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.loaded || done.To <= p.committed {
		return nil
	}

	p.completed = append(p.completed, done)
	advanced := p.committed
	for {
		best := advanced
		for _, candidate := range p.completed {
			if candidate.From <= advanced+1 && candidate.To > best {
				best = candidate.To
			}
		}
		if best == advanced {
			break
		}
		advanced = best
	}
	if advanced == p.committed {
		return nil
	}
	if err := p.save(p.key, advanced); err != nil {
		p.scheduled = p.committed
		p.completed = nil
		return err
	}

	p.committed = advanced
	kept := p.completed[:0]
	for _, candidate := range p.completed {
		if candidate.To > advanced {
			kept = append(kept, candidate)
		}
	}
	p.completed = kept
	if p.scheduled < p.committed {
		p.scheduled = p.committed
	}
	return nil
}

func (p *scanProgress) retryLater() {
	p.mu.Lock()
	p.scheduled = p.committed
	p.completed = nil
	p.mu.Unlock()
}

func (p *scanProgress) isScheduled(position int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.loaded && position > p.committed && position <= p.scheduled
}

type scanBatchAck struct {
	remaining atomic.Int64
	done      func()
}

func attachScanBatch(transfers []transfer, done func()) []transfer {
	if len(transfers) == 0 {
		done()
		return transfers
	}

	ack := &scanBatchAck{done: done}
	ack.remaining.Store(int64(len(transfers)))
	for i := range transfers {
		transfers[i].scanAck = ack
	}
	return transfers
}

func acknowledgeTransfer(item transfer) {
	if item.scanAck == nil || item.scanAck.remaining.Add(-1) != 0 {
		return
	}
	item.scanAck.done()
}

func completeScanRange(progress *scanProgress, done scanRange) func() {
	return func() {
		if err := progress.complete(done); err != nil {
			log.Task.Warn("persist scan cursor failed:", err)
		}
	}
}
