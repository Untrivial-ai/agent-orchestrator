package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type sessionRequestLog struct {
	mu       sync.Mutex
	requests []string
}

const cliInvokedRequest = "POST /internal/telemetry/cli-invoked"

func requestLogEntry(r *http.Request) string {
	entry := r.Method + " " + r.URL.Path
	if r.URL.RawQuery != "" {
		entry += "?" + r.URL.RawQuery
	}
	return entry
}

func appendPrimaryRequest(dst *[]string, r *http.Request) {
	entry := requestLogEntry(r)
	if entry == cliInvokedRequest {
		return
	}
	*dst = append(*dst, entry)
}

func (l *sessionRequestLog) append(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	appendPrimaryRequest(&l.requests, r)
}

func (l *sessionRequestLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.requests...)
}

func sessionCommandServer(t *testing.T) (*httptest.Server, *sessionRequestLog) {
	t.Helper()
	log := &sessionRequestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.append(r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions":
			active := r.URL.Query().Get("active")
			switch active {
			case "false":
				_, _ = io.WriteString(w, `{"sessions":[`+
					sessionJSON("demo-old", "demo", "worker", "terminated", true)+`,`+
					sessionJSON("demo-orch", "demo", "orchestrator", "terminated", true)+`]}`)
			default:
				_, _ = io.WriteString(w, `{"sessions":[`+
					sessionJSON("demo-2", "demo", "orchestrator", "idle", false)+`,`+
					sessionJSON("demo-1", "demo", "worker", "working", false)+`]}`)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/demo-1":
			_, _ = io.WriteString(w, `{"session":`+sessionJSON("demo-1", "demo", "worker", "working", false)+`}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo","repo":"https://github.com/aoagents/agent-orchestrator","defaultBranch":"main"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-1/pr/claim":
			var req claimPRRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if !req.AllowTakeover {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"error":"conflict","code":"PR_CLAIMED_BY_ACTIVE_SESSION","message":"PR is already claimed by active session demo-2 (omit --no-takeover to steal)"}`)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true,"sessionId":"demo-1","prs":[{"url":`+jsonQuote(req.PR)+`,"number":142,"state":"open","ci":"passing","review":"review_required","mergeability":"mergeable","reviewComments":false,"updatedAt":"2026-06-04T12:00:00Z"}],"branchChanged":true,"takenOverFrom":["demo-0"]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/cleanup":
			_, _ = io.WriteString(w, `{"ok":true,"cleaned":["demo-old","demo-orch"],"skipped":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-1/kill":
			_, _ = io.WriteString(w, `{"ok":true,"sessionId":"demo-1","freed":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-1/restore":
			_, _ = io.WriteString(w, `{"ok":true,"sessionId":"demo-1","session":`+sessionJSON("demo-1", "demo", "worker", "idle", false)+`}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/sessions/demo-1":
			var req sessionRenameRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true,"sessionId":"demo-1","displayName":`+jsonQuote(req.DisplayName)+`}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, log
}

func sessionJSON(id, project, kind, status string, terminated bool) string {
	b, _ := json.Marshal(map[string]any{
		"id":           id,
		"projectId":    project,
		"kind":         kind,
		"harness":      "codex",
		"displayName":  "Current Name",
		"activity":     map[string]any{"state": "idle", "lastActivityAt": "2026-06-02T12:00:00Z"},
		"isTerminated": terminated,
		"createdAt":    "2026-06-02T11:00:00Z",
		"updatedAt":    "2026-06-02T12:00:00Z",
		"status":       status,
	})
	return string(b)
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestSessionList_ProjectFilterAndDefaultFiltering(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, log := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "session", "ls", "--project", "demo")
	if err != nil {
		t.Fatalf("session ls failed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "demo:") || !strings.Contains(out, "demo-1") {
		t.Fatalf("output missing worker session:\n%s", out)
	}
	if strings.Contains(out, "demo-2") {
		t.Fatalf("orchestrator session should be hidden without --all:\n%s", out)
	}
	if !strings.Contains(out, "1 terminated session hidden") {
		t.Fatalf("hidden terminated hint missing:\n%s", out)
	}
	if !strings.Contains(out, "2 orchestrator sessions hidden. Use --all or `ao orchestrator ls` to show.") {
		t.Fatalf("hidden orchestrator hint missing:\n%s", out)
	}
	want := []string{
		"GET /api/v1/sessions?active=true&project=demo",
		"GET /api/v1/sessions?active=false&project=demo",
	}
	if got := log.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %#v, want %#v", got, want)
	}
}

func TestSessionList_HintsWhenOnlyTerminatedOrchestratorIsHidden(t *testing.T) {
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/sessions" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("active") == "false" {
			_, _ = io.WriteString(w, `{"sessions":[`+sessionJSON("demo-orch-old", "demo", "orchestrator", "terminated", true)+`]}`)
			return
		}
		_, _ = io.WriteString(w, `{"sessions":[]}`)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "session", "ls", "--project", "demo")
	if err != nil {
		t.Fatalf("session ls failed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "1 orchestrator session hidden. Use --all or `ao orchestrator ls` to show.") {
		t.Fatalf("terminated orchestrator hint missing:\n%s", out)
	}
	if strings.Contains(out, "terminated session hidden") {
		t.Fatalf("terminated orchestrator should not be reported as visible via --include-terminated alone:\n%s", out)
	}
}

func TestSessionList_JSONOutputDecodes(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "session", "ls", "--project", "demo", "--json")
	if err != nil {
		t.Fatalf("session ls --json failed: %v\nstderr=%s", err, errOut)
	}
	var got sessionListOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("session ls --json output is not decodable: %v\noutput=%s", err, out)
	}
	if got.Meta.HiddenTerminatedCount != 1 {
		t.Fatalf("hiddenTerminatedCount = %d, want 1", got.Meta.HiddenTerminatedCount)
	}
	if got.Meta.HiddenOrchestratorCount != 2 {
		t.Fatalf("hiddenOrchestratorCount = %d, want 2", got.Meta.HiddenOrchestratorCount)
	}
	if len(got.Data) != 1 {
		t.Fatalf("len(data) = %d, want 1; data=%#v", len(got.Data), got.Data)
	}
	if got.Data[0].ID != "demo-1" || got.Data[0].ProjectID != "demo" || got.Data[0].Role != "worker" {
		t.Fatalf("unexpected JSON entry: %#v", got.Data[0])
	}
}

func TestSessionList_AllIncludesOrchestratorsWithoutHiddenHint(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "session", "ls", "--project", "demo", "--all")
	if err != nil {
		t.Fatalf("session ls --all failed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "demo-1") || !strings.Contains(out, "demo-2") {
		t.Fatalf("output missing worker or orchestrator session:\n%s", out)
	}
	if strings.Contains(out, "orchestrator session hidden") {
		t.Fatalf("output reports hidden orchestrators with --all:\n%s", out)
	}
}

func TestSessionGet_SuccessWithProjectScope(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, log := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "session", "get", "demo-1", "-p", "demo")
	if err != nil {
		t.Fatalf("session get failed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "id: demo-1") || !strings.Contains(out, "project: demo") {
		t.Fatalf("unexpected get output:\n%s", out)
	}
	want := []string{"GET /api/v1/sessions/demo-1"}
	if got := log.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %#v, want %#v", got, want)
	}
}

func TestSessionGet_JSONOutputDecodes(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "session", "get", "demo-1", "--project", "demo", "--json")
	if err != nil {
		t.Fatalf("session get --json failed: %v\nstderr=%s", err, errOut)
	}
	var got sessionResponse
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("session get --json output is not decodable: %v\noutput=%s", err, out)
	}
	if got.Session.ID != "demo-1" || got.Session.ProjectID != "demo" || got.Session.Status != "working" {
		t.Fatalf("unexpected session JSON: %#v", got.Session)
	}
}

func TestSessionKill_SuccessWithProjectScope(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, log := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "session", "kill", "demo-1", "--project", "demo")
	if err != nil {
		t.Fatalf("session kill failed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "session demo-1 killed") {
		t.Fatalf("unexpected kill output:\n%s", out)
	}
	want := []string{"GET /api/v1/sessions/demo-1", "POST /api/v1/sessions/demo-1/kill"}
	if got := log.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %#v, want %#v", got, want)
	}
}

// TestSessionKill_PreservedWorkspaceNote: freed=false means the daemon
// terminated the session but kept the worktree (uncommitted changes are never
// force-deleted) — the CLI must say so instead of implying a full teardown.
func TestSessionKill_PreservedWorkspaceNote(t *testing.T) {
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-1/kill" {
			_, _ = io.WriteString(w, `{"ok":true,"sessionId":"demo-1","freed":false}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "session", "kill", "demo-1")
	if err != nil {
		t.Fatalf("session kill failed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "session demo-1 killed (workspace preserved)") {
		t.Fatalf("unexpected kill output:\n%s", out)
	}
}

func TestSessionRestore_SuccessWithProjectScope(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, log := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "session", "restore", "demo-1", "-p", "demo")
	if err != nil {
		t.Fatalf("session restore failed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "session demo-1 restored") || !strings.Contains(out, "project: demo") {
		t.Fatalf("unexpected restore output:\n%s", out)
	}
	want := []string{"GET /api/v1/sessions/demo-1", "POST /api/v1/sessions/demo-1/restore"}
	if got := log.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %#v, want %#v", got, want)
	}
}

func TestSessionCleanup_YesSkipsPrompt(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, log := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		In:           strings.NewReader("no\n"),
		ProcessAlive: func(int) bool { return true },
	}, "session", "cleanup", "--project", "demo", "--yes")
	if err != nil {
		t.Fatalf("session cleanup failed: %v\nstderr=%s", err, errOut)
	}
	if strings.Contains(out, "Type yes to confirm") {
		t.Fatalf("--yes should skip confirmation prompt:\n%s", out)
	}
	for _, want := range []string{"Checking for completed sessions", "Would clean demo-old", "Would clean demo-orch", "Cleaned: demo-old", "Cleaned: demo-orch", "Cleanup complete. 2 sessions cleaned."} {
		if !strings.Contains(out, want) {
			t.Fatalf("cleanup output missing %q:\n%s", want, out)
		}
	}
	want := []string{
		"GET /api/v1/sessions?active=false&project=demo",
		"POST /api/v1/sessions/cleanup?project=demo",
	}
	if got := log.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %#v, want %#v", got, want)
	}
}

// TestSessionCleanup_DryRunListsWithoutPromptingOrDeleting: --dry-run must
// preview the candidate sessions and stop there — no confirmation prompt and,
// critically, no POST to the cleanup endpoint, so nothing is removed.
func TestSessionCleanup_DryRunListsWithoutPromptingOrDeleting(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, log := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		// A prompt would block on empty input and fail; --dry-run must not read it.
		In:           strings.NewReader(""),
		ProcessAlive: func(int) bool { return true },
	}, "session", "cleanup", "--project", "demo", "--dry-run")
	if err != nil {
		t.Fatalf("session cleanup --dry-run failed: %v\nstderr=%s", err, errOut)
	}
	if strings.Contains(out, "Type yes to confirm") {
		t.Fatalf("--dry-run must not prompt for confirmation:\n%s", out)
	}
	for _, want := range []string{"Would clean demo-old", "Would clean demo-orch", "(dry-run: no sessions were removed)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Cleaned:") || strings.Contains(out, "sessions cleaned") {
		t.Fatalf("--dry-run must not report removals:\n%s", out)
	}
	// Only the candidate listing GET is allowed; no cleanup POST may be sent.
	want := []string{"GET /api/v1/sessions?active=false&project=demo"}
	if got := log.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %#v, want %#v", got, want)
	}
}

// TestSessionCleanup_ReportsSkippedWorkspaces: a session whose workspace was
// preserved must be listed with its reason and counted in the summary —
// previously the CLI printed "Would clean N" then "0 sessions cleaned" with no
// explanation, leaking workspaces invisibly.
func TestSessionCleanup_ReportsSkippedWorkspaces(t *testing.T) {
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions":
			_, _ = io.WriteString(w, `{"sessions":[`+
				sessionJSON("demo-old", "demo", "worker", "terminated", true)+`,`+
				sessionJSON("demo-orch", "demo", "orchestrator", "terminated", true)+`]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/cleanup":
			_, _ = io.WriteString(w, `{"ok":true,"cleaned":["demo-old"],"skipped":[{"sessionId":"demo-orch","reason":"workspace has uncommitted changes"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "session", "cleanup", "--project", "demo", "--yes")
	if err != nil {
		t.Fatalf("session cleanup failed: %v\nstderr=%s", err, errOut)
	}
	for _, want := range []string{
		"Cleaned: demo-old",
		"Skipped: demo-orch (workspace has uncommitted changes)",
		"Cleanup complete. 1 session cleaned, 1 skipped.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("cleanup output missing %q:\n%s", want, out)
		}
	}
}

func TestSessionCleanup_PromptFailsWithoutInput(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, log := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	out, _, err := executeCLI(t, Deps{
		In:           strings.NewReader(""),
		ProcessAlive: func(int) bool { return true },
	}, "session", "cleanup", "--project", "demo")
	if err == nil {
		t.Fatal("expected cleanup prompt without input to fail")
	}
	if got := ExitCode(err); got != 1 {
		t.Fatalf("exit code = %d, want 1", got)
	}
	if !strings.Contains(out, "Type yes to confirm") {
		t.Fatalf("output missing confirmation prompt:\n%s", out)
	}
	want := []string{"GET /api/v1/sessions?active=false&project=demo"}
	if got := log.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %#v, want %#v", got, want)
	}
}

func TestSessionRename_SuccessWithProjectScope(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, log := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "session", "rename", "demo-1", "New Name", "-p", "demo")
	if err != nil {
		t.Fatalf("session rename failed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, `session demo-1 renamed to "New Name"`) {
		t.Fatalf("unexpected rename output:\n%s", out)
	}
	want := []string{"GET /api/v1/sessions/demo-1", "PATCH /api/v1/sessions/demo-1"}
	if got := log.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %#v, want %#v", got, want)
	}
}

func TestSessionCommands_MissingIDIsUsageError(t *testing.T) {
	setConfigEnv(t)
	for _, sub := range []string{"get", "kill", "restore"} {
		t.Run(sub, func(t *testing.T) {
			_, _, err := executeCLI(t, Deps{}, "session", sub)
			if err == nil {
				t.Fatal("expected missing id to fail")
			}
			if got := ExitCode(err); got != 2 {
				t.Fatalf("exit code = %d, want 2 (err=%v)", got, err)
			}
		})
	}
}

func TestSessionRename_MissingNameIsUsageError(t *testing.T) {
	setConfigEnv(t)

	_, _, err := executeCLI(t, Deps{}, "session", "rename", "demo-1")
	if err == nil {
		t.Fatal("expected missing name to fail")
	}
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2 (err=%v)", got, err)
	}
}

func TestSessionGet_ProjectMismatchDoesNotPassScope(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "session", "get", "demo-1", "--project", "other")
	if err == nil {
		t.Fatal("expected project mismatch to fail")
	}
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
	if !strings.Contains(err.Error(), "not in project other") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSessionRename_ProjectMismatchDoesNotPatch(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, log := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
	}, "session", "rename", "demo-1", "New Name", "--project", "other")
	if err == nil {
		t.Fatal("expected project mismatch to fail")
	}
	if got := ExitCode(err); got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
	if !strings.Contains(err.Error(), "not in project other") {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"GET /api/v1/sessions/demo-1"}
	if got := log.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %#v, want %#v", got, want)
	}
}

func TestSessionClaimPR_ProjectScopeMismatchIsUsage(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, log := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "session", "claim-pr", "demo-1", "https://github.com/aoagents/agent-orchestrator/pull/142", "-p", "other")
	if err == nil || ExitCode(err) != 2 || !strings.Contains(err.Error(), "session demo-1 is not in project other") {
		t.Fatalf("err=%v exit=%d, want project mismatch usage", err, ExitCode(err))
	}
	want := []string{"GET /api/v1/sessions/demo-1"}
	if got := log.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests=%#v want %#v", got, want)
	}
}

func TestSessionClaimPR_UsesCurrentSessionFromEnv(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "demo-1")
	cfg := setConfigEnv(t)
	srv, log := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "session", "claim-pr", "142")
	if err != nil {
		t.Fatalf("claim-pr with AO_SESSION_ID failed: %v stderr=%s", err, errOut)
	}
	if !strings.Contains(out, "claimed PR #142") {
		t.Fatalf("unexpected output: %s", out)
	}
	want := []string{
		"GET /api/v1/sessions/demo-1",
		"GET /api/v1/projects/demo",
		"POST /api/v1/sessions/demo-1/pr/claim",
	}
	if got := log.all(); !reflect.DeepEqual(got, want) {
		t.Fatalf("requests=%#v want %#v", got, want)
	}
}

func TestSessionClaimPR_OneArgRequiresCurrentSessionEnv(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	setConfigEnv(t)

	_, _, err := executeCLI(t, Deps{}, "session", "claim-pr", "142")
	if err == nil || ExitCode(err) != 2 {
		t.Fatalf("err=%v exit=%d, want usage error", err, ExitCode(err))
	}
	if !strings.Contains(err.Error(), "pass <session-id> or set AO_SESSION_ID") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSessionClaimPR_HelpDocumentsCurrentSessionShorthand(t *testing.T) {
	out, _, err := executeCLI(t, Deps{}, "session", "claim-pr", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"claim-pr [<session-id>] <pr-ref>",
		"current session is read from AO_SESSION_ID",
		"ao session claim-pr 88",
		"ao session claim-pr mer-3 88",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("help missing %q:\n%s", want, out)
		}
	}
}

func TestSessionClaimPR_JSONAndNoTakeoverError(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := sessionCommandServer(t)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "session", "claim-pr", "demo-1", "https://github.com/aoagents/agent-orchestrator/pull/142", "--json")
	if err != nil {
		t.Fatalf("claim-pr --json failed: %v stderr=%s", err, errOut)
	}
	var got claimPRResponse
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.SessionID != "demo-1" || len(got.PRs) != 1 || got.PRs[0].Number != 142 {
		t.Fatalf("bad json err=%v got=%#v out=%s", err, got, out)
	}

	_, _, err = executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "session", "claim-pr", "demo-1", "https://github.com/aoagents/agent-orchestrator/pull/142", "--no-takeover")
	if err == nil || ExitCode(err) != 1 || !strings.Contains(err.Error(), "PR_CLAIMED_BY_ACTIVE_SESSION") {
		t.Fatalf("err=%v exit=%d, want takeover refusal runtime error", err, ExitCode(err))
	}
}

func TestSessionClaimPR_GitLabMR(t *testing.T) {
	cfg := setConfigEnv(t)
	var capturedReq claimPRRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/demo-1":
			_, _ = io.WriteString(w, `{"session":`+sessionJSON("demo-1", "demo", "worker", "working", false)+`}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo","repo":"https://gitlab.com/castai/ctxd","defaultBranch":"main"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-1/pr/claim":
			_ = json.NewDecoder(r.Body).Decode(&capturedReq)
			_, _ = io.WriteString(w, `{"ok":true,"sessionId":"demo-1","prs":[{"url":"`+capturedReq.PR+`","number":9,"state":"open","ci":"passing","review":"review_required","mergeability":"mergeable","reviewComments":false,"updatedAt":"2026-06-04T12:00:00Z"}],"branchChanged":false,"takenOverFrom":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "session", "claim-pr", "demo-1", "https://gitlab.com/castai/ctxd/-/merge_requests/9")
	if err != nil {
		t.Fatalf("claim-pr gitlab failed: %v", err)
	}
	if capturedReq.PR != "https://gitlab.com/castai/ctxd/-/merge_requests/9" {
		t.Fatalf("claim request PR = %q, want gitlab MR URL", capturedReq.PR)
	}
}

func TestSessionClaimPR_GitLabNumericRef(t *testing.T) {
	cfg := setConfigEnv(t)
	var capturedReq claimPRRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/demo-1":
			_, _ = io.WriteString(w, `{"session":`+sessionJSON("demo-1", "demo", "worker", "working", false)+`}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo","repo":"https://gitlab.com/castai/ctxd","defaultBranch":"main"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-1/pr/claim":
			_ = json.NewDecoder(r.Body).Decode(&capturedReq)
			_, _ = io.WriteString(w, `{"ok":true,"sessionId":"demo-1","prs":[{"url":"`+capturedReq.PR+`","number":9,"state":"open","ci":"passing","review":"review_required","mergeability":"mergeable","reviewComments":false,"updatedAt":"2026-06-04T12:00:00Z"}],"branchChanged":false,"takenOverFrom":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "session", "claim-pr", "demo-1", "9")
	if err != nil {
		t.Fatalf("claim-pr gitlab numeric failed: %v", err)
	}
	if capturedReq.PR != "https://gitlab.com/castai/ctxd/-/merge_requests/9" {
		t.Fatalf("claim request PR = %q, want expanded gitlab MR URL", capturedReq.PR)
	}
}

func TestSessionClaimPR_GHFallbackWhenProjectRepoMissing(t *testing.T) {
	cfg := setConfigEnv(t)
	log := &sessionRequestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.append(r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/demo-1":
			_, _ = io.WriteString(w, `{"session":`+sessionJSON("demo-1", "demo", "worker", "working", false)+`}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo","repo":"","defaultBranch":"main"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-1/pr/claim":
			var req claimPRRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			_, _ = io.WriteString(w, `{"ok":true,"sessionId":"demo-1","prs":[{"url":`+jsonQuote(req.PR)+`,"number":142,"state":"open","ci":"passing","review":"review_required","mergeability":"mergeable","reviewComments":false,"updatedAt":"2026-06-04T12:00:00Z"}],"branchChanged":false,"takenOverFrom":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	var ghDir string
	out, _, err := executeCLI(t, Deps{
		ProcessAlive: func(int) bool { return true },
		CommandOutputInDir: func(_ context.Context, dir, name string, args ...string) ([]byte, error) {
			ghDir = dir
			if name != "gh" {
				t.Fatalf("command name=%s", name)
			}
			return []byte("https://github.com/aoagents/agent-orchestrator\n"), nil
		},
	}, "session", "claim-pr", "demo-1", "142")
	if err != nil {
		t.Fatalf("claim-pr fallback failed: %v", err)
	}
	if ghDir != "/repo/demo" || !strings.Contains(out, "claimed PR #142") {
		t.Fatalf("ghDir=%q out=%s", ghDir, out)
	}
}
