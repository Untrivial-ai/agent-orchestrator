package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func executeLeg(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	deps := (Deps{Out: &out, ProcessAlive: func(int) bool { return true }}).withDefaults()
	cmd := newTestingLegCommand(&commandContext{deps: deps})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}
func TestTestingLegUsesSessionAndCheckedTargetContext(t *testing.T) {
	cfg := setConfigEnv(t)
	t.Setenv("AO_SESSION_ID", "worker-1")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/telemetry/cli-invoked" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/testing/sessions/worker-1/legs/head/start" || r.Header.Get(testingCapabilityHeader) != "" {
			t.Error("wrong management call", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		if strings.TrimSpace(string(b)) != "{}" {
			t.Error("client supplied target", string(b))
		}
		_, _ = io.WriteString(w, `{"runId":"head-run","attemptId":"head-attempt","workerSessionId":"worker-1","leg":"head","commitSha":"pinned","evidenceDir":"/retained","targetContext":{"checkoutPath":"/checkout","cliPath":"/target-ao","runFilePath":"/private/running.json","dataDir":"/private/data"}}`)
	}))
	defer server.Close()
	writeRunFileFor(t, cfg, server)
	for _, args := range [][]string{{"start", "head", "--json"}, {"start", "head"}} {
		out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, append([]string{"testing", "leg"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		if len(args) == 3 {
			var got testingLegStartedDTO
			if err := json.Unmarshal([]byte(out), &got); err != nil || got.TargetContext.CLIPath != "/target-ao" || got.Leg != "head" {
				t.Fatal(out, err)
			}
		} else if !strings.Contains(out, "commit SHA: pinned") || !strings.Contains(out, "target CLI: /target-ao") {
			t.Fatal(out)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestTestingLegValidationAndDaemonErrors(t *testing.T) {
	cfg := setConfigEnv(t)
	for _, args := range [][]string{{"start"}, {"start", "other"}, {"start", "base", "extra"}, {"start", "base"}} {
		t.Setenv("AO_SESSION_ID", "")
		_, err := executeLeg(t, args...)
		var misuse usageError
		if !errors.As(err, &misuse) {
			t.Fatal("not a usage error", err)
		}
	}
	t.Setenv("AO_SESSION_ID", "worker")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"code":"TEST_LEG_CLEANUP_FAILED","message":"owned listener remains","requestId":"leg-request"}`)
	}))
	defer server.Close()
	writeRunFileFor(t, cfg, server)
	_, err := executeLeg(t, "start", "head")
	if err == nil || !strings.Contains(err.Error(), "TEST_LEG_CLEANUP_FAILED") || !strings.Contains(err.Error(), "leg-request") {
		t.Fatal(err)
	}
}
