// Package observe contains observer-pattern primitives shared across the SCM
// and Tracker observation lanes. The pieces here are deliberately
// provider-agnostic: a polling goroutine supervisor, a lazy credential gate,
// and a bounded FIFO cache helper. Provider-specific normalization,
// persistence, and lifecycle reactions live in the sibling packages
// (observe/scm, future observe/tracker).
package observe

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// StartPollLoop launches a goroutine that calls poll immediately, then on every
// tick interval until ctx is done. The returned channel closes when the
// goroutine exits; callers wait on it during shutdown.
//
// The immediate first poll inside the goroutine (rather than before the ticker
// loop) keeps daemon startup non-blocking: callers see Start return after the
// goroutine is launched, not after the first network call.
//
// poll errors other than context.Canceled are logged via logger with name as a
// prefix, e.g. name="scm observer" -> "scm observer: initial poll failed".
func StartPollLoop(ctx context.Context, tick time.Duration, poll func(context.Context) error, logger *slog.Logger, name string) <-chan struct{} {
	if logger == nil {
		logger = slog.Default()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := poll(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error(name+": initial poll failed", "err", err)
		}
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := poll(ctx); err != nil && !errors.Is(err, context.Canceled) {
					logger.Error(name+": poll failed", "err", err)
				}
			}
		}
	}()
	return done
}

// CredentialProbe checks whether the observer's provider has usable credentials.
// Implementations return (false, nil) for a transient failure the observer
// should retry on the next tick, (false, non-nil) for an error the caller
// should surface, and (true, nil) when credentials are available.
type CredentialProbe func(ctx context.Context) (available bool, err error)

// CheckCredentialsOnce caches an available verdict but retries an unavailable
// verdict on subsequent polls, so signing in later resumes observation. The
// caller owns checked/disabled state via pointer.
//
// State transitions:
//   - probe == nil           → *checked = true, returns (true, nil). No gate.
//   - probe returns err      → state unchanged, returns (false, nil). Retried next tick.
//   - probe returns false    → *checked = true, *disabled = true, returns (false, nil). Retried next tick.
//   - probe returns true     → *checked = true, *disabled = false, returns (true, nil). Subsequent calls bypass the probe.
//
// The unavailable warning is emitted only when entering disabled state.
//
// A context-cancellation before the probe returns (false, ctx.Err()).
func CheckCredentialsOnce(ctx context.Context, probe CredentialProbe, checked, disabled *bool, logger *slog.Logger, name string) (bool, error) {
	if *checked && !*disabled {
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	if probe == nil {
		*checked = true
		*disabled = false
		return true, nil
	}
	available, err := probe(ctx)
	if err != nil {
		if !*checked {
			logger.Warn(name+" credentials check failed; will retry", "err", err)
		}
		return false, nil
	}
	*checked = true
	if !available {
		if !*disabled {
			logger.Warn(name + " disabled: provider credentials unavailable")
		}
		*disabled = true
		return false, nil
	}
	*disabled = false
	return true, nil
}

// CacheSet writes value to m[key] and tracks insertion order in *order for
// bounded FIFO eviction. If the bucket already had key, order is left
// unchanged; otherwise key is appended. When len(*order) exceeds maxEntries,
// the oldest keys are evicted from both order and m.
//
// maxEntries <= 0 disables eviction; callers that want bounded behavior must
// pass a positive value. The generic shape lets the same helper serve
// string-, time-, and bool-valued caches without per-type duplication.
func CacheSet[V any](m map[string]V, order *[]string, maxEntries int, key string, value V) {
	if _, ok := m[key]; !ok {
		*order = append(*order, key)
	}
	m[key] = value
	if maxEntries <= 0 {
		return
	}
	for len(*order) > maxEntries {
		evict := (*order)[0]
		*order = (*order)[1:]
		delete(m, evict)
	}
}

// CacheDelete removes key from m and the matching slot from *order. It is a
// no-op when key is absent.
func CacheDelete[V any](m map[string]V, order *[]string, key string) {
	if _, ok := m[key]; !ok {
		return
	}
	delete(m, key)
	dst := (*order)[:0]
	for _, cachedKey := range *order {
		if cachedKey != key {
			dst = append(dst, cachedKey)
		}
	}
	*order = dst
}
