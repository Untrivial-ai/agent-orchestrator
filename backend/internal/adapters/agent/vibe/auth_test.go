package vibe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type vibeInjectedAuthChecker interface {
	authStatusFor(context.Context, ports.AgentAuthCheck, authutil.Dependencies) (ports.AgentAuthStatus, error)
}

func TestVibeAuthStatusForImplementsScopedChecker(t *testing.T) {
	if _, ok := any(&Plugin{resolvedBinary: "vibe"}).(ports.AgentScopedAuthChecker); !ok {
		t.Fatal("Vibe does not implement ports.AgentScopedAuthChecker")
	}
}

func TestVibeAuthStatusUsesDefaultProviderKeyOnly(t *testing.T) {
	for _, test := range []struct {
		name string
		env  map[string]string
		want ports.AgentAuthStatus
	}{
		{name: "default Mistral key", env: map[string]string{"MISTRAL_API_KEY": "fixture-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "legacy Vibe key rejected", env: map[string]string{"VIBE_CODE_API_KEY": "fixture-key"}, want: ports.AgentAuthStatusUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.env["HOME"] = root
			got := runVibeAuth(t, ports.AgentAuthCheck{}, test.env)
			if got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}

func TestVibeAuthStatusMergesProjectModelAndProviderByIdentity(t *testing.T) {
	root := t.TempDir()
	vibeHome := filepath.Join(root, "vibe-home")
	project := filepath.Join(root, "workspace", "nested")
	writeVibeFixture(t, filepath.Join(vibeHome, "config.toml"), `
active_model = "shared"

[[providers]]
name = "global-provider"
api_base = "https://global.invalid/v1"
api_key_env_var = "GLOBAL_KEY"

[[providers]]
name = "project-provider"
api_base = "https://old.invalid/v1"
api_key_env_var = "OLD_PROJECT_KEY"

[[models]]
name = "global-model"
alias = "shared"
provider = "global-provider"
`)
	writeVibeFixture(t, filepath.Join(root, "workspace", ".vibe", "config.toml"), `
[[providers]]
name = "project-provider"
api_base = "https://project.invalid/v1"
api_key_env_var = "PROJECT_KEY"

[[models]]
name = "project-model"
alias = "shared"
provider = "project-provider"
`)

	env := map[string]string{
		"HOME": root, "VIBE_HOME": vibeHome,
		"PROJECT_KEY": "selected",
	}
	writeVibeFixture(t, filepath.Join(vibeHome, "trusted_folders.toml"), fmt.Sprintf("trusted = [%q]\n", filepath.Join(root, "workspace")))
	got := runVibeAuth(t, ports.AgentAuthCheck{WorkingDir: project}, env)
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestVibeAuthStatusUsesOnlySelectedProviderEnvironment(t *testing.T) {
	root := t.TempDir()
	vibeHome := filepath.Join(root, "vibe-home")
	writeVibeFixture(t, filepath.Join(vibeHome, "config.toml"), `
active_model = "custom"

[[providers]]
name = "custom-provider"
api_base = "https://custom.invalid/v1"
api_key_env_var = "CUSTOM_VIBE_KEY"

[[models]]
name = "custom-model"
alias = "custom"
provider = "custom-provider"
`)

	base := map[string]string{
		"HOME": root, "VIBE_HOME": vibeHome,
		"MISTRAL_API_KEY": "unrelated", "OTHER_PROVIDER_KEY": "unrelated",
	}
	if got := runVibeAuth(t, ports.AgentAuthCheck{}, base); got != ports.AgentAuthStatusUnknown {
		t.Fatalf("unrelated keys status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
	base["CUSTOM_VIBE_KEY"] = "selected"
	if got := runVibeAuth(t, ports.AgentAuthCheck{}, base); got != ports.AgentAuthStatusConfigured {
		t.Fatalf("selected key status = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestVibeAuthStatusReadsCompatibleDotenvAfterProcessEnvironment(t *testing.T) {
	root := t.TempDir()
	vibeHome := filepath.Join(root, "vibe-home")
	writeVibeFixture(t, filepath.Join(vibeHome, "config.toml"), `
active_model = "custom"

[[providers]]
name = "custom-provider"
api_base = "https://custom.invalid/v1"
api_key_env_var = "CUSTOM_VIBE_KEY"

[[models]]
name = "custom-model"
alias = "custom"
provider = "custom-provider"
`)
	writeVibeFixture(t, filepath.Join(vibeHome, ".env"), "# compatible dotenv\nexport CUSTOM_VIBE_KEY=\"fixture value\" # comment\n")

	got := runVibeAuth(t, ports.AgentAuthCheck{}, map[string]string{"HOME": root, "VIBE_HOME": vibeHome})
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestVibeAuthStatusReturnsNotApplicableForSelectedLlamaCpp(t *testing.T) {
	root := t.TempDir()
	vibeHome := filepath.Join(root, "vibe-home")
	writeVibeFixture(t, filepath.Join(vibeHome, "config.toml"), `active_model = "local"`)

	got := runVibeAuth(t, ports.AgentAuthCheck{}, map[string]string{"HOME": root, "VIBE_HOME": vibeHome})
	if got != ports.AgentAuthStatusNotApplicable {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusNotApplicable)
	}
}

func TestVibeAuthStatusAppliesRuntimeLayersInNativePrecedence(t *testing.T) {
	root := t.TempDir()
	vibeHome := filepath.Join(root, "vibe-home")
	project := filepath.Join(root, "workspace")
	dataDir := filepath.Join(root, "ao-data")
	agentRoot := filepath.Join(dataDir, "prompts", "session-1", "vibe")
	writeVibeFixture(t, filepath.Join(vibeHome, "config.toml"), `
active_model = "user"

[[providers]]
name = "user-provider"
api_base = "https://user.invalid/v1"
api_key_env_var = "USER_KEY"

[[providers]]
name = "project-provider"
api_base = "https://project.invalid/v1"
api_key_env_var = "PROJECT_KEY"

[[providers]]
name = "env-provider"
api_base = "https://env.invalid/v1"
api_key_env_var = "ENV_KEY"

[[providers]]
name = "runtime-provider"
api_base = "https://runtime.invalid/v1"
api_key_env_var = "RUNTIME_KEY"

[[models]]
name = "user-model"
alias = "user"
provider = "user-provider"

[[models]]
name = "project-model"
alias = "project"
provider = "project-provider"

[[models]]
name = "env-model"
alias = "env"
provider = "env-provider"

[[models]]
name = "runtime-model"
alias = "runtime"
provider = "runtime-provider"
`)
	writeVibeFixture(t, filepath.Join(project, ".vibe", "config.toml"), `active_model = "project"`)
	writeVibeFixture(t, filepath.Join(agentRoot, ".vibe", "agents", "review.toml"), `
active_model = "profile"

[[providers]]
name = "profile-provider"
api_base = "https://profile.invalid/v1"
api_key_env_var = "PROFILE_KEY"

[[models]]
name = "profile-model"
alias = "profile"
provider = "profile-provider"
`)

	baseEnv := map[string]string{
		"HOME": root, "VIBE_HOME": vibeHome, "VIBE_ACTIVE_MODEL": "env",
	}
	t.Run("environment overrides project", func(t *testing.T) {
		env := cloneStringMap(baseEnv)
		env["ENV_KEY"] = "selected"
		got := runVibeAuth(t, ports.AgentAuthCheck{WorkingDir: project}, env)
		if got != ports.AgentAuthStatusConfigured {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
		}
	})
	t.Run("AO model overrides environment", func(t *testing.T) {
		env := cloneStringMap(baseEnv)
		env["RUNTIME_KEY"] = "selected"
		got := runVibeAuth(t, ports.AgentAuthCheck{
			WorkingDir: project,
			Config:     ports.AgentConfig{Model: "runtime"},
		}, env)
		if got != ports.AgentAuthStatusConfigured {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
		}
	})
	t.Run("agent profile overrides AO model", func(t *testing.T) {
		env := cloneStringMap(baseEnv)
		env["PROFILE_KEY"] = "selected"
		got := runVibeAuth(t, ports.AgentAuthCheck{
			WorkingDir: project,
			DataDir:    dataDir,
			Config:     ports.AgentConfig{Model: "runtime"},
			Args:       []string{"vibe", "--add-dir", agentRoot, "--agent", "review"},
		}, env)
		if got != ports.AgentAuthStatusConfigured {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
		}
	})
}

func TestVibeAuthStatusUnknownAliasFallsBackToDefault(t *testing.T) {
	root := t.TempDir()
	vibeHome := filepath.Join(root, "vibe-home")
	writeVibeFixture(t, filepath.Join(vibeHome, "config.toml"), `active_model = "removed-alias"`)

	got := runVibeAuth(t, ports.AgentAuthCheck{}, map[string]string{
		"HOME": root, "VIBE_HOME": vibeHome, vibeDefaultAPIKeyEnvVar: "fixture-key",
	})
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want default model status %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestVibeAuthStatusProviderWithoutAPIKeyIsNotApplicable(t *testing.T) {
	root := t.TempDir()
	vibeHome := filepath.Join(root, "vibe-home")
	writeVibeFixture(t, filepath.Join(vibeHome, "config.toml"), `
active_model = "keyless"

[[providers]]
name = "keyless-provider"
api_base = "http://127.0.0.1:9000/v1"
api_key_env_var = ""

[[models]]
name = "local-model"
alias = "keyless"
provider = "keyless-provider"
`)

	got := runVibeAuth(t, ports.AgentAuthCheck{}, map[string]string{"HOME": root, "VIBE_HOME": vibeHome})
	if got != ports.AgentAuthStatusNotApplicable {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusNotApplicable)
	}
}

func TestVibeAuthStatusAppliesBuiltinAgentModelOverride(t *testing.T) {
	root := t.TempDir()
	vibeHome := filepath.Join(root, "vibe-home")
	writeVibeFixture(t, filepath.Join(vibeHome, "config.toml"), `active_model = "local"`)

	got := runVibeAuth(t, ports.AgentAuthCheck{Args: []string{"vibe", "--agent", "lean"}}, map[string]string{
		"HOME": root, "VIBE_HOME": vibeHome, vibeDefaultAPIKeyEnvVar: "fixture-key",
	})
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestVibeAuthStatusRejectsMissingSelectedAgentProfile(t *testing.T) {
	root := t.TempDir()
	vibeHome := filepath.Join(root, "vibe-home")
	writeVibeFixture(t, filepath.Join(vibeHome, "config.toml"), `active_model = "local"`)

	got := runVibeAuth(t, ports.AgentAuthCheck{Args: []string{"vibe", "--agent", "missing"}}, map[string]string{
		"HOME": root, "VIBE_HOME": vibeHome,
	})
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestVibeAuthStatusReturnsUnknownForMalformedOrUnresolvableConfig(t *testing.T) {
	for _, config := range []string{
		`active_model = [`,
		`active_model = "missing"`,
	} {
		root := t.TempDir()
		vibeHome := filepath.Join(root, "vibe-home")
		writeVibeFixture(t, filepath.Join(vibeHome, "config.toml"), config)
		got := runVibeAuth(t, ports.AgentAuthCheck{}, map[string]string{"HOME": root, "VIBE_HOME": vibeHome})
		if got != ports.AgentAuthStatusUnknown {
			t.Fatalf("config %q status = %q, want %q", config, got, ports.AgentAuthStatusUnknown)
		}
	}
}

func runVibeAuth(t *testing.T, check ports.AgentAuthCheck, env map[string]string) ports.AgentAuthStatus {
	t.Helper()
	plugin := &Plugin{resolvedBinary: "vibe"}
	checker, ok := any(plugin).(vibeInjectedAuthChecker)
	if !ok {
		t.Fatal("Vibe does not expose the injected auth resolver")
	}
	deps := authutil.Dependencies{
		Getenv: func(name string) string { return env[name] },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("Vibe auth discovery executed a command")
			return nil, errors.New("unexpected command")
		},
	}
	status, err := checker.authStatusFor(context.Background(), check, deps)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func writeVibeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func cloneStringMap(input map[string]string) map[string]string {
	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func TestVibeProjectConfigRequiresClosestTrustDecision(t *testing.T) {
	for _, tc := range []struct {
		name               string
		trusted, untrusted bool
		want               ports.AgentAuthStatus
	}{
		{"undecided", false, false, ports.AgentAuthStatusConfigured},
		{"trusted parent", true, false, ports.AgentAuthStatusNotApplicable},
		{"untrusted child", true, true, ports.AgentAuthStatusConfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			vibeHome := filepath.Join(home, ".vibe")
			project := filepath.Join(home, "project")
			writeVibeFixture(t, filepath.Join(project, ".vibe", "config.toml"), `active_model = "local"`)
			trust := ""
			if tc.trusted {
				trust += fmt.Sprintf("trusted = [%q]\n", project)
			}
			if tc.untrusted {
				trust += fmt.Sprintf("untrusted = [%q]\n", filepath.Join(project, ".vibe"))
			}
			writeVibeFixture(t, filepath.Join(vibeHome, "trusted_folders.toml"), trust)
			got := runVibeAuth(t, ports.AgentAuthCheck{WorkingDir: project}, map[string]string{"HOME": home, "VIBE_HOME": vibeHome, "MISTRAL_API_KEY": "fixture-key"})
			if got != tc.want {
				t.Fatalf("status=%q; want %q", got, tc.want)
			}
		})
	}
}

func TestVibeProjectSearchStopsBeforeHomeParent(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	project := filepath.Join(home, "project")
	vibeHome := filepath.Join(home, ".vibe")
	writeVibeFixture(t, filepath.Join(root, ".vibe", "config.toml"), `active_model = "local"`)
	writeVibeFixture(t, filepath.Join(vibeHome, "trusted_folders.toml"), fmt.Sprintf("trusted = [%q]\n", root))
	got := runVibeAuth(t, ports.AgentAuthCheck{WorkingDir: project}, map[string]string{"HOME": home, "VIBE_HOME": vibeHome, "MISTRAL_API_KEY": "fixture"})
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status=%q; want global configured", got)
	}
}
