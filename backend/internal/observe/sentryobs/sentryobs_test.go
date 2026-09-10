package sentryobs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

func TestScrubRedactsLocalPaths(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		// A trailing ':' is part of the matched path run (it must be, for C:\...),
		// so it is redacted along with the path.
		"open /Users/alice/secret/notes.md: no such file": "open [redacted-path] no such file",
		"read /home/bob/ao/worktree/x.go failed":          "read [redacted-path] failed",
		`stat C:\Users\carol\AppData\ao\db failed`:        "stat [redacted-path] failed",
		"no paths here": "no paths here",
	}
	for in, want := range cases {
		if got := scrub(in); got != want {
			t.Fatalf("scrub(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShouldCaptureStatus(t *testing.T) {
	t.Parallel()
	cases := map[int]bool{
		200: false,
		404: false,
		500: true,
		502: true,
		503: false, // transient contention (see #4325) — never an issue
		504: true,
	}
	for status, want := range cases {
		if got := ShouldCaptureStatus(status); got != want {
			t.Fatalf("ShouldCaptureStatus(%d) = %v, want %v", status, got, want)
		}
	}
}

func TestInitNoDSNIsNoOp(t *testing.T) {
	if err := Init(Config{}); err != nil {
		t.Fatalf("Init with empty DSN: %v", err)
	}
	if Enabled() {
		t.Fatal("Sentry should be disabled without a DSN")
	}
	// Capture calls must be safe no-ops when disabled.
	CaptureHTTPError(context.TODO(), errString("boom"), map[string]string{"path": "/x"}, "fp")
	CapturePanic(context.TODO(), "kaboom", "stack", nil, "fp")
	Flush(0)
}

func TestScrubEventStripsPathsAndContext(t *testing.T) {
	t.Parallel()
	event := &sentry.Event{
		Message:    "failed at /Users/dave/ao/main.go",
		ServerName: "daves-macbook.local",
		Request:    &sentry.Request{URL: "http://127.0.0.1:3001/api/v1/x"},
		Exception: []sentry.Exception{{
			Value: "open /home/eve/ws/file: denied",
			Stacktrace: &sentry.Stacktrace{Frames: []sentry.Frame{
				{AbsPath: "/Users/dave/ao/backend/internal/x.go", Filename: "/Users/dave/ao/backend/internal/x.go"},
			}},
		}},
	}
	out := scrubEvent(event)
	if out.Message != "failed at [redacted-path]" {
		t.Fatalf("message not scrubbed: %q", out.Message)
	}
	if out.ServerName != "" {
		t.Fatalf("server name not cleared: %q", out.ServerName)
	}
	if out.Request != nil {
		t.Fatal("request context not dropped")
	}
	if out.Exception[0].Value != "open [redacted-path] denied" {
		t.Fatalf("exception value not scrubbed: %q", out.Exception[0].Value)
	}
	f := out.Exception[0].Stacktrace.Frames[0]
	if f.AbsPath != "[redacted-path]" || f.Filename != "[redacted-path]" {
		t.Fatalf("frame paths not scrubbed: %+v", f)
	}
}

func TestPolicyDisableDrainsEnteredProviderCallAndRejectsLaterCapture(t *testing.T) {
	transport := &blockingTransport{started: make(chan struct{}, 1), release: make(chan struct{})}
	if err := Init(Config{DSN: "https://public@example.com/1", Transport: transport}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		SetPolicyEnabled(false)
		enabled.Store(false)
	})
	SetPolicyEnabled(true)

	captured := make(chan struct{})
	go func() {
		CaptureHTTPError(context.Background(), errString("before opt-out"), nil, "fp")
		close(captured)
	}()
	select {
	case <-transport.started:
	case <-time.After(time.Second):
		t.Fatal("provider call did not start")
	}

	SetPolicyEnabled(false)
	drained := make(chan error, 1)
	go func() { drained <- Drain(context.Background()) }()
	select {
	case err := <-drained:
		t.Fatalf("drain completed while provider call was active: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(transport.release)
	select {
	case <-captured:
	case <-time.After(time.Second):
		t.Fatal("capture did not finish")
	}
	if err := <-drained; err != nil {
		t.Fatalf("Drain: %v", err)
	}

	CaptureHTTPError(context.Background(), errString("after opt-out"), nil, "fp")
	if got := transport.calls.Load(); got != 1 {
		t.Fatalf("provider calls = %d, want only the call entered before opt-out", got)
	}
}

type blockingTransport struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

func (t *blockingTransport) Configure(sentry.ClientOptions) {}
func (t *blockingTransport) Flush(time.Duration) bool       { return true }
func (t *blockingTransport) FlushWithContext(context.Context) bool {
	return true
}
func (t *blockingTransport) Close() {}
func (t *blockingTransport) SendEvent(*sentry.Event) {
	t.calls.Add(1)
	t.started <- struct{}{}
	<-t.release
}

type errString string

func (e errString) Error() string { return string(e) }
