package autohand

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type autohandInjectedAuthChecker interface {
	authStatusFor(context.Context, ports.AgentAuthCheck, authutil.Dependencies) (ports.AgentAuthStatus, error)
}

func TestAutohandAuthStatusForImplementsScopedChecker(t *testing.T) {
	if _, ok := any(&Plugin{resolvedBinary: "autohand"}).(ports.AgentScopedAuthChecker); !ok {
		t.Fatal("Autohand does not implement ports.AgentScopedAuthChecker")
	}
}

func TestAutohandAuthStatusReadsEverySupportedConfigFormat(t *testing.T) {
	formats := map[string]string{
		"json": `{"provider":"openrouter","openrouter":{"apiKey":"fixture-key","model":"fixture-model"}}`,
		"toml": "provider = \"openrouter\"\n[openrouter]\napiKey = \"fixture-key\"\nmodel = \"fixture-model\"\n",
		"yaml": "provider: openrouter\nopenrouter:\n  apiKey: fixture-key\n  model: fixture-model\n",
		"yml":  "provider: openrouter\nopenrouter:\n  apiKey: fixture-key\n  model: fixture-model\n",
	}
	for extension, content := range formats {
		t.Run(extension, func(t *testing.T) {
			path := writeAutohandFixture(t, extension, content)
			got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path}}, map[string]string{"HOME": t.TempDir()}, nil)
			if got != ports.AgentAuthStatusConfigured {
				t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
			}
		})
	}
}

func TestAutohandAuthStatusConfigSelectionPrecedence(t *testing.T) {
	root := t.TempDir()
	autohandHome := filepath.Join(root, "custom-home")
	envConfig := writeAutohandFixture(t, "json", `{"provider":"ollama","ollama":{"model":"local"}}`)
	scopedConfig := writeAutohandFixture(t, "yaml", "provider: openrouter\nopenrouter:\n  apiKey: scoped-key\n  model: scoped-model\n")
	writeAutohandPath(t, filepath.Join(autohandHome, "config.yaml"), "provider: openrouter\nopenrouter:\n  apiKey: home-key\n  model: home-model\n")
	env := map[string]string{"HOME": root, "AUTOHAND_HOME": autohandHome, "AUTOHAND_CONFIG": envConfig}

	got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config=" + scopedConfig}}, env, nil)
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("scoped config status = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
	delete(env, "AUTOHAND_CONFIG")
	got = runAutohandAuth(t, ports.AgentAuthCheck{}, env, nil)
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("AUTOHAND_HOME status = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestAutohandAuthStatusAppliesWorkspaceLocalSettings(t *testing.T) {
	root := t.TempDir()
	autohandHome := filepath.Join(root, "autohand-home")
	workspace := filepath.Join(root, "workspace")
	writeAutohandPath(t, filepath.Join(autohandHome, "config.json"), `{
  "provider":"openrouter",
  "openrouter":{"apiKey":"global-key","model":"global-model"},
  "ollama":{"model":"local-model"}
}`)
	writeAutohandPath(t, filepath.Join(workspace, ".autohand", "settings.local.json"), `{
  "provider":"ollama",
  "model":"workspace-model"
}`)

	got := runAutohandAuth(t, ports.AgentAuthCheck{WorkingDir: workspace}, map[string]string{
		"HOME": root, "AUTOHAND_HOME": autohandHome,
	}, nil)
	if got != ports.AgentAuthStatusNotApplicable {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusNotApplicable)
	}
}

func TestAutohandAuthStatusAppliesCLIOverridesInOrder(t *testing.T) {
	path := writeAutohandFixture(t, "json", `{
  "provider":"openrouter",
  "openrouter":{"apiKey":"global-key","model":"global-model"},
  "ollama":{"model":"local-model"},
  "llamacpp":{"model":"llama-model"},
  "profiles":{"local":{"provider":"ollama"}}
}`)
	tests := []struct {
		name string
		args []string
	}{
		{name: "profile", args: []string{"--config", path, "--profile", "local"}},
		{name: "set", args: []string{"--config", path, "--set", "provider=ollama"}},
		{name: "provider wins over set", args: []string{"--config", path, "--set=provider=ollama", "--provider", "llamacpp"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := runAutohandAuth(t, ports.AgentAuthCheck{Args: test.args}, map[string]string{"HOME": t.TempDir()}, nil)
			if got != ports.AgentAuthStatusNotApplicable {
				t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusNotApplicable)
			}
		})
	}
}

func TestAutohandAuthStatusIgnoresUnrelatedSetAndAppliesNestedAuthSet(t *testing.T) {
	t.Run("unrelated setting leaves provider readiness intact", func(t *testing.T) {
		path := writeAutohandFixture(t, "json", `{"provider":"openrouter","openrouter":{"apiKey":"fixture-key","model":"fixture"}}`)
		got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path, "--set", "ui.theme=dark"}}, map[string]string{"HOME": t.TempDir()}, nil)
		if got != ports.AgentAuthStatusConfigured {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
		}
	})
	t.Run("nested auth setting completes selected provider", func(t *testing.T) {
		path := writeAutohandFixture(t, "json", `{"provider":"openai","openai":{"authMode":"chatgpt","model":"gpt-5","chatgptAuth":{"accountId":"account"}}}`)
		got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path, "--set=openai.chatgptAuth.accessToken=access"}}, map[string]string{"HOME": t.TempDir()}, nil)
		if got != ports.AgentAuthStatusConfigured {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
		}
	})
}

func TestAutohandAuthStatusEnvironmentCreatesProviderSectionsBeforeRunOverrides(t *testing.T) {
	t.Run("profile selects Autohand AI environment section", func(t *testing.T) {
		path := writeAutohandFixture(t, "json", `{"profiles":{"cloud":{"provider":"autohandai"}}}`)
		got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path, "--profile", "cloud"}}, map[string]string{
			"HOME": t.TempDir(), "AUTOHAND_AI_API_KEY": "fixture-key",
		}, nil)
		if got != ports.AgentAuthStatusConfigured {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
		}
	})
	t.Run("provider flag selects Azure environment section", func(t *testing.T) {
		path := writeAutohandFixture(t, "json", `{}`)
		got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path, "--provider", "azure"}}, map[string]string{
			"HOME": t.TempDir(), "AZURE_OPENAI_ENDPOINT": "https://fixture.openai.azure.com/openai/deployments/test", "AZURE_OPENAI_KEY": "fixture-key",
		}, nil)
		if got != ports.AgentAuthStatusConfigured {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
		}
	})
}

func TestAutohandAuthStatusProviderFlagCreatesAccountBackedAutohandAI(t *testing.T) {
	path := writeAutohandFixture(t, "json", `{"auth":{"token":"ahc_fixture-token","expiresAt":"2000-01-01T00:00:00Z"}}`)
	got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path, "--provider", "autohandai"}}, map[string]string{
		"HOME": t.TempDir(),
	}, nil)
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestAutohandAuthStatusUsesOnlySelectedProviderCredentials(t *testing.T) {
	path := writeAutohandFixture(t, "json", `{
  "provider":"openrouter",
  "openrouter":{"model":"selected"},
  "anthropic":{"apiKey":"unrelated-key","model":"other"},
  "auth":{"token":"unrelated-account-token","expiresAt":"2099-01-01T00:00:00Z"}
}`)
	check := ports.AgentAuthCheck{Args: []string{"--config", path}}
	if got := runAutohandAuth(t, check, map[string]string{"HOME": t.TempDir(), "ANTHROPIC_API_KEY": "unrelated"}, nil); got != ports.AgentAuthStatusUnknown {
		t.Fatalf("unrelated credentials status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
	if got := runAutohandAuth(t, check, map[string]string{"HOME": t.TempDir(), "OPENROUTER_API_KEY": "selected"}, nil); got != ports.AgentAuthStatusConfigured {
		t.Fatalf("selected credential status = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestAutohandAuthStatusSupportsExtensionAndCustomProviders(t *testing.T) {
	for _, test := range []struct {
		name   string
		config string
		want   ports.AgentAuthStatus
	}{
		{
			name:   "extension key",
			config: `{"provider":"extension:release","extensionProviders":{"extension:release":{"apiKey":"fixture-key","model":"release"}}}`,
			want:   ports.AgentAuthStatusConfigured,
		},
		{
			name:   "custom key",
			config: `{"provider":"custom:acme","customProviders":{"acme":{"id":"acme","displayName":"Acme","apiFormat":"openai-compatible","baseUrl":"https://acme.invalid/v1","apiKey":"fixture-key","model":"acme"}}}`,
			want:   ports.AgentAuthStatusConfigured,
		},
		{
			name:   "custom without required key",
			config: `{"provider":"custom:local","customProviders":{"local":{"id":"local","displayName":"Local","apiFormat":"openai-compatible","baseUrl":"http://127.0.0.1:9999/v1","apiKeyRequired":false,"model":"local"}}}`,
			want:   ports.AgentAuthStatusNotApplicable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeAutohandFixture(t, "json", test.config)
			got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path}}, map[string]string{"HOME": t.TempDir()}, nil)
			if got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAutohandAuthStatusUsesDocumentedProviderEnvironment(t *testing.T) {
	tests := []struct {
		provider string
		envKey   string
		extra    string
	}{
		{provider: "autohandai", envKey: "AUTOHAND_AI_API_KEY", extra: `"plan":"cloud","authMode":"api-key",`},
		{provider: "openrouter", envKey: "OPENROUTER_API_KEY"},
		{provider: "openai", envKey: "OPENAI_API_KEY", extra: `"authMode":"api-key",`},
		{provider: "llmgateway", envKey: "LLM_GATEWAY_API_KEY"},
		{provider: "deepseek", envKey: "DEEPSEEK_API_KEY"},
		{provider: "zai", envKey: "ZAI_API_KEY"},
		{provider: "sakana", envKey: "SAKANA_API_KEY"},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			config := fmt.Sprintf(`{"provider":%q,%q:{%s"model":"fixture-model"}}`, test.provider, test.provider, test.extra)
			path := writeAutohandFixture(t, "json", config)
			env := map[string]string{"HOME": t.TempDir(), test.envKey: "fixture-key"}
			got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path}}, env, nil)
			if got != ports.AgentAuthStatusConfigured {
				t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
			}
		})
	}
}

func TestAutohandAuthStatusUsesDefaultAndEnvironmentSelectedProvidersWithoutConfig(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "default OpenRouter", env: map[string]string{"OPENROUTER_API_KEY": "fixture-key"}},
		{name: "selected Autohand AI", env: map[string]string{"AUTOHAND_PROVIDER": "autohandai", "AUTOHAND_AI_API_KEY": "fixture-key"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.env["HOME"] = t.TempDir()
			got := runAutohandAuth(t, ports.AgentAuthCheck{}, test.env, nil)
			if got != ports.AgentAuthStatusConfigured {
				t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
			}
		})
	}
}

func TestAutohandAuthStatusBareModeUsesKeyOrHelperWithoutExecutingHelper(t *testing.T) {
	path := writeAutohandFixture(t, "json", `{"provider":"openrouter","openrouter":{"model":"fixture"},"auth":{"apiKeyHelper":"do-not-run --secret"}}`)
	check := ports.AgentAuthCheck{Args: []string{"--config", path, "--bare"}}
	run := func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("Autohand auth discovery executed auth.apiKeyHelper")
		return nil, errors.New("unexpected command")
	}
	deps := &authutil.Dependencies{Run: run}
	if got := runAutohandAuth(t, check, map[string]string{"HOME": t.TempDir()}, deps); got != ports.AgentAuthStatusConfigured {
		t.Fatalf("helper status = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
	if got := runAutohandAuth(t, check, map[string]string{"HOME": t.TempDir(), "AUTOHAND_API_KEY": "fixture-key"}, deps); got != ports.AgentAuthStatusConfigured {
		t.Fatalf("environment status = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestAutohandAuthStatusCloudCredentialChains(t *testing.T) {
	tests := []struct {
		name   string
		config string
		env    map[string]string
		deps   authutil.Dependencies
	}{
		{
			name:   "AWS Bedrock",
			config: `{"provider":"bedrock","bedrock":{"authMode":"aws-credentials","region":"us-east-1","model":"fixture"}}`,
			env:    map[string]string{"AWS_ACCESS_KEY_ID": "fixture-id", "AWS_SECRET_ACCESS_KEY": "fixture-secret"},
		},
		{
			name:   "Google ADC",
			config: `{"provider":"vertexai","vertexai":{"projectId":"fixture-project","model":"fixture"}}`,
			deps: authutil.Dependencies{LoadGoogleADC: func(context.Context) (authutil.CloudCredential, error) {
				return authutil.CloudCredential{Token: "fixture-token"}, nil
			}},
		},
		{
			name:   "Azure identity",
			config: `{"provider":"azure","azure":{"authMethod":"managed-identity","baseUrl":"https://fixture.openai.azure.com","deploymentName":"fixture-deployment","model":"fixture"}}`,
			deps: authutil.Dependencies{LoadAzure: func(context.Context) (authutil.CloudCredential, error) {
				return authutil.CloudCredential{Token: "fixture-token"}, nil
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeAutohandFixture(t, "json", test.config)
			if test.env == nil {
				test.env = make(map[string]string)
			}
			test.env["HOME"] = t.TempDir()
			got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path}}, test.env, &test.deps)
			if got != ports.AgentAuthStatusConfigured {
				t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
			}
		})
	}
}

func TestAutohandAuthStatusProviderSpecificOAuth(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   ports.AgentAuthStatus
	}{
		{
			name:   "OpenAI ChatGPT auth",
			config: `{"provider":"openai","openai":{"authMode":"chatgpt","model":"gpt-5","chatgptAuth":{"accessToken":"access","accountId":"account"}}}`,
			want:   ports.AgentAuthStatusConfigured,
		},
		{
			name:   "OpenAI API key cannot satisfy ChatGPT auth",
			config: `{"provider":"openai","openai":{"authMode":"chatgpt","model":"gpt-5","apiKey":"unrelated"}}`,
			want:   ports.AgentAuthStatusUnknown,
		},
		{
			name:   "xAI OAuth",
			config: `{"provider":"xai","xai":{"authMode":"oauth","model":"grok","oauthAuth":{"accessToken":"access"}}}`,
			want:   ports.AgentAuthStatusConfigured,
		},
		{
			name:   "xAI API key cannot satisfy OAuth",
			config: `{"provider":"xai","xai":{"authMode":"oauth","model":"grok","apiKey":"unrelated"}}`,
			want:   ports.AgentAuthStatusUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeAutohandFixture(t, "json", test.config)
			got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path}}, map[string]string{"HOME": t.TempDir()}, nil)
			if got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAutohandAuthStatusBedrockAPIModeDefaults(t *testing.T) {
	tests := []struct {
		name   string
		config string
		env    map[string]string
		want   ports.AgentAuthStatus
	}{
		{
			name:   "converse defaults to AWS credentials",
			config: `{"provider":"bedrock","bedrock":{"apiMode":"converse","region":"us-east-1","model":"fixture"}}`,
			env:    map[string]string{"AWS_ACCESS_KEY_ID": "fixture-id", "AWS_SECRET_ACCESS_KEY": "fixture-secret"},
			want:   ports.AgentAuthStatusConfigured,
		},
		{
			name:   "OpenAI chat defaults to Bedrock API key",
			config: `{"provider":"bedrock","bedrock":{"apiMode":"openai-chat","apiKey":"fixture-key","region":"us-east-1","model":"fixture"}}`,
			want:   ports.AgentAuthStatusConfigured,
		},
		{
			name:   "OpenAI responses does not accept AWS credentials by default",
			config: `{"provider":"bedrock","bedrock":{"apiMode":"openai-responses","region":"us-east-1","model":"fixture"}}`,
			env:    map[string]string{"AWS_ACCESS_KEY_ID": "fixture-id", "AWS_SECRET_ACCESS_KEY": "fixture-secret"},
			want:   ports.AgentAuthStatusUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeAutohandFixture(t, "json", test.config)
			if test.env == nil {
				test.env = make(map[string]string)
			}
			test.env["HOME"] = t.TempDir()
			got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path}}, test.env, nil)
			if got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAutohandAuthStatusAzureShapesAndAuthIsolation(t *testing.T) {
	azureEvidence := authutil.Dependencies{LoadAzure: func(context.Context) (authutil.CloudCredential, error) {
		return authutil.CloudCredential{Token: "fixture-token"}, nil
	}}
	tests := []struct {
		name   string
		config string
		deps   *authutil.Dependencies
		want   ports.AgentAuthStatus
	}{
		{
			name:   "base URL with API key",
			config: `{"provider":"azure","azure":{"authMethod":"api-key","baseUrl":"https://fixture.openai.azure.com/openai/deployments/test","apiKey":"fixture-key","model":"fixture"}}`,
			want:   ports.AgentAuthStatusConfigured,
		},
		{
			name:   "resource and deployment with Entra credentials",
			config: `{"provider":"azure","azure":{"authMethod":"entra-id","resourceName":"fixture","deploymentName":"deploy","tenantId":"tenant","clientId":"client","clientSecret":"secret","model":"fixture"}}`,
			want:   ports.AgentAuthStatusConfigured,
		},
		{
			name:   "managed identity uses Azure evidence",
			config: `{"provider":"azure","azure":{"authMethod":"managed-identity","baseUrl":"https://fixture.openai.azure.com/openai/deployments/test","model":"fixture"}}`,
			deps:   &azureEvidence,
			want:   ports.AgentAuthStatusConfigured,
		},
		{
			name:   "API key mode ignores identity evidence",
			config: `{"provider":"azure","azure":{"authMethod":"api-key","baseUrl":"https://fixture.openai.azure.com/openai/deployments/test","model":"fixture"}}`,
			deps:   &azureEvidence,
			want:   ports.AgentAuthStatusUnknown,
		},
		{
			name:   "managed identity ignores API key",
			config: `{"provider":"azure","azure":{"authMethod":"managed-identity","baseUrl":"https://fixture.openai.azure.com/openai/deployments/test","apiKey":"unrelated","model":"fixture"}}`,
			want:   ports.AgentAuthStatusUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeAutohandFixture(t, "json", test.config)
			got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path}}, map[string]string{"HOME": t.TempDir()}, test.deps)
			if got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAutohandAuthStatusUsesSelectedBedrockProfile(t *testing.T) {
	home := t.TempDir()
	writeAutohandPath(t, filepath.Join(home, ".aws", "credentials"), "[work]\naws_access_key_id=fixture-id\naws_secret_access_key=fixture-secret\n")
	path := writeAutohandFixture(t, "json", `{"provider":"bedrock","bedrock":{"authMode":"aws-credentials","profile":"work","region":"us-east-1","model":"fixture"}}`)
	got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path}}, map[string]string{"HOME": home}, nil)
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestAutohandAuthStatusLocalProvidersNeedNoAuthentication(t *testing.T) {
	for _, provider := range []string{"ollama", "llamacpp", "mlx", "blueprint-local"} {
		t.Run(provider, func(t *testing.T) {
			config := fmt.Sprintf(`{"provider":%q,%q:{"model":"fixture"}}`, provider, provider)
			if provider == "blueprint-local" {
				config = `{"provider":"blueprint-local","blueprintLocal":{"model":"fixture","modelPath":"/fixture/model","modelSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`
			}
			path := writeAutohandFixture(t, "json", config)
			got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path}}, map[string]string{"HOME": t.TempDir()}, nil)
			if got != ports.AgentAuthStatusNotApplicable {
				t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusNotApplicable)
			}
		})
	}

	path := writeAutohandFixture(t, "json", `{"provider":"autohandai","autohandai":{"plan":"local","model":"fixture"}}`)
	if got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path}}, map[string]string{"HOME": t.TempDir()}, nil); got != ports.AgentAuthStatusNotApplicable {
		t.Fatalf("Autohand local plan status = %q, want %q", got, ports.AgentAuthStatusNotApplicable)
	}
}

func TestAutohandAuthStatusAccountExpiryAndCredentialSeparation(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		config string
		want   ports.AgentAuthStatus
	}{
		{
			name:   "unexpired account token",
			config: `{"provider":"autohandai","autohandai":{"plan":"cloud","authMode":"account","model":"fantail"},"auth":{"token":"fixture-token","expiresAt":"2026-09-20T12:00:00Z"}}`,
			want:   ports.AgentAuthStatusConfigured,
		},
		{
			name:   "expired account token",
			config: `{"provider":"autohandai","autohandai":{"plan":"cloud","authMode":"account","model":"fantail"},"auth":{"token":"fixture-token","expiresAt":"2026-09-18T12:00:00Z"}}`,
			want:   ports.AgentAuthStatusUnauthorized,
		},
		{
			name:   "durable account token ignores stale expiry",
			config: `{"provider":"autohandai","autohandai":{"plan":"cloud","authMode":"account","model":"fantail"},"auth":{"token":"ahc_fixture-token","expiresAt":"2026-09-18T12:00:00Z"}}`,
			want:   ports.AgentAuthStatusConfigured,
		},
		{
			name:   "malformed account expiry",
			config: `{"provider":"autohandai","autohandai":{"plan":"cloud","authMode":"account","model":"fantail"},"auth":{"token":"fixture-token","expiresAt":"not-a-date"}}`,
			want:   ports.AgentAuthStatusUnknown,
		},
		{
			name:   "account token does not configure provider",
			config: `{"provider":"openrouter","openrouter":{"model":"fixture"},"auth":{"token":"fixture-token","expiresAt":"2026-09-20T12:00:00Z"}}`,
			want:   ports.AgentAuthStatusUnknown,
		},
		{
			name:   "provider key does not configure account mode",
			config: `{"provider":"autohandai","autohandai":{"plan":"cloud","authMode":"account","apiKey":"fixture-key","model":"fantail"}}`,
			want:   ports.AgentAuthStatusUnknown,
		},
		{
			name:   "account token requires explicit account mode",
			config: `{"provider":"autohandai","autohandai":{"plan":"cloud","model":"fantail"},"auth":{"token":"fixture-token","expiresAt":"2026-09-20T12:00:00Z"}}`,
			want:   ports.AgentAuthStatusUnknown,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeAutohandFixture(t, "json", test.config)
			deps := authutil.Dependencies{Now: func() time.Time { return now }}
			got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path}}, map[string]string{"HOME": t.TempDir()}, &deps)
			if got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAutohandAuthStatusValidatesSelectedProviderConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		config string
	}{
		{name: "missing local section", config: `{"provider":"ollama"}`},
		{name: "local missing model", config: `{"provider":"ollama","ollama":{"baseUrl":"http://localhost:11434"}}`},
		{name: "ordinary missing model", config: `{"provider":"openrouter","openrouter":{"apiKey":"fixture-key"}}`},
		{name: "custom missing base URL", config: `{"provider":"custom:acme","customProviders":{"acme":{"id":"acme","displayName":"Acme","apiFormat":"openai-compatible","apiKey":"fixture-key","model":"fixture"}}}`},
		{name: "custom missing model", config: `{"provider":"custom:acme","customProviders":{"acme":{"id":"acme","displayName":"Acme","apiFormat":"openai-compatible","baseUrl":"https://acme.invalid/v1","apiKey":"fixture-key"}}}`},
		{name: "custom mismatched ID", config: `{"provider":"custom:acme","customProviders":{"acme":{"id":"other","displayName":"Acme","apiFormat":"openai-compatible","baseUrl":"https://acme.invalid/v1","apiKey":"fixture-key","model":"fixture"}}}`},
		{name: "custom missing display name", config: `{"provider":"custom:acme","customProviders":{"acme":{"id":"acme","apiFormat":"openai-compatible","baseUrl":"https://acme.invalid/v1","apiKey":"fixture-key","model":"fixture"}}}`},
		{name: "extension missing model", config: `{"provider":"extension:release","extensionProviders":{"extension:release":{"apiKey":"fixture-key","baseUrl":"https://extension.invalid/v1"}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeAutohandFixture(t, "json", test.config)
			got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", path}}, map[string]string{"HOME": t.TempDir()}, nil)
			if got != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
			}
		})
	}
}

func TestAutohandAuthStatusProviderOverrideAndMalformedConfig(t *testing.T) {
	path := writeAutohandFixture(t, "json", `{"provider":"openrouter","openrouter":{"apiKey":"fixture-key","model":"fixture"},"ollama":{"model":"local"}}`)
	check := ports.AgentAuthCheck{Args: []string{"--config", path}}
	if got := runAutohandAuth(t, check, map[string]string{"HOME": t.TempDir(), "AUTOHAND_PROVIDER": "ollama"}, nil); got != ports.AgentAuthStatusNotApplicable {
		t.Fatalf("provider override status = %q, want %q", got, ports.AgentAuthStatusNotApplicable)
	}

	malformed := writeAutohandFixture(t, "toml", `provider = [`)
	malformedEnv := map[string]string{
		"HOME": t.TempDir(), "AUTOHAND_PROVIDER": "autohandai", "AUTOHAND_AI_API_KEY": "must-not-fail-open",
	}
	if got := runAutohandAuth(t, ports.AgentAuthCheck{Args: []string{"--config", malformed}}, malformedEnv, nil); got != ports.AgentAuthStatusUnknown {
		t.Fatalf("malformed config status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestAutohandAuthStatusUnknownWithoutDocumentedLocalCredential(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AUTOHAND_CONFIG", "")
	for _, name := range []string{
		"AUTOHAND_API_KEY", "AUTOHAND_AUTH_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY",
		"GEMINI_API_KEY", "GOOGLE_API_KEY", "OPENROUTER_API_KEY", "MISTRAL_API_KEY", "GROQ_API_KEY",
	} {
		t.Setenv(name, "")
	}
	status, err := (&Plugin{resolvedBinary: "autohand"}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusUnknown)
	}
}

func TestAutohandAuthStatusUsesAUTOHAND_CONFIG(t *testing.T) {
	t.Setenv("AUTOHAND_CONFIG", writeAutohandAuthConfig(t, `{
	  "provider": "autohandai",
	  "autohandai": {"plan": "cloud", "authMode": "account", "model": "fantail"},
	  "auth": {"token": "session-token"}
	}`))
	status, err := (&Plugin{resolvedBinary: "autohand"}).AuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusConfigured)
	}
}

func TestAutohandConfigAuthStatusAuthorized(t *testing.T) {
	path := writeAutohandAuthConfig(t, `{
  "auth": {"token": "session-token", "user": {"email": "agent@example.com"}},
  "provider": "zai",
  "zai": {"apiKey": "real-provider-key", "model": "glm-5.1"}
}`)

	got, err := autohandConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusAuthorized)
	}
}

func TestAutohandConfigAuthStatusUnknownWithAPIKeyHelper(t *testing.T) {
	path := writeAutohandAuthConfig(t, `{
  "auth": {"apiKeyHelper": "security find-generic-password -w -s autohand"}
}`)

	got, err := autohandConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestAutohandConfigAuthStatusUnknownWithMissingCloudToken(t *testing.T) {
	path := writeAutohandAuthConfig(t, `{
  "auth": {"token": ""},
  "provider": "zai",
  "zai": {"apiKey": "real-provider-key"}
}`)

	got, err := autohandConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestAutohandConfigAuthStatusAuthorizedWithPlaceholderProviderKey(t *testing.T) {
	path := writeAutohandAuthConfig(t, `{
  "auth": {"token": "session-token"},
  "provider": "zai",
  "zai": {"apiKey": "api key ", "model": "glm-5.1"}
}`)

	got, err := autohandConfigAuthStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusAuthorized)
	}
}

func TestAutohandConfigAuthStatusUnknownWhenMissing(t *testing.T) {
	got, err := autohandConfigAuthStatus(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func writeAutohandAuthConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runAutohandAuth(t *testing.T, check ports.AgentAuthCheck, env map[string]string, supplied *authutil.Dependencies) ports.AgentAuthStatus {
	t.Helper()
	plugin := &Plugin{resolvedBinary: "autohand"}
	checker, ok := any(plugin).(autohandInjectedAuthChecker)
	if !ok {
		t.Fatal("Autohand does not expose the injected auth resolver")
	}
	deps := authutil.Dependencies{}
	if supplied != nil {
		deps = *supplied
	}
	deps.Getenv = func(name string) string { return env[name] }
	if deps.Run == nil {
		deps.Run = func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("Autohand auth discovery executed a command")
			return nil, errors.New("unexpected command")
		}
	}
	status, err := checker.authStatusFor(context.Background(), check, deps)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func writeAutohandFixture(t *testing.T, extension, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config."+extension)
	writeAutohandPath(t, path, content)
	return path
}

func writeAutohandPath(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
