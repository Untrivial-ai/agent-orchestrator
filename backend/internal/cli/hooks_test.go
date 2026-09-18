package cli

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type activityCapture struct {
	body string
	path string
	hits int
}

func activityServer(t *testing.T, status int, respBody string) (*httptest.Server, *activityCapture) {
	t.Helper()
	capture := &activityCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/activity") {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		capture.body = string(body)
		capture.path = r.URL.Path
		capture.hits++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, capture
}

func assertActivityRequest(t *testing.T, got, want setActivityAPIRequest) {
	t.Helper()
	if got.ObservedAt.IsZero() {
		t.Fatal("hook omitted its observation time")
	}
	// The exact timestamp has a separate deterministic wire-contract test.
	got.ObservedAt = time.Time{}
	if got != want {
		t.Fatalf("body = %+v, want %+v", got, want)
	}
}

func capturedState(t *testing.T, capture *activityCapture) string {
	t.Helper()
	var req struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	return req.State
}

func capturedAgentSessionID(t *testing.T, capture *activityCapture) string {
	t.Helper()
	var req struct {
		AgentSessionID string `json:"agentSessionId"`
	}
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	return req.AgentSessionID
}

func TestHooks_ReviewerRoutesToReviewActivity(t *testing.T) {
	t.Setenv("AO_REVIEW_SESSION_ID", "review-7")
	t.Setenv("AO_REVIEW_WORKER_SESSION_ID", "worker-7")
	t.Setenv("AO_REVIEW_HARNESS", "opencode")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true,"reviewSessionId":"review-7"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"opencode-native-1"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "session-start")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.path != "/api/v1/reviews/review-7/activity" {
		t.Fatalf("path = %q, want /api/v1/reviews/review-7/activity", capture.path)
	}
	if got := capturedAgentSessionID(t, capture); got != "opencode-native-1" {
		t.Fatalf("agentSessionId = %q, want opencode-native-1", got)
	}
}

func TestHooks_ReviewerActivityOmitsToolCorrelationFields(t *testing.T) {
	t.Setenv("AO_REVIEW_SESSION_ID", "review-7")
	t.Setenv("AO_REVIEW_WORKER_SESSION_ID", "worker-7")
	t.Setenv("AO_REVIEW_HARNESS", "opencode")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true,"reviewSessionId":"review-7"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"tool_name":"Bash","tool_use_id":"toolu_42","tool_response":"ok"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "active")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.path != "/api/v1/reviews/review-7/activity" {
		t.Fatalf("path = %q, want /api/v1/reviews/review-7/activity", capture.path)
	}
	var req map[string]any
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	if _, ok := req["toolName"]; ok {
		t.Fatalf("reviewer activity included toolName: body=%s", capture.body)
	}
	if _, ok := req["toolUseId"]; ok {
		t.Fatalf("reviewer activity included toolUseId: body=%s", capture.body)
	}
}

func TestHooks_ReviewerRoutingTakesPrecedenceOverWorkerSession(t *testing.T) {
	t.Setenv("AO_REVIEW_SESSION_ID", "review-7")
	t.Setenv("AO_REVIEW_WORKER_SESSION_ID", "worker-context-only")
	t.Setenv("AO_SESSION_ID", "worker-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"opencode-native-1"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "session-start")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.path != "/api/v1/reviews/review-7/activity" {
		t.Fatalf("path = %q, want reviewer route", capture.path)
	}
}

func TestHooks_ReviewWorkerSessionIDDoesNotRouteWithoutReviewSessionID(t *testing.T) {
	t.Setenv("AO_REVIEW_WORKER_SESSION_ID", "worker-context-only")
	t.Setenv("AO_SESSION_ID", "")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"opencode-native-1"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "session-start")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.hits != 0 {
		t.Fatalf("review worker context routed unexpectedly: path=%q body=%s", capture.path, capture.body)
	}
}

func TestHooks_NotificationReportsBlocked(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true,"sessionId":"ao-7","state":"blocked"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "permission-blocked")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.path != "/api/v1/sessions/ao-7/activity" {
		t.Errorf("path = %q, want /api/v1/sessions/ao-7/activity", capture.path)
	}
	if got := capturedState(t, capture); got != "blocked" {
		t.Errorf("state = %q, want blocked", got)
	}
}

func TestHooks_ThreadsRuntimeLaunchID(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	t.Setenv("AO_RUNTIME_LAUNCH_ID", "launch-3")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "stop")
	if err != nil {
		t.Fatal(err)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatal(err)
	}
	if req.LaunchID != "launch-3" {
		t.Fatalf("launch id = %q, want launch-3", req.LaunchID)
	}
}

func TestHooks_PayloadLaunchIDFallbackWhenEnvUnset(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"launch_id":"launch-from-payload"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "permission-blocked")
	if err != nil {
		t.Fatal(err)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatal(err)
	}
	if req.LaunchID != "launch-from-payload" {
		t.Fatalf("launch id = %q, want launch-from-payload", req.LaunchID)
	}
	if got := capturedState(t, capture); got != "blocked" {
		t.Errorf("state = %q, want blocked", got)
	}
}

func TestHooks_StopReportsIdle(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "stop")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capturedState(t, capture); got != "idle" {
		t.Errorf("state = %q, want idle", got)
	}
}

func TestHooks_SessionStartReportsAgentSessionID(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"native-session-1"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "session-start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	want := setActivityAPIRequest{State: "active", Event: "session-start", AgentSessionID: "native-session-1"}
	assertActivityRequest(t, req, want)
}

func TestHooks_ActivityAlsoReportsNativeSessionID(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"native-session-1"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "stop")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	want := setActivityAPIRequest{State: "idle", Event: "stop", AgentSessionID: "native-session-1"}
	assertActivityRequest(t, req, want)
}

func TestHooks_UnknownAgentCannotReportNativeSessionID(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"untrusted-session"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "unknown-agent", "session-start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.hits != 0 {
		t.Fatalf("unknown agent reported metadata; hits=%d body=%s", capture.hits, capture.body)
	}
}

func TestHooks_ToolCorrelationFieldsAreCarried(t *testing.T) {
	// Tool-use signals must carry the event and the native tool identity so
	// lifecycle can clear a stale blocked only on the approved tool's post.
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"tool_name":"Bash","tool_use_id":"toolu_42","tool_response":"ok"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "active")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	want := setActivityAPIRequest{State: "active", Event: "active", ToolName: "Bash", ToolUseID: "toolu_42"}
	assertActivityRequest(t, req, want)
}

func TestHooks_EventWithoutToolIdentityOmitsIt(t *testing.T) {
	// Payloads that carry only tool_name still tag the event; the missing
	// identity field stays empty rather than inventing a value.
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"tool_name":"Bash"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "permission-blocked")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	want := setActivityAPIRequest{State: "blocked", Event: "permission-blocked", ToolName: "Bash", ToolUseID: ""}
	assertActivityRequest(t, req, want)
}

func TestHooks_OpenCodeUserPromptReportsActive(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"ses-1","prompt":"fix this"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "user-prompt-submit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capturedState(t, capture); got != "active" {
		t.Errorf("state = %q, want active", got)
	}
}

func TestHooks_RegisteredHarnessSessionStartReportsAgentSessionID(t *testing.T) {
	for _, agent := range []string{"opencode"} {
		t.Run(agent, func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", "ao-7")
			cfg := setConfigEnv(t)
			srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
			writeRunFileFor(t, cfg, srv)

			_, _, err := executeCLI(t, Deps{
				In:           strings.NewReader(`{"session_id":"` + agent + `-native-1"}`),
				ProcessAlive: func(int) bool { return true },
			}, "hooks", agent, "session-start")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if capture.hits != 1 {
				t.Fatalf("daemon calls = %d, want 1", capture.hits)
			}
			var req setActivityAPIRequest
			if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
				t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
			}
			want := setActivityAPIRequest{State: "active", Event: "session-start", AgentSessionID: agent + "-native-1"}
			assertActivityRequest(t, req, want)
		})
	}
}

func TestHooks_RejectsMalformedSessionID(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "../etc/passwd")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "stop")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.hits != 0 {
		t.Errorf("expected no daemon call for an out-of-alphabet session id, got %d", capture.hits)
	}
}

func TestHooks_NoSessionIDIsNoOp(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "permission-blocked")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.hits != 0 {
		t.Errorf("expected no daemon call for a non-AO session, got %d", capture.hits)
	}
}

func TestHooks_UntrackedEventIsNoOp(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "subagent-stop")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.hits != 0 {
		t.Errorf("expected no daemon call for an untracked event, got %d", capture.hits)
	}
}

func TestHooks_DaemonDownIsBestEffort(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	setConfigEnv(t) // no run-file written: daemon is "not running"

	_, _, err := executeCLI(t, Deps{
		In: strings.NewReader(`{}`),
	}, "hooks", "opencode", "stop")
	if err != nil {
		t.Fatalf("hooks must be best-effort (exit 0) when the daemon is down, got: %v", err)
	}
}

func TestHooks_RetryOnlyUncommittedActivityProjection(t *testing.T) {
	for _, tt := range []struct {
		name      string
		code      string
		failures  int32
		wantCalls int32
	}{
		{"transient contention", "ACTIVITY_PROJECTION_BUSY", 2, 3},
		{"persistent contention", "ACTIVITY_PROJECTION_BUSY", 9, 4},
		{"other unavailable error is not safe to repeat", "SERVICE_UNAVAILABLE", 2, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", "ao-7")
			cfg := setConfigEnv(t)
			var calls atomic.Int32
			payloads := make(chan string, 10)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				payloads <- string(body)
				if calls.Add(1) <= tt.failures {
					w.WriteHeader(http.StatusServiceUnavailable)
					_ = json.NewEncoder(w).Encode(map[string]string{"code": tt.code, "message": "try again", "requestId": "hook-request"})
					return
				}
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			t.Cleanup(srv.Close)
			writeRunFileFor(t, cfg, srv)
			_, _, err := executeCLI(t, Deps{In: strings.NewReader(`{"session_id":"native-1","last_assistant_message":"answer"}`),
				ProcessAlive: func(int) bool { return true }}, "hooks", "opencode", "stop")
			if err != nil || calls.Load() != tt.wantCalls {
				t.Fatalf("hook delivery = %d calls, %v; want %d", calls.Load(), err, tt.wantCalls)
			}
			first := <-payloads
			for i := int32(1); i < tt.wantCalls; i++ {
				if got := <-payloads; got != first {
					t.Fatalf("retry changed original signal: %s != %s", got, first)
				}
			}
			failure, logErr := os.ReadFile(filepath.Join(cfg.dataDir, "hooks.log"))
			if tt.failures < tt.wantCalls {
				if !errors.Is(logErr, fs.ErrNotExist) {
					t.Fatalf("successful retry logged a failed delivery: %s %v", failure, logErr)
				}
			} else if logErr != nil || !strings.Contains(string(failure), "hook-request") {
				t.Fatalf("exhausted retry lost request-correlated evidence: %s %v", failure, logErr)
			}
		})
	}
}

// TestHooks_DeliveryFailureGoesToHooksLog covers the durable failure sink:
// agents swallow hook stderr, so a delivery failure must also land in
// $AO_DATA_DIR/hooks.log — and a delivered hook must not write the file at all.
func TestHooks_DeliveryFailureGoesToHooksLog(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantLog bool
		wantIn  []string
	}{
		{
			name:    "daemon error is appended",
			status:  http.StatusInternalServerError,
			body:    `{"error":"internal","code":"BOOM","message":"boom"}`,
			wantLog: true,
			wantIn:  []string{"ao hooks opencode stop", "session=ao-7"},
		},
		{
			name:   "successful delivery writes nothing",
			status: http.StatusOK,
			body:   `{"ok":true}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", "ao-7")
			cfg := setConfigEnv(t)
			srv, _ := activityServer(t, tc.status, tc.body)
			writeRunFileFor(t, cfg, srv)

			_, _, err := executeCLI(t, Deps{
				In:           strings.NewReader(`{}`),
				ProcessAlive: func(int) bool { return true },
			}, "hooks", "opencode", "stop")
			if err != nil {
				t.Fatalf("hooks must exit 0, got: %v", err)
			}

			logPath := filepath.Join(cfg.dataDir, "hooks.log")
			data, err := os.ReadFile(logPath)
			if !tc.wantLog {
				if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("hooks.log should not exist after a delivered hook, got err=%v data=%q", err, data)
				}
				return
			}
			if err != nil {
				t.Fatalf("hooks.log not written: %v", err)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(string(data), want) {
					t.Errorf("hooks.log missing %q:\n%s", want, data)
				}
			}
		})
	}
}

// TestHooks_HooksLogTruncatesPastCap asserts the size guard: an append against
// a hooks.log already past the cap truncates it first, so a persistently
// failing hook cannot grow the file without bound.
func TestHooks_HooksLogTruncatesPastCap(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t) // no run file written: every delivery fails
	logPath := filepath.Join(cfg.dataDir, "hooks.log")
	if err := os.MkdirAll(cfg.dataDir, 0o750); err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("x", maxHooksLogBytes+1)
	if err := os.WriteFile(logPath, []byte(oversized), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := executeCLI(t, Deps{
		In: strings.NewReader(`{}`),
	}, "hooks", "opencode", "stop")
	if err != nil {
		t.Fatalf("hooks must exit 0, got: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxHooksLogBytes {
		t.Fatalf("hooks.log = %d bytes, want truncated below the %d cap", len(data), maxHooksLogBytes)
	}
	if !strings.Contains(string(data), "ao hooks opencode stop") {
		t.Errorf("truncated hooks.log missing the new failure line:\n%s", data)
	}
}

func TestHooks_DaemonErrorIsSwallowed(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, _ := activityServer(t, http.StatusInternalServerError,
		`{"error":"internal","code":"BOOM","message":"boom"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "opencode", "stop")
	if err != nil {
		t.Fatalf("hooks must exit 0 even on a daemon error, got: %v", err)
	}
	if !strings.Contains(errOut, "ao hooks") {
		t.Errorf("expected the failure surfaced to stderr, got %q", errOut)
	}
}
