package deepagents

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeepAgentsUpstreamConformance(t *testing.T) {
	binary := os.Getenv("AO_DEEPAGENTS_CONFORMANCE_BINARY")
	if binary == "" {
		t.Skip("set AO_DEEPAGENTS_CONFORMANCE_BINARY to a disposable pinned dcode executable")
	}
	abs, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("conformance binary: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version := commandOutput(ctx, t, abs, "--version")
	help := commandOutput(ctx, t, abs, "--help")

	installed, ok := parseSemver(version)
	if !ok {
		t.Fatalf("unrecognized dcode version %q", strings.TrimSpace(version))
	}
	contract := Contract{
		Version:          installed.String(),
		SystemPromptFile: strings.Contains(help, "--system-prompt-file"),
		IsolatedHooks:    strings.Contains(help, "--hooks-file"),
		InitialMessage:   strings.Contains(help, "--message"),
		ExactRestore:     strings.Contains(help, "--resume"),
		ACP:              strings.Contains(help, "--acp"),
	}
	if err := ValidateContract(contract); err != nil {
		t.Fatalf("DeepAgents production gate failed: %v", err)
	}

	// Passing the static gate is intentionally insufficient. The remaining
	// fields require the disposable-profile, PTY, hook and ACP transcript
	// evidence described in docs/harnesses/deepagents.md.
	t.Fatal("DeepAgents static flags passed, but behavioral conformance evidence has not been recorded")
}

func commandOutput(ctx context.Context, t *testing.T, binary string, args ...string) string {
	t.Helper()
	output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", binary, strings.Join(args, " "), err, output)
	}
	return string(output)
}
