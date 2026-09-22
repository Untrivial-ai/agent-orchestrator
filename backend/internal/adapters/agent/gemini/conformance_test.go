package gemini

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

var requiredHelpTokens = []string{
	"--prompt-interactive", "--approval-mode", "--model",
	"--session-id", "--resume", "--acp", "--skip-trust",
}

func TestParseVersionAtLeast060(t *testing.T) {
	tests := []struct {
		output string
		want   bool
	}{
		{"0.60.0", true},
		{"gemini-cli 1.0.0", true},
		{"0.59.9", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.output, func(t *testing.T) {
			got := supportedVersion(tt.output)
			if got != tt.want {
				t.Fatalf("supportedVersion(%q) = %v, want %v", tt.output, got, tt.want)
			}
		})
	}
}

func TestLiveGeminiReleaseContract(t *testing.T) {
	if os.Getenv("AO_LIVE_GEMINI") != "1" {
		t.Skip("set AO_LIVE_GEMINI=1 to test the installed Gemini CLI")
	}

	binary, err := exec.LookPath("gemini")
	if err != nil {
		t.Fatalf("resolve Gemini CLI: %v", err)
	}
	versionOutput, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("gemini --version: %v: %s", err, versionOutput)
	}
	ok := supportedVersion(string(versionOutput))
	if !ok {
		t.Fatalf("Gemini CLI version %q is below required 0.60.0 or unparsable", strings.TrimSpace(string(versionOutput)))
	}
	help, err := exec.Command(binary, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("gemini --help: %v: %s", err, help)
	}
	for _, token := range requiredHelpTokens {
		if !strings.Contains(string(help), token) {
			t.Errorf("gemini --help missing required token %q", token)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
}
