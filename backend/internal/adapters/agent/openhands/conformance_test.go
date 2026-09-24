package openhands

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// requiredHelpTokens are the CLI flags this adapter emits.
var requiredHelpTokens = []string{"--task", "--resume", "--always-approve", "--llm-approve"}

func TestLiveOpenHandsReleaseContract(t *testing.T) {
	if os.Getenv("AO_LIVE_OPENHANDS") != "1" {
		t.Skip("set AO_LIVE_OPENHANDS=1 to test the installed OpenHands CLI")
	}

	binary, err := exec.LookPath("openhands")
	if err != nil {
		t.Fatalf("resolve OpenHands CLI: %v", err)
	}
	versionOutput, err := exec.Command(binary, "--version").Output()
	if err != nil {
		t.Fatalf("openhands --version: %v: %s", err, versionOutput)
	}
	if !supportedVersion(string(versionOutput)) {
		t.Fatalf("OpenHands CLI version %q is below the minimum or unparsable", strings.TrimSpace(string(versionOutput)))
	}
	help, err := exec.Command(binary, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("openhands --help: %v: %s", err, help)
	}
	for _, token := range requiredHelpTokens {
		if !strings.Contains(string(help), token) {
			t.Errorf("openhands --help missing required token %q", token)
		}
	}
}
