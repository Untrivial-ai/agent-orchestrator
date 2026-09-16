package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

// sendServer wires an httptest server expecting POST /api/v1/sessions/{id}/send
// and captures the request body and path the CLI hit.
type sendCapture struct {
	body string
	path string
}

// writeRunFileFor points the CLI's run-file at srv so postJSON dials the test
// server. It mirrors the run-file convention the other CLI tests use.
func writeRunFileFor(t *testing.T, cfg testConfig, srv *httptest.Server) {
	t.Helper()
	if err := runfile.Write(cfg.runFile, runfile.Info{
		PID: os.Getpid(), Port: serverPort(t, srv.URL), StartedAt: time.Unix(100, 0).UTC(),
	}); err != nil {
		t.Fatalf("write run-file: %v", err)
	}
}

func sendServer(t *testing.T, status int, respBody string) (*httptest.Server, *sendCapture) {
	t.Helper()
	capture := &sendCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/sessions/") || !strings.HasSuffix(r.URL.Path, "/send") {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		capture.body = string(body)
		capture.path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, respBody)
	}))
	t.Cleanup(srv.Close)
	return srv, capture
}

func TestSend_Success(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	cfg := setConfigEnv(t)
	srv, capture := sendServer(t, http.StatusOK,
		`{"ok":true,"sessionId":"demo-1","message":"hello agent"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "send", "--session", "demo-1", "--message", "hello agent")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if capture.path != "/api/v1/sessions/demo-1/send" {
		t.Errorf("path = %q, want /api/v1/sessions/demo-1/send", capture.path)
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	if req.Message != "hello agent" {
		t.Errorf("captured message = %q, want %q", req.Message, "hello agent")
	}
}

func TestSend_SteerActiveTurnUsesProviderSteeringWithoutQueueing(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "source-2")
	cfg := setConfigEnv(t)
	var paths, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		paths = append(paths, r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"outcome":"steered","providerTurnId":"provider-turn-1","activityId":"activity-1"}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"send", "--session", "demo-1", "--steer", "--message", "correct course")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if len(paths) != 1 || paths[0] != "/api/v1/sessions/demo-1/conversation/steer-or-send" {
		t.Fatalf("paths = %v, want one atomic steer-or-send request", paths)
	}
	var req conversationMessageAPIRequest
	if err := json.Unmarshal([]byte(bodies[0]), &req); err != nil {
		t.Fatal(err)
	}
	if req.Text != "[from source-2] correct course" || req.ClientMessageID == "" {
		t.Errorf("request = %+v", req)
	}
	if !strings.Contains(out, "accepted by provider") || !strings.Contains(out, "action is not confirmed") {
		t.Errorf("output = %q", out)
	}
}

func TestSend_SteerIdleStartsOneNormalChatTurn(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	cfg := setConfigEnv(t)
	var paths, ids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		paths = append(paths, r.URL.Path)
		var req struct {
			Text string `json:"text"`
			ID   string `json:"clientMessageId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		ids = append(ids, req.ID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"outcome":"sent","turnId":"turn-2","state":"running","duplicate":false}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"send", "--session", "demo-1", "--steer", "--message", "next work")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	wantPaths := []string{
		"/api/v1/sessions/demo-1/conversation/steer-or-send",
	}
	if strings.Join(paths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("paths = %v, want %v", paths, wantPaths)
	}
	if len(ids) != 1 || ids[0] == "" {
		t.Fatalf("delivery ids = %v, want one stable non-empty id", ids)
	}
	if !strings.Contains(out, "normal Chat turn in running state") || !strings.Contains(out, "not confirmed") {
		t.Errorf("output = %q", out)
	}
}

func TestSend_SteerStateChangeRaceUsesOneAtomicRequest(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	cfg := setConfigEnv(t)
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"outcome":"steered","providerTurnId":"provider-running"}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"send", "--session", "demo-1", "--steer", "--message", "race correction")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	wantPaths := []string{
		"/api/v1/sessions/demo-1/conversation/steer-or-send",
	}
	if strings.Join(paths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("paths = %v, want %v", paths, wantPaths)
	}
	if !strings.Contains(out, "accepted by provider for active turn provider-running") {
		t.Fatalf("output = %q", out)
	}
}

func TestSend_SteerFailureDoesNotSilentlyQueue(t *testing.T) {
	tests := []struct {
		name   string
		status int
		code   string
	}{
		{name: "unsupported", status: http.StatusConflict, code: "CHAT_STEER_UNSUPPORTED"},
		{name: "provider failure", status: http.StatusBadGateway, code: "CHAT_PROVIDER_FAILED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := setConfigEnv(t)
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/internal/") {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				calls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = fmt.Fprintf(w, `{"error":"failure","code":%q,"message":"steer failed"}`, tt.code)
			}))
			t.Cleanup(srv.Close)
			writeRunFileFor(t, cfg, srv)

			_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
				"send", "--session", "demo-1", "--steer", "--message", "correction")
			if err == nil || !strings.Contains(err.Error(), tt.code) {
				t.Fatalf("err = %v, want %s", err, tt.code)
			}
			if calls != 1 {
				t.Fatalf("calls = %d, failed steer must not queue", calls)
			}
		})
	}
}

func TestSend_SteerUncertainExposesHandleForRecovery(t *testing.T) {
	cfg := setConfigEnv(t)
	var req conversationMessageAPIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":"conflict","code":"CHAT_STEER_UNCERTAIN","message":"delivery uncertain"}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"send", "--session", "demo-1", "--steer", "--message", "correction")
	if err == nil || !strings.Contains(err.Error(), "CHAT_STEER_UNCERTAIN") {
		t.Fatalf("err = %v, want uncertain steering error", err)
	}
	if req.ClientMessageID == "" {
		t.Fatal("clientMessageId is empty")
	}
	wantRecovery := "--steer --recover-only --client-message-id " + req.ClientMessageID
	if !strings.Contains(err.Error(), wantRecovery) {
		t.Fatalf("err = %q, want recovery command containing %q", err, wantRecovery)
	}
}

func TestSend_SteerTransportFailureExposesRetryHandle(t *testing.T) {
	cfg := setConfigEnv(t)
	captured := make(chan conversationMessageAPIRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var req conversationMessageAPIRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		captured <- req
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack response: %v", err)
			return
		}
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"send", "--session", "demo-1", "--steer", "--message", "correction")
	req := <-captured
	if err == nil || req.ClientMessageID == "" {
		t.Fatalf("err = %v clientMessageId = %q", err, req.ClientMessageID)
	}
	if !strings.Contains(err.Error(), "outcome is unknown") ||
		!strings.Contains(err.Error(), "--client-message-id "+req.ClientMessageID) {
		t.Fatalf("err = %q, want safe retry handle %q", err, req.ClientMessageID)
	}
}

func TestSend_SteerMalformedSuccessExposesRetryHandle(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing body"},
		{name: "truncated body", body: `{"outcome":"steered"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := setConfigEnv(t)
			var req conversationMessageAPIRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/internal/") {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, tt.body)
			}))
			t.Cleanup(srv.Close)
			writeRunFileFor(t, cfg, srv)

			_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
				"send", "--session", "demo-1", "--steer", "--message", "correction")
			if err == nil || req.ClientMessageID == "" {
				t.Fatalf("err = %v clientMessageId = %q", err, req.ClientMessageID)
			}
			if !strings.Contains(err.Error(), "outcome is unknown") ||
				!strings.Contains(err.Error(), "--client-message-id "+req.ClientMessageID) {
				t.Fatalf("err = %q, want safe retry handle %q", err, req.ClientMessageID)
			}
		})
	}
}

func TestSend_SteerServerFailureExposesRetryHandle(t *testing.T) {
	cfg := setConfigEnv(t)
	var req conversationMessageAPIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":"internal","code":"INTERNAL","message":"server failed"}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"send", "--session", "demo-1", "--steer", "--message", "correction")
	if err == nil || req.ClientMessageID == "" {
		t.Fatalf("err = %v clientMessageId = %q", err, req.ClientMessageID)
	}
	if !strings.Contains(err.Error(), "outcome is unknown") ||
		!strings.Contains(err.Error(), "--client-message-id "+req.ClientMessageID) {
		t.Fatalf("err = %q, want safe retry handle %q", err, req.ClientMessageID)
	}
}

func TestSend_SteerRecoverOnlyReusesHandleWithoutMessage(t *testing.T) {
	cfg := setConfigEnv(t)
	var req conversationMessageAPIRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"outcome":"steered","providerTurnId":"provider-turn-1"}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"send", "--session", "demo-1", "--steer", "--recover-only", "--client-message-id", "steer-1")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	if req.ClientMessageID != "steer-1" || !req.RecoverOnly || req.Text != "" {
		t.Fatalf("request = %+v, want recover-only request with original handle and no message", req)
	}
	if !strings.Contains(out, "Recovered steering receipt") || !strings.Contains(out, "steer-1") {
		t.Fatalf("output = %q", out)
	}
}

func TestSend_SteerRecoverOnlyRequiresHandle(t *testing.T) {
	_, _, err := executeCLI(t, Deps{},
		"send", "--session", "demo-1", "--steer", "--recover-only")
	var usage usageError
	if !errors.As(err, &usage) || !strings.Contains(err.Error(), "--client-message-id") {
		t.Fatalf("err = %v, want client message id usage error", err)
	}
}

func TestSend_SteerRecoverOnlyNeverFallsBackToNewMessage(t *testing.T) {
	cfg := setConfigEnv(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"error":"conflict","code":"CHAT_NO_ACTIVE_TURN","message":"idle"}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"send", "--session", "demo-1", "--steer", "--recover-only", "--client-message-id", "steer-1")
	if err == nil || !strings.Contains(err.Error(), "CHAT_NO_ACTIVE_TURN") {
		t.Fatalf("err = %v, want recovered no-active-turn result", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, recovery must never start a new delivery", calls)
	}
}

func TestSend_PrefixesMessageWithSenderSessionID(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "aa-47")
	cfg := setConfigEnv(t)
	srv, capture := sendServer(t, http.StatusOK,
		`{"ok":true,"sessionId":"demo-1","message":"hi"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "send", "--session", "demo-1", "--message", "  hi  ")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	want := "[from aa-47]   hi  "
	if req.Message != want {
		t.Errorf("captured message = %q, want %q", req.Message, want)
	}
}

func TestSend_BlankSenderSessionIDDoesNotPrefixMessage(t *testing.T) {
	t.Setenv("AO_SESSION_ID", " \t ")
	cfg := setConfigEnv(t)
	srv, capture := sendServer(t, http.StatusOK,
		`{"ok":true,"sessionId":"demo-1","message":"hello agent"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "send", "--session", "demo-1", "--message", "hello agent")
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	if req.Message != "hello agent" {
		t.Errorf("captured message = %q, want %q", req.Message, "hello agent")
	}
}

func TestSend_PreservesMessageWhitespace(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	cfg := setConfigEnv(t)
	srv, capture := sendServer(t, http.StatusOK, `{"ok":true,"sessionId":"demo-1","message":"hi"}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "send", "--session", "demo-1", "--message", "  hi  ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatalf("decode body: %v\nbody=%s", err, capture.body)
	}
	if req.Message != "  hi  " {
		t.Errorf("server received %q, want preserved whitespace", req.Message)
	}
}

func TestSend_EmptyMessageIsUsageError(t *testing.T) {
	setConfigEnv(t)
	_, _, err := executeCLI(t, Deps{}, "send", "--session", "demo-1", "--message", "   ")
	if err == nil {
		t.Fatal("expected usage error for empty message")
	}
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
	if !strings.Contains(err.Error(), "--message is required") {
		t.Fatalf("error missing usage message: %v", err)
	}
}

func TestSend_MissingSessionIsUsageError(t *testing.T) {
	setConfigEnv(t)
	_, _, err := executeCLI(t, Deps{}, "send", "--message", "hi")
	if err == nil {
		t.Fatal("expected usage error for missing --session")
	}
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
}

func TestSend_ServerBadRequestExits1(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := sendServer(t, http.StatusBadRequest,
		`{"error":"bad_request","code":"MESSAGE_REQUIRED","message":"Message is required"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "send", "--session", "demo-1", "--message", "hi")
	if err == nil {
		t.Fatal("expected runtime error from 400")
	}
	if got := ExitCode(err); got != 1 {
		t.Fatalf("exit code = %d, want 1", got)
	}
	if !strings.Contains(err.Error(), "MESSAGE_REQUIRED") && !strings.Contains(errOut, "MESSAGE_REQUIRED") {
		t.Fatalf("error did not surface the server error envelope: %v\nstderr=%s", err, errOut)
	}
}

func TestSend_ServerNotFoundExits1(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := sendServer(t, http.StatusNotFound,
		`{"error":"not_found","code":"SESSION_NOT_FOUND","message":"Unknown session"}`)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "send", "--session", "missing", "--message", "hi")
	if err == nil {
		t.Fatal("expected runtime error from 404")
	}
	if got := ExitCode(err); got != 1 {
		t.Fatalf("exit code = %d, want 1", got)
	}
}

func TestSend_ServerInternalErrorExits1(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := sendServer(t, http.StatusInternalServerError,
		`{"error":"internal","code":"SESSION_OPERATION_FAILED","message":"Session operation failed"}`)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "send", "--session", "demo-1", "--message", "hi")
	if err == nil {
		t.Fatal("expected runtime error from 500")
	}
	if got := ExitCode(err); got != 1 {
		t.Fatalf("exit code = %d, want 1", got)
	}
	// Regression guard: a future change that swallows the API envelope and
	// prints only "daemon returned HTTP 500" would silently hide what the
	// daemon was trying to tell the operator.
	if !strings.Contains(err.Error(), "SESSION_OPERATION_FAILED") && !strings.Contains(errOut, "SESSION_OPERATION_FAILED") {
		t.Fatalf("error did not surface the server error envelope: %v\nstderr=%s", err, errOut)
	}
}

func TestSend_DaemonNotRunningExits1(t *testing.T) {
	setConfigEnv(t)
	_, _, err := executeCLI(t, Deps{}, "send", "--session", "demo-1", "--message", "hi")
	if err == nil {
		t.Fatal("expected error when daemon is not running")
	}
	if got := ExitCode(err); got != 1 {
		t.Fatalf("exit code = %d, want 1", got)
	}
}

func TestSend_NetworkErrorExits1(t *testing.T) {
	cfg := setConfigEnv(t)
	// Start and immediately close a server so the run-file points at a closed port.
	srv, _ := sendServer(t, http.StatusOK, "{}")
	writeRunFileFor(t, cfg, srv)
	srv.Close()

	_, _, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "send", "--session", "demo-1", "--message", "hi")
	if err == nil {
		t.Fatal("expected runtime error from network failure")
	}
	if got := ExitCode(err); got != 1 {
		t.Fatalf("exit code = %d, want 1", got)
	}
}
