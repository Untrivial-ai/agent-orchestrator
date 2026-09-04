package sessionimport

import (
	"context"
	"runtime"
	"sync"
)

// scanWorkers bounds how many transcripts are read at once. Discovery is IO
// bound on many small files, so the useful width is well above the core count,
// but it is still capped: a history with thousands of transcripts must not open
// thousands of files at once.
func scanWorkers() int {
	n := runtime.NumCPU() * 2
	if n < 4 {
		n = 4
	}
	if n > 16 {
		n = 16
	}
	return n
}

// scanParallel maps fn over items across a bounded pool and returns the results
// that produced one, in input order.
//
// Order is preserved deliberately: discovery feeds a recency sort and a
// per-provider cap, and a scan whose output order depended on goroutine timing
// would make the list jitter between refreshes for no reason.
//
// The first error wins and cancels the rest. An item fn skips (ok=false) is
// simply absent from the result.
func scanParallel[T any, R any](ctx context.Context, items []T, fn func(context.Context, T) (R, bool, error)) ([]R, error) {
	if len(items) == 0 {
		return nil, nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type slot struct {
		value R
		ok    bool
	}
	slots := make([]slot, len(items))

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	sem := make(chan struct{}, scanWorkers())

	for i := range items {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()

			value, ok, err := fn(ctx, items[i])
			if err != nil {
				errOnce.Do(func() {
					firstErr = err
					cancel()
				})
				return
			}
			slots[i] = slot{value: value, ok: ok}
		}(i)
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	out := make([]R, 0, len(items))
	for _, s := range slots {
		if s.ok {
			out = append(out, s.value)
		}
	}
	return out, nil
}
