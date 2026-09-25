package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type automationCapture struct {
	method, path string
	body         []byte
}

func automationServer(t *testing.T, status int, response string) (*httptest.Server, *automationCapture) {
	t.Helper()
	capture := &automationCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.method, capture.path = r.Method, r.URL.Path
		capture.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	return server, capture
}

// The CLI must remain a thin HTTP client and preserve camel-case wire fields.
func TestAutomationCreatePostsDaemonContract(t *testing.T) {
	cfg := setConfigEnv(t)
	server, capture := automationServer(t, http.StatusCreated, `{"automation":{"id":"automation-1","displayName":"Morning","nextRunAt":"2026-08-26T03:30:00Z"}}`)
	writeRunFileFor(t, cfg, server)
	out, stderr, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }, Now: func() time.Time { return time.Now() }}, "automation", "create", "--project", "demo", "--name", "Morning", "--prompt", "Review", "--cron", "0 9 * * *", "--timezone", "Asia/Calcutta")
	if err != nil {
		t.Fatalf("create: %v stderr=%s", err, stderr)
	}
	if capture.method != http.MethodPost || capture.path != "/api/v1/automations" || !strings.Contains(out, "automation-1") {
		t.Fatalf("request=%s %s output=%s", capture.method, capture.path, out)
	}
	var body map[string]any
	if err := json.Unmarshal(capture.body, &body); err != nil {
		t.Fatal(err)
	}
	if body["projectId"] != "demo" || body["displayName"] != "Morning" || body["cron"] != "0 9 * * *" {
		t.Fatalf("body=%s", capture.body)
	}
}

func TestAutomationDeleteRequiresMatchingConfirmation(t *testing.T) {
	setConfigEnv(t)
	out, _, err := executeCLI(t, Deps{In: strings.NewReader("wrong\n")}, "automation", "delete", "automation-1")
	if err != nil || !strings.Contains(out, "aborted") {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

func TestAutomationDeleteSendsDeleteAfterMatchingConfirmation(t *testing.T) {
	cfg := setConfigEnv(t)
	server, capture := automationServer(t, http.StatusOK, `{}`)
	writeRunFileFor(t, cfg, server)

	out, stderr, err := executeCLI(t, Deps{In: strings.NewReader("automation-1\n"), ProcessAlive: func(int) bool { return true }}, "automation", "delete", "automation-1")
	if err != nil {
		t.Fatalf("delete: %v stderr=%s", err, stderr)
	}
	if capture.method != http.MethodDelete || capture.path != "/api/v1/automations/automation-1" {
		t.Fatalf("request=%s %s", capture.method, capture.path)
	}
	if !strings.Contains(out, "deleted automation automation-1") {
		t.Fatalf("out=%s", out)
	}
}

func TestAutomationGetRequiresIDAsUsageError(t *testing.T) {
	setConfigEnv(t)
	_, _, err := executeCLI(t, Deps{}, "automation", "get")
	if err == nil || ExitCode(err) != 2 {
		t.Fatalf("err=%v exit=%d", err, ExitCode(err))
	}
}
