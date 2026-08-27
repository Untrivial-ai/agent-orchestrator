package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestReviewRequestDefaultsToOriginAndForwardsOverrides(t *testing.T) {
	cfg := setConfigEnv(t)
	t.Setenv("AO_SESSION_ID", "mer-7")
	t.Setenv("AO_REVIEW_SESSION_ID", "")
	srv, capture := reviewServer(t, http.StatusCreated, `{"created":true,"reviews":[{"prNumber":42,"targetSha":"abc123","status":"running"}]}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, aliveDeps(), "review", "request", "--reviewer", "codex", "--model", "gpt-5.6")
	if err != nil {
		t.Fatalf("review request: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodPost || capture.path != "/api/v1/sessions/mer-7/reviews/trigger" {
		t.Fatalf("request = %s %s", capture.method, capture.path)
	}
	var req requestReviewRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if req.Harness != "codex" || req.Model != "gpt-5.6" || req.RequestedBySessionID != "mer-7" {
		t.Fatalf("request = %+v", req)
	}
	if !strings.Contains(out, "started AO review for PR #42 at abc123 (running)") {
		t.Fatalf("stdout = %q", out)
	}
}

func TestReviewRequestRejectsRecursiveReviewer(t *testing.T) {
	setConfigEnv(t)
	t.Setenv("AO_SESSION_ID", "mer-7")
	t.Setenv("AO_REVIEW_SESSION_ID", "review-7")

	_, _, err := executeCLI(t, aliveDeps(), "review", "request")
	if ExitCode(err) != 2 || err == nil || !strings.Contains(err.Error(), "cannot run inside an AO reviewer session") {
		t.Fatalf("err = %v", err)
	}
}

func TestReviewTriggerRejectsRecursiveReviewer(t *testing.T) {
	setConfigEnv(t)
	t.Setenv("AO_REVIEW_SESSION_ID", "review-7")

	_, _, err := executeCLI(t, aliveDeps(), "review", "trigger", "mer-7")
	if ExitCode(err) != 2 || err == nil || !strings.Contains(err.Error(), "cannot run inside an AO reviewer session") {
		t.Fatalf("err = %v", err)
	}
}

func TestReviewRequestWorkerCannotTargetAnotherSession(t *testing.T) {
	setConfigEnv(t)
	t.Setenv("AO_SESSION_ID", "mer-7")
	t.Setenv("AO_REVIEW_SESSION_ID", "")

	_, _, err := executeCLI(t, aliveDeps(), "review", "request", "mer-8")
	if ExitCode(err) != 2 || err == nil || !strings.Contains(err.Error(), "only request review for its own") {
		t.Fatalf("err = %v", err)
	}
}

func TestReviewStatusShowsFindingsForOrigin(t *testing.T) {
	cfg := setConfigEnv(t)
	t.Setenv("AO_SESSION_ID", "mer-7")
	srv, capture := reviewServer(t, http.StatusOK, `{"reviewerHarness":"codex","reviews":[{"prNumber":42,"title":"Fix race","targetSha":"abc123","status":"changes_requested","latestRun":{"id":"run-1","status":"complete","verdict":"changes_requested","body":"guard the transaction"}}]}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, aliveDeps(), "review", "status")
	if err != nil {
		t.Fatalf("review status: %v\nstderr=%s", err, errOut)
	}
	if capture.method != http.MethodGet || capture.path != "/api/v1/sessions/mer-7/reviews" {
		t.Fatalf("request = %s %s", capture.method, capture.path)
	}
	for _, want := range []string{"#42", "changes_requested", "guard the transaction"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q: %s", want, out)
		}
	}
}

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
