package cli

import (
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

func testingStartArgs(t *testing.T) []string {
	t.Helper()
	t.Setenv("USER", "investigator")
	issueFile, promptFile := filepath.Join(t.TempDir(), "issue.txt"), filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(issueFile, []byte("Quoted issue text\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(promptFile, []byte("Investigate the issue\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return []string{"testing", "start", "--project", "demo", "--issue-file", issueFile, "--commit", "abc123", "--prompt-file", promptFile}
}

func TestTestingStartUsesDaemonContract(t *testing.T) {
	for _, tc := range []struct {
		name     string
		extra    []string
		harness  string
		model    string
		effort   string
		recipe   string
		timeout  int
		issueURL string
		json     bool
		noPrompt bool
	}{
		{name: "defaults", recipe: "local-ao", timeout: 1800},
		{name: "without handwritten prompt", recipe: "local-ao", timeout: 1800, noPrompt: true},
		{name: "explicit", extra: []string{"--agent", "claude-code", "--model", "claude-opus-5-5", "--effort", "medium", "--recipe", "configured-recipe", "--timeout", "90", "--issue-url", "https://github.com/org/repo/issues/1", "--json"}, harness: "claude-code", model: "claude-opus-5-5", effort: "medium", recipe: "configured-recipe", timeout: 90, issueURL: "https://github.com/org/repo/issues/1", json: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := setConfigEnv(t)
			args := testingStartArgs(t)
			if tc.noPrompt {
				args = args[:len(args)-2]
			}
			args = append(args, tc.extra...)
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/internal/telemetry/cli-invoked" {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				if r.Method != http.MethodPost || r.Header.Get(testingCapabilityHeader) != "" {
					t.Error("management call must be POST without capability")
				}
				calls = append(calls, r.URL.Path)
				switch r.URL.Path {
				case "/api/v1/testing/runs":
					var input map[string]any
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Error(err)
					}
					want := map[string]any{"projectId": "demo", "issueUrl": tc.issueURL, "issueSnapshot": "Quoted issue text\n", "commitSha": "abc123", "recipeId": tc.recipe, "requester": "investigator"}
					if !reflect.DeepEqual(input, want) {
						t.Errorf("run body=%+v want=%+v", input, want)
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, `{"runId":"run-1","createdAt":"2026-10-06T00:00:00Z"}`)
				case "/api/v1/testing/runs/run-1/attempts":
					var input map[string]any
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Error(err)
					}
					prompt := "Investigate the issue\n"
					if tc.noPrompt {
						prompt = "Investigate the supplied issue or pull request."
					}
					want := map[string]any{"workerPrompt": prompt, "timeoutSeconds": float64(tc.timeout)}
					if tc.harness != "" {
						want["harness"] = tc.harness
					}
					if tc.model != "" {
						want["model"] = tc.model
					}
					if tc.effort != "" {
						want["effort"] = tc.effort
					}
					if !reflect.DeepEqual(input, want) {
						t.Errorf("attempt body=%+v want=%+v", input, want)
					}
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, `{"runId":"run-1","attemptId":"attempt-1","workerSessionId":"worker-1"}`)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			writeRunFileFor(t, cfg, server)
			out, stderr, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, args...)
			if err != nil || stderr != "" {
				t.Fatalf("start: out=%q stderr=%q err=%v", out, stderr, err)
			}
			if !reflect.DeepEqual(calls, []string{"/api/v1/testing/runs", "/api/v1/testing/runs/run-1/attempts"}) {
				t.Fatalf("management calls=%v", calls)
			}
			if tc.json {
				var result map[string]string
				if err := json.Unmarshal([]byte(out), &result); err != nil || !reflect.DeepEqual(result, map[string]string{"runId": "run-1", "attemptId": "attempt-1", "workerSessionId": "worker-1"}) {
					t.Fatalf("start JSON=%q", out)
				}
			} else if out != "run ID: run-1\nattempt ID: attempt-1\nworker session ID: worker-1\n" {
				t.Fatalf("start output=%q", out)
			}
		})
	}
}

func TestTestingManagementReadsAndCancels(t *testing.T) {
	for _, tc := range []struct {
		args               []string
		method, path, body string
		want               string
	}{
		{[]string{"testing", "stop", "attempt-1"}, http.MethodPost, "/api/v1/testing/attempts/attempt-1/cancel", `{"attemptId":"attempt-1","phase":"finished","outcome":"cancelled","cleanupState":"failed","recordingGap":"video unavailable"}`, "attempt-1 phase=finished outcome=cancelled cleanup=failed"},
		{[]string{"testing", "stop", "attempt-1", "--json"}, http.MethodPost, "/api/v1/testing/attempts/attempt-1/cancel", `{"attemptId":"attempt-1","phase":"finished","outcome":"cancelled","cleanupState":"complete","recordingGap":"video unavailable"}`, `"recordingGap": "video unavailable"`},
		{[]string{"testing", "evidence", "attempt-1"}, http.MethodGet, "/api/v1/testing/attempts/attempt-1/evidence", `{"evidence":[{"id":"receipt-1","attemptId":"attempt-1","kind":"report","relativePath":"report.md","mimeType":"text/markdown","sizeBytes":42,"sha256":"abc","createdAt":"2026-10-06T00:00:00Z"}]}`, `"relativePath": "report.md"`},
		{[]string{"testing", "evidence", "attempt-1"}, http.MethodGet, "/api/v1/testing/attempts/attempt-1/evidence", `{"evidence":[]}`, `"evidence": []`},
	} {
		t.Run(strings.Join(tc.args, " ")+tc.want, func(t *testing.T) {
			cfg := setConfigEnv(t)
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/internal/telemetry/cli-invoked" {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				calls++
				if r.Method != tc.method || r.URL.Path != tc.path || r.Header.Get(testingCapabilityHeader) != "" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)
			writeRunFileFor(t, cfg, server)
			out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, tc.args...)
			if err != nil || calls != 1 || !strings.Contains(out, tc.want) {
				t.Fatalf("calls=%d out=%q err=%v", calls, out, err)
			}
		})
	}
}

func TestTestingManagementPreservesDaemonErrors(t *testing.T) {
	for _, command := range []string{"start run", "start attempt", "stop", "evidence"} {
		t.Run(command, func(t *testing.T) {
			cfg := setConfigEnv(t)
			args := []string{"testing", command, "attempt-1"}
			if strings.HasPrefix(command, "start") {
				args = testingStartArgs(t)
			}
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/internal/telemetry/cli-invoked" {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				calls++
				if command == "start attempt" && r.URL.Path == "/api/v1/testing/runs" {
					_, _ = io.WriteString(w, `{"runId":"run-1"}`)
					return
				}
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"message":"target unavailable","code":"TEST_BLOCKED","requestId":"req-1"}`)
			}))
			t.Cleanup(server.Close)
			writeRunFileFor(t, cfg, server)
			out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, args...)
			if err == nil || ExitCode(err) != 1 || out != "" || !strings.Contains(err.Error(), "target unavailable") || !strings.Contains(err.Error(), "TEST_BLOCKED") || !strings.Contains(err.Error(), "req-1") {
				t.Fatalf("out=%q err=%v", out, err)
			}
			if command == "start attempt" {
				if calls != 2 || !strings.Contains(err.Error(), "run run-1 created") {
					t.Fatalf("created run must be retained in error: calls=%d err=%v", calls, err)
				}
			} else if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}

func TestTestingManagementUsage(t *testing.T) {
	setConfigEnv(t)
	base := testingStartArgs(t)
	cases := [][]string{
		{"testing", "start"}, {"testing", "start", "extra"},
		{"testing", "stop"}, {"testing", "stop", "a", "b"}, {"testing", "stop", " "},
		{"testing", "evidence"}, {"testing", "evidence", "a", "b"}, {"testing", "evidence", " "},
		{"testing", "mcp", "extra"}, {"testing", "mcp", "--target", "supervisor"},
	}
	for _, extra := range [][]string{
		{"--project", ""}, {"--commit", ""}, {"--recipe", ""}, {"--issue-file", ""},
		{"--timeout", "0"}, {"--timeout", "7201"}, {"--timeout", "abc"}, {"--target", "supervisor"},
	} {
		cases = append(cases, append(append([]string{}, base...), extra...))
	}
	for _, args := range cases {
		_, _, err := executeCLI(t, Deps{}, args...)
		if err == nil || ExitCode(err) != 2 {
			t.Fatalf("args=%v err=%v exit=%d", args, err, ExitCode(err))
		}
	}
}

func TestTestingStartRejectsIncompleteDaemonIdentifiers(t *testing.T) {
	for _, tc := range []struct{ run, attempt string }{
		{`{}`, `{}`},
		{`{"runId":"run-1"}`, `{"runId":"run-1","attemptId":"attempt-1"}`},
		{`{"runId":"run-1"}`, `{"runId":"other-run","attemptId":"attempt-1","workerSessionId":"worker-1"}`},
	} {
		t.Run(tc.run+tc.attempt, func(t *testing.T) {
			cfg := setConfigEnv(t)
			args := testingStartArgs(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/internal/telemetry/cli-invoked" {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				if r.URL.Path == "/api/v1/testing/runs" {
					_, _ = io.WriteString(w, tc.run)
				} else {
					_, _ = io.WriteString(w, tc.attempt)
				}
			}))
			t.Cleanup(server.Close)
			writeRunFileFor(t, cfg, server)
			out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, args...)
			if err == nil || ExitCode(err) != 1 || out != "" {
				t.Fatalf("incomplete daemon result accepted: out=%q err=%v", out, err)
			}
		})
	}
}

func TestTestingStartRejectsInvalidFiles(t *testing.T) {
	for _, tc := range []struct {
		name, flag string
		data       []byte
	}{
		{"empty issue", "--issue-file", []byte("\n ")},
		{"large issue", "--issue-file", []byte(strings.Repeat("a", (256<<10)+1))},
		{"empty prompt", "--prompt-file", nil},
		{"large prompt", "--prompt-file", []byte(strings.Repeat("a", (64<<10)+1))},
		{"invalid UTF-8", "--prompt-file", []byte{0xff}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setConfigEnv(t)
			args := testingStartArgs(t)
			file := filepath.Join(t.TempDir(), "invalid.txt")
			if err := os.WriteFile(file, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			args = append(args, tc.flag, file)
			_, _, err := executeCLI(t, Deps{}, args...)
			if err == nil || ExitCode(err) != 2 {
				t.Fatalf("invalid file accepted: %v", err)
			}
		})
	}
}
