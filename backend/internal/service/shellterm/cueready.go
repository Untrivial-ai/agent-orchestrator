package shellterm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// cueShellReadiness installs a one-shot startup signal. It never sends input
// before the shell itself has reached its first prompt.
type cueShellReadiness struct {
	argv    []string
	env     map[string]string
	file    string
	cleanup func()
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func powershellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func prepareCueShellReadiness(dataDir string, argv []string) (cueShellReadiness, error) {
	result := cueShellReadiness{argv: append([]string(nil), argv...), env: map[string]string{}, cleanup: func() {}}
	if len(argv) == 0 {
		return result, nil
	}
	name := strings.ToLower(filepath.Base(argv[0]))
	if runtime.GOOS == "windows" {
		switch name {
		case "bash.exe", "sh.exe", "pwsh.exe", "powershell.exe", "cmd.exe":
		default:
			return result, nil
		}
	} else {
		switch name {
		case "bash", "zsh", "sh":
		default:
			return result, nil
		}
	}
	dir := filepath.Join(dataDir, "cue-readiness")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return result, fmt.Errorf("prepare cue shell readiness: %w", err)
	}
	f, err := os.CreateTemp(dir, "ready-*")
	if err != nil {
		return result, fmt.Errorf("prepare cue shell readiness: %w", err)
	}
	result.file = f.Name()
	result.env["AO_CUE_READY_FILE"] = result.file
	_ = f.Close()
	result.cleanup = func() { _ = os.Remove(result.file) }
	switch name {
	case "bash", "bash.exe":
		// Bash runs PROMPT_COMMAND immediately before rendering the first prompt.
		// A profile that replaces it will cause a safe timeout.
		hook := "if [ -z \"$AO_CUE_READY_SENT\" ]; then printf ready > " + shellSingleQuote(result.file) + "; AO_CUE_READY_SENT=1; fi"
		if previous := os.Getenv("PROMPT_COMMAND"); previous != "" {
			hook = previous + "; " + hook
		}
		result.env["PROMPT_COMMAND"] = hook
	case "sh", "sh.exe":
		// An interactive POSIX sh reads ENV before its first prompt.
		profile := result.file + ".env"
		content := ""
		if previous := os.Getenv("ENV"); previous != "" {
			content = ". " + shellSingleQuote(previous) + "\n"
		}
		content += "printf ready > " + shellSingleQuote(result.file) + "\n"
		if err := os.WriteFile(profile, []byte(content), 0o600); err != nil {
			result.cleanup()
			return cueShellReadiness{}, err
		}
		result.env["ENV"] = profile
		result.cleanup = func() { _ = os.Remove(result.file); _ = os.Remove(profile) }
	case "zsh":
		// Source the user's original startup files before installing a one-shot
		// precmd hook. Restore ZDOTDIR for subsequent shells.
		wrapper := result.file + ".zsh"
		if err := os.Mkdir(wrapper, 0o700); err != nil {
			result.cleanup()
			return cueShellReadiness{}, err
		}
		original := os.Getenv("ZDOTDIR")
		if original == "" {
			original = os.Getenv("HOME")
		}
		for _, file := range []string{".zshenv", ".zshrc"} {
			content := "if [[ -f " + shellSingleQuote(filepath.Join(original, file)) + " ]]; then source " + shellSingleQuote(filepath.Join(original, file)) + "; fi\n"
			if file == ".zshenv" {
				content += "export ZDOTDIR=" + shellSingleQuote(wrapper) + "\n"
			}
			if file == ".zshrc" {
				content += "function _ao_cue_ready { print -rn -- ready > " + shellSingleQuote(result.file) + "; precmd_functions=(${precmd_functions:#_ao_cue_ready}); }\n"
				content += "precmd_functions+=(_ao_cue_ready)\n"
				if os.Getenv("ZDOTDIR") == "" {
					content += "unset ZDOTDIR\n"
				} else {
					content += "export ZDOTDIR=" + shellSingleQuote(os.Getenv("ZDOTDIR")) + "\n"
				}
			}
			if err := os.WriteFile(filepath.Join(wrapper, file), []byte(content), 0o600); err != nil {
				_ = os.RemoveAll(wrapper)
				result.cleanup()
				return cueShellReadiness{}, err
			}
		}
		result.env["ZDOTDIR"] = wrapper
		result.cleanup = func() { _ = os.Remove(result.file); _ = os.RemoveAll(wrapper) }
	case "pwsh.exe", "powershell.exe":
		// -Command runs after the profile; preserve the user's prompt function.
		file := powershellSingleQuote(result.file)
		script := "$global:aoCueOriginalPrompt = (Get-Command prompt -CommandType Function).ScriptBlock; " +
			"function global:prompt { if (-not $global:aoCueReadySent) { " +
			"Set-Content -LiteralPath " + file + " -Value ready -NoNewline; $global:aoCueReadySent = $true }; " +
			"& $global:aoCueOriginalPrompt }"
		result.argv = append(result.argv, "-NoExit", "-Command", script)
	case "cmd.exe":
		// /K runs a tiny script after cmd AutoRun and remains interactive. Keep
		// the redirection inside the script so go-pty need only quote its path.
		script := result.file + ".cmd"
		if err := os.WriteFile(script, []byte("@echo off\r\necho ready>\""+result.file+"\"\r\n"), 0o600); err != nil {
			result.cleanup()
			return cueShellReadiness{}, err
		}
		result.argv = append(result.argv, "/K", script)
		result.cleanup = func() { _ = os.Remove(result.file); _ = os.Remove(script) }
	}
	return result, nil
}

var cueShellReadyTimeout = 10 * time.Second

func (s *Service) waitForCueShellReady(ctx context.Context, handle ports.RuntimeHandle, file string) error {
	if file == "" {
		return apierr.Conflict("CUE_SHELL_NOT_READY", "AO cannot verify that this shell is ready; the terminal remains open for manual use", nil)
	}
	waitCtx, cancel := context.WithTimeout(ctx, cueShellReadyTimeout)
	defer cancel()
	ticker := time.NewTicker(initialInputPollInterval)
	defer ticker.Stop()
	for {
		if data, err := os.ReadFile(file); err == nil && strings.TrimSpace(string(data)) == "ready" {
			return nil
		}
		alive, err := s.runtime.IsChildAlive(waitCtx, handle)
		if err == nil && !alive {
			return apierr.Conflict("CUE_SHELL_NOT_READY", "The shell exited before it was ready; the terminal remains open for inspection", nil)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-waitCtx.Done():
			if err := ctx.Err(); err != nil {
				return err
			}
			return apierr.Conflict("CUE_SHELL_NOT_READY", "The shell did not become ready; the terminal remains open for manual use", nil)
		case <-ticker.C:
		}
	}
}
