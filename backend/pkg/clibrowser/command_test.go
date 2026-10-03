package clibrowser_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/clibrowser"
)

type recordingTransport struct {
	actions      []string
	argsByAction map[string]map[string]any
	response     clibrowser.Response
	status       clibrowser.Status
	err          error
}

func (r *recordingTransport) BrowserAction(_ context.Context, action string, args map[string]any) (clibrowser.Response, error) {
	if r.err != nil {
		return clibrowser.Response{}, r.err
	}
	r.actions = append(r.actions, action)
	if r.argsByAction == nil {
		r.argsByAction = map[string]map[string]any{}
	}
	r.argsByAction[action] = args
	return r.response, nil
}

func (r *recordingTransport) BrowserStatus(context.Context) (clibrowser.Status, error) {
	if r.err != nil {
		return clibrowser.Status{}, r.err
	}
	return r.status, nil
}

func TestNewCommandMountsFullVerbTree(t *testing.T) {
	cmd := clibrowser.NewCommand(&recordingTransport{}, nil, "")
	want := []string{
		"status", "open", "snapshot", "act", "click", "dblclick", "focus", "drag",
		"fill", "type", "press", "hover", "highlight", "unhighlight", "tabs", "tab",
		"devtools", "scroll", "select", "check", "uncheck", "get", "wait",
		"screenshot", "network", "frame", "dialog", "console", "errors",
	}
	got := map[string]bool{}
	for _, sub := range cmd.Commands() {
		got[sub.Name()] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("verb %q missing from browser command tree", name)
		}
	}
}

func TestNewCommandLongOverride(t *testing.T) {
	defaultCmd := clibrowser.NewCommand(&recordingTransport{}, nil, "")
	if !strings.Contains(defaultCmd.Long, "The desktop app must be open.") {
		t.Fatalf("default Long = %q", defaultCmd.Long)
	}
	cloudCmd := clibrowser.NewCommand(&recordingTransport{}, nil, "cloud wording")
	if cloudCmd.Long != "cloud wording" {
		t.Fatalf("overridden Long = %q", cloudCmd.Long)
	}
	if cloudCmd.Short != defaultCmd.Short {
		t.Error("Short must not change with the Long override")
	}
}

func TestNewCommandStatusUsesTransport(t *testing.T) {
	transport := &recordingTransport{status: clibrowser.Status{
		SessionID: "ao-1", Connected: true, Transport: "vm-chromium",
	}}
	cmd := clibrowser.NewCommand(transport, nil, "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"status"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out.String(), "Browser runtime: connected (vm-chromium)") {
		t.Fatalf("status output = %q", out.String())
	}
}

func TestNewCommandScreenshotClockSeam(t *testing.T) {
	fixed := time.Date(2026, 9, 17, 10, 30, 0, 0, time.UTC)
	transport := &recordingTransport{response: clibrowser.Response{
		RequestID: "r1", SessionID: "ao-1", Action: "screenshot",
		Result: map[string]any{"data": "cG5n", "width": float64(10), "height": float64(20)},
	}}
	cmd := clibrowser.NewCommand(transport, func() time.Time { return fixed }, "")
	var out bytes.Buffer
	dir := t.TempDir()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"screenshot", dir + "/x.png"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if !strings.Contains(out.String(), "Saved") || !strings.Contains(out.String(), "10x20") {
		t.Fatalf("screenshot output = %q", out.String())
	}
}

func TestUsageErrorWrapsAndUnwraps(t *testing.T) {
	inner := errors.New("bad usage")
	wrapped := &clibrowser.UsageError{Err: inner}
	var shared *clibrowser.UsageError
	if !errors.As(wrapped, &shared) {
		t.Fatal("UsageError must be detectable via errors.As")
	}
	if shared.Error() != "bad usage" || !errors.Is(wrapped, inner) {
		t.Fatalf("UsageError rendering = %q", shared.Error())
	}
}

func TestCurrentIdentityRequiresEnv(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	t.Setenv("AO_BROWSER_CAPABILITY", "")
	_, _, err := clibrowser.CurrentIdentity()
	var shared *clibrowser.UsageError
	if !errors.As(err, &shared) {
		t.Fatalf("want UsageError, got %v", err)
	}
	t.Setenv("AO_SESSION_ID", "ao-1")
	_, _, err = clibrowser.CurrentIdentity()
	if !errors.As(err, &shared) {
		t.Fatalf("want UsageError for missing capability, got %v", err)
	}
	t.Setenv("AO_BROWSER_CAPABILITY", "tok")
	sessionID, capability, err := clibrowser.CurrentIdentity()
	if err != nil || sessionID != "ao-1" || capability != "tok" {
		t.Fatalf("CurrentIdentity = %q %q %v", sessionID, capability, err)
	}
}
