package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestTestingPRStartInfersAOProjectAndStartsOneComparison(t *testing.T) {
	cfg := setConfigEnv(t)
	t.Setenv("USER", "worker")
	t.Setenv("AO_PROJECT_ID", "demo")
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/internal/telemetry/cli-invoked" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/projects/demo":
			_, _ = io.WriteString(w, `{"status":"ok","project":{"id":"demo","path":"/repo/demo"}}`)
		case "/api/v1/testing/runs":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input["prUrl"] != "https://github.com/o/r/pull/1" || input["projectId"] != "demo" || input["requester"] != "worker" {
				t.Error("wrong intake body", input)
			}
			if _, found := input["issueSnapshot"]; found {
				t.Error("manual snapshot sent for URL intake")
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"runId":"base","headRunId":"head"}`)
		case "/api/v1/testing/comparisons":
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input["runId"] != "base" || input["headRunId"] != "head" || input["timeoutSeconds"] != float64(1800) || input["workerPrompt"] == "" {
				t.Error("wrong comparison body", input)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"runId":"base","attemptId":"pending-base","workerSessionId":"one-worker"}`)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	writeRunFileFor(t, cfg, server)
	out, stderr, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "testing", "start", "--pr", "https://github.com/o/r/pull/1", "--json")
	if err != nil || stderr != "" || !strings.Contains(out, `"workerSessionId": "one-worker"`) {
		t.Fatalf("PR start: %q %q %v", out, stderr, err)
	}
	if !reflect.DeepEqual(calls, []string{"GET /api/v1/projects/demo", "POST /api/v1/testing/runs", "POST /api/v1/testing/comparisons"}) {
		t.Fatal("unexpected launch calls", calls)
	}
}
func TestTestingPRStartRejectsMixedManualFlags(t *testing.T) {
	for _, flag := range []string{"--commit", "--issue-file", "--issue-url"} {
		t.Run(flag, func(t *testing.T) {
			_, _, err := executeCLI(t, Deps{}, "testing", "start", "--pr", "https://github.com/o/r/pull/1", flag, "manual")
			if err == nil || !strings.Contains(err.Error(), "--pr cannot be combined") {
				t.Fatalf("mixed flags admitted: %v", err)
			}
		})
	}
}
