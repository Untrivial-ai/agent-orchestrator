package plandex

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const conformanceBinaryEnv = "AO_PLANDEX_CONFORMANCE_BINARY"

// TestPlandexUpstreamConformance is an opt-in executable gate. It is expected
// to fail for cli/v2.2.1; a future release passes only after its documented CLI
// surface exposes every capability below and the behavioral probes are expanded
// to exercise those contracts.
func TestPlandexUpstreamConformance(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv(conformanceBinaryEnv))
	if binary == "" {
		t.Skipf("set %s to a pinned Plandex executable", conformanceBinaryEnv)
	}
	absBinary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatalf("resolve binary path: %v", err)
	}

	home := t.TempDir()
	workspace := t.TempDir()
	version := runPlandex(t, absBinary, home, workspace, "version")
	rootHelp := runPlandex(t, absBinary, home, workspace, "--help")
	replHelp := runPlandex(t, absBinary, home, workspace, "repl", "--help")
	cdHelp := runPlandex(t, absBinary, home, workspace, "cd", "--help")
	stopHelp := runPlandex(t, absBinary, home, workspace, "stop", "--help")
	combined := strings.ToLower(strings.Join([]string{rootHelp, replHelp, cdHelp, stopHelp}, "\n"))

	contract := Contract{
		Version:                    strings.TrimSpace(version),
		RaceFreeInitialTask:        hasAny(combined, "--message", "--prompt-interactive", "--initial-prompt"),
		HiddenStandingInstructions: hasAny(combined, "--system-prompt", "--system-prompt-file", "--instructions-file"),
		IsolatedObserverHooks:      hasAny(combined, "--hooks-file", "--hook-config"),
		NativeSessionID:            hasAny(combined, "--session-id", "--plan-id"),
		ExactRestoreByID:           hasAny(combined, "--resume-id", "--resume <", "resume [plan-id]"),
		TruthfulPermissions:        hasAny(combined, "--permission-mode", "--approval-mode"),
		BoundedCancellation:        strings.Contains(strings.ToLower(stopHelp), "--plan-id"),
		AuthenticationStatus:       hasAny(combined, "auth status", "whoami", "status --auth"),
	}
	if err := ValidateContract(contract); err != nil {
		t.Fatalf("Plandex %s does not satisfy the AO harness contract: %v", contract.Version, err)
	}

	if _, err := os.Stat(filepath.Join(workspace, ".plandex-v2")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("help/version probes changed workspace state: %v", err)
	}
}

func runPlandex(t *testing.T, binary, home, workspace string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = workspace
	cmd.Env = append(withoutEnv(os.Environ(), "HOME", "PLANDEX_SKIP_UPGRADE", "NO_COLOR"),
		"HOME="+home,
		"PLANDEX_SKIP_UPGRADE=1",
		"NO_COLOR=1",
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("%s %s: %v", binary, strings.Join(args, " "), ctx.Err())
	}
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", binary, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func withoutEnv(env []string, names ...string) []string {
	blocked := make(map[string]struct{}, len(names))
	for _, name := range names {
		blocked[name] = struct{}{}
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, ok := strings.Cut(entry, "=")
		if _, remove := blocked[name]; ok && remove {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func hasAny(text string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(text, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func TestConformanceFailureNamesEveryCurrentBlocker(t *testing.T) {
	err := ValidateContract(ObservedContract())
	if err == nil {
		t.Fatal("ValidateContract(ObservedContract()) error = nil")
	}
	message := err.Error()
	for _, phrase := range []string{
		"initial-task",
		"standing-instruction",
		"observation hooks",
		"session identity",
		"restore-by-id",
		"permission mapping",
		"session cancellation",
		"authentication status",
	} {
		if !strings.Contains(message, phrase) {
			t.Errorf("contract error %q does not contain %q", message, phrase)
		}
	}
	if t.Failed() {
		t.Logf("full error: %v", err)
	}
}
