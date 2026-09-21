package deepagents

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
	conformanceBinaryEnv = "AO_DEEPAGENTS_CONFORMANCE_BINARY"
	conformanceSHAEnv    = "AO_DEEPAGENTS_CONFORMANCE_SHA256"
)

// TestDeepAgentsUpstreamConformance is intentionally opt-in. It probes a real,
// explicitly selected executable using disposable HOME, DEEPAGENTS_HOME, AO
// data, and workspace directories. Static help output can prove only the CLI
// surface. Behavioral capabilities (session identity and ACP load/replay,
// permissions, and cancellation) remain false until this test is extended with
// executable evidence for the pinned upstream release, causing the registration
// gate to fail closed rather than infer support.
func TestDeepAgentsUpstreamConformance(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv(conformanceBinaryEnv))
	if binary == "" {
		t.Skipf("set %s to an absolute dcode/deepagents-code path to run the live gate", conformanceBinaryEnv)
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

	workspace := t.TempDir()
	aoData := t.TempDir()
	home := t.TempDir()
	profile := filepath.Join(home, ".deepagents")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatalf("create empty disposable profile: %v", err)
	}

	before, err := snapshotTrees(home, workspace)
	if err != nil {
		t.Fatalf("snapshot disposable inputs: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	environment := isolatedConformanceEnv(home, profile, aoData)
	pythonOutput := runConformanceCommand(ctx, t, pythonInterpreter(t, binary), workspace, environment, "--version")
	if err := validatePythonVersion(string(pythonOutput)); err != nil {
		t.Fatal(err)
	}
	versionOutput := runConformanceCommand(ctx, t, binary, workspace, environment, "--version")
	helpOutput := runConformanceCommand(ctx, t, binary, workspace, environment, "--help")
	authHelp := runOptionalConformanceCommand(ctx, binary, workspace, environment, "auth", "status", "--help")

	after, err := snapshotTrees(home, workspace)
	if err != nil {
		t.Fatalf("snapshot disposable inputs after probe: %v", err)
	}
	if diff := diffSnapshots(before, after); diff != "" {
		t.Fatalf("read-only conformance probes changed profile/workspace files:\n%s", diff)
	}

	surface := parseCommandSurface(string(versionOutput), string(helpOutput), string(authHelp))
	contract := contractFromCommandSurface(surface)
	t.Logf("binary=%s sha256=%s version=%q surface=%+v contract=%+v", binary, hash, strings.TrimSpace(string(versionOutput)), surface, contract)

	t.Run("TUI contract", func(t *testing.T) {
		if err := ValidateTUIContract(contract); err != nil {
			t.Fatalf("DeepAgents TUI contract is not proven; do not register the terminal harness: %v", err)
		}
	})
	t.Run("ACP contract", func(t *testing.T) {
		if err := ValidateACPContract(contract); err != nil {
			t.Fatalf("DeepAgents ACP contract is not proven; do not register the Chat driver: %v", err)
		}
	})
}

func TestContractFromCommandSurface(t *testing.T) {
	surface := parseCommandSurface(
		"deepagents-code 0.1.72\n",
		"Usage: dcode [OPTIONS]\n  -m, --message TEXT\n  -r, --resume THREAD_ID\n  --acp\n  --system-prompt-file PATH\n  --hooks-file PATH\n",
		"Usage: dcode auth status PROVIDER\n",
	)
	contract := contractFromCommandSurface(surface)

	if contract.Version != "0.1.72" {
		t.Fatalf("Version = %q, want 0.1.72", contract.Version)
	}
	if !surface.SystemPromptFile || !surface.HooksFile || !surface.InitialMessage || !surface.ResumeByID || !surface.ACP || !surface.AuthStatus {
		t.Fatalf("surface = %+v", surface)
	}
	if contract.ArtifactFingerprint || contract.PreservesUserProfile || contract.SystemPromptFile || contract.SupportedOverlay || contract.IsolatedHooks || contract.InitialMessage || contract.ExactRestore || contract.TUISessionID || contract.ACP || contract.ACPNewSession || contract.ACPStableSessionID || contract.ACPLoad || contract.ACPMissingHistory || contract.ACPReplay || contract.ACPStreaming || contract.ACPPermissions || contract.ACPCancel || contract.ACPRecovery || contract.ACPWorkspaceMismatch || contract.ACPModelConfig || contract.ACPCapabilityClaims || contract.AuthStatus {
		t.Fatalf("contract inferred behavioral guarantees from help: %+v", contract)
	}
}

func TestParseCommandSurfaceRequiresRestoreValue(t *testing.T) {
	tests := []struct {
		name string
		help string
		want bool
	}{
		{name: "long value", help: "--resume THREAD_ID", want: true},
		{name: "long equals value", help: "--resume=THREAD_ID", want: true},
		{name: "short value", help: "-r THREAD_ID", want: true},
		{name: "bare long flag", help: "--resume"},
		{name: "bare short flag", help: "-r"},
		{name: "unrelated text", help: "resume the latest session"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			surface := parseCommandSurface("0.1.72", test.help, "")
			if surface.ResumeByID != test.want {
				t.Fatalf("ResumeByID = %t, want %t", surface.ResumeByID, test.want)
			}
		})
	}
}

func TestValidatePythonVersion(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantErr bool
	}{
		{name: "minimum", output: "Python 3.12.0"},
		{name: "newer minor", output: "Python 3.14.1"},
		{name: "old", output: "Python 3.11.9", wantErr: true},
		{name: "future major", output: "Python 4.0.0", wantErr: true},
		{name: "unparseable", output: "Python development", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validatePythonVersion(test.output); (err != nil) != test.wantErr {
				t.Fatalf("validatePythonVersion(%q) error = %v, wantErr %t", test.output, err, test.wantErr)
			}
		})
	}
}

func TestParseSemverRejectsPrereleaseAndExtraComponents(t *testing.T) {
	for _, value := range []string{"0.1.72rc1", "0.1.72-rc.1", "0.1.72.99"} {
		t.Run(value, func(t *testing.T) {
			if parsed, ok := parseSemver(value); ok {
				t.Fatalf("parseSemver(%q) = %s, want rejected", value, parsed)
			}
		})
	}
}

func TestIsolatedConformanceEnvScrubsCredentials(t *testing.T) {
	t.Setenv("PATH", "/test/bin")
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("PYTHONHOME", "/real/python-home")
	t.Setenv("PYTHONPATH", "/real/python-path")
	t.Setenv("XDG_CONFIG_HOME", "/real/config")
	t.Setenv("XDG_DATA_HOME", "/real/data")
	t.Setenv("XDG_STATE_HOME", "/real/state")
	t.Setenv("HOME", "/real/home")
	t.Setenv("DEEPAGENTS_HOME", "/real/profile")
	t.Setenv("AO_DATA_DIR", "/real/ao")

	environment := isolatedConformanceEnv("/isolated/home", "/isolated/profile", "/isolated/ao")
	want := map[string]string{
		"PATH":            "/test/bin",
		"HOME":            "/isolated/home",
		"DEEPAGENTS_HOME": "/isolated/profile",
		"AO_DATA_DIR":     "/isolated/ao",
	}
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		switch key {
		case "OPENAI_API_KEY", "PYTHONHOME", "PYTHONPATH", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME":
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

func TestVerifyFileSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dcode")
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
	if _, err := verifyFileSHA256(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("verifyFileSHA256() accepted a mismatched digest")
	}
}

func TestPythonInterpreterAndSnapshotMutationDetection(t *testing.T) {
	root := t.TempDir()
	wrapper := filepath.Join(root, "dcode")
	if err := os.WriteFile(wrapper, []byte("#!/usr/bin/python3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := pythonInterpreter(t, wrapper); got != "/usr/bin/python3" {
		t.Fatalf("pythonInterpreter() = %q, want /usr/bin/python3", got)
	}

	before, err := snapshotTrees(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrapper, []byte("#!/usr/bin/python3\n# changed\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := snapshotTrees(root)
	if err != nil {
		t.Fatal(err)
	}
	if diff := diffSnapshots(before, after); !strings.Contains(diff, "modified "+wrapper) {
		t.Fatalf("diffSnapshots() = %q, want modified wrapper", diff)
	}
}

// commandSurface records only syntax advertised by help output. It must never
// be treated as proof that the corresponding behavior is safe or correct.
type commandSurface struct {
	Version          string
	SystemPromptFile bool
	HooksFile        bool
	InitialMessage   bool
	ResumeByID       bool
	ACP              bool
	AuthStatus       bool
}

func parseCommandSurface(versionOutput, helpOutput, authHelp string) commandSurface {
	versionText := strings.TrimSpace(versionOutput)
	if version, ok := parseSemver(versionOutput); ok {
		versionText = version.String()
	}
	help := strings.ToLower(helpOutput)
	auth := strings.ToLower(authHelp)
	return commandSurface{
		Version:          versionText,
		SystemPromptFile: hasFlag(help, "--system-prompt-file"),
		HooksFile:        hasFlag(help, "--hooks-file"),
		InitialMessage:   hasFlag(help, "--message") || hasShortFlag(help, "-m"),
		ResumeByID:       hasValueFlag(help, "--resume") || hasValueFlag(help, "-r"),
		ACP:              hasFlag(help, "--acp"),
		AuthStatus:       strings.Contains(auth, "auth status"),
	}
}

// contractFromCommandSurface deliberately carries forward only the executable
// version. Help text can advertise a flag, but it cannot prove append-only
// prompt behavior, additive hook merging, race-free initial delivery, exact
// restore semantics, lifecycle identity, truthful auth, or any ACP behavior.
func contractFromCommandSurface(surface commandSurface) Contract {
	return Contract{Version: surface.Version}
}

func hasFlag(help, flag string) bool {
	return regexp.MustCompile(`(^|[\s,])` + regexp.QuoteMeta(flag) + `([\s=,]|$)`).MatchString(help)
}

func hasShortFlag(help, flag string) bool {
	return regexp.MustCompile(`(^|[\s,])` + regexp.QuoteMeta(flag) + `([\s,]|$)`).MatchString(help)
}

func hasValueFlag(help, flag string) bool {
	return regexp.MustCompile(`(^|[\s,])` + regexp.QuoteMeta(flag) + `(?:=|\s+)\[?[a-z][a-z0-9_-]*\]?([\s,]|$)`).MatchString(help)
}

func isolatedConformanceEnv(home, profile, aoData string) []string {
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
	filtered := make([]string, 0, len(allowed)+3)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if allowed[strings.ToUpper(key)] {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, "HOME="+home, "DEEPAGENTS_HOME="+profile, "AO_DATA_DIR="+aoData)
}

func pythonInterpreter(t *testing.T, binary string) string {
	t.Helper()
	contents, err := os.ReadFile(binary)
	if err != nil {
		t.Fatalf("read conformance binary shebang: %v", err)
	}
	firstLine, _, _ := strings.Cut(string(contents), "\n")
	if !strings.HasPrefix(firstLine, "#!") {
		t.Fatalf("cannot prove Python runtime for %q: executable has no shebang", binary)
	}
	fields := strings.Fields(strings.TrimPrefix(firstLine, "#!"))
	if len(fields) == 0 {
		t.Fatalf("cannot prove Python runtime for %q: empty shebang", binary)
	}
	if filepath.Base(fields[0]) == "env" {
		if len(fields) != 2 || !strings.HasPrefix(filepath.Base(fields[1]), "python") {
			t.Fatalf("cannot prove Python runtime for %q from shebang %q", binary, firstLine)
		}
		resolved, err := exec.LookPath(fields[1])
		if err != nil {
			t.Fatalf("resolve Python interpreter %q: %v", fields[1], err)
		}
		return resolved
	}
	if len(fields) != 1 || !strings.HasPrefix(filepath.Base(fields[0]), "python") {
		t.Fatalf("cannot prove Python runtime for %q from shebang %q", binary, firstLine)
	}
	return fields[0]
}

func validatePythonVersion(output string) error {
	version, ok := parseSemver(output)
	if !ok {
		return fmt.Errorf("unrecognized Python version %q", strings.TrimSpace(output))
	}
	if version[0] != 3 || version[1] < 12 {
		return fmt.Errorf("DeepAgents conformance requires Python >=3.12,<4; found %s", version)
	}
	return nil
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

func runOptionalConformanceCommand(ctx context.Context, binary, workspace string, environment []string, args ...string) []byte {
	command := exec.CommandContext(ctx, binary, args...)
	command.Dir = workspace
	command.Env = environment
	output, _ := command.CombinedOutput()
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
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			key := root + string(filepath.Separator) + relative
			if entry.IsDir() {
				snapshot[key] = fmt.Sprintf("dir:%#o", info.Mode().Perm())
				return nil
			}
			digest, err := hashFile(path)
			if err != nil {
				return err
			}
			snapshot[key] = fmt.Sprintf("file:%#o:%s", info.Mode().Perm(), digest)
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
			differences = append(differences, fmt.Sprintf("added %s", path))
		case after[path] == "":
			differences = append(differences, fmt.Sprintf("removed %s", path))
		case before[path] != after[path]:
			differences = append(differences, fmt.Sprintf("modified %s", path))
		}
	}
	return strings.Join(differences, "\n")
}
