package kimi

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestReviewCommandUsesPlanModeAndEmptySkillsDirectory(t *testing.T) {
	r := &Reviewer{resolveBinary: func(context.Context) (string, error) { return "/opt/kimi", nil }}
	root := t.TempDir()
	inv := ports.ReviewInvocation{
		DataDir: filepath.Join(root, "ao-data"), ReviewerID: "review-worker-1",
		TaskPromptRoot: root, SystemPromptFile: filepath.Join(root, "system.md"), Prompt: "Read task.",
	}
	spec, err := r.ReviewCommand(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	skillsDir := filepath.Join(root, skillsDirectoryName)
	want := []string{"/opt/kimi", "--plan", "--auto", "--skills-dir", skillsDir}
	if !slices.Equal(spec.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", spec.Argv, want)
	}
	if !strings.Contains(spec.InitialMessage, inv.SystemPromptFile) || !strings.Contains(spec.InitialMessage, inv.Prompt) {
		t.Fatalf("initial message = %q", spec.InitialMessage)
	}
	entries, err := os.ReadDir(skillsDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("skills directory entries = %#v, %v", entries, err)
	}
	for _, forbidden := range []string{"--print", "--quiet", "--command", "--yolo"} {
		if slices.Contains(spec.Argv, forbidden) {
			t.Fatalf("argv contains forbidden flag %q: %#v", forbidden, spec.Argv)
		}
	}
}

// TestReviewCommandIsolatesKimiProfileFromHostConfiguration proves the read-only
// hardening: Kimi's CLI has no sandbox flag or tool allow/deny list (unlike
// claude-code/codex/opencode), so AO instead redirects KIMI_CODE_HOME (and the
// generic HOME/XDG/tmp variables) to an AO-owned root under DataDir, matching
// the reviewgateway pattern already shipped for agy/continueagent/goose/qwen/vibe.
// Without this, Kimi inherited the daemon's real environment and ran with
// KIMI_CODE_HOME unset, i.e. against the user's actual ~/.kimi-code profile,
// hooks, and config.
func TestReviewCommandIsolatesKimiProfileFromHostConfiguration(t *testing.T) {
	r := &Reviewer{resolveBinary: func(context.Context) (string, error) { return "/opt/kimi", nil }}
	root := t.TempDir()
	dataDir := filepath.Join(root, "ao-data")
	inv := ports.ReviewInvocation{
		DataDir: dataDir, ReviewerID: "review-worker-1",
		TaskPromptRoot: filepath.Join(root, "prompts"), SystemPromptFile: filepath.Join(root, "system.md"),
		WorkspacePath: filepath.Join(root, "checkout"), Prompt: "Read task.",
	}
	spec, err := r.ReviewCommand(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	wantConfigRoot := filepath.Join(dataDir, "reviewer-runtime", "review-worker-1", "config")
	if spec.Env["HOME"] != wantConfigRoot || spec.Env[kimiCodeHomeEnv] != wantConfigRoot {
		t.Fatalf("AO-owned Kimi profile root = %#v, want HOME and %s = %q", spec.Env, kimiCodeHomeEnv, wantConfigRoot)
	}
	if spec.Env["AO_DATA_DIR"] != dataDir {
		t.Fatalf("AO_DATA_DIR = %q, want %q", spec.Env["AO_DATA_DIR"], dataDir)
	}
	// The checkout stays the reviewer's working directory: Kimi genuinely needs
	// to read the PR locally, and only its own profile/config/credentials/hooks
	// are isolated, not the reviewed source.
	if spec.WorkingDirectory != inv.WorkspacePath {
		t.Fatalf("WorkingDirectory = %q, want the worker checkout %q", spec.WorkingDirectory, inv.WorkspacePath)
	}
}

// TestReviewCommandSeedsPrivateProfileFromHostKimiConfig mirrors
// TestReviewCommandSeedsPrivateProfileFromHostVibeHome /
// TestReviewCommandSeedsPrivateProfileFromHostConfig: the AO-owned profile must
// still authenticate, so the user's real config.toml and credentials file are
// copied (never referenced in place) into the isolated root.
func TestReviewCommandSeedsPrivateProfileFromHostKimiConfig(t *testing.T) {
	hostHome := t.TempDir()
	t.Setenv(kimiCodeHomeEnv, hostHome)
	hostConfig := []byte("api_key = \"host-key\"\n")
	if err := os.WriteFile(filepath.Join(hostHome, "config.toml"), hostConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	credentialsDir := filepath.Join(hostHome, "credentials")
	if err := os.MkdirAll(credentialsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	hostCredentials := []byte(`{"access_token":"host-token"}`)
	if err := os.WriteFile(filepath.Join(credentialsDir, "kimi-code.json"), hostCredentials, 0o600); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	r := &Reviewer{resolveBinary: func(context.Context) (string, error) { return "/opt/kimi", nil }}
	spec, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		DataDir: filepath.Join(root, "ao-data"), ReviewerID: "review-worker-1",
		TaskPromptRoot: filepath.Join(root, "prompts"), SystemPromptFile: filepath.Join(root, "system.md"),
		WorkspacePath: filepath.Join(root, "checkout"), Prompt: "Read task.",
	})
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string][]byte{
		"config.toml": hostConfig,
		filepath.Join("credentials", "kimi-code.json"): hostCredentials,
	} {
		copied := filepath.Join(spec.Env[kimiCodeHomeEnv], name)
		data, err := os.ReadFile(copied)
		if err != nil {
			t.Fatalf("read copied %s: %v", name, err)
		}
		if string(data) != string(want) {
			t.Fatalf("copied %s = %q, want %q", name, string(data), string(want))
		}
		info, err := os.Stat(copied)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("copied %s mode = %o, want 600", name, info.Mode().Perm())
		}
	}
}

// TestReviewCommandForwardsKimiAPIKeyEnvVars proves env-var-based auth (the
// alternative to config.toml/credentials) still reaches the isolated launch,
// mirroring TestReviewCommandExportsContinueAPIKey for the continue reviewer.
func TestReviewCommandForwardsKimiAPIKeyEnvVars(t *testing.T) {
	t.Setenv("MOONSHOT_API_KEY", "moonshot-key")
	root := t.TempDir()
	r := &Reviewer{resolveBinary: func(context.Context) (string, error) { return "/opt/kimi", nil }}
	spec, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{
		DataDir: filepath.Join(root, "ao-data"), ReviewerID: "review-worker-1",
		TaskPromptRoot: filepath.Join(root, "prompts"), SystemPromptFile: filepath.Join(root, "system.md"),
		Prompt: "Read task.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Env["MOONSHOT_API_KEY"] != "moonshot-key" {
		t.Fatalf("MOONSHOT_API_KEY was not forwarded into the isolated reviewer env: %#v", spec.Env)
	}
}

func TestReviewCommandPreflightValidationAndCancel(t *testing.T) {
	r := &Reviewer{resolveBinary: func(context.Context) (string, error) { return "/opt/kimi", nil }}
	spec, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{})
	if err != nil || !slices.Equal(spec.Argv, []string{"/opt/kimi", "--plan", "--auto"}) {
		t.Fatalf("spec = %#v, %v", spec, err)
	}
	if _, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{TaskPromptRoot: t.TempDir()}); err == nil {
		t.Fatal("expected missing system prompt rejection")
	}
	r.resolveBinary = func(context.Context) (string, error) { return "kimi", nil }
	if _, err := r.ReviewCommand(context.Background(), ports.ReviewInvocation{}); err == nil {
		t.Fatal("expected relative binary rejection")
	}
	cancel, err := r.ReviewCancel(context.Background())
	if err != nil || cancel.Mode != ports.ReviewCancelInterrupt || cancel.Interrupts != 2 {
		t.Fatalf("cancel = %#v, %v", cancel, err)
	}
}
