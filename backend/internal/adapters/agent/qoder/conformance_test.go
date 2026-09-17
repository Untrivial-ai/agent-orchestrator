package qoder

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstalledQoderContract(t *testing.T) {
	if os.Getenv("AO_QODER_E2E") != "1" {
		t.Skip("set AO_QODER_E2E=1")
	}
	bin := os.Getenv("AO_QODER_BINARY")
	if bin == "" {
		bin = "qoder"
	}
	version, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateVersionOutput(string(version)); err != nil {
		t.Fatal(err)
	}
	help, err := exec.Command(bin, "--help").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--session-id", "--resume", "--prompt-interactive", "--append-system-prompt", "--model", "--reasoning-effort", "--permission-mode", "--allowed-tools", "--disallowed-tools"} {
		if !strings.Contains(string(help), flag) {
			t.Fatalf("qoder help missing %s", flag)
		}
	}
}

func TestPinnedQoderFixtures(t *testing.T) {
	help, err := os.ReadFile(filepath.Join("testdata", "help.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--session-id", "--resume", "--prompt-interactive", "--append-system-prompt", "--model", "--reasoning-effort", "--permission-mode", "--allowed-tools", "--disallowed-tools"} {
		if !strings.Contains(string(help), flag) {
			t.Errorf("fixture missing %s", flag)
		}
	}
	var response struct {
		Result struct {
			AgentInfo struct {
				Version string `json:"version"`
			} `json:"agentInfo"`
			AgentCapabilities struct {
				LoadSession        bool            `json:"loadSession"`
				PromptCapabilities map[string]bool `json:"promptCapabilities"`
			} `json:"agentCapabilities"`
		} `json:"result"`
	}
	data, err := os.ReadFile(filepath.Join("testdata", "acp_initialize.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatal(err)
	}
	if !response.Result.AgentCapabilities.LoadSession || len(response.Result.AgentCapabilities.PromptCapabilities) == 0 {
		t.Fatalf("incomplete ACP capabilities: %#v", response.Result.AgentCapabilities)
	}
	if err := validateVersionOutput(response.Result.AgentInfo.Version); err != nil {
		t.Fatal(err)
	}
}
