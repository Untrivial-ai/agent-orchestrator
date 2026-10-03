package deepseekharness

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// The live tests below pin the two headless contracts GetLaunchCommand depends
// on against the real binary, rather than against the published docs alone.
// They are opt-in because they need a real `dsh` install:
//
//	AO_LIVE_DEEPSEEK=1 go test ./internal/adapters/agent/deepseekharness/ -run Live -v
//
// None of them needs a working model route: each assertion lands before DeepSeek
// Harness reaches its provider, so an install without credentials still proves
// the contract.
func liveBinary(t *testing.T) string {
	t.Helper()
	if os.Getenv("AO_LIVE_DEEPSEEK") != "1" {
		t.Skip("set AO_LIVE_DEEPSEEK=1 to test the installed DeepSeek Harness")
	}
	binary, err := exec.LookPath("dsh")
	if err != nil {
		t.Fatalf("resolve DeepSeek Harness: %v", err)
	}
	return binary
}

// runLive runs one dsh invocation to completion and returns its combined output.
// Output is captured to a real file rather than a pipe: a pipe would be
// inherited by the node child the dsh shim spawns on Windows, so reading one
// can outlive the process the test is waiting on. It reports timedOut instead of
// hanging when the run does not finish.
func runLive(t *testing.T, timeout time.Duration, args ...string) (out string, timedOut bool, err error) {
	t.Helper()
	sink, err := os.CreateTemp(t.TempDir(), "dsh-*.log")
	if err != nil {
		t.Fatalf("capture file: %v", err)
	}
	defer func() { _ = sink.Close() }()

	cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // test-only
	cmd.Stdin = strings.NewReader("")
	cmd.Stdout = sink
	cmd.Stderr = sink
	if startErr := cmd.Start(); startErr != nil {
		t.Fatalf("start: %v", startErr)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	select {
	case err = <-waited:
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		timedOut = true
	}
	body, readErr := os.ReadFile(sink.Name())
	if readErr != nil {
		t.Fatalf("read capture: %v", readErr)
	}
	return string(body), timedOut, err
}

// TestLiveHeadlessRejectsAnUnknownSessionID is why a fresh launch must not pass
// AO's session id: Harness treats --session-id as adopting a session it has
// already persisted, so AO's own id — which it supplies on every spawn — would
// fail the launch outright.
func TestLiveHeadlessRejectsAnUnknownSessionID(t *testing.T) {
	binary := liveBinary(t)
	out, timedOut, err := runLive(t, time.Minute, binary, "--profile", headlessProfile,
		"--session-id", "ao-sess-unknown-"+t.Name(), "print ok")
	if timedOut {
		t.Fatalf("run did not finish; output: %s", out)
	}
	if err == nil {
		t.Fatalf("an unknown --session-id was accepted; output: %s", out)
	}
	if !strings.Contains(out, "does not exist") {
		t.Fatalf("unexpected failure for an unknown session id: %s", out)
	}
}

// TestLiveHeadlessBlocksWithoutATask is why a promptless launch is refused in
// process: with no task argument Harness falls back to reading stdin, and an AO
// terminal session never writes to it, so the session would hang until the
// supervisor killed it.
func TestLiveHeadlessBlocksWithoutATask(t *testing.T) {
	binary := liveBinary(t)

	// A pipe whose write end stays open models a terminal that never sends a
	// line, which is what a promptless AO session gives the process. Output goes
	// to the null device rather than a pipe: reading one would outlive the
	// process on Windows, where the dsh shim's node child inherits the handle.
	stdin, writer := io.Pipe()
	cmd := exec.Command(binary, "--profile", headlessProfile) //nolint:gosec // test-only
	cmd.Stdin = stdin
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	select {
	case err := <-waited:
		_ = writer.Close()
		t.Fatalf("a task-less headless run exited (%v) instead of waiting on stdin", err)
	case <-time.After(15 * time.Second):
	}

	// Close the write end so the run sees EOF and exits on its own: killing the
	// shim would leave its node child behind.
	_ = writer.Close()
	select {
	case <-waited:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Error("the run did not exit after stdin closed")
	}
}

// TestLiveLaunchCommandIsAcceptedByTheBinary drives the argv the adapter builds
// for the config AO actually supplies — SessionID always populated — and asserts
// it clears both traps above. It stops at the credential check on an install
// with no model route, which is already past the contract under test.
func TestLiveLaunchCommandIsAcceptedByTheBinary(t *testing.T) {
	binary := liveBinary(t)
	p := &Plugin{resolvedBinary: binary}
	argv, err := p.GetLaunchCommand(context.Background(), ports.LaunchConfig{
		SessionID: "ao-sess-unknown-" + t.Name(),
		Prompt:    "print ok",
	})
	if err != nil {
		t.Fatalf("launch command: %v", err)
	}

	out, timedOut, runErr := runLive(t, time.Minute, argv...)
	if timedOut {
		t.Fatalf("the launch argv blocked instead of running the task: %s", out)
	}
	for _, trap := range []string{"does not exist", "a task is required"} {
		if strings.Contains(out, trap) {
			t.Fatalf("the launch argv hit %q: %s (err %v)", trap, out, runErr)
		}
	}
}

// TestLiveCredentialStoreLayoutIsAccepted pins the credential-store layout
// AuthStatus reads. It writes the document AO treats as evidence into a private
// DSH_HOME and asserts the real Harness loads it: a store AO calls authorized
// but Harness refuses is a false ready badge.
//
// The key is not a real one, so the run still fails — but it fails at the
// provider ("Authentication Fails"), which only happens once the document has
// parsed and the key has been read out of it.
func TestLiveCredentialStoreLayoutIsAccepted(t *testing.T) {
	binary := liveBinary(t)
	home := t.TempDir()
	store := filepath.Join(home, ".credentials.yaml")
	if err := os.WriteFile(store, []byte("version: 1\nrefs:\n  DEEPSEEK_API_KEY: sk-not-a-real-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DSH_HOME", home)
	t.Setenv(credentialsPathEnv, store)
	t.Setenv(deepseekCredentialKey, "")

	// AO reads the document as authorization evidence.
	p := &Plugin{resolvedBinary: binary}
	status, err := p.AuthStatus(context.Background())
	if err != nil {
		t.Fatalf("auth status: %v", err)
	}
	if status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want authorized for the version-1 refs layout", status)
	}

	// Harness loads the same document rather than rejecting it.
	out, timedOut, _ := runLive(t, time.Minute, binary, "--profile", headlessProfile, "print ok")
	if timedOut {
		t.Fatalf("run did not finish; output: %s", out)
	}
	for _, rejection := range []string{"pre-release flat layout", "credentials-local:", "MISSING_CREDENTIAL"} {
		if strings.Contains(out, rejection) {
			t.Fatalf("Harness rejected a store AO reports as authorized (%q): %s", rejection, out)
		}
	}
}
