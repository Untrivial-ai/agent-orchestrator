//go:build !windows

package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFakeApprovalACP is launched by the disposable opencode shim below. It
// deliberately leaves the prompt blocked until AO answers the approval.
func TestFakeApprovalACP(t *testing.T) {
	if os.Getenv("AO_E2E_FAKE_APPROVAL_ACP") != "1" {
		return
	}
	logPath := os.Getenv("AO_E2E_ACP_CALLS")
	record := func(method string) {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprintln(f, method)
		_ = f.Close()
	}
	record(fmt.Sprintf("provider-pid:%d", os.Getpid()))
	var promptID json.RawMessage
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var frame struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			continue
		}
		if frame.Method == "" && string(frame.ID) == `"permission-1"` {
			record("permission-response")
			_, _ = fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fake-provider-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"approved once"}}}}`)
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", promptID)
			continue
		}
		record(frame.Method)
		switch frame.Method {
		case "initialize":
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"resume":{}}},"authMethods":[]}}`+"\n", frame.ID)
		case "session/new", "session/load", "session/resume":
			_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"fake-provider-session"}}`+"\n", frame.ID)
		case "session/prompt":
			promptID = append(promptID[:0], frame.ID...)
			record("session/request_permission")
			_, _ = fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":"permission-1","method":"session/request_permission","params":{"sessionId":"fake-provider-session","toolCall":{"toolCallId":"tool-1","title":"Approve restart","kind":"edit"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"},{"optionId":"reject","name":"Reject","kind":"reject_once"}]}}`)
		default:
			if len(frame.ID) > 0 {
				_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{}}`+"\n", frame.ID)
			}
		}
	}
	os.Exit(0)
}

func TestPendingACPApprovalSurvivesDaemonSIGKILL(t *testing.T) {
	binDir := t.TempDir()
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\ncase \"$1\" in\n  acp) exec \"$AO_E2E_TEST_BINARY\" -test.run='^TestFakeApprovalACP$' ;;\n  auth) printf '0 credentials\\n' ;;\n  --version) printf '1.0.0\\n' ;;\n  *) exit 2 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENCODE_DATA_DIR", t.TempDir())
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("AO_E2E_TEST_BINARY", testBinary)
	t.Setenv("AO_E2E_FAKE_APPROVAL_ACP", "1")
	callsPath := filepath.Join(t.TempDir(), "provider-calls.log")
	t.Setenv("AO_E2E_ACP_CALLS", callsPath)

	dataDir := t.TempDir()
	d := startDaemon(t, dataDir)
	project := seedProject(t, d, "fake-approval-restart")
	setPermissions(t, d, project, "default")
	session := spawn(t, d, map[string]any{
		"projectId": project, "kind": "worker", "harness": "opencode", "mode": "chat",
		"prompt": "Request approval, then finish.",
	}).Session.ID
	before := d.awaitConversation(session, 30*time.Second, "pending approval before restart", func(s snapshot) bool {
		_, ok := s.pendingApproval()
		return ok
	})
	approval, _ := before.pendingApproval()
	if approval.RequestID == "" || len(approval.decisions()) != 2 || len(before.Turns) != 1 || len(before.Activities) != 1 {
		t.Fatalf("invalid pending approval before restart:\n%s", describe(before))
	}
	var read struct {
		Session struct {
			Status string `json:"status"`
		} `json:"session"`
	}
	d.mustCall("GET", "/sessions/"+session, http.StatusOK, nil, &read)
	if read.Session.Status != "needs_input" {
		t.Fatalf("status before restart = %q, want needs_input", read.Session.Status)
	}
	hostPID := persistentHostPID(t, dataDir, session)
	d.kill()
	if !processAlive(hostPID) {
		t.Fatalf("detached ACP host %d died with daemon", hostPID)
	}
	restarted := startDaemon(t, dataDir)
	restarted.awaitLiveController(session, 30*time.Second)
	after := restarted.conversation(session)
	replayed, pending := after.pendingApproval()
	if !pending || replayed.RequestID != approval.RequestID || len(after.Turns) != 1 || len(after.Activities) != 1 ||
		after.Turns[0].ID != before.Turns[0].ID || terminal(after.Turns[0].State) {
		t.Fatalf("pending approval changed across restart:\n%s", describe(after))
	}
	restarted.mustCall("GET", "/sessions/"+session, http.StatusOK, nil, &read)
	if read.Session.Status != "needs_input" {
		t.Fatalf("status after restart = %q, want needs_input", read.Session.Status)
	}
	restarted.mustCall("POST", "/sessions/"+session+"/conversation/approvals/"+replayed.RequestID+"/resolve",
		http.StatusNoContent, map[string]any{"decisionId": "allow"}, nil)
	finished := restarted.awaitConversation(session, 30*time.Second, "approved turn to finish", func(s snapshot) bool {
		return len(s.Turns) == 1 && terminal(s.Turns[0].State) &&
			(s.Turns[0].State != "completed" || contains(s.assistantText(), "approved once"))
	})
	if finished.Turns[0].State != "completed" || !contains(finished.assistantText(), "approved once") ||
		len(finished.Activities) != 1 || finished.Activities[0].Status != "resolved" {
		t.Fatalf("approved turn failed:\n%s", describe(finished))
	}
	if got := persistentHostPID(t, dataDir, session); got != hostPID {
		t.Fatalf("provider host replaced across restart: %d -> %d", hostPID, got)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"provider-pid:", "initialize", "session/new", "session/prompt", "session/request_permission", "permission-response"} {
		if got := strings.Count(string(calls), method); got != 1 {
			t.Fatalf("provider method %s called %d times:\n%s", method, got, calls)
		}
	}
	restarted.mustCall("POST", "/sessions/"+session+"/kill", http.StatusOK, nil, nil)
}

// An unacknowledged first task can be retried with the same clientRequestId
// after a daemon crash during workspace setup, without duplicate workers.
func TestFirstTaskStartupRetryAfterDaemonSIGKILL(t *testing.T) {
	binDir := t.TempDir()
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\ncase \"$1\" in\n  acp) exec \"$AO_E2E_TEST_BINARY\" -test.run='^TestFakeApprovalACP$' ;;\n  auth) printf '0 credentials\\n' ;;\n  --version) printf '1.0.0\\n' ;;\n  *) exit 2 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OPENCODE_DATA_DIR", t.TempDir())
	t.Setenv("OPENCODE_CONFIG_CONTENT", "")
	t.Setenv("AO_E2E_TEST_BINARY", testBinary)
	t.Setenv("AO_E2E_FAKE_APPROVAL_ACP", "1")
	callsPath := filepath.Join(t.TempDir(), "provider-calls.log")
	t.Setenv("AO_E2E_ACP_CALLS", callsPath)

	dataDir := t.TempDir()
	d := startDaemon(t, dataDir)
	project := seedProject(t, d, "first-task-kill")
	marker := filepath.Join(t.TempDir(), "provisioning")
	release := filepath.Join(t.TempDir(), "release")
	postCreate := fmt.Sprintf("printf ready > %q; while [ ! -e %q ]; do sleep 0.05; done", marker, release)
	d.mustCall("PUT", "/projects/"+project+"/config", http.StatusOK, map[string]any{
		"config": map[string]any{"postCreate": []string{postCreate}},
	}, nil)
	req := map[string]any{
		"projectId": project, "kind": "worker", "harness": "opencode", "mode": "chat",
		"prompt": "Request approval, then finish.", "clientRequestId": "first-task-retry",
	}
	done := make(chan struct{})
	go func() {
		_, _ = d.call("POST", "/sessions", req, nil)
		close(done)
	}()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("first task never entered postCreate: %v\n%s", err, d.tailLog())
	}
	d.kill()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("first task HTTP request did not end after daemon kill")
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	restarted := startDaemon(t, dataDir)
	var sessions struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	var retry spawned
	status, err := restarted.call("POST", "/sessions", req, &retry)
	if err != nil || status != http.StatusCreated || retry.Session.ID == "" {
		t.Fatalf("first-start retry did not create a session: status=%d err=%v session=%q\n%s", status, err, retry.Session.ID, restarted.tailLog())
	}
	restarted.mustCall("GET", "/sessions", http.StatusOK, nil, &sessions)
	if len(sessions.Sessions) != 1 || sessions.Sessions[0].ID != retry.Session.ID {
		t.Fatalf("sessions after retry = %+v, want one %q", sessions.Sessions, retry.Session.ID)
	}
	finished := restarted.awaitConversation(retry.Session.ID, 30*time.Second, "first task after restart", func(s snapshot) bool {
		return len(s.Turns) == 1 && terminal(s.Turns[0].State) && contains(s.assistantText(), "approved once")
	})
	if finished.Turns[0].State != "completed" || !contains(finished.assistantText(), "approved once") {
		t.Fatalf("first task not completed after restart:\n%s", describe(finished))
	}
	var projectResponse struct {
		Project struct {
			Path string `json:"path"`
		} `json:"project"`
	}
	restarted.mustCall("GET", "/projects/"+project, http.StatusOK, nil, &projectResponse)
	worktrees, err := exec.Command("git", "-C", projectResponse.Project.Path, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count("\n"+string(worktrees), "\nworktree "); got != 2 {
		t.Fatalf("git worktree count = %d, want base repo + one worker:\n%s", got, worktrees)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(calls), "session/prompt"); got != 1 {
		t.Fatalf("provider got %d prompts, want one:\n%s", got, calls)
	}
	restarted.mustCall("POST", "/sessions/"+retry.Session.ID+"/kill", http.StatusOK, nil, nil)
}
