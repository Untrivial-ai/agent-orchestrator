package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkerEnvironmentIsolatesSCMCredentialsAndProfile(t *testing.T) {
	dataDir := t.TempDir()
	hostHome := filepath.Join(t.TempDir(), "host")
	got := WorkerEnvironment([]string{
		"PATH=/tools", "HOME=" + hostHome, "USERPROFILE=" + hostHome,
		"GH_TOKEN=secret", "GITHUB_TOKEN=secret", "GITLAB_TOKEN=secret",
		"SSH_AUTH_SOCK=agent.sock", "GIT_ASKPASS=askpass.exe",
		"AO_GIT_BROKER_TOKEN=broker-secret",
	}, map[string]string{
		"AO_SESSION_ID": "session-1", "AO_DATA_DIR": dataDir,
		"GH_TOKEN": "project-secret",
	})
	env := environmentMap(got)
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN", "SSH_AUTH_SOCK", "GIT_ASKPASS", "AO_GIT_BROKER_TOKEN"} {
		if value := environmentValue(env, key); value != "" {
			t.Fatalf("%s leaked with value %q", key, value)
		}
	}
	wantHome := filepath.Join(dataDir, "workers", "session-1", "home")
	if gotHome := environmentValue(env, "HOME"); gotHome != wantHome {
		t.Fatalf("HOME = %q, want %q", gotHome, wantHome)
	}
	if gotProfile := environmentValue(env, "USERPROFILE"); gotProfile != wantHome {
		t.Fatalf("USERPROFILE = %q, want %q", gotProfile, wantHome)
	}
	if gotClaude := environmentValue(env, "CLAUDE_CONFIG_DIR"); gotClaude != filepath.Join(wantHome, ".claude") {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q", gotClaude)
	}
	if gotCodex := environmentValue(env, "CODEX_HOME"); gotCodex != filepath.Join(wantHome, ".codex") {
		t.Fatalf("CODEX_HOME = %q", gotCodex)
	}
	for key, want := range map[string]string{
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull,
		"GIT_CONFIG_KEY_0": "credential.helper", "GIT_CONFIG_VALUE_0": "",
		"GIT_TERMINAL_PROMPT": "0", "GCM_INTERACTIVE": "Never",
	} {
		if value := environmentValue(env, key); value != want {
			t.Fatalf("%s = %q, want %q", key, value, want)
		}
	}
}

func TestWorkerEnvironmentKeepsLocalGitUsable(t *testing.T) {
	got := WorkerEnvironment(os.Environ(), map[string]string{
		"AO_SESSION_ID": "local-git", "AO_DATA_DIR": t.TempDir(),
	})
	joined := strings.Join(got, "\x00")
	for _, key := range []string{"PATH=", "GIT_AUTHOR_NAME=", "GIT_AUTHOR_EMAIL="} {
		if !strings.Contains(joined, key) {
			t.Fatalf("worker environment missing %s", key)
		}
	}
}

func TestWorkerEnvironmentLeavesNonWorkerProcessOnMergePath(t *testing.T) {
	got := WorkerEnvironment([]string{"HOME=/host", "GH_TOKEN=secret"}, map[string]string{"AO_DATA_DIR": "/data"})
	env := environmentMap(got)
	if value := environmentValue(env, "GH_TOKEN"); value != "secret" {
		t.Fatalf("non-worker GH_TOKEN = %q", value)
	}
	if value := environmentValue(env, "HOME"); value != "/host" {
		t.Fatalf("non-worker HOME = %q", value)
	}
}

func TestWorkerEnvironmentRejectsHostProviderFallback(t *testing.T) {
	got := WorkerEnvironment([]string{
		"PATH=/tools", "ANTHROPIC_AUTH_TOKEN=CANARY", "ANTHROPIC_BASE_URL=https://host.invalid",
		"OPENAI_API_KEY=HOST_OPENAI", "CLAUDE_CODE_SUBAGENT_MODEL=host-model",
	}, map[string]string{"AO_SESSION_ID": "no-provider", "AO_DATA_DIR": t.TempDir()})
	env := environmentMap(got)
	for _, key := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "OPENAI_API_KEY", "CLAUDE_CODE_SUBAGENT_MODEL"} {
		if value := environmentValue(env, key); value != "" {
			t.Fatalf("Host Provider environment %s leaked with value %q", key, value)
		}
	}
}

func TestWorkerEnvironmentKeepsOnlyExplicitSelectedProviderOverlay(t *testing.T) {
	got := WorkerEnvironment([]string{
		"ANTHROPIC_AUTH_TOKEN=TASK_A_SECRET", "OPENAI_API_KEY=HOST_OPENAI",
	}, map[string]string{
		"AO_SESSION_ID": "task-b", "AO_DATA_DIR": t.TempDir(),
		"ANTHROPIC_AUTH_TOKEN": "TASK_B_SECRET", "ANTHROPIC_MODEL": "deepseek-model",
	})
	env := environmentMap(got)
	if got := environmentValue(env, "ANTHROPIC_AUTH_TOKEN"); got != "TASK_B_SECRET" {
		t.Fatalf("selected Provider token = %q", got)
	}
	if got := environmentValue(env, "OPENAI_API_KEY"); got != "" {
		t.Fatalf("unselected Host OpenAI key leaked: %q", got)
	}
}
