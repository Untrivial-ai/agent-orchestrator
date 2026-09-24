package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResearchReturnsReportToOrchestrator(t *testing.T) {
	for _, status := range []string{"completed", "failed", "running"} {
		t.Run(status, func(t *testing.T) {
			cfg := setConfigEnv(t)
			t.Setenv("AO_SESSION_ID", "mer-orch")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/internal/telemetry/cli-invoked" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost && r.URL.Path == "/api/v1/sessions/mer-orch/research" {
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["prompt"] != "Find the entry point" {
						t.Errorf("prompt = %v, %v", body, err)
					}
					_, _ = w.Write([]byte(`{"research":{"id":"run-1","status":"queued"}}`))
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/sessions/mer-orch/research/run-1" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				run := map[string]any{"id": "run-1", "status": status, "result": "Entry: src/main.go:12", "error": "provider failed"}
				if status == "running" {
					run["approval"] = map[string]any{"requestId": "approval-1", "summary": "Allow command?", "options": []map[string]string{{"id": "allow", "label": "Allow once"}}}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"research": run})
			}))
			t.Cleanup(srv.Close)
			writeRunFileFor(t, cfg, srv)
			out, _, err := executeCLI(t, aliveDeps(), "research", "Find the entry point")
			if status == "failed" {
				if err == nil || !strings.Contains(err.Error(), "provider failed") {
					t.Fatalf("failure = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if status == "completed" && !strings.Contains(out, "Entry: src/main.go:12") {
				t.Fatalf("report not returned: %s", out)
			}
			if status == "running" && !strings.Contains(out, "--wait --session mer-orch") {
				t.Fatalf("approval recovery instructions missing: %s", out)
			}
		})
	}
}

func TestResearchRequiresOrchestratorID(t *testing.T) {
	setConfigEnv(t)
	t.Setenv("AO_SESSION_ID", "")
	_, _, err := executeCLI(t, aliveDeps(), "research", "question")
	if err == nil || !strings.Contains(err.Error(), "session id is required") {
		t.Fatalf("missing session: %v", err)
	}
}
