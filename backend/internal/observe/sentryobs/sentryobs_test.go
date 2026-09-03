package sentryobs

import (
	"context"
	"testing"

	"github.com/getsentry/sentry-go"
)

func TestScrubRedactsLocalPaths(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		// A trailing ':' is part of the matched path run (it must be, for C:\...),
		// so it is redacted along with the path.
		"open /Users/alice/secret/notes.md: no such file":             "open [redacted-path] no such file",
		"read /home/bob/ao/worktree/x.go failed":                      "read [redacted-path] failed",
		`stat C:\Users\carol\AppData\ao\db failed`:                    "stat [redacted-path] failed",
		"open /Users/dave/Library/Mobile Documents/report.md: denied": "open [redacted-path] denied",
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

type errString string

func (e errString) Error() string { return string(e) }
