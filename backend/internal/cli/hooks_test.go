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
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
)

type activityCapture struct {
	body string
	path string
	hits int
}

// activityServer accepts POST /api/v1/sessions/{id}/activity and records what
// the CLI sent. It mirrors sendServer in send_test.go.
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

func TestHooks_ReportsUsageTranscriptMetadata(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "")
	t.Setenv("CLAUDE_CODE_USE_VERTEX", "")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true,"sessionId":"ao-7","state":""}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		In: strings.NewReader(`{
			"session_id":"native-7",
			"transcript_path":"/home/user/.claude/projects/p/native-7.jsonl",
			"model":"claude-sonnet",
			"agent_id":"sub-2",
			"agent_transcript_path":"/home/user/.claude/projects/p/agent-sub-2.jsonl",
			"cli_version":"9.4.1"
		}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "claude-code", "subagent-stop")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if req.AgentSessionID != "native-7" || req.Usage == nil {
		t.Fatalf("request = %+v", req)
	}
	if req.Usage.Harness != "claude-code" ||
		req.Usage.ProviderID != "anthropic" ||
		req.Usage.TranscriptPath != "/home/user/.claude/projects/p/native-7.jsonl" ||
		req.Usage.SubagentID != "sub-2" ||
		req.Usage.SubagentTranscriptPath != "/home/user/.claude/projects/p/agent-sub-2.jsonl" {
		t.Fatalf("usage metadata = %+v", req.Usage)
	}
	if strings.Contains(capture.body, "sourceCliVersion") {
		t.Fatalf("usage request retained obsolete CLI version metadata: %s", capture.body)
	}
}

func TestClaudeHookUsageProviderHintUsesTrustedProcessRouting(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		bedrock string
		vertex  string
		want    string
	}{
		{name: "default anthropic", want: "anthropic"},
		{name: "official anthropic api", baseURL: "https://api.anthropic.com/v1/messages", want: "anthropic"},
		{name: "official zai api", baseURL: "https://api.z.ai/api/anthropic", want: "zai"},
		{name: "bedrock precedence", baseURL: "https://custom.invalid", bedrock: "1", want: "bedrock"},
		{name: "vertex precedence", baseURL: "https://api.z.ai", vertex: "true", want: "vertex_ai"},
		// A route AO cannot name is still a route. Reporting silence would make
		// it indistinguishable from "no hook has run", which is what lets the
		// legacy repairer fall back to the model that answered — and that
		// fallback would bill a proxied session at Anthropic list rates.
		{name: "conflicting flags", bedrock: "1", vertex: "1", want: pricing.UnidentifiedBillingRoute},
		{name: "unknown custom route", baseURL: "https://token:secret@custom.invalid/v1",
			want: pricing.UnidentifiedBillingRoute},
		{name: "unparseable base url", baseURL: "://nonsense", want: pricing.UnidentifiedBillingRoute},
		{name: "non http scheme", baseURL: "ftp://example.com", want: pricing.UnidentifiedBillingRoute},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ANTHROPIC_BASE_URL", test.baseURL)
			t.Setenv("CLAUDE_CODE_USE_BEDROCK", test.bedrock)
			t.Setenv("CLAUDE_CODE_USE_VERTEX", test.vertex)
			t.Setenv("ANTHROPIC_API_KEY", "credential-must-not-persist")
			got := hookUsageMetadata("claude-code", []byte(`{"transcript_path":"/tmp/transcript.jsonl"}`))
			if got == nil || got.ProviderID != test.want {
				t.Fatalf("usage metadata = %+v, want provider %q", got, test.want)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if test.baseURL != "" && strings.Contains(string(encoded), test.baseURL) ||
				strings.Contains(string(encoded), "credential-must-not-persist") || strings.Contains(string(encoded), "secret") {
				t.Fatalf("usage metadata persisted routing secret or URL: %s", encoded)
			}
		})
	}

	t.Setenv("ANTHROPIC_BASE_URL", "https://api.z.ai")
	if got := hookUsageMetadata("codex", []byte(`{"transcript_path":"/tmp/rollout.jsonl"}`)); got == nil || got.ProviderID != "" {
		t.Fatalf("Codex inherited Claude routing hint: %+v", got)
	}
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
	t.Setenv("AO_REVIEW_HARNESS", "codex")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true,"reviewSessionId":"review-7"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"codex-native-1"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "codex", "session-start")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.path != "/api/v1/reviews/review-7/activity" {
		t.Fatalf("path = %q, want /api/v1/reviews/review-7/activity", capture.path)
	}
	if got := capturedAgentSessionID(t, capture); got != "codex-native-1" {
		t.Fatalf("agentSessionId = %q, want codex-native-1", got)
	}
}

func TestHooks_ReviewerActivityOmitsToolCorrelationFields(t *testing.T) {
	t.Setenv("AO_REVIEW_SESSION_ID", "review-7")
	t.Setenv("AO_REVIEW_WORKER_SESSION_ID", "worker-7")
	t.Setenv("AO_REVIEW_HARNESS", "claude-code")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true,"reviewSessionId":"review-7"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"tool_name":"Bash","tool_use_id":"toolu_42","tool_response":"ok"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "claude-code", "post-tool-use")
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
		In:           strings.NewReader(`{"session_id":"codex-native-1"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "codex", "session-start")
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
		In:           strings.NewReader(`{"session_id":"codex-native-1"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "codex", "session-start")
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
		In:           strings.NewReader(`{"notification_type":"permission_prompt"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "claude-code", "notification")
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

func TestHooks_AiderNotificationDoesNotReadInheritedStdin(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	reader, writer := io.Pipe()
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		_, _, err := executeCLI(t, Deps{
			In:           reader,
			ProcessAlive: func(int) bool { return true },
		}, "hooks", "aider", "notification")
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Aider notification blocked on inherited stdin")
	}
	if capture.hits != 1 || capturedState(t, capture) != "waiting_input" {
		t.Fatalf("activity capture = %+v, want one waiting_input report", *capture)
	}
}

func TestHooks_IdlePromptReportsIdle(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true,"sessionId":"ao-7","state":"idle"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"notification_type":"idle_prompt"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "claude-code", "notification")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if got := capturedState(t, capture); got != "idle" {
		t.Errorf("state = %q, want idle (idle_prompt is not a blocking request)", got)
	}
}

func TestHooks_SessionEndReportsExited(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"reason":"logout"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "claude-code", "session-end")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capturedState(t, capture); got != "exited" {
		t.Errorf("state = %q, want exited", got)
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
	}, "hooks", "codex", "stop")
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
	}, "hooks", "claude-code", "stop")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capturedState(t, capture); got != "idle" {
		t.Errorf("state = %q, want idle", got)
	}
}

func TestHooks_StopReportsConversationFacts(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	t.Setenv("AO_RUNTIME_LAUNCH_ID", "launch-3")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	payload := `{"prompt":"finish the regression test","last_assistant_message":"I updated the generation fence.","transcript_path":"/tmp/provider/session.jsonl"}`
	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(payload),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "claude-code", "stop")
	if err != nil {
		t.Fatal(err)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatal(err)
	}
	if req.LatestUserPrompt != "finish the regression test" || req.LatestAssistantUpdate != "I updated the generation fence." {
		t.Fatalf("conversation facts = %#v", req)
	}
	if req.TranscriptPath != "/tmp/provider/session.jsonl" {
		t.Fatalf("transcript path = %q", req.TranscriptPath)
	}
}

func TestHooks_ContinueStopReportsClaudeCompatibleConversationFacts(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	payload := `{"prompt":"finish the Continue fix","last_assistant_message":"I updated the detector.","transcript_path":"/tmp/continue/session.jsonl"}`
	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(payload),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "continue", "stop")
	if err != nil {
		t.Fatal(err)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatal(err)
	}
	if req.LatestUserPrompt != "finish the Continue fix" || req.LatestAssistantUpdate != "I updated the detector." || req.TranscriptPath != "/tmp/continue/session.jsonl" {
		t.Fatalf("conversation facts = %#v", req)
	}
}

func TestHooks_NonSwitchingHarnessDoesNotReportConversationFacts(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	payload := `{"prompt":"private cursor prompt","last_assistant_message":"private cursor response","transcript_path":"/tmp/cursor/session.jsonl"}`
	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(payload),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "cursor", "stop")
	if err != nil {
		t.Fatal(err)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatal(err)
	}
	if req.LatestUserPrompt != "" || req.LatestAssistantUpdate != "" || req.TranscriptPath != "" {
		t.Fatalf("non-switching harness reported conversation facts: %#v", req)
	}
}

func TestHookConversationFactsExcludesAOCoordinationUserTurns(t *testing.T) {
	for _, prompt := range []string{
		"<ao-handoff-request>\nprepare context",
		"<ao-handoff-request switch-id=\"switch-1\">\nprepare context",
		"AO transferred the previous agent's context in hidden system instructions. Continue the unfinished action.",
	} {
		got := hookConversationFacts([]byte(`{"prompt":` + mustJSONString(t, prompt) + `,"lastAssistantMessage":"ok"}`))
		if got.LatestUserPrompt != "" {
			t.Fatalf("prompt %q was retained as real user intent", prompt)
		}
		wantAssistant := "ok"
		if strings.HasPrefix(prompt, "<ao-handoff-request") {
			wantAssistant = ""
		}
		if got.LatestAssistantUpdate != wantAssistant {
			t.Fatalf("assistant update = %q, want %q", got.LatestAssistantUpdate, wantAssistant)
		}
	}
}

func TestHookMetadataAndConversationFactsTolerateMalformedOtherProjection(t *testing.T) {
	t.Run("malformed usage retains conversation", func(t *testing.T) {
		payload := []byte(`{"prompt":"continue investigating","lastAssistantMessage":"updated","transcriptPath":"/tmp/conversation.jsonl","model":false}`)
		conversation := hookConversationFacts(payload)
		if conversation.LatestUserPrompt != "continue investigating" || conversation.LatestAssistantUpdate != "updated" || conversation.TranscriptPath != "/tmp/conversation.jsonl" {
			t.Fatalf("conversation = %+v", conversation)
		}
		if usage := hookUsageMetadata("claude-code", payload); usage != nil {
			t.Fatalf("usage = %+v, want nil", usage)
		}
	})

	t.Run("malformed conversation retains usage", func(t *testing.T) {
		payload := []byte(`{"prompt":false,"transcript_path":"/tmp/usage.jsonl","model":"claude-sonnet","agent_id":"sub-1"}`)
		usage := hookUsageMetadata("claude-code", payload)
		if usage == nil || usage.TranscriptPath != "/tmp/usage.jsonl" || usage.ModelID != "claude-sonnet" || usage.SubagentID != "sub-1" {
			t.Fatalf("usage = %+v", usage)
		}
	})
}

func mustJSONString(t *testing.T, value string) string {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestHooks_SessionStartReportsNativeSessionIDWithoutActivity(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"019f6af0-codex-session"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "codex", "session-start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	want := setActivityAPIRequest{Event: "session-start", AgentSessionID: "019f6af0-codex-session"}
	if req != want {
		t.Fatalf("body = %+v, want %+v", req, want)
	}
}

func TestHooks_ActivityAlsoReportsNativeSessionID(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"claude-session-1"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "claude-code", "stop")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	want := setActivityAPIRequest{State: "idle", Event: "stop", AgentSessionID: "claude-session-1"}
	if req != want {
		t.Fatalf("body = %+v, want %+v", req, want)
	}
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

func TestHooks_ClaudeCodePermissionRequestReportsBlocked(t *testing.T) {
	// claude-code installs the pre/post-tool-use trio, so a permission-request
	// blocked state can be correlated and cleared — it is the one harness that
	// reports blocked.
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"tool_name":"Bash"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "claude-code", "permission-request")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capturedState(t, capture); got != "blocked" {
		t.Errorf("state = %q, want blocked", got)
	}
}

func TestHooks_PostToolUseCarriesCorrelationFields(t *testing.T) {
	// Tool-use signals must carry the event and the native tool identity so
	// lifecycle can clear a stale blocked only on the approved tool's post.
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"tool_name":"Bash","tool_use_id":"toolu_42","tool_response":"ok"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "claude-code", "post-tool-use")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	want := setActivityAPIRequest{State: "active", Event: "post-tool-use", ToolName: "Bash", ToolUseID: "toolu_42"}
	if req != want {
		t.Errorf("body = %+v, want %+v", req, want)
	}
}

func TestHooks_EventWithoutToolIdentityOmitsIt(t *testing.T) {
	// Adapters whose payloads carry no tool fields (codex permission-request
	// payload here has tool_name only) still tag the event; missing identity
	// fields stay empty rather than inventing values.
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"tool_name":"Bash"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "codex", "permission-request")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	want := setActivityAPIRequest{State: "waiting_input", Event: "permission-request", ToolName: "Bash", ToolUseID: ""}
	if req != want {
		t.Errorf("body = %+v, want %+v", req, want)
	}
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

func TestHooks_CodexSessionStartReportsAgentSessionID(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"codex-native-1"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "codex", "session-start")
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
	want := setActivityAPIRequest{Event: "session-start", AgentSessionID: "codex-native-1"}
	if req != want {
		t.Fatalf("body = %+v, want %+v", req, want)
	}
}

func TestHooks_CodexBlankSessionIDIsIgnored(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"   "}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "codex", "session-start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.hits != 0 {
		t.Fatalf("daemon calls = %d, want 0", capture.hits)
	}
}

func TestHooks_ClaudeCodeSessionStartReportsAgentSessionID(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"claude-native-1"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "claude-code", "session-start")
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
	want := setActivityAPIRequest{Event: "session-start", AgentSessionID: "claude-native-1"}
	if req != want {
		t.Fatalf("body = %+v, want %+v", req, want)
	}
}

func TestHooks_ClaudeCodeBlankSessionIDIsIgnored(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"   "}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "claude-code", "session-start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.hits != 0 {
		t.Fatalf("daemon calls = %d, want 0", capture.hits)
	}
}

func TestHooks_ClaudeCompatibleSessionStartReportsAgentSessionID(t *testing.T) {
	for _, agent := range []string{"grok", "muse"} {
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
			want := setActivityAPIRequest{Event: "session-start", AgentSessionID: agent + "-native-1"}
			if req != want {
				t.Fatalf("body = %+v, want %+v", req, want)
			}
		})
	}
}

func TestHooks_MuseUserPromptReportsActive(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"muse-native-1"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "muse", "user-prompt-submit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	want := setActivityAPIRequest{State: "active", Event: "user-prompt-submit", AgentSessionID: "muse-native-1"}
	if req != want {
		t.Fatalf("body = %+v, want %+v", req, want)
	}
}

func TestHooks_RegisteredHarnessSessionStartReportsAgentSessionID(t *testing.T) {
	for _, agent := range []string{"opencode", "qwen", "kimi", "kilocode", "goose"} {
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
			if req != want {
				t.Fatalf("body = %+v, want %+v", req, want)
			}
		})
	}
}

func TestHooks_VibePostAgentReportsSessionIDAndIdle(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"session_id":"vibe-native-1","hook_event_name":"post_agent"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "vibe", "post-agent")
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
	want := setActivityAPIRequest{State: "idle", Event: "post-agent", AgentSessionID: "vibe-native-1"}
	if req != want {
		t.Fatalf("body = %+v, want %+v", req, want)
	}
}

func TestHooks_AgySessionStartReportsConversationID(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	promptDir := filepath.Join(cfg.dataDir, "prompts", "ao-7")
	if err := os.MkdirAll(promptDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "system.md"), []byte("follow AO standing instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	out, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"conversationId":"agy-native-1"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "agy", "session-start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("expected Agy session-start context output")
	}
	if capture.hits != 1 {
		t.Fatalf("daemon calls = %d, want 1", capture.hits)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	want := setActivityAPIRequest{Event: "session-start", AgentSessionID: "agy-native-1"}
	if req != want {
		t.Fatalf("body = %+v, want %+v", req, want)
	}
}

func TestHooks_AgyModernEventsReturnValidJSON(t *testing.T) {
	for _, event := range []string{"pre-invocation", "post-tool-use", "stop"} {
		t.Run(event, func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", "ao-7")
			cfg := setConfigEnv(t)
			srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
			writeRunFileFor(t, cfg, srv)

			out, _, err := executeCLI(t, Deps{
				In:           strings.NewReader(`{"conversationId":"agy-native-1"}`),
				ProcessAlive: func(int) bool { return true },
			}, "hooks", "agy", event)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var response map[string]any
			if err := json.Unmarshal([]byte(out), &response); err != nil {
				t.Fatalf("hook output is not valid JSON: %q: %v", out, err)
			}
			if len(response) != 0 {
				t.Fatalf("hook output = %s, want empty JSON object", out)
			}
			if capture.hits != 1 {
				t.Fatalf("daemon calls = %d, want 1", capture.hits)
			}
		})
	}

	t.Run("outside AO session", func(t *testing.T) {
		t.Setenv("AO_SESSION_ID", "")
		out, _, err := executeCLI(t, Deps{}, "hooks", "agy", "stop")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		var response map[string]any
		if err := json.Unmarshal([]byte(out), &response); err != nil {
			t.Fatalf("hook output is not valid JSON: %q: %v", out, err)
		}
		if len(response) != 0 {
			t.Fatalf("hook output = %s, want empty JSON object", out)
		}
	})
}

func TestHooks_CopilotSessionStartReportsSessionID(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"sessionId":"copilot-native-1"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "copilot", "session-start")
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
	want := setActivityAPIRequest{State: "active", Event: "session-start", AgentSessionID: "copilot-native-1"}
	if req != want {
		t.Fatalf("body = %+v, want %+v", req, want)
	}
}

func TestHooks_DevinSessionStartInjectsSystemPromptContext(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	promptDir := filepath.Join(cfg.dataDir, "prompts", "ao-7")
	if err := os.MkdirAll(promptDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "system.md"), []byte("follow AO standing instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	out, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"source":"startup"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "devin", "session-start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode Devin hook output: %v\n%s", err, out)
	}
	if got.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Fatalf("hookEventName = %q", got.HookSpecificOutput.HookEventName)
	}
	if got.HookSpecificOutput.AdditionalContext != "follow AO standing instructions" {
		t.Fatalf("additionalContext = %q", got.HookSpecificOutput.AdditionalContext)
	}
	if got := capturedState(t, capture); got != "active" {
		t.Errorf("state = %q, want active", got)
	}
}

func TestHooks_AgySessionStartInjectsSystemPromptContext(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	promptDir := filepath.Join(cfg.dataDir, "prompts", "ao-7")
	if err := os.MkdirAll(promptDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptDir, "system.md"), []byte("follow AO standing instructions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	out, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"source":"startup"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "agy", "session-start")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode Agy hook output: %v\n%s", err, out)
	}
	if got.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Fatalf("hookEventName = %q", got.HookSpecificOutput.HookEventName)
	}
	if got.HookSpecificOutput.AdditionalContext != "follow AO standing instructions" {
		t.Fatalf("additionalContext = %q", got.HookSpecificOutput.AdditionalContext)
	}
	if capture.hits != 0 {
		t.Errorf("Agy session-start should only inject context, got %d daemon calls", capture.hits)
	}
}

func TestHooks_RejectsMalformedSessionID(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "../etc/passwd")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"reason":"logout"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "claude-code", "session-end")
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
		In:           strings.NewReader(`{"notification_type":"idle_prompt"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "claude-code", "notification")
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
		In:           strings.NewReader(`{"notification_type":"auth_success"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "claude-code", "notification")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.hits != 0 {
		t.Errorf("expected no daemon call for an untracked notification, got %d", capture.hits)
	}
}

func TestHooks_DaemonDownIsBestEffort(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	setConfigEnv(t) // no run-file written: daemon is "not running"

	_, _, err := executeCLI(t, Deps{
		In: strings.NewReader(`{"reason":"logout"}`),
	}, "hooks", "claude-code", "session-end")
	if err != nil {
		t.Fatalf("hooks must be best-effort (exit 0) when the daemon is down, got: %v", err)
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
			wantIn:  []string{"ao hooks claude-code session-end", "session=ao-7"},
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
				In:           strings.NewReader(`{"reason":"logout"}`),
				ProcessAlive: func(int) bool { return true },
			}, "hooks", "claude-code", "session-end")
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
		In: strings.NewReader(`{"reason":"logout"}`),
	}, "hooks", "claude-code", "session-end")
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
	if !strings.Contains(string(data), "ao hooks claude-code session-end") {
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
		In:           strings.NewReader(`{"reason":"logout"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "claude-code", "session-end")
	if err != nil {
		t.Fatalf("hooks must exit 0 even on a daemon error, got: %v", err)
	}
	if !strings.Contains(errOut, "ao hooks") {
		t.Errorf("expected the failure surfaced to stderr, got %q", errOut)
	}
}

func TestHooks_CursorBeforeShellDefaultModeReportsBlocked(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	t.Setenv("AO_PERMISSION_MODE", "default")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	stdout, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"command":"git status"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "cursor", "before-shell-execution")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capturedState(t, capture); got != "blocked" {
		t.Fatalf("state = %q, want blocked", got)
	}
	var out cursorPermissionHookOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &out); err != nil {
		t.Fatalf("decode stdout: %v\nstdout=%q", err, stdout)
	}
	if out.Permission != "ask" {
		t.Fatalf("permission = %q, want ask", out.Permission)
	}
}

func TestHooks_CursorBeforeShellAutoModeReportsActive(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	t.Setenv("AO_PERMISSION_MODE", "auto")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	stdout, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"command":"git status"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "cursor", "before-shell-execution")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capturedState(t, capture); got != "active" {
		t.Fatalf("state = %q, want active", got)
	}
	var out cursorPermissionHookOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &out); err != nil {
		t.Fatalf("decode stdout: %v\nstdout=%q", err, stdout)
	}
	if out.Permission != "allow" {
		t.Fatalf("permission = %q, want allow", out.Permission)
	}
}

func TestHooks_CursorAskFailsClosedWhenBlockedActivityWriteFails(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		closeServer bool
	}{
		{name: "daemon 500", status: http.StatusInternalServerError},
		{name: "daemon unreachable", status: http.StatusOK, closeServer: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", "ao-7")
			t.Setenv("AO_PERMISSION_MODE", "default")
			cfg := setConfigEnv(t)
			srv, _ := activityServer(t, tt.status, `{"ok":false,"code":"WRITE_FAILED","message":"write failed"}`)
			writeRunFileFor(t, cfg, srv)
			if tt.closeServer {
				srv.Close()
			}

			stdout, _, err := executeCLI(t, Deps{
				In:           strings.NewReader(`{"command":"git push"}`),
				ProcessAlive: func(int) bool { return true },
			}, "hooks", "cursor", "before-shell-execution")
			if err == nil {
				t.Fatal("permission hook error = nil, want fail-closed error")
			}
			if strings.TrimSpace(stdout) != "" {
				t.Fatalf("permission hook stdout = %q, want no permission response", stdout)
			}
		})
	}
}

func TestHooks_CursorAfterShellExecutionReportsActive(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "ao-7")
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(`{"command":"git status","output":"ok"}`),
		ProcessAlive: func(int) bool { return true },
	}, "hooks", "cursor", "after-shell-execution")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := capturedState(t, capture); got != "active" {
		t.Fatalf("state = %q, want active", got)
	}
}

func TestHooks_CursorTerminalFailureReportsCorrelatedCompletion(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		wantEvent string
		wantTool  string
	}{
		{
			name:      "shell permission denied",
			payload:   `{"tool_name":"Shell","tool_input":{"command":"git push"},"failure_type":"permission_denied"}`,
			wantEvent: "cursor-shell-terminal-failure",
			wantTool:  "git push",
		},
		{
			name:      "shell error",
			payload:   `{"tool_name":"Shell","tool_input":{"command":"npm test"},"failure_type":"error"}`,
			wantEvent: "cursor-shell-terminal-failure",
			wantTool:  "npm test",
		},
		{
			name:      "shell timeout",
			payload:   `{"tool_name":"Shell","tool_input":{"command":"sleep 60"},"failure_type":"timeout"}`,
			wantEvent: "cursor-shell-terminal-failure",
			wantTool:  "sleep 60",
		},
		{
			name:      "shell interrupt",
			payload:   `{"tool_name":"Shell","tool_input":{"command":"go test ./..."},"is_interrupt":true}`,
			wantEvent: "cursor-shell-terminal-failure",
			wantTool:  "go test ./...",
		},
		{
			name:      "mcp permission denied",
			payload:   `{"tool_name":"MCP:deploy","failure_type":"permission_denied"}`,
			wantEvent: "cursor-mcp-terminal-failure",
			wantTool:  "deploy",
		},
		{
			name:      "mcp error",
			payload:   `{"tool_name":"MCP:search","failure_type":"error"}`,
			wantEvent: "cursor-mcp-terminal-failure",
			wantTool:  "search",
		},
		{
			name:      "mcp timeout",
			payload:   `{"tool_name":"MCP:deploy","tool_input":{"environment":"prod"},"failure_type":"timeout"}`,
			wantEvent: "cursor-mcp-terminal-failure",
			wantTool:  "deploy",
		},
		{
			name:      "mcp interrupt",
			payload:   `{"tool_name":"MCP:fetch","is_interrupt":true}`,
			wantEvent: "cursor-mcp-terminal-failure",
			wantTool:  "fetch",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", "ao-7")
			cfg := setConfigEnv(t)
			srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
			writeRunFileFor(t, cfg, srv)

			_, _, err := executeCLI(t, Deps{
				In:           strings.NewReader(tt.payload),
				ProcessAlive: func(int) bool { return true },
			}, "hooks", "cursor", "post-tool-use-failure")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var req struct {
				State    string `json:"state"`
				Event    string `json:"event"`
				ToolName string `json:"toolName"`
			}
			if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
				t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
			}
			if req.State != "active" || req.Event != tt.wantEvent || req.ToolName != tt.wantTool {
				t.Fatalf("terminal-failure activity = %+v, want state=active event=%q toolName=%q", req, tt.wantEvent, tt.wantTool)
			}
		})
	}
}
