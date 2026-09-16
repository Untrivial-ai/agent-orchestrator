package codebuddy

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const minimumCodeBuddyVersion = "2.151.0"

type packageFixture struct {
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	Integrity string            `json:"distIntegrity"`
	Bin       map[string]string `json:"bin"`
}

type terminalSemantic struct {
	name string
	flag string
}

var requiredTerminalSemantics = []terminalSemantic{
	{name: "interactive initial prompt", flag: "[prompt]"},
	{name: "append system prompt", flag: "--append-system-prompt <prompt>"},
	{name: "model", flag: "--model <model>"},
	{name: "permission mode", flag: "--permission-mode <mode>"},
	{name: "allowed tools", flag: "--allowedTools <tools...>"},
	{name: "disallowed tools", flag: "--disallowedTools <tools...>"},
	{name: "caller-assigned session ID", flag: "--session-id <uuid>"},
	{name: "exact restore", flag: "--resume [sessionId]"},
	{name: "version", flag: "--version"},
	{name: "help", flag: "--help"},
}

func TestPinnedPackageIdentityFixture(t *testing.T) {
	var fixture packageFixture
	readJSONFixture(t, "package.json", &fixture)
	if fixture.Name != "@tencent-ai/codebuddy-code" {
		t.Fatalf("package name = %q", fixture.Name)
	}
	if fixture.Version != minimumCodeBuddyVersion {
		t.Fatalf("package version = %q, want %q", fixture.Version, minimumCodeBuddyVersion)
	}
	if fixture.Integrity != "sha512-Y9xoUX4nsEsG5oz/AbRNH3ZmghmxCuB6dFAZC6xStLOnoxntPco2g4TtYuBi5c/POYQVLYs/ZbpsMx6zc99vFg==" {
		t.Fatalf("unexpected package integrity %q", fixture.Integrity)
	}
	for _, alias := range []string{"codebuddy", "cbc", "codebuddy-code"} {
		if fixture.Bin[alias] != "./bin/codebuddy" {
			t.Errorf("bin[%q] = %q", alias, fixture.Bin[alias])
		}
	}
}

func TestTerminalHelpContractFixture(t *testing.T) {
	assertHelpMatchesContract(t, readFixture(t, "help.txt"))
}

func TestACPInitializeFixture(t *testing.T) {
	var response struct {
		Result struct {
			ProtocolVersion   int `json:"protocolVersion"`
			AgentCapabilities struct {
				LoadSession        bool `json:"loadSession"`
				PromptCapabilities struct {
					Image           bool `json:"image"`
					EmbeddedContext bool `json:"embeddedContext"`
				} `json:"promptCapabilities"`
			} `json:"agentCapabilities"`
			AuthMethods []struct {
				ID string `json:"id"`
			} `json:"authMethods"`
		} `json:"result"`
	}
	readJSONFixture(t, "acp-initialize.json", &response)
	if response.Result.ProtocolVersion != 1 || !response.Result.AgentCapabilities.LoadSession {
		t.Fatalf("ACP initialize does not advertise protocol v1 session loading")
	}
	if !response.Result.AgentCapabilities.PromptCapabilities.Image ||
		!response.Result.AgentCapabilities.PromptCapabilities.EmbeddedContext {
		t.Fatalf("ACP prompt capabilities = %#v", response.Result.AgentCapabilities.PromptCapabilities)
	}
	if len(response.Result.AuthMethods) == 0 {
		t.Fatal("ACP initialize advertises no authentication methods")
	}
}

func TestInstalledCodeBuddyContract(t *testing.T) {
	if os.Getenv("AO_CODEBUDDY_E2E") != "1" {
		t.Skip("set AO_CODEBUDDY_E2E=1")
	}
	binary := findCodeBuddy(t)
	version := runCodeBuddy(t, binary, "--version")
	if !strings.Contains(version, minimumCodeBuddyVersion) {
		t.Fatalf("version output %q does not contain %q", version, minimumCodeBuddyVersion)
	}
	assertHelpMatchesContract(t, runCodeBuddy(t, binary, "--help"))
}

func assertHelpMatchesContract(t *testing.T, help string) {
	t.Helper()
	for _, semantic := range requiredTerminalSemantics {
		if !strings.Contains(help, semantic.flag) {
			t.Errorf("missing terminal contract %q (%s)", semantic.name, semantic.flag)
		}
	}
	resumeLine := regexp.MustCompile(`(?m)^\s+-r, --resume \[sessionId\]`).FindString(help)
	if resumeLine == "" {
		t.Error("restore must accept an explicit session ID; --continue alone is insufficient")
	}
}

func findCodeBuddy(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"codebuddy", "cbc", "codebuddy-code"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	t.Fatal("no CodeBuddy executable found (tried codebuddy, cbc, codebuddy-code)")
	return ""
}

func runCodeBuddy(t *testing.T, binary string, args ...string) string {
	t.Helper()
	out, err := exec.Command(binary, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", binary, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func readJSONFixture(t *testing.T, name string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(readFixture(t, name)), target); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}
