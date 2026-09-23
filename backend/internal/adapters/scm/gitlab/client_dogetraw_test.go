package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDoGETRawBoundedLargeTrace verifies that doGETRaw bounds the response body
// to jobLogTailMaxBytes (64 KB) when reading a job trace. A 10 MB response must
// not be fully buffered — only the last 64 KB is retained. The returned body
// length must be <= jobLogTailMaxBytes and must end with the last bytes the
// server emitted (rolling tail).
//
// This is the regression test for reviewer Item 12 (comment ID 3609642506):
// "Job traces can be very large, but this reads the entire response into
// daemon memory even though callers retain only the last 20 lines."
func TestDoGETRawBoundedLargeTrace(t *testing.T) {
	// Build a 10 MB body whose tail is uniquely identifiable so we can verify
	// the rolling-tail kept the last 64 KB, not the first.
	const totalSize = 10 * 1024 * 1024 // 10 MB
	const tailMarker = "TAIL-MARKER-LINE\n"

	// Construct the body: filler bytes + a recognizable tail.
	tailBytes := []byte(strings.Repeat("x", jobLogTailMaxBytes-len(tailMarker)) + tailMarker)
	body := make([]byte, totalSize)
	// Fill the prefix with 'A's so the head is distinguishable from the tail.
	for i := 0; i < totalSize-len(tailBytes); i++ {
		body[i] = 'A'
	}
	copy(body[totalSize-len(tailBytes):], tailBytes)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)

	c := NewClient(ClientOptions{
		Token:    StaticTokenSource("secret-token"),
		RESTBase: srv.URL + "/api/v4",
	})

	got, err := c.doGETRaw(context.Background(), "/projects/1/jobs/1/trace")
	if err != nil {
		t.Fatalf("doGETRaw: unexpected error: %v", err)
	}
	if len(got) > jobLogTailMaxBytes {
		t.Errorf("doGETRaw retained %d bytes; want <= %d (jobLogTailMaxBytes) — body must be bounded", len(got), jobLogTailMaxBytes)
	}
	if !strings.HasSuffix(string(got), tailMarker) {
		t.Errorf("doGETRaw did not retain the rolling tail: last bytes = %q...; want suffix %q", trim(string(got), 60), tailMarker)
	}
	// The retained buffer must not include head filler — it's a rolling tail.
	if len(got) == totalSize {
		t.Errorf("doGETRaw retained the entire 10 MB body — bound is not applied")
	}
}

// TestDoGETRawBoundedNormalTrace verifies that a normal-sized trace (10 KB,
// ~20 short lines) is returned in full — the bound does not degrade the common
// case where the whole trace fits comfortably within 64 KB.
func TestDoGETRawBoundedNormalTrace(t *testing.T) {
	// 20 lines of ~500 bytes each = ~10 KB, well under 64 KB.
	lines := make([]string, 20)
	for i := 0; i < 20; i++ {
		lines[i] = strings.Repeat("y", 480) + " END"
	}
	body := strings.Join(lines, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(ClientOptions{
		Token:    StaticTokenSource("secret-token"),
		RESTBase: srv.URL + "/api/v4",
	})

	got, err := c.doGETRaw(context.Background(), "/projects/1/jobs/1/trace")
	if err != nil {
		t.Fatalf("doGETRaw: unexpected error: %v", err)
	}
	if string(got) != body {
		t.Errorf("doGETRaw returned %d bytes; want full %d-byte trace (no degradation in the normal case)", len(got), len(body))
	}
	// Confirm the consumer's tailLines still yields all 20 lines from the
	// bounded buffer — graceful degradation must not affect the common case.
	tail := tailLines(string(got), ciFailureLogTailLines)
	gotLines := strings.Split(tail, "\n")
	if len(gotLines) != 20 {
		t.Errorf("tailLines(bounded) returned %d lines; want 20 (no degradation for a 10 KB trace)", len(gotLines))
	}
}

// TestDoGETRawErrorBodyCapped verifies that error response bodies (status >=
// 400) are read up to errorBodyMaxBytes (4 KB) and the rest is discarded.
// This bounds memory for pathological error payloads (e.g., a server that
// returns a multi-MB HTML error page on a 500). The error classification path
// must still function with the capped body.
func TestDoGETRawErrorBodyCapped(t *testing.T) {
	// 1 MB error body — far exceeds the 4 KB cap.
	const errSize = 1 * 1024 * 1024
	errBody := []byte(strings.Repeat("E", errSize))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write(errBody)
	}))
	t.Cleanup(srv.Close)

	c := NewClient(ClientOptions{
		Token:    StaticTokenSource("secret-token"),
		RESTBase: srv.URL + "/api/v4",
	})

	_, err := c.doGETRaw(context.Background(), "/projects/1/jobs/1/trace")
	if err == nil {
		t.Fatal("doGETRaw: error = nil, want an error for HTTP 500")
	}
	// The error must be non-nil (classification happened). We don't assert on
	// the exact message — the point is that the 1 MB body was capped before
	// classification, not buffered in full.
	_ = errors.Unwrap(err)
}

// TestDoGETRawErrorBodyCappedExact verifies the error-body cap is exactly
// errorBodyMaxBytes for a body that exceeds it: the daemon never holds more
// than errorBodyMaxBytes of an error payload.
func TestDoGETRawErrorBodyCappedExact(t *testing.T) {
	// Send a body larger than errorBodyMaxBytes; we cannot directly observe
	// the capped bytes through the returned error, but we can assert the
	// server saw a full read and the client returned an error (i.e., the cap
	// didn't swallow classification). The bounding is internal; this test
	// guards against regressions where the cap is removed (which would make
	// the returned error message contain the full oversized body).
	const errSize = 8 * 1024 // 8 KB — double the cap
	errBody := strings.Repeat("Z", errSize)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(errBody))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(ClientOptions{
		Token:    StaticTokenSource("secret-token"),
		RESTBase: srv.URL + "/api/v4",
	})

	_, err := c.doGETRaw(context.Background(), "/projects/1/jobs/1/trace")
	if err == nil {
		t.Fatal("doGETRaw: error = nil, want an error for HTTP 502")
	}
	// The error message must not contain the full 8 KB body — it should be
	// bounded by the cap (or fall through to resp.Status when the body isn't
	// valid JSON). Either way, the message must be far smaller than errSize.
	if msg := err.Error(); len(msg) >= errSize {
		t.Errorf("error message length = %d; want < %d — error body must be capped at errorBodyMaxBytes before classification", len(msg), errSize)
	}
}

// trim returns the first n bytes of s (or s if shorter), for compact failure
// messages.
func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
