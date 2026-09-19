package geminiacp

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestLiveGeminiACPContract is intentionally a registration gate. The full
// initialize/new/load/stream/permission/cancel/replay matrix must replace this
// executable probe and pass before a Gemini Chat driver is registered.
func TestLiveGeminiACPContract(t *testing.T) {
	if os.Getenv("AO_LIVE_GEMINI") != "1" {
		t.Skip("set AO_LIVE_GEMINI=1 to test the installed Gemini CLI")
	}
	binary, err := exec.LookPath("gemini")
	if err != nil {
		t.Fatalf("resolve Gemini CLI: %v", err)
	}
	help, err := exec.Command(binary, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("gemini --help: %v: %s", err, help)
	}
	if !strings.Contains(string(help), "--acp") {
		t.Fatal("gemini --help does not advertise --acp")
	}
	t.Fatal("Gemini ACP conformance is not yet proven: initialize/auth, session load, streaming, permissions, cancellation, restart, and replay must pass before registration")
}
