// Package operation reports bounded preparation work without exposing object
// keys, credentials or signed URLs to the caller's output.
package operation

import (
	"context"
	"sync"
)

type Event struct {
	Stage            string
	Completed, Total int
	Detail           string
}

type reporter struct {
	mu     sync.Mutex
	report func(Event)
}
type reporterKey struct{}

// WithReporter serializes progress callbacks, including concurrent work stages.
func WithReporter(ctx context.Context, report func(Event)) context.Context {
	return context.WithValue(ctx, reporterKey{}, &reporter{report: report})
}

func Report(ctx context.Context, event Event) {
	if r, ok := ctx.Value(reporterKey{}).(*reporter); ok && r.report != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.report(event)
	}
}

// For processes independent indexed jobs with at most eight concurrent jobs.
// On the first failure it cancels in-flight work and waits for cleanup. The
// caller owns distinct result slots and can apply them in input order afterward.
func For(ctx context.Context, stage string, total int, work func(context.Context, int) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	Report(ctx, Event{Stage: stage, Total: total})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	var progressMu sync.Mutex
	completed := 0
	for range min(8, total) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				if err := work(ctx, i); err != nil {
					once.Do(func() { firstErr = err; cancel() })
					return
				}
				progressMu.Lock()
				completed++
				Report(ctx, Event{Stage: stage, Completed: completed, Total: total})
				progressMu.Unlock()
			}
		}()
	}
dispatch:
	for i := 0; i < total; i++ {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
