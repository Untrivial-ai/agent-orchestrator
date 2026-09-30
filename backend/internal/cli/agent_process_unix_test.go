//go:build !windows

package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
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
	want := setActivityAPIRequest{State: "exited", Event: "process-exited", LaunchID: "launch-3"}
	if req != want {
		t.Fatalf("exit report = %+v, want %+v", req, want)
	}
}

func TestAgentProcessSuperviseRejectsInvalidGeneration(t *testing.T) {
	_, _, err := executeCLI(t, Deps{}, "agent-process", "supervise", "--session", "ao-7", "--launch", "../stale", "--", "true")
	if err == nil {
		t.Fatal("invalid launch id should be rejected before starting the child")
	}
}

// TestAgentProcessSuperviseAcceptsDottedProjectSessionID guards against a
// regression where AO session ids derived from a dotted project id
// ("{ProjectID}-{num}", e.g. a project named like a domain) were accepted by
// projectIDPattern but rejected here, permanently failing every session for
// that project regardless of num.
func TestAgentProcessSuperviseAcceptsDottedProjectSessionID(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, _ := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{
		In:           strings.NewReader(""),
		ProcessAlive: func(int) bool { return true },
	}, "agent-process", "supervise", "--session", "my.project-16", "--launch", "launch-3", "--", "sh", "-c", "printf ok")
	if err != nil {
		t.Fatalf("dotted project session id should be accepted: %v\nstderr=%s", err, errOut)
	}
	if out != "ok" {
		t.Fatalf("stdout = %q, want ok", out)
	}
}

func TestAgentProcessSuperviseRejectsTraversalSessionID(t *testing.T) {
	for _, id := range []string{"../evil", "a/b", ".hidden"} {
		_, _, err := executeCLI(t, Deps{}, "agent-process", "supervise", "--session", id, "--launch", "launch-3", "--", "true")
		if err == nil {
			t.Fatalf("session id %q should be rejected", id)
		}
	}
}
