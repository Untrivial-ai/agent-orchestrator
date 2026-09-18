//go:build windows

package systemexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const windowsAgentShellScript = "[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)\n" + "$marker = $env:AO_AGENT_PROBE_MARKER\n" +
	"[Console]::Out.WriteLine(\"`n$marker`tPATH`t$env:Path\")\n" +
	"foreach ($name in $env:AO_AGENT_PROBE_NAMES.Split(';')) {\n" +
	"  $command = Get-Command -Name $name -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1\n" +
	"  if ($null -ne $command) { [Console]::Out.WriteLine(\"$marker`tFOUND`t$name`t$($command.Path)\") }\n" +
	"}\n" +
	"[Console]::Out.WriteLine(\"$marker`tDONE\")"

func agentShellCommand(ctx context.Context, configured, marker string, names []string) (*exec.Cmd, error) {
	shell := strings.TrimSpace(configured)
	if shell == "" {
		var err error
		// Match the automatic terminal preference: current PowerShell first.
		for _, candidate := range []string{"pwsh.exe", "powershell.exe"} {
			shell, err = exec.LookPath(candidate)
			if err == nil {
				break
			}
		}
		if err != nil {
			return nil, fmt.Errorf("find PowerShell: %w", err)
		}
	}
	base := strings.ToLower(filepath.Base(shell))
	if base != "powershell.exe" && base != "pwsh.exe" {
		return nil, fmt.Errorf("unsupported Windows shell %q", shell)
	}
	args := []string{"-NoLogo", "-NonInteractive", "-Command", windowsAgentShellScript}
	cmd := exec.CommandContext(ctx, shell, args...)
	cmd.Env = append(os.Environ(), "AO_AGENT_PROBE_MARKER="+marker, "AO_AGENT_PROBE_NAMES="+strings.Join(names, ";"))
	return cmd, nil //nolint:gosec // shell is restricted and script is fixed.
}
