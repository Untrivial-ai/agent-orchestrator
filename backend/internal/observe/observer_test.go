package observe

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestCacheSet_InsertsAndOrders(t *testing.T) {
	m := map[string]string{}
	var order []string
	CacheSet(m, &order, 4, "a", "1")
	CacheSet(m, &order, 4, "b", "2")
	CacheSet(m, &order, 4, "c", "3")
	if got := m["b"]; got != "2" {
		t.Fatalf("m[b] = %q want %q", got, "2")
	}
	if len(order) != 3 || order[0] != "a" || order[2] != "c" {
		t.Fatalf("order = %v, want [a b c]", order)
	}
}

func TestCacheSet_UpdateDoesNotRepeatOrder(t *testing.T) {
	m := map[string]string{}
	var order []string
	CacheSet(m, &order, 4, "a", "1")
	CacheSet(m, &order, 4, "a", "1b")
	if got := m["a"]; got != "1b" {
		t.Fatalf("m[a] = %q want %q", got, "1b")
	}
	if len(order) != 1 || order[0] != "a" {
		t.Fatalf("order = %v, want [a] (repeat sets must not duplicate the slot)", order)
	}
}

func TestCacheSet_EvictsOldestPastMax(t *testing.T) {
	m := map[string]int{}
	var order []string
	for i, k := range []string{"a", "b", "c", "d"} {
		CacheSet(m, &order, 2, k, i)
	}
	if _, ok := m["a"]; ok {
		t.Fatalf("a should have been evicted, got %v", m)
	}
	if _, ok := m["b"]; ok {
		t.Fatalf("b should have been evicted, got %v", m)
	}
	if len(order) != 2 || order[0] != "c" || order[1] != "d" {
		t.Fatalf("order = %v, want [c d]", order)
	}
}

func TestCacheSet_GenericOverTime(t *testing.T) {
	m := map[string]time.Time{}
	var order []string
	now := time.Unix(1700000000, 0)
	CacheSet(m, &order, 4, "k", now)
	if !m["k"].Equal(now) {
		t.Fatalf("time round-trip failed: %v vs %v", m["k"], now)
	}
}

func TestCacheDelete_RemovesKeyAndOrderSlot(t *testing.T) {
	m := map[string]bool{}
	var order []string
	CacheSet(m, &order, 4, "a", true)
	CacheSet(m, &order, 4, "b", true)
	CacheDelete(m, &order, "a")
	if _, ok := m["a"]; ok {
		t.Fatalf("a should be removed: %v", m)
	}
	if len(order) != 1 || order[0] != "b" {
		t.Fatalf("order = %v, want [b]", order)
	}
}

func TestCacheDelete_MissingKeyIsNoop(t *testing.T) {
	m := map[string]bool{"a": true}
	order := []string{"a"}
	CacheDelete(m, &order, "z")
	if !m["a"] || len(order) != 1 || order[0] != "a" {
		t.Fatalf("missing-key delete must not mutate, got m=%v order=%v", m, order)
	}
}

func TestCheckCredentialsOnce_NilProbeMarksChecked(t *testing.T) {
	var checked, disabled bool
	ok, err := CheckCredentialsOnce(context.Background(), nil, &checked, &disabled, quietLogger(), "test")
	if err != nil || !ok {
		t.Fatalf("nil probe: ok=%v err=%v", ok, err)
	}
	if !checked || disabled {
		t.Fatalf("nil probe: checked=%v disabled=%v", checked, disabled)
	}
}

func TestCheckCredentialsOnce_ProbeAvailable(t *testing.T) {
	var checked, disabled bool
	calls := 0
	probe := func(context.Context) (bool, error) { calls++; return true, nil }
	if ok, err := CheckCredentialsOnce(context.Background(), probe, &checked, &disabled, quietLogger(), "test"); err != nil || !ok {
		t.Fatalf("first call: ok=%v err=%v", ok, err)
	}
	if !checked || disabled {
		t.Fatalf("after success: checked=%v disabled=%v", checked, disabled)
	}
	// Second call must NOT re-invoke probe.
	if ok, err := CheckCredentialsOnce(context.Background(), probe, &checked, &disabled, quietLogger(), "test"); err != nil || !ok {
		t.Fatalf("second call: ok=%v err=%v", ok, err)
	}
	if calls != 1 {
		t.Fatalf("probe should run once, ran %d", calls)
	}
}

func TestCheckCredentialsOnce_ProbeUnavailableRetries(t *testing.T) {
	var checked, disabled bool
	calls := 0
	probe := func(context.Context) (bool, error) { calls++; return false, nil }
	ok, err := CheckCredentialsOnce(context.Background(), probe, &checked, &disabled, quietLogger(), "test")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, want (false, nil)", ok, err)
	}
	if !checked || !disabled {
		t.Fatalf("after unavailable: checked=%v disabled=%v", checked, disabled)
	}
	// Subsequent calls must keep reporting unavailable, while checking again
	// so credentials added after startup can resume observation.
	for i := 0; i < 3; i++ {
		ok, err := CheckCredentialsOnce(context.Background(), probe, &checked, &disabled, quietLogger(), "test")
		if err != nil || ok {
			t.Fatalf("repeat call %d: ok=%v err=%v, want (false, nil)", i, ok, err)
		}
	}
	if calls != 4 {
		t.Fatalf("probe should retry while disabled, ran %d times", calls)
	}
}

func TestCheckCredentialsOnce_TransientErrorRetries(t *testing.T) {
	var checked, disabled bool
	calls := 0
	probe := func(context.Context) (bool, error) {
		calls++
		if calls == 1 {
			return false, errors.New("transient")
		}
		return true, nil
	}
	if ok, err := CheckCredentialsOnce(context.Background(), probe, &checked, &disabled, quietLogger(), "test"); err != nil || ok {
		t.Fatalf("first call: ok=%v err=%v, want (false,nil)", ok, err)
	}
	if checked || disabled {
		t.Fatalf("transient error must leave state untouched: checked=%v disabled=%v", checked, disabled)
	}
	if ok, err := CheckCredentialsOnce(context.Background(), probe, &checked, &disabled, quietLogger(), "test"); err != nil || !ok {
		t.Fatalf("retry call: ok=%v err=%v, want (true,nil)", ok, err)
	}
}

func TestStartPollLoop_FirstPollImmediateThenTicks(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	poll := func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := StartPollLoop(ctx, 10*time.Millisecond, poll, quietLogger(), "test")
	// Wait for at least 2 polls (initial + one tick).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := calls
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("done channel not closed after cancel")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 2 {
		t.Fatalf("expected at least 2 polls, got %d", calls)
	}
}

func TestStartPollLoop_LogsPollErrorWithoutPanic(t *testing.T) {
	var ran atomic.Int32
	poll := func(context.Context) error {
		ran.Add(1)
		return errors.New("boom")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := StartPollLoop(ctx, 10*time.Millisecond, poll, quietLogger(), "test")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ran.Load() >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("done channel not closed after cancel")
	}
	if ran.Load() < 2 {
		t.Fatalf("expected at least 2 polls under error path, got %d", ran.Load())
	}
}
