package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	telemetryadapter "github.com/aoagents/agent-orchestrator/backend/internal/adapters/telemetry"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type lazyRecordingSink struct{ emits, closes atomic.Int32 }

func (s *lazyRecordingSink) Emit(context.Context, ports.TelemetryEvent) { s.emits.Add(1) }
func (s *lazyRecordingSink) Close(context.Context) error                { s.closes.Add(1); return nil }

func TestLazyTelemetryConsentLifecycle(t *testing.T) {
	var enabled atomic.Bool
	var builds atomic.Int32
	next := &lazyRecordingSink{}
	sink := newLazyTelemetrySink(enabled.Load, func() (ports.EventSink, error) {
		builds.Add(1)
		return next, nil
	})
	sink.Emit(t.Context(), ports.TelemetryEvent{}) // Dropped, not replayed.
	if builds.Load() != 0 {
		t.Fatal("initialized before consent")
	}
	enabled.Store(true)
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() { defer wg.Done(); sink.Emit(t.Context(), ports.TelemetryEvent{}) }()
	}
	wg.Wait()
	if builds.Load() != 1 || next.emits.Load() != 32 {
		t.Fatalf("builds=%d emits=%d", builds.Load(), next.emits.Load())
	}
	enabled.Store(false)
	sink.Emit(t.Context(), ports.TelemetryEvent{})
	if next.emits.Load() != 32 {
		t.Fatal("emitted after revocation")
	}
	enabled.Store(true)
	sink.Emit(t.Context(), ports.TelemetryEvent{})
	if builds.Load() != 1 || next.emits.Load() != 33 {
		t.Fatal("grant replaced pipeline or replayed events")
	}
	for range 32 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = sink.Close(t.Context()); sink.Emit(t.Context(), ports.TelemetryEvent{}) }()
	}
	wg.Wait()
	if next.closes.Load() != 1 || next.emits.Load() != 33 {
		t.Fatal("close not exclusive/idempotent")
	}
}

func TestLazyTelemetryInitFailureRetriesAndCloseBeforeInit(t *testing.T) {
	var calls int
	next := &lazyRecordingSink{}
	sink := newLazyTelemetrySink(func() bool { return true }, func() (ports.EventSink, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("init failed")
		}
		return next, nil
	})
	sink.Emit(t.Context(), ports.TelemetryEvent{})
	if next.emits.Load() != 0 {
		t.Fatal("failed init emitted")
	}
	sink.Emit(t.Context(), ports.TelemetryEvent{})
	if calls != 2 || next.emits.Load() != 1 {
		t.Fatal("later event did not retry")
	}
	_ = sink.Close(t.Context())
	unopened := newLazyTelemetrySink(func() bool { return true }, func() (ports.EventSink, error) { t.Fatal("initialized after close"); return nil, nil })
	_ = unopened.Close(t.Context())
	unopened.Emit(t.Context(), ports.TelemetryEvent{})
}

type blockingTelemetrySink struct {
	entered, release chan struct{}
	lazyRecordingSink
}

func (s *blockingTelemetrySink) Emit(context.Context, ports.TelemetryEvent) {
	close(s.entered)
	<-s.release
	s.emits.Add(1)
}
func TestLazyTelemetryEmitVersusClose(t *testing.T) {
	next := &blockingTelemetrySink{entered: make(chan struct{}), release: make(chan struct{})}
	sink := newLazyTelemetrySink(func() bool { return true }, func() (ports.EventSink, error) { return next, nil })
	emitted := make(chan struct{})
	go func() { sink.Emit(t.Context(), ports.TelemetryEvent{}); close(emitted) }()
	<-next.entered
	closed := make(chan struct{})
	go func() { _ = sink.Close(t.Context()); close(closed) }()
	if next.closes.Load() != 0 {
		t.Fatal("closed during Emit")
	}
	close(next.release)
	<-emitted
	<-closed
	if next.closes.Load() != 1 {
		t.Fatal("not closed")
	}
	sink.Emit(t.Context(), ports.TelemetryEvent{})
}

func TestLazyTelemetrySharedIdentityTenureAndLateGrant(t *testing.T) {
	dir := t.TempDir()
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	path := filepath.Join(dir, "telemetry_tenure.json")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"first_seen":%q,"last_active":%q,"active_days":10}`, yesterday, yesterday)), 0600); err != nil {
		t.Fatal(err)
	}
	var enabled atomic.Bool
	var builds int
	requests := make(chan map[string]any, 4)
	client := wiringTestRoundTripper(func(req *http.Request) (*http.Response, error) {
		defer req.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			return nil, err
		}
		requests <- body
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	factory := func() (ports.EventSink, error) {
		builds++
		return telemetryadapter.NewPostHogSink(dir, "test-key", "https://example.test", "test", "codex", client, slog.Default())
	}
	sink := newLazyTelemetrySink(enabled.Load, factory)
	sink.Emit(t.Context(), ports.TelemetryEvent{Name: "ao.daemon.started"})
	if builds != 0 {
		t.Fatal("pre-consent event constructed exporter")
	}
	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	var lookups atomic.Int32
	go func() {
		defer close(done)
		runGitHubAccountTelemetry(ctx, sink, enabled.Load, func(context.Context) (ports.SCMIdentity, error) {
			lookups.Add(1)
			return ports.SCMIdentity{Login: "octocat", Human: true}, nil
		}, accountAuthority{}, ticks)
	}()
	ticks <- time.Now() // Disabled startup pass.
	if lookups.Load() != 0 {
		t.Fatal("lookup before consent")
	}
	enabled.Store(true)
	sink.Emit(t.Context(), ports.TelemetryEvent{Name: "ao.daemon.started"})
	ticks <- time.Now().Add(time.Second)
	// Next tick synchronizes completion of the grant observation.
	ticks <- time.Now().Add(2 * time.Second)
	enabled.Store(false)
	ticks <- time.Now().Add(3 * time.Second)
	cancel()
	<-done
	sink.Emit(t.Context(), ports.TelemetryEvent{Name: "ao.daemon.started"})
	if err := sink.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if builds != 1 || lookups.Load() != 1 {
		t.Fatalf("builds=%d lookups=%d", builds, lookups.Load())
	}
	if len(requests) != 2 {
		t.Fatalf("exports=%d, want normal+identity only", len(requests))
	}
	for range 2 {
		props := (<-requests)["properties"].(map[string]any)
		if props["active_days"] != float64(11) {
			t.Fatalf("tenure=%v", props["active_days"])
		}
	}
	var state struct {
		ActiveDays int `json:"active_days"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.ActiveDays != 11 {
		t.Fatalf("persisted tenure=%d", state.ActiveDays)
	}
	// A new process would load the exact same persisted count.
	enabled.Store(true)
	restarted := newLazyTelemetrySink(enabled.Load, factory)
	restarted.Emit(t.Context(), ports.TelemetryEvent{Name: "ao.daemon.started"})
	if err := restarted.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if props := (<-requests)["properties"].(map[string]any); props["active_days"] != float64(11) {
		t.Fatalf("restart tenure=%v", props["active_days"])
	}
}
