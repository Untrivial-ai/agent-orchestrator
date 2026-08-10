package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func authorizedAgentsJSON(agent string) string {
	info := `{"id":` + jsonQuote(agent) + `,"label":` + jsonQuote(agent) + `,"authStatus":"authorized"}`
	return `{"supported":[` + info + `],"installed":[` + info + `],"authorized":[` + info + `]}`
}

func TestSpawnHelpListsPrimeAgentHarness(t *testing.T) {
	out, errOut, err := executeCLI(t, Deps{}, "spawn", "--help")
	if err != nil {
		t.Fatalf("spawn --help: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, "prime-agent") {
		t.Fatalf("spawn help does not list prime-agent:\n%s", out)
	}
}

// TestSpawnCommand_MissingProjectContext asserts `ao spawn` gives a project
// setup hint when neither --project, AO_PROJECT_ID, nor cwd can resolve one.
func TestSpawnCommand_MissingProjectContext(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects" {
			_, _ = io.WriteString(w, `{"projects":[]}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--agent", "codex", "--name", "worker")
	if err == nil {
		t.Fatal("expected an error when project context is missing")
	}
	if !strings.Contains(err.Error(), "ao project add --path <repo-path> --worker-agent <agent>") {
		t.Fatalf("error = %v, want project add hint", err)
	}
	if want := []string{"GET /api/v1/projects"}; !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

// TestProjectAddCommand_RequiresPath asserts `ao project add` rejects a missing
// --path before touching the network.
func TestProjectAddCommand_RequiresPath(t *testing.T) {
	var out, errb bytes.Buffer
	root := NewRootCommand(Deps{Out: &out, Err: &errb})
	root.SetArgs([]string{"project", "add"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected an error when --path is missing")
	}
	if !strings.Contains(err.Error(), "--path is required") {
		t.Fatalf("error = %v, want it to mention --path is required", err)
	}
}

func TestSpawnClaimPRWiring(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo","repo":"https://github.com/aoagents/agent-orchestrator","defaultBranch":"main"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh":
			_, _ = io.WriteString(w, authorizedAgentsJSON("codex"))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			_, _ = io.WriteString(w, `{"session":{"id":"demo-9","status":"idle"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-9/pr/claim":
			var req claimPRRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.PR != "https://github.com/aoagents/agent-orchestrator/pull/142" || req.AllowTakeover {
				t.Fatalf("claim request = %#v", req)
			}
			_, _ = io.WriteString(w, `{"ok":true,"sessionId":"demo-9","prs":[{"url":"https://github.com/aoagents/agent-orchestrator/pull/142","number":142,"state":"open","ci":"passing","review":"review_required","mergeability":"mergeable","reviewComments":false,"updatedAt":"2026-06-04T12:00:00Z"}],"branchChanged":false,"takenOverFrom":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--project", "demo", "--agent", "codex", "--name", "worker", "--claim-pr", "142", "--no-takeover")
	if err != nil {
		t.Fatalf("spawn claim-pr failed: %v stderr=%s", err, errOut)
	}
	if !strings.Contains(out, "claimed https://github.com/aoagents/agent-orchestrator/pull/142") {
		t.Fatalf("output missing claimed label: %s", out)
	}
	want := []string{"GET /api/v1/projects/demo", "POST /api/v1/agents/refresh", "POST /api/v1/sessions", "POST /api/v1/sessions/demo-9/pr/claim"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestSpawnClaimPRFailureRollsBackSession(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	sessions := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo","repo":"https://github.com/aoagents/agent-orchestrator","defaultBranch":"main"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh":
			_, _ = io.WriteString(w, authorizedAgentsJSON("codex"))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			sessions["demo-10"] = true
			_, _ = io.WriteString(w, `{"session":{"id":"demo-10","status":"idle"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-10/pr/claim":
			if !sessions["demo-10"] {
				t.Fatal("claim called before session existed")
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"not_found","code":"PR_NOT_FOUND","message":"PR not found"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/demo-10/rollback":
			delete(sessions, "demo-10")
			_, _ = io.WriteString(w, `{"ok":true,"sessionId":"demo-10","deleted":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--project", "demo", "--agent", "codex", "--name", "worker", "--claim-pr", "142")
	if err == nil {
		t.Fatal("expected spawn claim failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "failed to claim PR 142") || !strings.Contains(msg, "rolled back session demo-10") {
		t.Fatalf("error = %v", err)
	}
	if sessions["demo-10"] {
		t.Fatalf("spawned session still present after claim rollback: %#v", sessions)
	}
	want := []string{"GET /api/v1/projects/demo", "POST /api/v1/agents/refresh", "POST /api/v1/sessions", "POST /api/v1/sessions/demo-10/pr/claim", "POST /api/v1/sessions/demo-10/rollback"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestSpawnNoTakeoverRequiresClaimPR(t *testing.T) {
	_, _, err := executeCLI(t, Deps{}, "spawn", "--project", "demo", "--name", "worker", "--no-takeover")
	if err == nil || ExitCode(err) != 2 || !strings.Contains(err.Error(), "--no-takeover requires --claim-pr") {
		t.Fatalf("err=%v exit=%d", err, ExitCode(err))
	}
}

// TestSpawnCommand_RequiresName asserts `ao spawn` rejects a missing --name
// without contacting the daemon.
func TestSpawnCommand_RequiresName(t *testing.T) {
	_, _, err := executeCLI(t, Deps{}, "spawn", "--project", "demo", "--agent", "codex")
	if err == nil || ExitCode(err) != 2 || !strings.Contains(err.Error(), "--name is required") {
		t.Fatalf("err=%v exit=%d, want --name is required", err, ExitCode(err))
	}
}

// TestSpawnCommand_RejectsOverlongName asserts `ao spawn` rejects a --name
// longer than 20 characters without contacting the daemon.
func TestSpawnCommand_RejectsOverlongName(t *testing.T) {
	_, _, err := executeCLI(t, Deps{}, "spawn", "--project", "demo", "--name", strings.Repeat("x", 21))
	if err == nil || ExitCode(err) != 2 || !strings.Contains(err.Error(), "20 characters or fewer") {
		t.Fatalf("err=%v exit=%d, want 20 characters or fewer", err, ExitCode(err))
	}
}

func TestSpawnResolvesProjectFromEnvAndDefaultAgent(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	var req spawnRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo","config":{"worker":{"agent":"codex"}}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh":
			_, _ = io.WriteString(w, authorizedAgentsJSON("codex"))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"session":{"id":"demo-11","status":"idle"},"promptBytes":0,"systemPromptBytes":123}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	t.Setenv("AO_PROJECT_ID", "demo")

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--prompt", "Fix failing tests in auth", "--name", "worker")
	if err != nil {
		t.Fatalf("spawn failed: %v stderr=%s", err, errOut)
	}
	if !strings.Contains(out, "spawned session demo-11") {
		t.Fatalf("output missing spawn: %s", out)
	}
	if !strings.Contains(out, "[prompt 0 B, system 123 B]") {
		t.Fatalf("output missing system-only prompt metrics: %s", out)
	}
	if req.ProjectID != "demo" || req.Harness != "codex" || req.DisplayName != "worker" {
		t.Fatalf("spawn request = %#v", req)
	}
	want := []string{"GET /api/v1/projects/demo", "POST /api/v1/agents/refresh", "POST /api/v1/sessions"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestSpawnResolvesProjectFromAOSessionID(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	var req spawnRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/demo-1":
			_, _ = io.WriteString(w, `{"session":`+sessionJSON("demo-1", "demo", "worker", "idle", false)+`}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo","config":{"worker":{"agent":"codex"}}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh":
			_, _ = io.WriteString(w, authorizedAgentsJSON("codex"))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"session":{"id":"demo-15","status":"idle"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	t.Setenv("AO_SESSION_ID", "demo-1")

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--prompt", "Fix tests", "--name", "worker")
	if err != nil {
		t.Fatalf("spawn failed: %v stderr=%s", err, errOut)
	}
	if req.ProjectID != "demo" || req.Harness != "codex" {
		t.Fatalf("spawn request = %#v", req)
	}
	want := []string{"GET /api/v1/sessions/demo-1", "GET /api/v1/projects/demo", "POST /api/v1/agents/refresh", "POST /api/v1/sessions"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestSpawnAOSessionIDFailureRequiresProject(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"not_found","code":"SESSION_NOT_FOUND","message":"Session not found"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	t.Setenv("AO_SESSION_ID", "missing")

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--agent", "codex", "--name", "worker")
	if err == nil || !strings.Contains(err.Error(), `project could not be resolved from AO_SESSION_ID "missing"; pass --project`) {
		t.Fatalf("err=%v, want AO_SESSION_ID project error", err)
	}
	want := []string{"GET /api/v1/sessions/missing"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestSpawnResolvesProjectFromCWD(t *testing.T) {
	cfg := setConfigEnv(t)
	repo := filepath.Join(t.TempDir(), "repo")
	subdir := filepath.Join(repo, "pkg")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(subdir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	var req spawnRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects":
			_, _ = io.WriteString(w, `{"projects":[{"id":"demo","name":"Demo"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":`+jsonQuote(repo)+`,"config":{"worker":{"agent":"codex"}}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh":
			_, _ = io.WriteString(w, authorizedAgentsJSON("codex"))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"session":{"id":"demo-12","status":"idle"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--prompt", "Fix tests", "--name", "worker")
	if err != nil {
		t.Fatalf("spawn failed: %v stderr=%s", err, errOut)
	}
	if req.ProjectID != "demo" || req.Harness != "codex" {
		t.Fatalf("spawn request = %#v", req)
	}
}

func TestSpawnDefaultsToScratchWhenOnlyActiveProject(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	var req spawnRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects":
			_, _ = io.WriteString(w, `{"projects":[{"id":"scratch","name":"Scratch","kind":"scratch"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/scratch":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"scratch","name":"Scratch","kind":"scratch","path":"/ao/scratch","config":{"worker":{"agent":"codex"}}}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh":
			_, _ = io.WriteString(w, authorizedAgentsJSON("codex"))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"session":{"id":"scratch-1","status":"idle"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--name", "Try AO", "--prompt", "Try AO")
	if err != nil {
		t.Fatalf("spawn failed: %v stderr=%s", err, errOut)
	}
	if !strings.Contains(out, "spawned session scratch-1") {
		t.Fatalf("output missing scratch session: %s", out)
	}
	if req.ProjectID != "scratch" || req.Harness != "codex" || req.Branch != "" {
		t.Fatalf("spawn request = %#v", req)
	}
	want := []string{"GET /api/v1/projects", "GET /api/v1/projects/scratch", "POST /api/v1/agents/refresh", "POST /api/v1/sessions"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestSpawnScratchRejectsGitOnlyFlags(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "branch", args: []string{"spawn", "--project", "scratch", "--agent", "codex", "--name", "Scratch Task", "--branch", "feature/x"}, wantErr: "scratch projects do not support --branch"},
		{name: "claim pr", args: []string{"spawn", "--project", "scratch", "--agent", "codex", "--name", "Scratch Task", "--claim-pr", "142"}, wantErr: "scratch projects do not support --claim-pr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := setConfigEnv(t)
			var requests []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				appendPrimaryRequest(&requests, r)
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/scratch":
					_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"scratch","name":"Scratch","kind":"scratch","path":"/ao/scratch","config":{"worker":{"agent":"codex"}}}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			writeRunFileFor(t, cfg, srv)

			_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, tc.args...)
			if err == nil || ExitCode(err) != 2 || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err=%v exit=%d, want %q", err, ExitCode(err), tc.wantErr)
			}
			want := []string{"GET /api/v1/projects/scratch"}
			if !reflect.DeepEqual(requests, want) {
				t.Fatalf("requests=%#v want %#v", requests, want)
			}
		})
	}
}

func TestSpawnStaleUnauthorizedAgentRefreshesProbesThenAllows(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	var req spawnRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh":
			_, _ = io.WriteString(w, `{"supported":[{"id":"codex","label":"Codex"}],"installed":[{"id":"codex","label":"Codex","authStatus":"unauthorized"}],"authorized":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/codex/probe":
			_, _ = io.WriteString(w, `{"agent":{"id":"codex","label":"Codex","authStatus":"authorized"},"supported":true,"installed":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"session":{"id":"demo-12","status":"idle"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--project", "demo", "--agent", "codex", "--name", "worker")
	if err != nil {
		t.Fatalf("spawn failed: %v stderr=%s", err, errOut)
	}
	if errOut != "" {
		t.Fatalf("stderr = %q, want no warning after fresh authorized probe", errOut)
	}
	if req.ProjectID != "demo" || req.Harness != "codex" {
		t.Fatalf("spawn request = %#v", req)
	}
	want := []string{"GET /api/v1/projects/demo", "POST /api/v1/agents/refresh", "POST /api/v1/agents/codex/probe", "POST /api/v1/sessions"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestSpawnFreshUnauthorizedWarnsAndAllows(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	var req spawnRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh":
			_, _ = io.WriteString(w, `{"supported":[{"id":"codex","label":"Codex"}],"installed":[{"id":"codex","label":"Codex","authStatus":"unauthorized"}],"authorized":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/codex/probe":
			_, _ = io.WriteString(w, `{"agent":{"id":"codex","label":"Codex","authStatus":"unauthorized"},"supported":true,"installed":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"session":{"id":"demo-12","status":"idle"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--project", "demo", "--agent", "codex", "--name", "worker")
	if err != nil {
		t.Fatalf("spawn failed: %v stderr=%s", err, errOut)
	}
	if !strings.Contains(errOut, "may need auth according to a fresh local probe") {
		t.Fatalf("stderr missing warning: %s", errOut)
	}
	if req.ProjectID != "demo" || req.Harness != "codex" {
		t.Fatalf("spawn request = %#v", req)
	}
	want := []string{"GET /api/v1/projects/demo", "POST /api/v1/agents/refresh", "POST /api/v1/agents/codex/probe", "POST /api/v1/sessions"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestSpawnUnavailableFreshProbeWarnsAndAllows(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	var req spawnRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh":
			_, _ = io.WriteString(w, `{"supported":[{"id":"codex","label":"Codex"}],"installed":[{"id":"codex","label":"Codex","authStatus":"unauthorized"}],"authorized":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/codex/probe":
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = io.WriteString(w, `{"message":"not implemented","code":"NOT_IMPLEMENTED"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"session":{"id":"demo-12","status":"idle"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--project", "demo", "--agent", "codex", "--name", "worker")
	if err != nil {
		t.Fatalf("spawn failed: %v stderr=%s", err, errOut)
	}
	if !strings.Contains(errOut, "fresh readiness probe is unavailable") {
		t.Fatalf("stderr missing warning: %s", errOut)
	}
	if req.ProjectID != "demo" || req.Harness != "codex" {
		t.Fatalf("spawn request = %#v", req)
	}
	want := []string{"GET /api/v1/projects/demo", "POST /api/v1/agents/refresh", "POST /api/v1/agents/codex/probe", "POST /api/v1/sessions"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestSpawnUnsupportedAgentRefreshesThenBlocks(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh":
			_, _ = io.WriteString(w, `{"supported":[{"id":"codex","label":"Codex"}],"installed":[],"authorized":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--project", "demo", "--agent", "unknown", "--name", "worker")
	if err == nil || !strings.Contains(err.Error(), "agent \"unknown\" is not supported") {
		t.Fatalf("err=%v, want unsupported", err)
	}
	want := []string{"GET /api/v1/projects/demo", "POST /api/v1/agents/refresh"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestSpawnNotInstalledAgentRefreshesThenBlocks(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh":
			_, _ = io.WriteString(w, `{"supported":[{"id":"codex","label":"Codex"}],"installed":[],"authorized":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/codex/probe":
			_, _ = io.WriteString(w, `{"agent":{"id":"codex","label":"Codex"},"supported":true,"installed":false}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--project", "demo", "--agent", "codex", "--name", "worker")
	if err == nil || !strings.Contains(err.Error(), "agent \"codex\" needs install") {
		t.Fatalf("err=%v, want needs install", err)
	}
	want := []string{"GET /api/v1/projects/demo", "POST /api/v1/agents/refresh", "POST /api/v1/agents/codex/probe"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestSpawnStaleNotInstalledFreshInstalledWarnsAndAllows(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	var req spawnRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh":
			_, _ = io.WriteString(w, `{"supported":[{"id":"codex","label":"Codex"}],"installed":[],"authorized":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/codex/probe":
			_, _ = io.WriteString(w, `{"agent":{"id":"codex","label":"Codex","authStatus":"unknown"},"supported":true,"installed":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"session":{"id":"demo-12","status":"idle"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--project", "demo", "--agent", "codex", "--name", "worker")
	if err != nil {
		t.Fatalf("spawn failed: %v stderr=%s", err, errOut)
	}
	if !strings.Contains(errOut, "auth status is unknown") {
		t.Fatalf("stderr missing warning: %s", errOut)
	}
	if req.ProjectID != "demo" || req.Harness != "codex" {
		t.Fatalf("spawn request = %#v", req)
	}
	want := []string{"GET /api/v1/projects/demo", "POST /api/v1/agents/refresh", "POST /api/v1/agents/codex/probe", "POST /api/v1/sessions"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestSpawnUnavailableFreshProbeForNotInstalledWarnsAndAllows(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	var req spawnRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh":
			_, _ = io.WriteString(w, `{"supported":[{"id":"codex","label":"Codex"}],"installed":[],"authorized":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/codex/probe":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"not found","code":"NOT_FOUND"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"session":{"id":"demo-12","status":"idle"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--project", "demo", "--agent", "codex", "--name", "worker")
	if err != nil {
		t.Fatalf("spawn failed: %v stderr=%s", err, errOut)
	}
	if !strings.Contains(errOut, "fresh readiness probe is unavailable") {
		t.Fatalf("stderr missing warning: %s", errOut)
	}
	if req.ProjectID != "demo" || req.Harness != "codex" {
		t.Fatalf("spawn request = %#v", req)
	}
	want := []string{"GET /api/v1/projects/demo", "POST /api/v1/agents/refresh", "POST /api/v1/agents/codex/probe", "POST /api/v1/sessions"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestSpawnFreshProbeServerErrorBlocks(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh":
			_, _ = io.WriteString(w, `{"supported":[{"id":"codex","label":"Codex"}],"installed":[],"authorized":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/codex/probe":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"message":"probe failed","code":"PROBE_FAILED","requestId":"req-1"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--project", "demo", "--agent", "codex", "--name", "worker")
	if err == nil || !strings.Contains(err.Error(), "probe failed (PROBE_FAILED) [request req-1]") {
		t.Fatalf("err=%v, want probe server error", err)
	}
	want := []string{"GET /api/v1/projects/demo", "POST /api/v1/agents/refresh", "POST /api/v1/agents/codex/probe"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestSpawnSkipAgentCheckBypassesOnlyPreflight(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	var req spawnRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"session":{"id":"demo-14","status":"idle"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--project", "demo", "--agent", "unsupported", "--skip-agent-check", "--name", "worker")
	if err != nil {
		t.Fatalf("spawn failed: %v stderr=%s", err, errOut)
	}
	if req.ProjectID != "demo" || req.Harness != "unsupported" {
		t.Fatalf("spawn request = %#v", req)
	}
	want := []string{"GET /api/v1/projects/demo", "POST /api/v1/sessions"}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestSpawnUnknownAuthRefreshesWarnsAndAllows(t *testing.T) {
	cfg := setConfigEnv(t)
	var req spawnRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","name":"Demo","path":"/repo/demo"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agents/refresh":
			_, _ = io.WriteString(w, `{"supported":[{"id":"codex","label":"Codex"}],"installed":[{"id":"codex","label":"Codex","authStatus":"unknown"}],"authorized":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions":
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			_, _ = io.WriteString(w, `{"session":{"id":"demo-13","status":"idle"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	_, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "spawn", "--project", "demo", "--agent", "codex", "--name", "worker")
	if err != nil {
		t.Fatalf("spawn failed: %v stderr=%s", err, errOut)
	}
	if !strings.Contains(errOut, "auth status is unknown") {
		t.Fatalf("stderr missing warning: %s", errOut)
	}
	if req.ProjectID != "demo" || req.Harness != "codex" {
		t.Fatalf("spawn request = %#v", req)
	}
}

// TestSpawnCommand_RejectsInvalidKind asserts `ao spawn` rejects a --kind value
// outside worker/orchestrator at the CLI boundary, without contacting the daemon.
func TestSpawnCommand_RejectsInvalidKind(t *testing.T) {
	// Pass a valid --name so this exercises the --kind boundary specifically:
	// spawn validates the required --name before --kind, so omitting it would
	// trip the "--name is required" error instead of the kind error.
	_, _, err := executeCLI(t, Deps{}, "spawn", "--project", "demo", "--name", "orch", "--kind", "orchestartor")
	if err == nil || ExitCode(err) != 2 || !strings.Contains(err.Error(), `--kind must be "worker" or "orchestrator"`) {
		t.Fatalf("err=%v exit=%d, want --kind validation error", err, ExitCode(err))
	}
}

func TestSpawnCommand_RejectsModelWithSpacesOrQuotes(t *testing.T) {
	// --model must be a single token so it cannot smuggle extra launch args
	// through the harness adapter. A quoted/spacey value is rejected before any
	// project resolution happens.
	_, _, err := executeCLI(t, Deps{}, "spawn", "--project", "demo", "--name", "w", "--model", "claude opus")
	if err == nil || ExitCode(err) != 2 || !strings.Contains(err.Error(), "--model must be a single model id") {
		t.Fatalf("err=%v exit=%d, want --model whitespace validation error", err, ExitCode(err))
	}
	_, _, err = executeCLI(t, Deps{}, "spawn", "--project", "demo", "--name", "w", "--model", `foo"bar`)
	if err == nil || ExitCode(err) != 2 || !strings.Contains(err.Error(), "--model must be a single model id") {
		t.Fatalf("err=%v exit=%d, want --model quote validation error", err, ExitCode(err))
	}
}

// TestResolveSpawnHarness_OrchestratorDefault asserts the orchestrator role falls
// back to the project's orchestrator agent (and worker to the worker agent), while
// an explicit --agent always wins.
func TestResolveSpawnHarness_OrchestratorDefault(t *testing.T) {
	project := projectDetails{
		ID: "demo",
		Config: &projectConfig{
			Worker:       roleOverride{Agent: "codex"},
			Orchestrator: roleOverride{Agent: "claude-code"},
		},
	}
	if got, err := resolveSpawnHarness("", "orchestrator", project); err != nil || got != "claude-code" {
		t.Fatalf("orchestrator default: got %q err %v, want claude-code", got, err)
	}
	if got, err := resolveSpawnHarness("", "worker", project); err != nil || got != "codex" {
		t.Fatalf("worker default: got %q err %v, want codex", got, err)
	}
	if got, err := resolveSpawnHarness("aider", "orchestrator", project); err != nil || got != "aider" {
		t.Fatalf("explicit agent: got %q err %v, want aider", got, err)
	}
	// Unset kind is the default `ao spawn` path and must resolve to worker.agent.
	if got, err := resolveSpawnHarness("", "", project); err != nil || got != "codex" {
		t.Fatalf("unset kind: got %q err %v, want codex", got, err)
	}
	// Orchestrator spawn with no orchestrator.agent configured surfaces the
	// --orchestrator-agent hint (the error branch this PR adds).
	noOrch := projectDetails{ID: "demo", Config: &projectConfig{Worker: roleOverride{Agent: "codex"}}}
	if _, err := resolveSpawnHarness("", "orchestrator", noOrch); err == nil || !strings.Contains(err.Error(), "--orchestrator-agent") {
		t.Fatalf("missing orchestrator agent: err=%v, want --orchestrator-agent hint", err)
	}
}
