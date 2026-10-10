package cua

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestProviderCallDiagnosticsUseStructuredCauseBeforeFallback(t *testing.T) {
	const blob = "PRIVATE_SCREENSHOT_BLOB"
	const secret = "private prompt words"
	for _, tc := range []struct {
		name, stdout, stderr, want, code string
	}{
		{"summary", `{"code":"ax_window_unresolved","summary":"AX window unavailable","reason":"ignored reason"}`, "ignored stderr", "AX window unavailable", "ax_window_unresolved"},
		{"message", `{"message":"AX permission denied"}`, "ignored stderr", "AX permission denied", "provider_failure"},
		{"reason_object", `{"reason":{"message":"AX lookup failed","screenshot":"` + blob + `","prompt":"` + secret + `"}}`, "ignored stderr", "AX lookup failed", "provider_failure"},
		{"error_object", `{"error":{"cause":{"message":"AX focused element missing"},"screenshot":"` + blob + `"}}`, "ignored stderr", "AX focused element missing", "provider_failure"},
		{"stderr", `{"unrecognized":"` + blob + `","prompt":"` + secret + `"}`, "AX response unavailable", "AX response unavailable", "provider_failure"},
		{"non_json_stdout", "AX provider disconnected", "", "AX provider disconnected", "provider_failure"},
		{"unknown_json", `{"unrecognized":"` + blob + `","prompt":"` + secret + `"}`, "", "no recognized provider diagnostic", "provider_failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			cause := errors.New("exit status 1")
			f.runner.hook = func(string, []string) (Output, error) {
				return Output{Stdout: []byte(tc.stdout), Stderr: []byte(tc.stderr)}, cause
			}
			err := f.adapter.call(context.Background(), "get_window_state", nil, nil)
			var failure *Error
			if !errors.As(err, &failure) || !errors.Is(err, cause) || !errors.Is(err, ErrProvider) || failure.Code != tc.code {
				t.Fatalf("provider classification or cause lost: %v", err)
			}
			for _, want := range []string{"call get_window_state", "exit status 1", tc.want} {
				if !strings.Contains(failure.Detail, want) {
					t.Fatalf("missing diagnostic %q in %q", want, failure.Detail)
				}
			}
			for _, private := range []string{blob, secret, "ignored stderr", "ignored reason"} {
				if strings.Contains(err.Error(), private) {
					t.Fatalf("unselected or private data leaked: %q", private)
				}
			}
		})
	}
}

func TestProviderCallDiagnosticsPreserveActualNonzeroStderr(t *testing.T) {
	f := newFixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f.runner.hook = func(string, []string) (Output, error) {
		return (commandRunner{}).Run(context.Background(), executable, []string{"-test.run=^TestProviderCallDiagnosticExitHelper$"}, append(os.Environ(), "GO_WANT_CUA_DIAGNOSTIC_HELPER=1"))
	}
	err = f.adapter.call(context.Background(), "get_window_state", nil, nil)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !errors.Is(err, ErrProvider) {
		t.Fatalf("actual command failure lost: %v", err)
	}
	for _, want := range []string{"call get_window_state", "exit status 1", "decode:", "AX exact window unavailable"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing actual command diagnostic %q: %v", want, err)
		}
	}
}

func TestProviderCallDiagnosticExitHelper(t *testing.T) {
	if os.Getenv("GO_WANT_CUA_DIAGNOSTIC_HELPER") != "1" {
		return
	}
	_, _ = fmt.Fprintln(os.Stdout, "non-JSON provider failure")
	_, _ = fmt.Fprintln(os.Stderr, "AX exact window unavailable")
	os.Exit(1)
}

func TestProviderCallDiagnosticsBoundAndRedactOutput(t *testing.T) {
	for _, tc := range []struct {
		name, stdout, stderr string
		args                 map[string]any
		private              []string
	}{
		{
			name: "credentials_prompt_and_capture",
			stderr: "AX focus unavailable\npassword=\"private password with spaces\"\nAPI_KEY=short-secret\nAO_TEST_CAPABILITY=short-capability\nBearer tiny-bearer\nprompt=\"private prompt words\"\nscreenshot=\"PRIVATE_SCREENSHOT_BLOB\"\n" +
				"provider echoed typed-private-content\ndata:image/png;base64," + strings.Repeat("aB9+/", 50),
			args:    map[string]any{"text": "typed-private-content"},
			private: []string{"private password with spaces", "short-secret", "short-capability", "tiny-bearer", "private prompt words", "PRIVATE_SCREENSHOT_BLOB", "typed-private-content", strings.Repeat("aB9+/", 10)},
		},
		{
			name: "argument_spans_scan_boundary", stderr: "AX focus unavailable " + strings.Repeat("private words ", 1000),
			args: map[string]any{"text": strings.Repeat("private words ", 1000)}, private: []string{"private words"},
		},
		{
			name: "overlapping_arguments", stderr: "AX focus unavailable: typed private session-id content",
			args: map[string]any{"session": "session-id", "text": "typed private session-id content"}, private: []string{"typed private", "session-id", "content"},
		},
		{name: "secret_code", stdout: `{"code":"` + strings.Repeat("t", 43) + `","summary":"AX focus unavailable"}`, private: []string{strings.Repeat("t", 43)}},
		{name: "oversized", stderr: strings.Repeat("AX lookup failed. ", 2000)},
		{name: "oversized_utf8", stderr: strings.Repeat("AX lookup failed: 窗口. ", 2000)},
		{name: "binary_capture", stderr: "AX focus unavailable\x00PRIVATE_SCREENSHOT_BLOB", private: []string{"PRIVATE_SCREENSHOT_BLOB"}},
		{name: "malformed_json", stdout: `AX focus unavailable {"screenshot":"PRIVATE_SCREENSHOT_BLOB","prompt":"private prompt words"`, private: []string{"PRIVATE_SCREENSHOT_BLOB", "private prompt words"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.runner.hook = func(string, []string) (Output, error) {
				return Output{Stdout: []byte(tc.stdout), Stderr: []byte(tc.stderr)}, errors.New("exit status 1")
			}
			err := f.adapter.call(context.Background(), "type_text", tc.args, nil)
			if err == nil || len(err.Error()) > providerDiagnosticLimit || !utf8.ValidString(err.Error()) || !strings.Contains(err.Error(), "call type_text") {
				t.Fatalf("diagnostic is absent, oversized or invalid: %v", err)
			}
			for _, private := range tc.private {
				if strings.Contains(err.Error(), private) {
					t.Fatalf("private diagnostic data leaked: %q", private)
				}
			}
			if strings.HasPrefix(tc.name, "oversized") && (!strings.Contains(err.Error(), "AX lookup failed") || !strings.Contains(err.Error(), "[truncated]")) {
				t.Fatal("bounded output lost useful cause or truncation marker", err)
			}
		})
	}
}

func TestProviderDriverCheckPreservesCancellation(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.adapter.started = func(ctx context.Context, _ int) (time.Time, error) { return time.Time{}, ctx.Err() }
	err := f.adapter.call(ctx, "get_window_state", nil, nil)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "call get_window_state") {
		t.Fatal("driver check lost cancellation or operation", err)
	}
}

func TestProviderCallDiagnosticsPreserveCancellationChain(t *testing.T) {
	for _, want := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(want.Error(), func(t *testing.T) {
			f := newFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if errors.Is(want, context.DeadlineExceeded) {
				ctx, cancel = context.WithDeadline(context.Background(), time.Unix(1, 0))
				defer cancel()
			}
			cause := errors.New("exit status 1")
			f.runner.hook = func(string, []string) (Output, error) { return Output{}, cause }
			err := f.adapter.call(ctx, "press_key", nil, nil)
			if !errors.Is(err, want) || !errors.Is(err, cause) || !errors.Is(err, ErrProvider) || !strings.Contains(err.Error(), "context: "+want.Error()) {
				t.Fatalf("cancellation or provider cause lost: %v", err)
			}
		})
	}
}

func TestProviderResultDecodeDiagnosticNamesOperation(t *testing.T) {
	f := newFixture(t)
	f.runner.hook = func(string, []string) (Output, error) {
		return Output{Stdout: []byte(`{"windows":"PRIVATE_SCREENSHOT_BLOB"}`)}, nil
	}
	var result struct {
		Windows []window `json:"windows"`
	}
	err := f.adapter.call(context.Background(), "list_windows", nil, &result)
	var failure *Error
	var decode *json.UnmarshalTypeError
	if !errors.As(err, &failure) || failure.Code != "provider_protocol" || !errors.As(err, &decode) || !errors.Is(err, ErrProvider) || !strings.Contains(err.Error(), "call list_windows") || !strings.Contains(err.Error(), "decode:") || strings.Contains(err.Error(), "PRIVATE_SCREENSHOT_BLOB") {
		t.Fatalf("structured result decode lost safe context or leaked payload: %v", err)
	}
}
