//go:build !windows

package cli

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestAgentProcessSuperviseReportsExitAndPreservesOutput(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		In:           strings.NewReader(""),
		ProcessAlive: func(int) bool { return true },
	}, "agent-process", "supervise", "--session", "ao-7", "--launch", "launch-3", "--", "sh", "-c", "printf supervised; exit 23")
	if err != nil {
		t.Fatalf("supervise returned child exit as command failure: %v\nstderr=%s", err, errOut)
	}
	if out != "supervised" {
		t.Fatalf("stdout = %q, want supervised", out)
	}
	var req setActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatal(err)
	}
	if req.ExitCode == nil || *req.ExitCode != 23 {
		t.Fatalf("exit code = %v, want 23", req.ExitCode)
	}
	req.ExitCode = nil
	want := setActivityAPIRequest{State: "exited", Event: "process-exited", LaunchID: "launch-3"}
	if req != want {
		t.Fatalf("exit report = %+v, want %+v", req, want)
	}
}

func TestAgentProcessSuperviseExitCauses(t *testing.T) {
	for _, tt := range []struct {
		name      string
		argv      []string
		wantCode  bool
		wantCause domain.LaunchFailureCause
	}{
		{"zero", []string{"sh", "-c", "exit 0"}, true, ""},
		{"startup failure", []string{filepath.Join(t.TempDir(), "missing-command")}, false, domain.LaunchFailureProcessStartFailed},
		{"signal", []string{"sh", "-c", "kill -TERM $$"}, false, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := setConfigEnv(t)
			srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
			writeRunFileFor(t, cfg, srv)
			args := append([]string{"agent-process", "supervise", "--session", "ao-7", "--launch", "launch-3", "--"}, tt.argv...)
			_, errOut, err := executeCLI(t, Deps{In: strings.NewReader(""), ProcessAlive: func(int) bool { return true }}, args...)
			if err != nil {
				t.Fatalf("supervisor failed: %v; %s", err, errOut)
			}
			var req setActivityAPIRequest
			if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
				t.Fatal(err)
			}
			if req.State != "exited" || req.LaunchID != "launch-3" || req.LaunchFailureCause != tt.wantCause {
				t.Fatalf("unexpected exit report: %+v", req)
			}
			if (req.ExitCode != nil) != tt.wantCode || (req.ExitCode != nil && *req.ExitCode != 0) {
				t.Fatalf("unexpected exit code: %v", req.ExitCode)
			}
			if tt.wantCause == domain.LaunchFailureProcessStartFailed && !strings.Contains(errOut, "start managed agent") {
				t.Fatalf("startup diagnostic missing: %s", errOut)
			}
		})
	}
}

func TestAgentProcessSuperviseRejectsInvalidGeneration(t *testing.T) {
	_, _, err := executeCLI(t, Deps{}, "agent-process", "supervise", "--session", "ao-7", "--launch", "../stale", "--", "true")
	if err == nil {
		t.Fatal("invalid launch id should be rejected before starting the child")
	}
}
