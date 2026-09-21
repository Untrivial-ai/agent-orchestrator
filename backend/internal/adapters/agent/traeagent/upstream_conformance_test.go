package traeagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	conformanceBinaryEnv = "AO_TRAE_CONFORMANCE_BINARY"
	conformanceSHAEnv    = "AO_TRAE_CONFORMANCE_SHA256"
)

// TestTraeUpstreamConformance is an opt-in gate for an explicitly selected
// executable. It uses disposable HOME, XDG, AO data, and workspace roots and
// exposes no ambient provider credentials. Help output establishes syntax only;
// the contract remains fail-closed until behavioral probes prove the fields in
// Contract for an official release.
func TestTraeUpstreamConformance(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv(conformanceBinaryEnv))
	if binary == "" {
		t.Skipf("set %s to an absolute trae-cli path to run the live gate", conformanceBinaryEnv)
	}
	if !filepath.IsAbs(binary) {
		t.Fatalf("%s must be an absolute path, got %q", conformanceBinaryEnv, binary)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatalf("stat conformance binary: %v", err)
	}
	if info.IsDir() {
		t.Fatalf("conformance binary %q is a directory", binary)
	}
	hash, err := verifyFileSHA256(binary, os.Getenv(conformanceSHAEnv))
	if err != nil {
		t.Fatalf("verify conformance binary: %v", err)
	}

	home := t.TempDir()
	xdg := t.TempDir()
	aoData := t.TempDir()
	workspace := t.TempDir()
	environment := isolatedConformanceEnv(home, xdg, aoData)
	before, err := snapshotTrees(home, xdg, aoData, workspace)
	if err != nil {
		t.Fatalf("snapshot disposable inputs: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	versionOutput := runConformanceCommand(ctx, t, binary, workspace, environment, "--version")
	helpOutput := runConformanceCommand(ctx, t, binary, workspace, environment, "--help")
	runHelp := runConformanceCommand(ctx, t, binary, workspace, environment, "run", "--help")
	interactiveHelp := runConformanceCommand(ctx, t, binary, workspace, environment, "interactive", "--help")

	after, err := snapshotTrees(home, xdg, aoData, workspace)
	if err != nil {
		t.Fatalf("snapshot disposable inputs after probe: %v", err)
	}
	if diff := diffSnapshots(before, after); diff != "" {
		t.Fatalf("read-only conformance probes changed profile/workspace files:\n%s", diff)
	}

	surface := parseCommandSurface(
		string(versionOutput),
		string(helpOutput),
		string(runHelp),
		string(interactiveHelp),
	)
	contract := contractFromCommandSurface(surface)
	t.Logf("binary=%s sha256=%s surface=%+v contract=%+v", binary, hash, surface, contract)

	t.Run("TUI contract", func(t *testing.T) {
		if err := ValidateTUIContract(contract); err != nil {
			t.Fatalf("Trae Agent TUI contract is not proven; do not register the terminal harness: %v", err)
		}
	})
	t.Run("ACP contract", func(t *testing.T) {
		if err := ValidateACPContract(contract); err != nil {
			t.Fatalf("Trae Agent ACP contract is not proven; do not register the Chat driver: %v", err)
		}
	})
}

func TestParseCommandSurfaceRecordsSyntaxOnly(t *testing.T) {
	surface := parseCommandSurface(
		"trae-cli, version 0.1.0\n",
		"Commands:\n  interactive\n  run\n",
		"Usage: trae-cli run [OPTIONS] [TASK]\n  --provider TEXT\n  --model TEXT\n  --config-file PATH\n  --trajectory-file PATH\n",
		"Usage: trae-cli interactive [OPTIONS]\n  --provider TEXT\n  --model TEXT\n  --config-file PATH\n  --trajectory-file PATH\n",
	)
	if surface.Version != AuditedVersion || !surface.RunTaskArgument || !surface.Interactive || !surface.Model || !surface.Provider || !surface.ConfigFile || !surface.TrajectoryFile {
		t.Fatalf("parseCommandSurface() = %+v", surface)
	}
	if surface.InteractiveInitialPrompt || surface.SystemPrompt || surface.Hooks || surface.Permissions || surface.ResumeByID || surface.ACP || surface.AuthStatus {
		t.Fatalf("parseCommandSurface() invented unsupported syntax: %+v", surface)
	}
	contract := contractFromCommandSurface(surface)
	if contract.Version != AuditedVersion {
		t.Fatalf("contract version = %q, want %q", contract.Version, AuditedVersion)
	}
	if contract.ModelConfiguration {
		t.Fatal("help syntax must not prove model configuration behavior")
	}
}

func TestIsolatedConformanceEnvScrubsCredentialsAndConfig(t *testing.T) {
	t.Setenv("PATH", "/test/bin")
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("ANTHROPIC_API_KEY", "must-not-leak")
	t.Setenv("TRAE_CONFIG_FILE", "/real/config.yaml")
	t.Setenv("PYTHONHOME", "/real/python-home")
	t.Setenv("PYTHONPATH", "/real/python-path")
	t.Setenv("HOME", "/real/home")

	environment := isolatedConformanceEnv("/isolated/home", "/isolated/xdg", "/isolated/ao")
	want := map[string]string{
		"PATH":            "/test/bin",
		"HOME":            "/isolated/home",
		"XDG_CONFIG_HOME": "/isolated/xdg/config",
		"XDG_DATA_HOME":   "/isolated/xdg/data",
		"XDG_STATE_HOME":  "/isolated/xdg/state",
		"AO_DATA_DIR":     "/isolated/ao",
	}
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "TRAE_CONFIG_FILE", "PYTHONHOME", "PYTHONPATH":
			t.Fatalf("isolatedConformanceEnv() leaked %s", key)
		}
		if expected, ok := want[key]; ok {
			if value != expected {
				t.Errorf("isolatedConformanceEnv() %s = %q, want %q", key, value, expected)
			}
			delete(want, key)
		}
	}
	for key, value := range want {
		t.Errorf("isolatedConformanceEnv() missing %s=%q", key, value)
	}
}

func TestVerifyFileSHA256AndSnapshotMutationDetection(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "trae-cli")
	if err := os.WriteFile(path, []byte("test executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest, err := hashFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := verifyFileSHA256(path, strings.ToUpper(digest)); err != nil || got != digest {
		t.Fatalf("verifyFileSHA256() = %q, %v; want %q, nil", got, err, digest)
	}
	if _, err := verifyFileSHA256(path, "not-a-digest"); err == nil {
		t.Fatal("verifyFileSHA256() accepted a malformed digest")
	}

	before, err := snapshotTrees(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := snapshotTrees(root)
	if err != nil {
		t.Fatal(err)
	}
	if diff := diffSnapshots(before, after); !strings.Contains(diff, "modified "+path) {
		t.Fatalf("diffSnapshots() = %q, want modified path", diff)
	}
}

func TestSnapshotIncludesAODataRoot(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	aoData := t.TempDir()
	workspace := t.TempDir()
	before, err := snapshotTrees(home, xdg, aoData, workspace)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(aoData, "unexpected")
	if err := os.WriteFile(path, []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := snapshotTrees(home, xdg, aoData, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if diff := diffSnapshots(before, after); !strings.Contains(diff, "added "+path) {
		t.Fatalf("diffSnapshots() = %q, want AO data addition", diff)
	}
}

type commandSurface struct {
	Version                  string
	RunTaskArgument          bool
	Interactive              bool
	InteractiveInitialPrompt bool
	SystemPrompt             bool
	Hooks                    bool
	Permissions              bool
	ResumeByID               bool
	Model                    bool
	Provider                 bool
	ConfigFile               bool
	TrajectoryFile           bool
	ACP                      bool
	AuthStatus               bool
}

func parseCommandSurface(versionOutput, helpOutput, runHelp, interactiveHelp string) commandSurface {
	versionText := strings.TrimSpace(versionOutput)
	if version, ok := parseSemver(versionOutput); ok {
		versionText = version.String()
	}
	root := strings.ToLower(helpOutput)
	run := strings.ToLower(runHelp)
	interactive := strings.ToLower(interactiveHelp)
	all := strings.Join([]string{root, run, interactive}, "\n")
	return commandSurface{
		Version:                  versionText,
		RunTaskArgument:          regexp.MustCompile(`(?m)^usage:.*(?:\[task\]|\btask\b)`).MatchString(run),
		Interactive:              strings.Contains(root, "interactive"),
		InteractiveInitialPrompt: hasValueFlag(interactive, "--message") || hasValueFlag(interactive, "--prompt"),
		SystemPrompt:             hasFlag(all, "--system-prompt") || hasFlag(all, "--system-prompt-file") || hasFlag(all, "--append-system-prompt"),
		Hooks:                    hasFlag(all, "--hooks") || hasFlag(all, "--hooks-file") || hasFlag(all, "--hook-config"),
		Permissions:              hasFlag(all, "--permission-mode") || hasFlag(all, "--approval-mode"),
		ResumeByID:               hasValueFlag(all, "--resume") || hasValueFlag(all, "--session-id"),
		Model:                    hasValueFlag(all, "--model"),
		Provider:                 hasValueFlag(all, "--provider"),
		ConfigFile:               hasValueFlag(all, "--config-file"),
		TrajectoryFile:           hasValueFlag(all, "--trajectory-file"),
		ACP:                      hasFlag(all, "--acp"),
		AuthStatus:               strings.Contains(root, "auth status") || strings.Contains(root, "auth-status"),
	}
}

// contractFromCommandSurface deliberately carries forward only the version.
// A displayed option cannot prove behavioral isolation, durable identity,
// exact restore, cancellation, permissions, or ACP semantics.
func contractFromCommandSurface(surface commandSurface) Contract {
	return Contract{Version: surface.Version}
}

func hasFlag(help, flag string) bool {
	return regexp.MustCompile(`(^|[\s,])` + regexp.QuoteMeta(flag) + `([\s=,]|$)`).MatchString(help)
}

func hasValueFlag(help, flag string) bool {
	return regexp.MustCompile(`(^|[\s,])` + regexp.QuoteMeta(flag) + `(?:=|\s+)\[?[a-z][a-z0-9_-]*\]?([\s,]|$)`).MatchString(help)
}

func isolatedConformanceEnv(home, xdg, aoData string) []string {
	allowed := map[string]bool{
		"COLORTERM":     true,
		"LANG":          true,
		"LC_ALL":        true,
		"LC_CTYPE":      true,
		"NO_COLOR":      true,
		"PATH":          true,
		"SSL_CERT_DIR":  true,
		"SSL_CERT_FILE": true,
		"TEMP":          true,
		"TERM":          true,
		"TMP":           true,
		"TMPDIR":        true,
	}
	filtered := make([]string, 0, len(allowed)+5)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if allowed[strings.ToUpper(key)] {
			filtered = append(filtered, entry)
		}
	}
	return append(
		filtered,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(xdg, "config"),
		"XDG_DATA_HOME="+filepath.Join(xdg, "data"),
		"XDG_STATE_HOME="+filepath.Join(xdg, "state"),
		"AO_DATA_DIR="+aoData,
	)
}

func runConformanceCommand(ctx context.Context, t *testing.T, binary, workspace string, environment []string, args ...string) []byte {
	t.Helper()
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = workspace
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s %s: %v\n%s", binary, strings.Join(args, " "), err, output)
	}
	return output
}

type fileSnapshot map[string]string

func snapshotTrees(roots ...string) (fileSnapshot, error) {
	snapshot := make(fileSnapshot)
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if entry.IsDir() {
				snapshot[path] = fmt.Sprintf("dir:%#o", info.Mode().Perm())
				return nil
			}
			digest, err := hashFile(path)
			if err != nil {
				return err
			}
			snapshot[path] = fmt.Sprintf("file:%#o:%s", info.Mode().Perm(), digest)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return snapshot, nil
}

func hashFile(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:]), nil
}

func verifyFileSHA256(path, expected string) (string, error) {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(expected) {
		return "", fmt.Errorf("expected SHA-256 must contain 64 hexadecimal characters")
	}
	actual, err := hashFile(path)
	if err != nil {
		return "", err
	}
	if actual != expected {
		return actual, fmt.Errorf("SHA-256 = %s, want %s", actual, expected)
	}
	return actual, nil
}

func diffSnapshots(before, after fileSnapshot) string {
	keys := make(map[string]struct{}, len(before)+len(after))
	for path := range before {
		keys[path] = struct{}{}
	}
	for path := range after {
		keys[path] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for path := range keys {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)

	var differences []string
	for _, path := range ordered {
		switch {
		case before[path] == "":
			differences = append(differences, "added "+path)
		case after[path] == "":
			differences = append(differences, "removed "+path)
		case before[path] != after[path]:
			differences = append(differences, "modified "+path)
		}
	}
	return strings.Join(differences, "\n")
}
