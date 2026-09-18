//go:build !windows

package systemexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const unixAgentShellScript = `marker=$1
shift
printf '\n%s\tPATH\t%s\n' "$marker" "$PATH"
for name do
  printf '%s\tFOUND\t%s\t' "$marker" "$name"
  command -v "$name" 2>/dev/null || printf '\n'
done
printf '%s\tDONE\n' "$marker"`

func agentShellCommand(ctx context.Context, configured, marker string, names []string) (*exec.Cmd, error) {
	shell := strings.TrimSpace(configured)
	if shell == "" {
		shell = strings.TrimSpace(os.Getenv("SHELL"))
		if shell == "" {
			shell = "/bin/sh"
		}
	}
	if !supportedUnixShell(shell) {
		return nil, fmt.Errorf("unsupported login shell %q", shell)
	}
	flags := "-lc"
	if filepath.Base(shell) == "zsh" || filepath.Base(shell) == "bash" {
		flags = "-lic"
	}
	args := make([]string, 0, 4+len(names))
	args = append(args, flags, unixAgentShellScript, "ao-agent-shell", marker)
	args = append(args, names...)
	return exec.CommandContext(ctx, shell, args...), nil //nolint:gosec // shell is restricted and script is fixed.
}

func supportedUnixShell(shell string) bool {
	if !filepath.IsAbs(shell) {
		return false
	}
	switch filepath.Base(shell) {
	case "bash", "zsh", "sh":
		return true
	default:
		return false
	}
}
