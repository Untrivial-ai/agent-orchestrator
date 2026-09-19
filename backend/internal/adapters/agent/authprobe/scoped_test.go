package authprobe

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestRunScopedCommandAppliesWorkingDirectoryAndEffectiveEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("executable fixture uses a Unix shebang")
	}
	workspace := t.TempDir()
	expectedDir, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "probe")
	script := `#!/bin/sh
printf '%s|%s|%s|%s|%s' "$(pwd)" "$AO_TEST_INHERITED" "$AO_TEST_OVERLAY" "${AO_TEST_MASK+x}" "$AO_TEST_MASK"
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AO_TEST_INHERITED", "inherited")
	t.Setenv("AO_TEST_MASK", "inherited")

	out, err := RunScopedCommand(context.Background(), ports.AgentAuthCheck{
		WorkingDir: workspace,
		Env: map[string]string{
			"AO_TEST_OVERLAY": "scoped",
			"AO_TEST_MASK":    "",
		},
	}, binary)
	if err != nil {
		t.Fatal(err)
	}
	want := expectedDir + "|inherited|scoped|x|"
	if string(out) != want {
		t.Fatalf("scope = %q, want %q", out, want)
	}
}
