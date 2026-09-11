package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const hookPRURL = "https://github.com/example/frontend/pull/123"

func createdPRHookPayload(command, stdout string) string {
	payload, _ := json.Marshal(map[string]any{
		"tool_name":     "Bash",
		"tool_input":    map[string]any{"command": command},
		"tool_response": map[string]any{"stdout": stdout, "stderr": "", "interrupted": false},
	})
	return string(payload)
}

func TestHookCreatedPR(t *testing.T) {
	command := "gh pr create --repo example/frontend \\\n  --base dev \\\n  --head ao/workspace-4-dependabot-stage1 \\\n  --title \"chore(deps): Stage 1 safe Dependabot bumps\" \\\n  --body-file /tmp/pr_body.md"
	good := createdPRHookPayload(command, hookPRURL+"\n")
	for _, tt := range []struct {
		name, agent, event, payload string
		want                        bool
	}{
		{"multiline create on renamed branch", "claude-code", "post-tool-use", good, true},
		{"change directory", "claude-code", "post-tool-use", createdPRHookPayload("cd frontend && gh pr create --fill", hookPRURL), true},
		{"failure event", "claude-code", "post-tool-use-failure", good, false},
		{"before execution", "claude-code", "pre-tool-use", good, false},
		{"other harness", "codex", "post-tool-use", good, false},
		{"wrong tool", "claude-code", "post-tool-use", strings.Replace(good, `"Bash"`, `"Read"`, 1), false},
		{"malformed", "claude-code", "post-tool-use", `{`, false},
		{"interrupted", "claude-code", "post-tool-use", strings.Replace(good, `"interrupted":false`, `"interrupted":true`, 1), false},
		{"nonzero camel exit", "claude-code", "post-tool-use", strings.Replace(good, `"interrupted":false`, `"interrupted":false,"exitCode":1`, 1), false},
		{"nonzero snake exit", "claude-code", "post-tool-use", strings.Replace(good, `"interrupted":false`, `"interrupted":false,"exit_code":1`, 1), false},
		{"zero exit", "claude-code", "post-tool-use", strings.Replace(good, `"interrupted":false`, `"interrupted":false,"exitCode":0`, 1), true},
		{"background tool", "claude-code", "post-tool-use", strings.Replace(good, `"command":`, `"run_in_background":true,"command":`, 1), false},
		{"stderr only", "claude-code", "post-tool-use", strings.Replace(createdPRHookPayload("gh pr create --fill", ""), `"stderr":""`, `"stderr":"`+hookPRURL+`"`, 1), false},
		{"view existing", "claude-code", "post-tool-use", createdPRHookPayload("gh pr view --json url --jq .url", hookPRURL), false},
		{"printed command", "claude-code", "post-tool-use", createdPRHookPayload("echo 'gh pr create'", hookPRURL), false},
		{"masked failure", "claude-code", "post-tool-use", createdPRHookPayload("gh pr create || echo "+hookPRURL, hookPRURL), false},
		{"later output", "claude-code", "post-tool-use", createdPRHookPayload("gh pr create --help; echo "+hookPRURL, hookPRURL), false},
		{"pipeline", "claude-code", "post-tool-use", createdPRHookPayload("gh pr create | cat", hookPRURL), false},
		{"background shell", "claude-code", "post-tool-use", createdPRHookPayload("gh pr create &", hookPRURL), false},
		{"negated", "claude-code", "post-tool-use", createdPRHookPayload("! gh pr create", hookPRURL), false},
		{"unexecuted branch", "claude-code", "post-tool-use", createdPRHookPayload("if false; then gh pr create; fi", hookPRURL), false},
		{"heredoc", "claude-code", "post-tool-use", createdPRHookPayload("cat <<'EOF'\ngh pr create\nEOF", hookPRURL), false},
		{"shell parse error", "claude-code", "post-tool-use", createdPRHookPayload("gh pr create '", hookPRURL), false},
		{"redirect", "claude-code", "post-tool-use", createdPRHookPayload("gh pr create > /tmp/pr", hookPRURL), false},
		{"oversize command", "claude-code", "post-tool-use", createdPRHookPayload("gh pr create --title "+strings.Repeat("a", maxHookInteractionLen), hookPRURL), false},
		{"multiple URLs", "claude-code", "post-tool-use", createdPRHookPayload(command, hookPRURL+"\n"+hookPRURL), false},
		{"prose", "claude-code", "post-tool-use", createdPRHookPayload(command, "See "+hookPRURL), false},
		{"lookalike host", "claude-code", "post-tool-use", createdPRHookPayload(command, "https://github.com.evil.test/o/r/pull/1"), false},
		{"credentials", "claude-code", "post-tool-use", createdPRHookPayload(command, "https://github.com@evil.test/o/r/pull/1"), false},
		{"fragment", "claude-code", "post-tool-use", createdPRHookPayload(command, hookPRURL+"#comment"), false},
		{"zero PR", "claude-code", "post-tool-use", createdPRHookPayload(command, "https://github.com/o/r/pull/0"), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := hookCreatedPR(tt.agent, tt.event, []byte(tt.payload))
			want := ""
			if tt.want {
				want = hookPRURL
			}
			if got != want {
				t.Fatalf("created PR = %q, want %q", got, want)
			}
		})
	}
}

func TestHooks_RegisterCreatedPR(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusConflict, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", "workspace-4")
			t.Setenv("AO_REVIEW_SESSION_ID", "")
			cfg := setConfigEnv(t)
			dataDir := t.TempDir()
			t.Setenv("AO_DATA_DIR", dataDir)
			claims, activity := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/sessions/workspace-4/pr/claim":
					claims++
					var req claimPRRequest
					if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&req) != nil || req.PR != hookPRURL || req.AllowTakeover {
						t.Errorf("unexpected claim: method=%s request=%+v", r.Method, req)
					}
					w.WriteHeader(status)
					if status != http.StatusOK {
						_, _ = io.WriteString(w, `{"code":"PR_CLAIM_FAILED","message":"cannot claim","requestId":"hook-request"}`)
					}
				case "/api/v1/sessions/workspace-4/activity":
					activity++
					_, _ = io.WriteString(w, `{}`)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			writeRunFileFor(t, cfg, srv)
			out, errOut, err := executeCLI(t, Deps{In: strings.NewReader(createdPRHookPayload("gh pr create --fill", hookPRURL)), ProcessAlive: func(int) bool { return true }}, "hooks", "claude-code", "post-tool-use")
			if err != nil || out != "" || claims != 1 || activity != 1 {
				t.Fatalf("err=%v stdout=%q claims=%d activity=%d stderr=%s", err, out, claims, activity, errOut)
			}
			if status == http.StatusOK {
				if errOut != "" {
					t.Fatalf("stderr=%s", errOut)
				}
			} else {
				log, err := os.ReadFile(filepath.Join(dataDir, hooksLogName))
				if err != nil || !strings.Contains(string(log), "register created PR") || !strings.Contains(errOut, "hook-request") {
					t.Fatalf("missing diagnostic: log=%s err=%v stderr=%s", log, err, errOut)
				}
			}
		})
	}
}

func TestHooks_ReviewerDoesNotRegisterPR(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "worker-1")
	t.Setenv("AO_REVIEW_SESSION_ID", "review-1")
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/reviews/review-1/activity" {
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	writeRunFileFor(t, cfg, srv)
	_, _, err := executeCLI(t, Deps{In: strings.NewReader(createdPRHookPayload("gh pr create --fill", hookPRURL)), ProcessAlive: func(int) bool { return true }}, "hooks", "claude-code", "post-tool-use")
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegisterHookPRBoundsRequest(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "worker-1")
	t.Setenv("AO_REVIEW_SESSION_ID", "")
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected real request") }))
	defer srv.Close()
	writeRunFileFor(t, cfg, srv)
	// Inspect the transport deadline without sleeping through the timeout.
	claims := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/pr/claim") {
			return nil, context.DeadlineExceeded
		}
		claims++
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Errorf("unbounded hook request: %v", deadline)
		}
		return nil, context.DeadlineExceeded
	})}
	_, _, err := executeCLI(t, Deps{HTTPClient: client, In: strings.NewReader(createdPRHookPayload("gh pr create --fill", hookPRURL)), ProcessAlive: func(int) bool { return true }}, "hooks", "claude-code", "post-tool-use")
	if err != nil || claims != 1 {
		t.Fatalf("err=%v claims=%d", err, claims)
	}
}
