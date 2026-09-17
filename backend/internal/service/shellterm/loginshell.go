package shellterm

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// resolveUserLoginShell returns the argv a standalone terminal launches.
//
// Unlike an agent session — where the argv is a specific CLI the adapter
// resolved and the runtime must be able to prove exists — a shell terminal
// wants whatever shell the user already lives in. $SHELL (unix) and ComSpec
// (Windows) are the values the OS itself uses for that question, so they are
// preferred over probing PATH for a hardcoded list.
//
// The fallbacks are last-resort only: an empty argv would be rejected by the
// runtime adapters, so a terminal that cannot name a shell must not open at
// all rather than open something unusable.
func resolveUserLoginShell(preference string) (argv []string, usedFallback bool) {
	if runtime.GOOS == "windows" {
		return resolveWindowsShell(preference)
	}
	if shell := os.Getenv("SHELL"); shell != "" {
		return []string{shell}, false
	}
	for _, candidate := range []string{"zsh", "bash", "sh"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return []string{path}, false
		}
	}
	return nil, false
}

// resolveWindowsShell honors an explicit selector first. An unavailable named
// shell or custom executable falls back to the same automatic order AO used
// before shell selection was configurable.
func resolveWindowsShell(preference string) (argv []string, usedFallback bool) {
	preference = strings.TrimSpace(preference)
	if preference == "" || strings.EqualFold(preference, "auto") {
		return resolveAutomaticWindowsShell(), false
	}

	var resolved []string
	switch strings.ToLower(preference) {
	case "git-bash":
		resolved = resolveGitBash()
	case "pwsh":
		resolved = resolveKnownWindowsShell("pwsh.exe")
	case "powershell":
		resolved = resolveKnownWindowsShell("powershell.exe")
	case "cmd":
		// ComSpec is authoritative for the user's Windows Command Prompt. It
		// must win even when cmd.exe is not separately discoverable through PATH.
		if comSpec := strings.TrimSpace(os.Getenv("ComSpec")); comSpec != "" {
			if path, err := exec.LookPath(comSpec); err == nil {
				resolved = []string{path}
			}
		}
		if len(resolved) == 0 {
			resolved = resolveKnownWindowsShell("cmd.exe")
		}
	default:
		if path, err := exec.LookPath(preference); err == nil {
			resolved = windowsShellArgv(path)
		}
	}
	if len(resolved) > 0 {
		return resolved, false
	}
	return resolveAutomaticWindowsShell(), true
}

// resolveAutomaticWindowsShell preserves AO's historical Windows behavior.
func resolveAutomaticWindowsShell() []string {
	for _, candidate := range []string{"pwsh.exe", "powershell.exe"} {
		if argv := resolveKnownWindowsShell(candidate); len(argv) > 0 {
			return argv
		}
	}
	if comSpec := os.Getenv("ComSpec"); comSpec != "" {
		return []string{comSpec}
	}
	if path, err := exec.LookPath("cmd.exe"); err == nil {
		return []string{path}
	}
	return nil
}

func resolveKnownWindowsShell(candidate string) []string {
	path, err := exec.LookPath(candidate)
	if err != nil {
		return nil
	}
	return windowsShellArgv(path)
}

func windowsShellArgv(path string) []string {
	switch strings.ToLower(filepath.Base(path)) {
	case "pwsh.exe", "powershell.exe":
		return []string{path, "-NoLogo"}
	case "bash.exe", "sh.exe":
		return []string{path, "--login", "-i"}
	default:
		return []string{path}
	}
}

func resolveGitBash() []string {
	seen := map[string]struct{}{}
	for _, candidate := range gitBashCandidates() {
		if candidate == "" {
			continue
		}
		key := strings.ToLower(filepath.Clean(candidate))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return []string{candidate, "--login", "-i"}
		}
	}
	if path, err := exec.LookPath("bash.exe"); err == nil {
		return []string{path, "--login", "-i"}
	}
	return nil
}

func gitBashCandidates() []string {
	candidates := make([]string, 0, 8)
	if gitPath, err := exec.LookPath("git.exe"); err == nil {
		gitDir := filepath.Dir(gitPath)
		gitRoot := filepath.Dir(gitDir)
		candidates = append(candidates,
			filepath.Join(gitRoot, "bin", "bash.exe"),
			filepath.Join(gitRoot, "usr", "bin", "bash.exe"),
		)
	}
	for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")} {
		if root != "" {
			candidates = append(candidates, filepath.Join(root, "Git", "bin", "bash.exe"))
		}
	}
	if localAppData := os.Getenv("LocalAppData"); localAppData != "" {
		candidates = append(candidates, filepath.Join(localAppData, "Programs", "Git", "bin", "bash.exe"))
	}
	return candidates
}
