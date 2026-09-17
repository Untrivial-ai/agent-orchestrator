package processenv

import (
	"context"
	"os/exec"
	"testing"
)

func TestMergePreservesVariableSpellingForBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal("regression requires Bash: ", err)
	}
	t.Setenv("buildMode", "parent")
	t.Setenv("inheritedMode", "unchanged")
	cmd := exec.CommandContext(context.Background(), bash, "--noprofile", "--norc", "-c", `printf '%s:%s' "$buildMode" "$inheritedMode"`)
	cmd.Env = Merge(map[string]string{"buildMode": "production"})
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != "production:unchanged" {
		t.Fatalf("Bash project variable: %v: %q", err, output)
	}
}
