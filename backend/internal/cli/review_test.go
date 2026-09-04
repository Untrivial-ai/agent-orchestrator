package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reviewCapture records the method/path/body of the request the CLI made.
type reviewCapture struct {
	method string
	path   string
	body   string
}

func reviewServer(t *testing.T, status int, respBody string) (*httptest.Server, *reviewCapture) {
	t.Helper()
	capture := &reviewCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		capture.method = r.Method
		capture.path = r.URL.Path
		capture.body = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, capture
}

func aliveDeps() Deps { return Deps{ProcessAlive: func(int) bool { return true }} }

func TestReviewSubmitReadsBodyFile(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := reviewServer(t, http.StatusOK, `{"review":{"verdict":"changes_requested"}}`)
	writeRunFileFor(t, cfg, srv)

	bodyFile := filepath.Join(t.TempDir(), "review.md")
	if err := os.WriteFile(bodyFile, []byte("please fix"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, errOut, err := executeCLI(t, aliveDeps(),
		"review", "submit", "mer-1", "--run", "run-1", "--verdict", "changes_requested", "--body", bodyFile)
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodPost || capture.path != "/api/v1/sessions/mer-1/reviews/submit" {
		t.Fatalf("request = %s %s", capture.method, capture.path)
	}
	var req submitReviewRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if req.RunID != "run-1" || req.Verdict != "changes_requested" || req.Body != "please fix" {
		t.Fatalf("request = %+v", req)
	}
}

func TestReviewSubmitReadsBodyFromStdin(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := reviewServer(t, http.StatusOK, `{"review":{"verdict":"changes_requested"}}`)
	writeRunFileFor(t, cfg, srv)

	deps := aliveDeps()
	deps.In = strings.NewReader("please fix from stdin")
	_, errOut, err := executeCLI(t, deps,
		"review", "submit", "mer-1", "--run", "run-1", "--verdict", "changes_requested", "--body", "-")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	var req submitReviewRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if req.Body != "please fix from stdin" {
		t.Fatalf("body = %q, want the stdin contents", req.Body)
	}
}

func TestReviewSubmitAcceptsUnderscoreFlags(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := reviewServer(t, http.StatusOK, `{"review":{"verdict":"changes_requested"}}`)
	writeRunFileFor(t, cfg, srv)

	// Reviewer agents often spell --review-id as --review_id; both must work.
	_, errOut, err := executeCLI(t, aliveDeps(),
		"review", "submit", "mer-1", "--run", "run-1", "--verdict", "changes_requested", "--review_id", "98765")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	var req submitReviewRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if req.GithubReviewID != "98765" {
		t.Fatalf("githubReviewId = %q, want 98765", req.GithubReviewID)
	}
}

func TestReviewSubmitBatchReadsReviewsFromStdin(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := reviewServer(t, http.StatusOK, `{"reviews":[{"id":"run-1","verdict":"changes_requested"},{"id":"run-2","verdict":"approved"}]}`)
	writeRunFileFor(t, cfg, srv)

	deps := aliveDeps()
	deps.In = strings.NewReader(`{"reviews":[{"runId":"run-1","verdict":"changes_requested","body":"fix auth","githubReviewId":"101"},{"runId":"run-2","verdict":"approved","body":"looks good"}]}`)
	out, errOut, err := executeCLI(t, deps, "review", "submit", "mer-1", "--reviews", "-")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "recorded 2 review(s) for mer-1") {
		t.Fatalf("stdout = %q", out)
	}
	var req submitReviewRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(req.Reviews) != 2 || req.Reviews[0].RunID != "run-1" || req.Reviews[0].GithubReviewID != "101" || req.Reviews[1].Verdict != "approved" {
		t.Fatalf("request = %+v", req)
	}
	if req.RunID != "" || req.Verdict != "" {
		t.Fatalf("batch request should not also set legacy fields: %+v", req)
	}
}

func TestReviewSubmitBatchRetriesAcrossDaemonRestart(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := reviewServer(t, http.StatusOK, `{"reviews":[{"id":"run-1","verdict":"changes_requested"}]}`)

	deps := aliveDeps()
	deps.In = strings.NewReader(`{"reviews":[{"runId":"run-1","verdict":"changes_requested","body":"fix auth","githubReviewId":"101"}]}`)
	retries := 0
	deps.Sleep = func(time.Duration) {
		retries++
		writeRunFileFor(t, cfg, srv)
	}

	out, errOut, err := executeCLI(t, deps, "review", "submit", "mer-1", "--reviews", "-")
	if err != nil {
		t.Fatalf("submit should survive a daemon restart: %v\nstderr=%s", err, errOut)
	}
	if retries != 1 {
		t.Fatalf("retry waits = %d, want 1", retries)
	}
	if !strings.Contains(out, "recorded 1 review(s) for mer-1") {
		t.Fatalf("stdout = %q", out)
	}
	var req submitReviewRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(req.Reviews) != 1 || req.Reviews[0].RunID != "run-1" || req.Reviews[0].Body != "fix auth" || req.Reviews[0].GithubReviewID != "101" {
		t.Fatalf("retried request = %+v", req)
	}
}

func TestReviewSubmitRetriesUncertainTransportFailureWithIdenticalPayload(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := reviewServer(t, http.StatusOK, `{}`)
	writeRunFileFor(t, cfg, srv)

	var bodies []string
	deps := aliveDeps()
	deps.In = strings.NewReader(`{"reviews":[{"runId":"run-1","verdict":"approved","githubReviewId":"101"}]}`)
	deps.Sleep = func(time.Duration) {}
	deps.HTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/v1/sessions/mer-1/reviews/submit" {
			return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody}, nil
		}
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(raw))
		if len(bodies) == 1 {
			// The daemon may have committed before the connection broke. A retry
			// must therefore send byte-for-byte equivalent idempotency fields.
			return nil, io.ErrUnexpectedEOF
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"reviews":[{"id":"run-1","verdict":"approved"}]}`)),
		}, nil
	})}

	if _, errOut, err := executeCLI(t, deps, "review", "submit", "mer-1", "--reviews", "-"); err != nil {
		t.Fatalf("retry uncertain result: %v\nstderr=%s", err, errOut)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Fatalf("request bodies = %#v, want two identical attempts", bodies)
	}
}

func TestReviewSubmitDoesNotRetryDaemonAPIRejection(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := reviewServer(t, http.StatusConflict, `{"message":"review run already recorded a different body","code":"REVIEW_INVALID"}`)
	writeRunFileFor(t, cfg, srv)

	deps := aliveDeps()
	waits := 0
	deps.Sleep = func(time.Duration) { waits++ }
	_, _, err := executeCLI(t, deps, "review", "submit", "mer-1", "--run", "run-1", "--verdict", "approved")
	if err == nil || !strings.Contains(err.Error(), "REVIEW_INVALID") {
		t.Fatalf("err = %v, want daemon rejection", err)
	}
	if waits != 0 {
		t.Fatalf("retry waits = %d, want 0", waits)
	}
}

func TestReviewSubmitCancellationStopsUnavailableRetry(t *testing.T) {
	setConfigEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	deps := aliveDeps()
	waits := 0
	deps.Sleep = func(time.Duration) {
		waits++
		cancel()
	}
	c := &commandContext{deps: deps.withDefaults()}
	err := c.postReviewJSON(ctx, "sessions/mer-1/reviews/submit", submitReviewRequest{
		RunID: "run-1", Verdict: "approved",
	}, &reviewRunResponse{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context cancellation", err)
	}
	if waits != 1 {
		t.Fatalf("retry waits = %d, want 1", waits)
	}
}

func TestReviewSubmitUsesSessionFlag(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := reviewServer(t, http.StatusOK, `{"review":{"verdict":"approved"}}`)
	writeRunFileFor(t, cfg, srv)

	if _, errOut, err := executeCLI(t, aliveDeps(), "review", "submit", "--session", "mer-7", "--run", "run-7", "--verdict", "approved"); err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.path != "/api/v1/sessions/mer-7/reviews/submit" {
		t.Fatalf("path = %q, want mer-7", capture.path)
	}
}

func TestReviewSubmitTooManyArgsIsUsageError(t *testing.T) {
	setConfigEnv(t)
	_, _, err := executeCLI(t, aliveDeps(), "review", "submit", "mer-1", "mer-2")
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (usage); err=%v", got, err)
	}
}

func TestReviewSubmitMissingVerdictIsUsageError(t *testing.T) {
	setConfigEnv(t)
	_, _, err := executeCLI(t, aliveDeps(), "review", "submit", "mer-1", "--run", "run-1")
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (usage); err=%v", got, err)
	}
}

func TestReviewSubmitMissingWorkerIsUsageError(t *testing.T) {
	setConfigEnv(t)
	_, _, err := executeCLI(t, aliveDeps(), "review", "submit", "--run", "run-1", "--verdict", "approved")
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (usage); err=%v", got, err)
	}
}

func TestReviewSubmitMissingRunIsUsageError(t *testing.T) {
	setConfigEnv(t)
	_, _, err := executeCLI(t, aliveDeps(), "review", "submit", "mer-1", "--verdict", "approved")
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (usage); err=%v", got, err)
	}
}

func TestReviewStopPostsCancel(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := reviewServer(t, http.StatusOK, `{}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, aliveDeps(), "review", "stop", "mer-1")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodPost || capture.path != "/api/v1/sessions/mer-1/reviews/cancel" {
		t.Fatalf("request = %s %s", capture.method, capture.path)
	}
	if !strings.Contains(out, "cancelled review for mer-1") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestReviewStopUsesSessionFlag(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := reviewServer(t, http.StatusOK, `{}`)
	writeRunFileFor(t, cfg, srv)

	if _, errOut, err := executeCLI(t, aliveDeps(), "review", "stop", "--session", "mer-7"); err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.path != "/api/v1/sessions/mer-7/reviews/cancel" {
		t.Fatalf("path = %q, want mer-7", capture.path)
	}
}

func TestReviewStopMissingSessionIsUsageError(t *testing.T) {
	setConfigEnv(t)
	_, _, err := executeCLI(t, aliveDeps(), "review", "stop")
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (usage); err=%v", got, err)
	}
}

func TestReviewStopTooManyArgsIsUsageError(t *testing.T) {
	setConfigEnv(t)
	_, _, err := executeCLI(t, aliveDeps(), "review", "stop", "mer-1", "mer-2")
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (usage); err=%v", got, err)
	}
	// Assert the message so the test distinguishes the cobra atMostOneArg error
	// from the fallback "worker session id is required" usage error (both exit 2).
	if err == nil || !strings.Contains(err.Error(), "accepts at most 1 arg") {
		t.Fatalf("err = %v, want an \"accepts at most 1 arg\" usage error", err)
	}
}

func TestReviewRestartTooManyArgsIsUsageError(t *testing.T) {
	setConfigEnv(t)
	_, _, err := executeCLI(t, aliveDeps(), "review", "restart", "mer-1", "mer-2")
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (usage); err=%v", got, err)
	}
	// Assert the message so the test distinguishes the cobra atMostOneArg error
	// from the fallback "worker session id is required" usage error (both exit 2).
	if err == nil || !strings.Contains(err.Error(), "accepts at most 1 arg") {
		t.Fatalf("err = %v, want an \"accepts at most 1 arg\" usage error", err)
	}
}

func TestReviewRestartPostsTriggerCreated(t *testing.T) {
	cfg := setConfigEnv(t)
	// 201 with created:true means a new review pass was started.
	srv, capture := reviewServer(t, http.StatusCreated, `{"created":true}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, aliveDeps(), "review", "restart", "mer-1")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodPost || capture.path != "/api/v1/sessions/mer-1/reviews/trigger" {
		t.Fatalf("request = %s %s", capture.method, capture.path)
	}
	if !strings.Contains(out, "started a new review for mer-1") {
		t.Fatalf("stdout = %q, want the created message", out)
	}
}

func TestReviewRestartPostsTriggerReused(t *testing.T) {
	cfg := setConfigEnv(t)
	// 200 with created:false means an existing run for the same commit was reused.
	srv, capture := reviewServer(t, http.StatusOK, `{"created":false}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, aliveDeps(), "review", "restart", "mer-1")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodPost || capture.path != "/api/v1/sessions/mer-1/reviews/trigger" {
		t.Fatalf("request = %s %s", capture.method, capture.path)
	}
	if !strings.Contains(out, "reused the existing review for mer-1") {
		t.Fatalf("stdout = %q, want the reused message", out)
	}
}

func TestReviewRestartUsesSessionFlag(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := reviewServer(t, http.StatusOK, `{}`)
	writeRunFileFor(t, cfg, srv)

	if _, errOut, err := executeCLI(t, aliveDeps(), "review", "restart", "--session", "mer-7"); err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.path != "/api/v1/sessions/mer-7/reviews/trigger" {
		t.Fatalf("path = %q, want mer-7", capture.path)
	}
}

func TestReviewRestartMissingSessionIsUsageError(t *testing.T) {
	setConfigEnv(t)
	_, _, err := executeCLI(t, aliveDeps(), "review", "restart")
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (usage); err=%v", got, err)
	}
}

func TestReviewListGetsReviews(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := reviewServer(t, http.StatusOK, `{
		"reviewerHandleId":"handle-1",
		"reviews":[{
			"prUrl":"https://github.com/example/repo/pull/42",
			"prNumber":42,
			"title":"Fix session resume",
			"targetSha":"abc123",
			"status":"changes_requested",
			"latestRun":{"id":"run-1","reviewId":"review-1","verdict":"changes_requested"}
		}]
	}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, aliveDeps(), "review", "ls", "mer-1")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodGet || capture.path != "/api/v1/sessions/mer-1/reviews" {
		t.Fatalf("request = %s %s", capture.method, capture.path)
	}
	for _, want := range []string{"PR", "STATUS", "VERDICT", "TITLE", "#42", "changes_requested", "Fix session resume"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %q, want %q", out, want)
		}
	}
}

func TestReviewListJSONPreservesResponse(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := reviewServer(t, http.StatusOK, `{
		"reviewerHandleId":"handle-1",
		"reviews":[{
			"prUrl":"https://github.com/example/repo/pull/42",
			"prNumber":42,
			"title":"Fix session resume",
			"targetSha":"abc123",
			"status":"running",
			"latestRun":{"id":"run-1","reviewId":"review-1","status":"running"},
			"previousRun":{"id":"run-0","reviewId":"review-1","status":"completed","verdict":"approved"}
		}]
	}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, aliveDeps(), "review", "ls", "mer-1", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	var res listReviewsResponse
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if res.ReviewerHandleID != "handle-1" || len(res.Reviews) != 1 {
		t.Fatalf("response = %+v", res)
	}
	if res.Reviews[0].LatestRun == nil || res.Reviews[0].LatestRun.ReviewID != "review-1" {
		t.Fatalf("latest run = %+v", res.Reviews[0].LatestRun)
	}
	if res.Reviews[0].PreviousRun == nil || res.Reviews[0].PreviousRun.ID != "run-0" {
		t.Fatalf("previous run = %+v", res.Reviews[0].PreviousRun)
	}
}

func TestReviewListEmpty(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := reviewServer(t, http.StatusOK, `{"reviewerHandleId":"","reviews":[]}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, aliveDeps(), "review", "list", "mer-1")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "No reviews found for mer-1.") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestReviewListRequiresExactlyOneArgument(t *testing.T) {
	setConfigEnv(t)

	for _, args := range [][]string{
		{"review", "ls"},
		{"review", "ls", "mer-1", "mer-2"},
	} {
		_, _, err := executeCLI(t, aliveDeps(), args...)
		if got := ExitCode(err); got != 2 {
			t.Fatalf("args = %v, exit code = %d, want 2; err=%v", args, got, err)
		}
	}
}

func TestReviewListSurfacesDaemonError(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := reviewServer(t, http.StatusNotFound, `{"message":"session not found","code":"SESSION_NOT_FOUND","requestId":"req-2"}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, aliveDeps(), "review", "ls", "missing")
	if got := ExitCode(err); got != 1 {
		t.Fatalf("exit code = %d, want 1; err=%v", got, err)
	}
	for _, want := range []string{"session not found", "SESSION_NOT_FOUND", "req-2"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, want %q", err, want)
		}
	}
}

func TestReviewActionCommandNames(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		path string
	}{
		{name: "cancel", cmd: "cancel", path: "/api/v1/sessions/mer-1/reviews/cancel"},
		{name: "trigger", cmd: "trigger", path: "/api/v1/sessions/mer-1/reviews/trigger"},
		{name: "execute alias", cmd: "execute", path: "/api/v1/sessions/mer-1/reviews/trigger"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := setConfigEnv(t)
			srv, capture := reviewServer(t, http.StatusOK, `{}`)
			writeRunFileFor(t, cfg, srv)

			if _, errOut, err := executeCLI(t, aliveDeps(), "review", tt.cmd, "mer-1"); err != nil {
				t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
			}
			if capture.method != http.MethodPost || capture.path != tt.path {
				t.Fatalf("request = %s %s", capture.method, capture.path)
			}
		})
	}
}
