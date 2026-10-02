package workerexec

import (
	"strings"
	"testing"
)

func TestCodexActivityHooksInstallInterrupt(t *testing.T) {
	args := codexActivityHookArgs("/usr/local/bin/ao")
	for _, arg := range args {
		if strings.Contains(arg, "hooks.Interrupt=") && strings.Contains(arg, "ao hooks codex interrupt") {
			return
		}
	}
	t.Fatalf("Codex launch args %q do not install the interrupt activity hook", args)
}
