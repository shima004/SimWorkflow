package operation

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestParallelWorkIsBoundedAndReportsOrderedCompletion(t *testing.T) {
	var events []Event
	ctx := WithReporter(context.Background(), func(e Event) { events = append(events, e) })
	ready := make(chan struct{}, 32)
	release := make(chan struct{})
	var active, peak atomic.Int32
	results := make([]int, 32)
	done := make(chan error, 1)
	go func() {
		done <- For(ctx, "test", len(results), func(ctx context.Context, i int) error {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			ready <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			results[i]++
			return nil
		})
	}()
	for range 8 {
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("work remained sequential")
		}
	}
	if active.Load() != 8 {
		close(release)
		t.Fatal("expected eight concurrent jobs")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if peak.Load() > 8 || len(events) != 33 {
		t.Fatal("concurrency or progress bound failed")
	}
	for i, e := range events {
		if e.Completed != i || e.Total != 32 || e.Stage != "test" {
			t.Fatal("completion events are not ordered")
		}
	}
	for _, n := range results {
		if n != 1 {
			t.Fatal("job was skipped or duplicated")
		}
	}
}

func TestParallelFailureCancelsAndWaitsForWorkers(t *testing.T) {
	want := errors.New("fixture failure")
	var active atomic.Int32
	err := For(context.Background(), "test", 100, func(ctx context.Context, i int) error {
		active.Add(1)
		defer active.Add(-1)
		if i == 0 {
			return want
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, want) || active.Load() != 0 {
		t.Fatal("failure was lost or workers leaked", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := For(ctx, "test", 1, func(context.Context, int) error { t.Fatal("cancelled work started"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
