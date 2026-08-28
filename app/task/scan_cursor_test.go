package task

import (
	"errors"
	"testing"
)

func newMemoryScanProgress(position int64) (*scanProgress, *int64) {
	stored := position
	progress := newScanProgress("test")
	progress.load = func(string) (int64, bool, error) { return stored, true, nil }
	progress.save = func(_ string, value int64) error {
		stored = value
		return nil
	}
	return progress, &stored
}

func TestScanProgressOnlyCommitsContinuousRanges(t *testing.T) {
	progress, stored := newMemoryScanProgress(100)
	ranges, err := progress.schedule(130, 10, 3)
	if err != nil || len(ranges) != 3 {
		t.Fatalf("schedule = %#v, %v", ranges, err)
	}
	if err := progress.complete(ranges[1]); err != nil {
		t.Fatalf("complete second range: %v", err)
	}
	if *stored != 100 {
		t.Fatalf("out-of-order completion advanced cursor to %d", *stored)
	}
	if err := progress.complete(ranges[0]); err != nil {
		t.Fatalf("complete first range: %v", err)
	}
	if *stored != 120 {
		t.Fatalf("continuous cursor = %d, want 120", *stored)
	}
}

func TestScanProgressReloadsPersistedPosition(t *testing.T) {
	progress, _ := newMemoryScanProgress(75)
	ranges, err := progress.schedule(85, 5, 2)
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	want := []scanRange{{From: 76, To: 80}, {From: 81, To: 85}}
	if len(ranges) != len(want) || ranges[0] != want[0] || ranges[1] != want[1] {
		t.Fatalf("restart ranges = %#v, want %#v", ranges, want)
	}
}

func TestScanProgressRetriesAfterCursorSaveFailure(t *testing.T) {
	progress, stored := newMemoryScanProgress(10)
	ranges, err := progress.schedule(20, 10, 1)
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	progress.save = func(string, int64) error { return errors.New("database unavailable") }
	if err := progress.complete(ranges[0]); err == nil {
		t.Fatal("cursor save failure was ignored")
	}
	if *stored != 10 {
		t.Fatalf("failed save changed stored cursor to %d", *stored)
	}
	progress.save = func(_ string, value int64) error {
		*stored = value
		return nil
	}
	retry, err := progress.schedule(20, 10, 1)
	if err != nil || len(retry) != 1 || retry[0] != (scanRange{From: 11, To: 20}) {
		t.Fatalf("retry ranges = %#v, %v", retry, err)
	}
}

func TestScanBatchCompletesAfterEveryTransferIsAcknowledged(t *testing.T) {
	completed := 0
	items := attachScanBatch(make([]transfer, 2), func() { completed++ })
	acknowledgeTransfer(items[0])
	if completed != 0 {
		t.Fatal("scan batch completed before all transfers were handled")
	}
	acknowledgeTransfer(items[1])
	if completed != 1 {
		t.Fatalf("scan batch completion count = %d, want 1", completed)
	}
}

func TestScanProgressRecognizesScheduledPositions(t *testing.T) {
	progress, _ := newMemoryScanProgress(10)
	if _, err := progress.schedule(20, 5, 2); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if !progress.isScheduled(15) || progress.isScheduled(10) || progress.isScheduled(21) {
		t.Fatal("scheduled position boundaries are incorrect")
	}
}
