// Package sentryobs is the daemon-side Sentry integration. It captures genuine
// server faults (5xx and panics) with their Go stack, grouped by the same
// telemetrymeta fingerprint the PostHog path uses so an issue lines up across
// both. It is the surface where the daemon's INTERNAL_ERROR 500 root causes
// finally get a stack instead of an opaque count.
//
// It is a no-op until a DSN is configured (AO_SENTRY_DSN), and deny-by-default
// on privacy: no PII, no server name, no request data, no breadcrumbs, and
// local paths are scrubbed from messages and stack frames before send. Transient
// conditions (503 SERVICE_UNAVAILABLE) are deliberately NOT captured — they are
// retryable contention, not faults, and would drown the signal.
package sentryobs

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getsentry/sentry-go"
)

var enabled atomic.Bool

var captureGate = struct {
	sync.Mutex
	policyEnabled bool
	active        int
	waiters       []chan struct{}
}{}

var (
	homePath = regexp.MustCompile(`/(?:Users|home)/[^\s"']+`)
	winPath  = regexp.MustCompile(`[A-Za-z]:\\[^\s"']+|\\\\[^\s"']+`)
)

func scrub(s string) string {
	return winPath.ReplaceAllString(homePath.ReplaceAllString(s, "[redacted-path]"), "[redacted-path]")
}

// Config configures the daemon Sentry client.
type Config struct {
	DSN         string
	Release     string
	Environment string
	SampleRate  float64 // <=0 or >1 defaults to 1.0
	Transport   sentry.Transport
}

// Init initializes the global Sentry client. A blank DSN leaves it a permanent
// no-op. Safe to call once at daemon startup.
func Init(cfg Config) error {
	enabled.Store(false)
	if cfg.DSN == "" {
		return nil
	}
	rate := cfg.SampleRate
	if rate <= 0 || rate > 1 {
		rate = 1.0
	}
	transport := cfg.Transport
	if transport == nil {
		// A synchronous transport leaves no SDK queue that could send after an
		// opt-out acknowledgement. The policy drain below waits for any send
		// already in progress.
		transport = sentry.NewHTTPSyncTransport()
	}
	if err := sentry.Init(sentry.ClientOptions{
		Dsn:           cfg.DSN,
		Release:       cfg.Release,
		Environment:   cfg.Environment,
		EnableTracing: false,
		SampleRate:    rate,
		Transport:     transport,
		// PII is off by default; BeforeSend additionally clears ServerName and
		// Request and scrubs local paths, so no environment leaks regardless.
		// Deny-by-default: scrub the event before it leaves the process.
		BeforeSend: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			return scrubEvent(event)
		},
		// No automatic breadcrumbs (they can carry URLs/paths).
		BeforeBreadcrumb: func(_ *sentry.Breadcrumb, _ *sentry.BreadcrumbHint) *sentry.Breadcrumb {
			return nil
		},
	}); err != nil {
		return err
	}
	enabled.Store(true)
	return nil
}

// Enabled reports whether Sentry is active (a DSN was configured).
func Enabled() bool { return enabled.Load() }

// SetPolicyEnabled mirrors the crash-durable telemetry authority. Capture
// paths consult it synchronously so a watcher-observed opt-out closes intake
// even when the SDK itself was initialized earlier in the process.
func SetPolicyEnabled(value bool) {
	captureGate.Lock()
	captureGate.policyEnabled = value
	captureGate.Unlock()
}

func enterCapture() bool {
	if !enabled.Load() {
		return false
	}
	captureGate.Lock()
	defer captureGate.Unlock()
	if !captureGate.policyEnabled {
		return false
	}
	captureGate.active++
	return true
}

func leaveCapture() {
	captureGate.Lock()
	captureGate.active--
	if captureGate.active == 0 {
		for _, waiter := range captureGate.waiters {
			close(waiter)
		}
		captureGate.waiters = nil
	}
	captureGate.Unlock()
}

// Drain waits for every provider call that entered before intake closed.
func Drain(ctx context.Context) error {
	captureGate.Lock()
	if captureGate.active == 0 {
		captureGate.Unlock()
		return nil
	}
	done := make(chan struct{})
	captureGate.waiters = append(captureGate.waiters, done)
	captureGate.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func scrubEvent(event *sentry.Event) *sentry.Event {
	event.Message = scrub(event.Message)
	event.ServerName = "" // never leak the machine hostname
	event.Request = nil   // no URLs/headers/cookies
	for i := range event.Exception {
		event.Exception[i].Value = scrub(event.Exception[i].Value)
		if st := event.Exception[i].Stacktrace; st != nil {
			for j := range st.Frames {
				st.Frames[j].AbsPath = scrub(st.Frames[j].AbsPath)
				st.Frames[j].Filename = scrub(st.Frames[j].Filename)
			}
		}
	}
	return event
}

// ShouldCaptureStatus reports whether an HTTP status is a genuine server fault
// worth an issue. 5xx qualifies, except 503 (transient/retryable contention).
func ShouldCaptureStatus(status int) bool {
	return status >= http.StatusInternalServerError && status != http.StatusServiceUnavailable
}

// CaptureHTTPError captures a server-fault error with the given tags and a
// fingerprint identical to the PostHog grouping key. No-op when disabled or the
// error is nil.
func CaptureHTTPError(ctx context.Context, err error, tags map[string]string, fingerprint string) {
	if err == nil || ctx.Err() != nil || !enterCapture() {
		return
	}
	defer leaveCapture()
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelError)
		applyTags(scope, tags)
		if fingerprint != "" {
			scope.SetFingerprint([]string{fingerprint})
		}
		sentry.CaptureException(err)
	})
}

// CapturePanic captures a recovered panic with its Go stack (as scrubbed extra)
// at fatal level. No-op when disabled.
func CapturePanic(ctx context.Context, recovered any, stack string, tags map[string]string, fingerprint string) {
	if ctx.Err() != nil || !enterCapture() {
		return
	}
	defer leaveCapture()
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetLevel(sentry.LevelFatal)
		applyTags(scope, tags)
		if stack != "" {
			scope.SetContext("runtime", sentry.Context{"go_stack": scrub(stack)})
		}
		if fingerprint != "" {
			scope.SetFingerprint([]string{fingerprint})
		}
		sentry.CaptureException(fmt.Errorf("panic: %v", recovered))
	})
}

func applyTags(scope *sentry.Scope, tags map[string]string) {
	scope.SetTag("platform", "daemon")
	for k, v := range tags {
		if v != "" {
			scope.SetTag(k, v)
		}
	}
}

// Flush waits up to timeout for buffered events to send. Call on shutdown.
func Flush(timeout time.Duration) {
	if enabled.Load() {
		sentry.Flush(timeout)
	}
}
