package sessionmanager

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type fixedBrowserCapability string

func (f fixedBrowserCapability) Issue(_ domain.SessionID) (string, string, error) {
	return string(f), "verifier-1", nil
}

func TestSpawnEnvProjectVarsCannotOverrideInternal(t *testing.T) {
	env := spawnEnv("mer-1", "mer", "issue-9", "/data", map[string]string{
		"FOO":        "bar",
		EnvSessionID: "hacked", // a project must not override AO-internal vars
		EnvProjectID: "hacked",
	})
	if env["FOO"] != "bar" {
		t.Fatalf("FOO = %q, want bar", env["FOO"])
	}
	if env[EnvSessionID] != "mer-1" {
		t.Fatalf("AO_SESSION_ID = %q, want mer-1 (internal wins)", env[EnvSessionID])
	}
	if env[EnvProjectID] != "mer" {
		t.Fatalf("AO_PROJECT_ID = %q, want mer (internal wins)", env[EnvProjectID])
	}
}

func TestRuntimeEnvInjectsBrowserCapability(t *testing.T) {
	manager := &Manager{
		dataDir:             "/data",
		browserCapabilities: fixedBrowserCapability("capability-1"),
		executable:          func() (string, error) { return filepath.Join("/opt", "aod", "ao"), nil },
		logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	env, verifier, err := manager.launchRuntimeEnv("mer-1", "mer", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if env[EnvBrowserCapability] != "capability-1" {
		t.Fatalf("%s = %q", EnvBrowserCapability, env[EnvBrowserCapability])
	}
	if verifier != "verifier-1" {
		t.Fatalf("verifier = %q", verifier)
	}
}

func TestRuntimeEnvClearsDaemonBrowserRuntimeSecrets(t *testing.T) {
	manager := &Manager{
		dataDir:    "/data",
		executable: func() (string, error) { return filepath.Join("/opt", "aod", "ao"), nil },
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	env := manager.runtimeEnv("mer-1", "mer", "", map[string]string{
		EnvBrowserRuntimeToken:      "runtime-secret",
		EnvBrowserRuntimeTokenStdin: "1",
	})
	if env[EnvBrowserRuntimeToken] != "" || env[EnvBrowserRuntimeTokenStdin] != "" {
		t.Fatalf("daemon browser runtime credentials leaked to worker: token=%q stdin=%q", env[EnvBrowserRuntimeToken], env[EnvBrowserRuntimeTokenStdin])
	}
}

func TestRuntimeEnvPinsHooksToDaemonRunFile(t *testing.T) {
	daemonRunFile := filepath.Join(t.TempDir(), "daemon-running.json")
	t.Setenv("AO_RUN_FILE", filepath.Join(t.TempDir(), "inherited-wrong-daemon.json"))
	manager := &Manager{
		dataDir:     "/data",
		runFilePath: daemonRunFile,
		executable:  func() (string, error) { return filepath.Join("/opt", "aod", "ao"), nil },
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	env := manager.runtimeEnv("mer-1", "mer", "", map[string]string{
		"AO_RUN_FILE": "/project/cannot-redirect-hooks.json",
	})
	if got, want := env["AO_RUN_FILE"], daemonRunFile; got != want {
		t.Fatalf("AO_RUN_FILE = %q, want daemon run-file %q", got, want)
	}
}

func TestHookPATH(t *testing.T) {
	sep := string(os.PathListSeparator)
	daemonExe := filepath.Join("/opt", "aod", "ao")
	daemonDir := filepath.Dir(daemonExe)
	exeOK := func() (string, error) { return daemonExe, nil }

	cases := []struct {
		name       string
		executable func() (string, error)
		daemonPATH string
		projectEnv map[string]string
		want       string
		wantErr    bool
	}{
		{
			name:       "prepends daemon dir to inherited PATH",
			executable: exeOK,
			daemonPATH: "/usr/bin" + sep + "/bin",
			want:       daemonDir + sep + "/usr/bin" + sep + "/bin",
		},
		{
			name:       "project PATH override is the base",
			executable: exeOK,
			daemonPATH: "/usr/bin",
			projectEnv: map[string]string{"PATH": "/proj/bin"},
			want:       daemonDir + sep + "/proj/bin",
		},
		{
			name:       "empty base PATH yields the daemon dir alone",
			executable: exeOK,
			want:       daemonDir,
		},
		{
			name:       "unresolvable executable fails",
			executable: func() (string, error) { return "", errors.New("no exe") },
			daemonPATH: "/usr/bin",
			wantErr:    true,
		},
		{
			// A daemon binary not named "ao" cannot anchor `ao` resolution by
			// having its directory prepended, so the pin must be refused.
			name:       "executable not named ao fails",
			executable: func() (string, error) { return filepath.Join("/opt", "aod", "ao-daemon"), nil },
			daemonPATH: "/usr/bin",
			wantErr:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(key string) string {
				if key == "PATH" {
					return tc.daemonPATH
				}
				return ""
			}
			got, err := HookPATH(tc.executable, getenv, tc.projectEnv)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("HookPATH = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("HookPATH: %v", err)
			}
			if got != tc.want {
				t.Fatalf("HookPATH = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEffectiveHarnessAndAgentConfig(t *testing.T) {
	cfg := domain.ProjectConfig{
		AgentConfig:  domain.AgentConfig{Model: "base", Mode: "low", Permissions: domain.PermissionModeAuto},
		Worker:       domain.RoleOverride{Harness: domain.HarnessCodex, AgentConfig: domain.AgentConfig{Model: "worker", Mode: "high"}},
		Orchestrator: domain.RoleOverride{Harness: domain.HarnessClaudeCode},
	}

	// Explicit harness always wins.
	if h := effectiveHarness(domain.HarnessAider, domain.KindWorker, cfg); h != domain.HarnessAider {
		t.Fatalf("explicit harness = %q, want aider", h)
	}
	// Empty harness falls back to the role override per kind.
	if h := effectiveHarness("", domain.KindWorker, cfg); h != domain.HarnessCodex {
		t.Fatalf("worker harness = %q, want codex", h)
	}
	if h := effectiveHarness("", domain.KindOrchestrator, cfg); h != domain.HarnessClaudeCode {
		t.Fatalf("orchestrator harness = %q, want claude-code", h)
	}

	// Role override merges over the base agent config (set fields win; unset keep base).
	got := effectiveAgentConfig(domain.KindWorker, cfg)
	if got.Model != "worker" || got.Mode != "high" || got.Permissions != domain.PermissionModeAuto {
		t.Fatalf("merged worker config = %#v, want model=worker mode=high permissions=auto", got)
	}
	// Orchestrator has no agent-config override, so the base config is used as-is.
	if got := effectiveAgentConfig(domain.KindOrchestrator, cfg); got.Model != "base" {
		t.Fatalf("orchestrator config = %#v, want base", got)
	}
}

func TestApplySymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows symlink creation requires a host privilege outside this unit test")
	}
	project := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("X=1"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A present source is linked; a missing source is skipped, not an error.
	if err := applySymlinks(project, workspace, []string{".env", "missing.txt"}); err != nil {
		t.Fatalf("applySymlinks: %v", err)
	}
	target := filepath.Join(workspace, ".env")
	if data, err := os.ReadFile(target); err != nil || string(data) != "X=1" {
		t.Fatalf("symlinked .env = %q err=%v", data, err)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "missing.txt")); !os.IsNotExist(err) {
		t.Fatal("missing source should not have been linked")
	}
}

func TestApplySymlinksRejectsParentTraversal(t *testing.T) {
	project := t.TempDir()
	workspace := t.TempDir()
	// A "..", "/" or "../" segment escapes the project tree and must be refused
	// before any stat/link runs, so a project config cannot link in arbitrary
	// host files.
	for _, bad := range []string{"../escape", "/etc/passwd", "a/../../b", ".."} {
		if err := applySymlinks(project, workspace, []string{bad}); err == nil {
			t.Fatalf("applySymlinks(%q) accepted an unsafe path", bad)
		}
	}
}

func TestRunPostCreate(t *testing.T) {
	workspace := t.TempDir()
	if err := runPostCreate(context.Background(), workspace, []string{"echo hi > out.txt"}); err != nil {
		t.Fatalf("runPostCreate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "out.txt")); err != nil {
		t.Fatalf("post-create command did not run in workspace: %v", err)
	}
	// A failing command surfaces an error.
	if err := runPostCreate(context.Background(), workspace, []string{"exit 3"}); err == nil {
		t.Fatal("expected error from failing post-create command")
	}
}
