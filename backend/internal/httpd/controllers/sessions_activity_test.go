package controllers_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	usagesvc "github.com/aoagents/agent-orchestrator/backend/internal/service/usage"
)

type fakeActivityRecorder struct {
	gotID     domain.SessionID
	gotSignal ports.ActivitySignal
	calls     int
	err       error
}

type fakeUsageHookRecorder struct {
	gotID     domain.SessionID
	gotSignal usagesvc.HookSignal
	calls     int
	err       error
}

func (f *fakeUsageHookRecorder) RecordHook(_ context.Context, id domain.SessionID, signal usagesvc.HookSignal) error {
	f.calls++
	f.gotID = id
	f.gotSignal = signal
	return f.err
}

func (f *fakeActivityRecorder) ApplyActivitySignal(_ context.Context, id domain.SessionID, s ports.ActivitySignal) error {
	f.calls++
	f.gotID = id
	f.gotSignal = s
	return f.err
}

func newActivityTestServer(t *testing.T, rec *fakeActivityRecorder) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps := httpd.APIDeps{}
	if rec != nil {
		deps.Activity = rec
	}
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, deps, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSessionsAPI_ActivityForwardsUsageMetadataWithoutChangingActivity(t *testing.T) {
	usage := &fakeUsageHookRecorder{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{UsageHooks: usage}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity", `{
		"event":"subagent-stop",
		"agentSessionId":"native-1",
		"usage":{
			"harness":"claude-code",
			"providerId":"zai",
			"transcriptPath":"/tmp/main.jsonl",
			"modelId":"claude-sonnet",
			"subagentId":"sub-1",
			"subagentTranscriptPath":"/tmp/sub.jsonl"
		}
	}`)
	if status != http.StatusOK {
		t.Fatalf("activity = %d, want 200; body=%s", status, body)
	}
	if usage.calls != 1 || usage.gotID != "ao-1" {
		t.Fatalf("usage calls=%d id=%q", usage.calls, usage.gotID)
	}
	if usage.gotSignal.NativeSessionID != "native-1" ||
		usage.gotSignal.Harness != domain.HarnessClaudeCode ||
		usage.gotSignal.ProviderHint != "zai" ||
		usage.gotSignal.SubagentTranscriptPath != "/tmp/sub.jsonl" {
		t.Fatalf("usage signal = %+v", usage.gotSignal)
	}
}

func TestSessionsAPI_ActivitySanitizesAndBoundsUsageMetadata(t *testing.T) {
	usage := &fakeUsageHookRecorder{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(
		config.Config{},
		log,
		nil,
		httpd.APIDeps{UsageHooks: usage},
		httpd.ControlDeps{},
	))
	t.Cleanup(srv.Close)

	overlongPath := "/tmp/" + strings.Repeat("x", 4096) + ".jsonl"
	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity", `{
		"event":"subagent-stop",
		"agentSessionId":"native-1",
		"usage":{
			"harness":"claude-\u001bcode",
			"transcriptPath":"/tmp/\u001bmain.jsonl",
			"modelId":"claude-\u001bsonnet",
			"subagentId":"sub-\u001b1",
			"subagentTranscriptPath":"`+overlongPath+`"
		}
	}`)
	if status != http.StatusOK {
		t.Fatalf("activity = %d, want 200; body=%s", status, body)
	}
	if usage.calls != 1 {
		t.Fatalf("usage calls=%d, want 1", usage.calls)
	}
	if usage.gotSignal.Harness != domain.HarnessClaudeCode ||
		usage.gotSignal.TranscriptPath != "/tmp/main.jsonl" ||
		usage.gotSignal.ModelID != "claude-sonnet" ||
		usage.gotSignal.SubagentID != "sub-1" ||
		usage.gotSignal.SubagentTranscriptPath != "" {
		t.Fatalf("sanitized usage signal = %+v", usage.gotSignal)
	}
}

func TestSessionsAPI_UsageOnlyUnknownSessionReturnsNotFound(t *testing.T) {
	usage := &fakeUsageHookRecorder{err: usagesvc.ErrUsageSessionNotFound}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(
		config.Config{},
		log,
		nil,
		httpd.APIDeps{UsageHooks: usage},
		httpd.ControlDeps{},
	))
	t.Cleanup(srv.Close)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/missing/activity",
		`{"event":"session-start","agentSessionId":"native-1"}`)
	if status != http.StatusNotFound {
		t.Fatalf("activity = %d, want 404; body=%s", status, body)
	}
}

func TestSessionsAPI_ActivityForwardsMetadataOnlySessionStartToUsage(t *testing.T) {
	usage := &fakeUsageHookRecorder{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(
		config.Config{},
		log,
		nil,
		httpd.APIDeps{UsageHooks: usage},
		httpd.ControlDeps{},
	))
	t.Cleanup(srv.Close)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity",
		`{"event":"session-start","agentSessionId":"codex-native-1","launchId":"launch-7"}`)
	if status != http.StatusOK {
		t.Fatalf("activity = %d, want 200; body=%s", status, body)
	}
	if usage.calls != 1 || usage.gotID != "ao-1" {
		t.Fatalf("usage calls=%d id=%q", usage.calls, usage.gotID)
	}
	if usage.gotSignal.Event != "session-start" ||
		usage.gotSignal.NativeSessionID != "codex-native-1" ||
		usage.gotSignal.LaunchID != "launch-7" ||
		usage.gotSignal.Harness != "" {
		t.Fatalf("usage signal = %+v", usage.gotSignal)
	}
}

func TestSessionsAPI_ActivityUsageFailureDoesNotRejectAgentSignal(t *testing.T) {
	usage := &fakeUsageHookRecorder{err: errors.New("usage storage unavailable")}
	activity := &fakeActivityRecorder{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(
		config.Config{},
		log,
		nil,
		httpd.APIDeps{Activity: activity, UsageHooks: usage},
		httpd.ControlDeps{},
	))
	t.Cleanup(srv.Close)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity",
		`{"state":"idle","event":"session-end","agentSessionId":"native-1"}`)
	if status != http.StatusOK {
		t.Fatalf("activity = %d, want 200; body=%s", status, body)
	}
	if activity.calls != 1 || usage.calls != 1 {
		t.Fatalf("activity calls=%d usage calls=%d, want 1/1", activity.calls, usage.calls)
	}
}

func TestSessionsAPI_ActivityForwardsOrdinaryEventWithoutUsageMetadata(t *testing.T) {
	activity := &fakeActivityRecorder{}
	usage := &fakeUsageHookRecorder{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(
		config.Config{},
		log,
		nil,
		httpd.APIDeps{Activity: activity, UsageHooks: usage},
		httpd.ControlDeps{},
	))
	t.Cleanup(srv.Close)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity",
		`{"state":"active","event":"post-tool-use","agentSessionId":"codex-native-1","launchId":"launch-1"}`)
	if status != http.StatusOK {
		t.Fatalf("activity = %d, want 200; body=%s", status, body)
	}
	if activity.calls != 1 || usage.calls != 1 {
		t.Fatalf("activity calls=%d usage calls=%d, want 1/1", activity.calls, usage.calls)
	}
	if usage.gotSignal.Event != "post-tool-use" ||
		usage.gotSignal.LaunchID != "launch-1" ||
		usage.gotSignal.NativeSessionID != "codex-native-1" ||
		usage.gotSignal.Harness != "" {
		t.Fatalf("usage signal = %+v", usage.gotSignal)
	}
}

func TestSessionsAPI_ActivityAppliesSignal(t *testing.T) {
	rec := &fakeActivityRecorder{}
	srv := newActivityTestServer(t, rec)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity", `{"state":"waiting_input"}`)
	if status != http.StatusOK {
		t.Fatalf("activity = %d, want 200; body=%s", status, body)
	}
	var resp struct {
		OK        bool   `json:"ok"`
		SessionID string `json:"sessionId"`
		State     string `json:"state"`
	}
	mustJSON(t, body, &resp)
	if !resp.OK || resp.SessionID != "ao-1" || resp.State != "waiting_input" {
		t.Fatalf("activity response = %#v", resp)
	}
	if rec.calls != 1 || rec.gotID != "ao-1" {
		t.Fatalf("recorder calls=%d id=%q", rec.calls, rec.gotID)
	}
	if !rec.gotSignal.Valid || rec.gotSignal.State != domain.ActivityWaitingInput {
		t.Fatalf("recorder signal = %#v", rec.gotSignal)
	}
}

func TestSessionsAPI_ActivityAcceptsBlocked(t *testing.T) {
	rec := &fakeActivityRecorder{}
	srv := newActivityTestServer(t, rec)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity", `{"state":"blocked"}`)
	if status != http.StatusOK {
		t.Fatalf("activity = %d, want 200; body=%s", status, body)
	}
	if !rec.gotSignal.Valid || rec.gotSignal.State != domain.ActivityBlocked {
		t.Fatalf("recorder signal = %#v", rec.gotSignal)
	}
}

func TestSessionsAPI_ActivityThreadsCorrelationFields(t *testing.T) {
	// The optional correlation fields ride into the signal (sanitized); a
	// body without them (old CLIs) keeps producing a plain state-only signal,
	// which the other tests in this file pin.
	rec := &fakeActivityRecorder{}
	srv := newActivityTestServer(t, rec)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity",
		`{"state":"active","event":"post-tool-use","toolName":"Bash","toolUseId":"toolu_42","launchId":"launch-7"}`)
	if status != http.StatusOK {
		t.Fatalf("activity = %d, want 200; body=%s", status, body)
	}
	want := ports.ActivitySignal{Valid: true, State: domain.ActivityActive, Event: "post-tool-use", ToolName: "Bash", ToolUseID: "toolu_42", LaunchID: "launch-7"}
	if rec.gotSignal != want {
		t.Fatalf("recorder signal = %#v, want %#v", rec.gotSignal, want)
	}
}

func TestSessionsAPI_ActivityAcceptsMetadataOnlyAgentSessionID(t *testing.T) {
	rec := &fakeActivityRecorder{}
	srv := newActivityTestServer(t, rec)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity",
		`{"event":"session-start","agentSessionId":"native-session-1"}`)
	if status != http.StatusOK {
		t.Fatalf("activity = %d, want 200; body=%s", status, body)
	}
	want := ports.ActivitySignal{Event: "session-start", AgentSessionID: "native-session-1"}
	if rec.gotSignal != want {
		t.Fatalf("recorder signal = %#v, want %#v", rec.gotSignal, want)
	}
}

func TestSessionsAPI_ActivityThreadsAgentSessionIDWithState(t *testing.T) {
	rec := &fakeActivityRecorder{}
	srv := newActivityTestServer(t, rec)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity",
		`{"state":"idle","event":"stop","agentSessionId":"native-session-1"}`)
	if status != http.StatusOK {
		t.Fatalf("activity = %d, want 200; body=%s", status, body)
	}
	want := ports.ActivitySignal{Valid: true, State: domain.ActivityIdle, Event: "stop", AgentSessionID: "native-session-1"}
	if rec.gotSignal != want {
		t.Fatalf("recorder signal = %#v, want %#v", rec.gotSignal, want)
	}
}

func TestSessionsAPI_ActivityCapsOverlongCorrelationFields(t *testing.T) {
	// Overlong values are dropped, not truncated: a truncated id could never
	// match its pre/post counterpart, so an empty value (fail-safe: no
	// correlated clear) is strictly better.
	rec := &fakeActivityRecorder{}
	srv := newActivityTestServer(t, rec)

	long := make([]byte, 300)
	for i := range long {
		long[i] = 'a'
	}
	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity",
		`{"state":"active","event":"post-tool-use","toolUseId":"`+string(long)+`"}`)
	if status != http.StatusOK {
		t.Fatalf("activity = %d, want 200; body=%s", status, body)
	}
	if rec.gotSignal.ToolUseID != "" {
		t.Fatalf("overlong toolUseId not dropped: %q", rec.gotSignal.ToolUseID)
	}
	if rec.gotSignal.Event != "post-tool-use" {
		t.Fatalf("in-bounds event dropped: %#v", rec.gotSignal)
	}
}

func TestSessionsAPI_ActivityRejectsUnknownState(t *testing.T) {
	rec := &fakeActivityRecorder{}
	srv := newActivityTestServer(t, rec)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity", `{"state":"napping"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_ACTIVITY_STATE")
	if rec.calls != 0 {
		t.Fatalf("recorder should not be called for an invalid state; calls=%d", rec.calls)
	}
}

func TestSessionsAPI_ActivityRejectsEmptyMetadataOnlyRequest(t *testing.T) {
	rec := &fakeActivityRecorder{}
	srv := newActivityTestServer(t, rec)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity", `{"event":"session-start"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "ACTIVITY_OR_SESSION_ID_REQUIRED")
	if rec.calls != 0 {
		t.Fatalf("recorder should not be called for an empty metadata request; calls=%d", rec.calls)
	}
}

func TestSessionsAPI_ActivityRejectsOverlongMetadataOnlySessionID(t *testing.T) {
	rec := &fakeActivityRecorder{}
	srv := newActivityTestServer(t, rec)
	longID := strings.Repeat("a", 300)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity",
		`{"event":"session-start","agentSessionId":"`+longID+`"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "ACTIVITY_OR_SESSION_ID_REQUIRED")
	if rec.calls != 0 {
		t.Fatalf("recorder should not be called for an overlong session id; calls=%d", rec.calls)
	}
}

func TestSessionsAPI_ActivityRejectsBadJSON(t *testing.T) {
	srv := newActivityTestServer(t, &fakeActivityRecorder{})

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity", `{`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
}

func TestSessionsAPI_ActivityMissingSessionIs404(t *testing.T) {
	srv := newActivityTestServer(t, &fakeActivityRecorder{err: ports.ErrSessionNotFound})

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/missing/activity", `{"state":"idle"}`)
	assertErrorCode(t, body, status, http.StatusNotFound, "SESSION_NOT_FOUND")
}

func TestSessionsAPI_ActivityRecorderErrorIs500(t *testing.T) {
	srv := newActivityTestServer(t, &fakeActivityRecorder{err: errors.New("boom")})

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity", `{"state":"exited"}`)
	assertErrorCode(t, body, status, http.StatusInternalServerError, "INTERNAL_ERROR")
}

func TestSessionsAPI_ActivityWithoutRecorderIs501(t *testing.T) {
	srv := newActivityTestServer(t, nil)

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/sessions/ao-1/activity", `{"state":"idle"}`)
	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")
}
