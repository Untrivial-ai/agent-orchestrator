package vibe

import (
	"context"
	"errors"
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
		"GLOBAL_KEY": "unrelated", "OLD_PROJECT_KEY": "stale", "PROJECT_KEY": "selected",
	}
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
