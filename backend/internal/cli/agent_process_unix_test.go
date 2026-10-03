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
	if capture.path != "/api/v1/sessions/ao-7/activity" {
		t.Fatalf("exit report path = %q, want session activity endpoint", capture.path)
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

func TestAgentProcessSuperviseReportsReviewExitToReviewActivity(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)
	_, errOut, err := executeCLI(t, Deps{In: strings.NewReader("")}, "agent-process", "supervise", "--review", "review-7", "--launch", "launch-3", "--", "sh", "-c", "exit 0")
	if err != nil {
		t.Fatalf("supervise returned child exit as command failure: %v\nstderr=%s", err, errOut)
	}
	if capture.path != "/api/v1/reviews/review-7/activity" {
		t.Fatalf("exit report path = %q, want review activity endpoint", capture.path)
	}
	var req setReviewActivityAPIRequest
	if err := json.Unmarshal([]byte(capture.body), &req); err != nil {
		t.Fatal(err)
	}
	want := setReviewActivityAPIRequest{State: "exited", Event: "process-exited", LaunchID: "launch-3"}
	if req != want {
		t.Fatalf("review exit report = %+v, want %+v", req, want)
	}
}

func TestAgentProcessSuperviseReportsReviewExitWithSessionIdentity(t *testing.T) {
	cfg := setConfigEnv(t)
	srv, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
	writeRunFileFor(t, cfg, srv)
	_, errOut, err := executeCLI(t, Deps{In: strings.NewReader("")}, "agent-process", "supervise", "--session", "review-mer-1", "--activity-review", "review-7", "--launch", "launch-3", "--", "sh", "-c", "exit 0")
	if err != nil {
		t.Fatalf("supervise returned child exit as command failure: %v\nstderr=%s", err, errOut)
	}
	if capture.path != "/api/v1/reviews/review-7/activity" {
		t.Fatalf("exit report path = %q, want review activity endpoint", capture.path)
	}
}
