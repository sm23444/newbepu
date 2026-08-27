package task

import (
	"context"
	"errors"
	"testing"

	tgo "github.com/xssnick/tonutils-go/ton"
)

func tonTestBlock(shard int64, seqno uint32) *tgo.BlockIDExt {
	return &tgo.BlockIDExt{Workchain: 0, Shard: shard, SeqNo: seqno}
}

func tonTestParentResolver(edges map[tonBlockKey][]*tgo.BlockIDExt) func(*tgo.BlockIDExt) ([]*tgo.BlockIDExt, error) {
	return func(block *tgo.BlockIDExt) ([]*tgo.BlockIDExt, error) {
		parents, ok := edges[tonBlockID(block)]
		if !ok {
			return nil, errors.New("unexpected parent lookup")
		}

		return parents, nil
	}
}

func tonTestBlockKeys(blocks []*tgo.BlockIDExt) []tonBlockKey {
	keys := make([]tonBlockKey, 0, len(blocks))
	for _, block := range blocks {
		keys = append(keys, tonBlockID(block))
	}

	return keys
}

func requireTonBlockKeys(t *testing.T, got []*tgo.BlockIDExt, want ...*tgo.BlockIDExt) {
	t.Helper()
	gotKeys := tonTestBlockKeys(got)
	wantKeys := tonTestBlockKeys(want)
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("block count = %d, want %d; got %v", len(gotKeys), len(wantKeys), gotKeys)
	}
	for i := range wantKeys {
		if gotKeys[i] != wantKeys[i] {
			t.Fatalf("block[%d] = %+v, want %+v; all=%v", i, gotKeys[i], wantKeys[i], gotKeys)
		}
	}
}

func TestCollectTonShardBlocksWalksLinearParentsOldestFirst(t *testing.T) {
	a10 := tonTestBlock(1, 10)
	a11 := tonTestBlock(1, 11)
	a12 := tonTestBlock(1, 12)
	a13 := tonTestBlock(1, 13)
	edges := map[tonBlockKey][]*tgo.BlockIDExt{
		tonBlockID(a13): {a12},
		tonBlockID(a12): {a11},
		tonBlockID(a11): {a10},
	}

	got, err := collectTonShardBlocks([]*tgo.BlockIDExt{a13}, []*tgo.BlockIDExt{a10}, tonTestParentResolver(edges))
	if err != nil {
		t.Fatalf("collect shard blocks: %v", err)
	}
	requireTonBlockKeys(t, got, a11, a12, a13)
}

func TestCollectTonShardBlocksHandlesSplitAndSharedParent(t *testing.T) {
	parent10 := tonTestBlock(1, 10)
	shared11 := tonTestBlock(1, 11)
	left12 := tonTestBlock(2, 12)
	right12 := tonTestBlock(3, 12)
	edges := map[tonBlockKey][]*tgo.BlockIDExt{
		tonBlockID(left12):   {shared11},
		tonBlockID(right12):  {shared11},
		tonBlockID(shared11): {parent10},
	}

	got, err := collectTonShardBlocks([]*tgo.BlockIDExt{left12, right12}, []*tgo.BlockIDExt{parent10}, tonTestParentResolver(edges))
	if err != nil {
		t.Fatalf("collect split shard blocks: %v", err)
	}
	requireTonBlockKeys(t, got, shared11, left12, right12)
}

func TestCollectTonShardBlocksHandlesMergeParents(t *testing.T) {
	left10 := tonTestBlock(2, 10)
	right10 := tonTestBlock(3, 10)
	merged11 := tonTestBlock(1, 11)
	merged12 := tonTestBlock(1, 12)
	edges := map[tonBlockKey][]*tgo.BlockIDExt{
		tonBlockID(merged12): {merged11},
		tonBlockID(merged11): {left10, right10},
	}

	got, err := collectTonShardBlocks([]*tgo.BlockIDExt{merged12}, []*tgo.BlockIDExt{left10, right10}, tonTestParentResolver(edges))
	if err != nil {
		t.Fatalf("collect merged shard blocks: %v", err)
	}
	requireTonBlockKeys(t, got, merged11, merged12)
}

func TestCollectTonShardBlocksSkipsAlreadyKnownTip(t *testing.T) {
	tip := tonTestBlock(1, 10)
	called := false
	got, err := collectTonShardBlocks([]*tgo.BlockIDExt{tip}, []*tgo.BlockIDExt{tip}, func(*tgo.BlockIDExt) ([]*tgo.BlockIDExt, error) {
		called = true
		return nil, nil
	})
	if err != nil {
		t.Fatalf("collect known shard tip: %v", err)
	}
	if called {
		t.Fatal("parent resolver called for an already known shard tip")
	}
	requireTonBlockKeys(t, got)
}

func TestCollectTonShardBlocksPropagatesParentError(t *testing.T) {
	tip := tonTestBlock(1, 10)
	wantErr := errors.New("parent lookup failed")
	_, err := collectTonShardBlocks([]*tgo.BlockIDExt{tip}, nil, func(*tgo.BlockIDExt) ([]*tgo.BlockIDExt, error) {
		return nil, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("collect error = %v, want %v", err, wantErr)
	}
}

func TestRetryTonBlockRetriesSameBlockUntilSuccess(t *testing.T) {
	calls := 0
	err := retryTonBlock(context.Background(), 42, 0, func(seqno uint32) error {
		calls++
		if seqno != 42 {
			t.Fatalf("retried seqno = %d, want 42", seqno)
		}
		if calls < 3 {
			return errors.New("temporary failure")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("retry block: %v", err)
	}
	if calls != 3 {
		t.Fatalf("process calls = %d, want 3", calls)
	}
}

func TestRetryTonBlockStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := retryTonBlock(ctx, 42, 0, func(uint32) error {
		return errors.New("temporary failure")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retry error = %v, want context canceled", err)
	}
}
