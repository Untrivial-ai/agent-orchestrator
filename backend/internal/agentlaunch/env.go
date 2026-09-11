package agentlaunch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const aoBinaryName = "ao"

// PinnedPATH prepends an AO-only directory to the supplied
// PATH. It rejects executables not named ao because their directory cannot
// guarantee the identity of a bare ao command.
func PinnedPATH(executable func() (string, error), getenv func(string) string, configured map[string]string, dataDir string) (string, error) {
	exe, err := executable()
	if err != nil {
		return "", fmt.Errorf("resolve daemon executable: %w", err)
	}
	dir, err := pinDirectory(exe, dataDir)
	if err != nil {
		return "", err
	}
	if dir == "" {
		return "", fmt.Errorf("daemon executable %s is not named %q", exe, aoBinaryName)
	}
	base := configuredPATH(configured, runtime.GOOS == "windows")
	if base == "" {
		base = getenv("PATH")
	}
	if base == "" {
		return dir, nil
	}
	return dir + string(os.PathListSeparator) + base, nil
}

func configuredPATH(configured map[string]string, caseInsensitive bool) string {
	if path, ok := configured["PATH"]; ok {
		return path
	}
	if caseInsensitive {
		for key, value := range configured {
			if strings.EqualFold(key, "PATH") {
				return value
			}
		}
	}
	return ""
}

// PinnedDir returns the directory PinnedPATH would place first.
func PinnedDir(executable func() (string, error), dataDir string) string {
	exe, err := executable()
	if err != nil {
		return ""
	}
	dir, _ := pinDirectory(exe, dataDir)
	return dir
}

func pinnedDirForExecutable(exe string) string {
	name := filepath.Base(exe)
	if runtime.GOOS == "windows" {
		name = strings.TrimSuffix(strings.ToLower(name), ".exe")
	}
	if name != aoBinaryName {
		return ""
	}
	return filepath.Dir(exe)
}

// AugmentRuntimePATHForLaunchBinary prepends the resolved launch binary
// directory to the child PATH. For Node-backed CLI shims, it also prepends a
// concrete Node runtime directory so shebangs like #!/usr/bin/env node work for
// children of GUI-launched processes whose PATH may omit shell-manager setup.
func AugmentRuntimePATHForLaunchBinary(ctx context.Context, env map[string]string, argv []string, lookPath func(string) (string, error), pinnedDir string) {
	bin, ok := launchBinary(argv)
	if !ok || !filepath.IsAbs(bin) {
		return
	}
	launchDir := filepath.Dir(bin)
	if launchDir == "." || launchDir == string(filepath.Separator) {
		return
	}
	dirs := []string{launchDir}
	if isNodeLaunchBinary(bin) {
		if nodeDir := nodeRuntimeDir(ctx, lookPath); nodeDir != "" && nodeDir != launchDir {
			dirs = append(dirs, nodeDir)
		}
	}
	var parts []string
	if path := env["PATH"]; path != "" {
		parts = strings.Split(path, string(os.PathListSeparator))
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if !containsPathDir(parts, dirs[i]) {
			parts = append([]string{dirs[i]}, parts...)
		}
	}
	parts = restorePinnedDir(parts, pinnedDir)
	env["PATH"] = strings.Join(parts, string(os.PathListSeparator))
}

func restorePinnedDir(parts []string, dir string) []string {
	if dir == "" || len(parts) == 0 || parts[0] == dir {
		return parts
	}
	for i, part := range parts {
		if part != dir {
			continue
		}
		out := make([]string, 0, len(parts))
		out = append(out, dir)
		out = append(out, parts[:i]...)
		return append(out, parts[i+1:]...)
	}
	return parts
}

func launchBinary(argv []string) (string, bool) {
	if len(argv) == 0 {
		return "", false
	}
	if filepath.Base(argv[0]) != "env" {
		return argv[0], true
	}
	for _, arg := range argv[1:] {
		if strings.Contains(arg, "=") {
			continue
		}
		return arg, true
	}
	return "", false
}

func isNodeLaunchBinary(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	const maxShebangBytes = 4096
	buf := make([]byte, maxShebangBytes)
	n, _ := f.Read(buf)
	line := string(buf[:n])
	if newline := strings.IndexByte(line, '\n'); newline >= 0 {
		line = line[:newline]
	}
	if !strings.HasPrefix(line, "#!") {
		return false
	}
	for _, field := range strings.Fields(strings.TrimPrefix(line, "#!")) {
		if filepath.Base(field) == "node" {
			return true
		}
	}
	return false
}

func containsPathDir(parts []string, dir string) bool {
	for _, part := range parts {
		if part == dir {
			return true
		}
	}
	return false
}

func nodeRuntimeDir(ctx context.Context, lookPath func(string) (string, error)) string {
	if err := ctx.Err(); err != nil || runtime.GOOS == "windows" {
		return ""
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if node, err := lookPath("node"); err == nil && node != "" {
		return filepath.Dir(node)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	fnmDir := os.Getenv("FNM_DIR")
	if fnmDir == "" {
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			fnmDir = filepath.Join(xdg, "fnm")
		} else if runtime.GOOS == "darwin" {
			fnmDir = filepath.Join(home, "Library", "Application Support", "fnm")
		} else {
			fnmDir = filepath.Join(home, ".local", "share", "fnm")
		}
	}
	voltaHome := os.Getenv("VOLTA_HOME")
	if voltaHome == "" {
		voltaHome = filepath.Join(home, ".volta")
	}
	nvm := versionedNodeMatches(filepath.Join(home, ".nvm", "versions", "node", "*", "bin", "node"))
	if data, err := os.ReadFile(filepath.Join(home, ".nvm", "alias", "default")); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			nvm = preferNodeVersion(nvm, fields[0])
		}
	}
	fnmMatches := versionedNodeMatches(filepath.Join(fnmDir, "node-versions", "*", "installation", "bin", "node"))
	candidates := make([]string, 0, len(nvm)+len(fnmMatches)+3)
	candidates = append(candidates, nvm...)
	candidates = append(candidates, fnmMatches...)
	// Prefer explicitly selected/versioned runtimes over manager and package-
	// manager shims. A dormant ~/.volta installation must not override the NVM
	// default or newest fnm runtime merely because the GUI omitted shell setup.
	candidates = append(candidates, filepath.Join(voltaHome, "bin", "node"), "/opt/homebrew/bin/node", "/usr/local/bin/node")
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return ""
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return filepath.Dir(candidate)
		}
	}
	return ""
}

func versionedNodeMatches(pattern string) []string {
	matches, _ := filepath.Glob(pattern)
	sort.SliceStable(matches, func(i, j int) bool {
		return compareNodeVersion(nodeVersionFromPath(matches[i]), nodeVersionFromPath(matches[j])) > 0
	})
	return matches
}

func nodeVersionFromPath(path string) string {
	dir := filepath.Dir(path)
	if filepath.Base(dir) == "bin" {
		dir = filepath.Dir(dir)
	}
	if filepath.Base(dir) == "installation" {
		dir = filepath.Dir(dir)
	}
	return filepath.Base(dir)
}

func preferNodeVersion(paths []string, version string) []string {
	version = normalizeNodeVersion(version)
	for i, path := range paths {
		if normalizeNodeVersion(nodeVersionFromPath(path)) != version {
			continue
		}
		out := make([]string, 0, len(paths))
		out = append(out, path)
		out = append(out, paths[:i]...)
		out = append(out, paths[i+1:]...)
		return out
	}
	return paths
}

func compareNodeVersion(a, b string) int {
	av, aok := parseNodeVersion(a)
	bv, bok := parseNodeVersion(b)
	for i := range av {
		if av[i] != bv[i] {
			if av[i] > bv[i] {
				return 1
			}
			return -1
		}
	}
	if aok != bok {
		if aok {
			return 1
		}
		return -1
	}
	return strings.Compare(a, b)
}

func parseNodeVersion(version string) ([3]int, bool) {
	var parsed [3]int
	fields := strings.Split(normalizeNodeVersion(version), ".")
	if len(fields) == 0 || fields[0] == "" {
		return parsed, false
	}
	for i := 0; i < len(fields) && i < len(parsed); i++ {
		n, err := strconv.Atoi(fields[i])
		if err != nil {
			return [3]int{}, false
		}
		parsed[i] = n
	}
	return parsed, true
}

func normalizeNodeVersion(version string) string {
	return strings.TrimPrefix(strings.TrimSpace(version), "v")
}
