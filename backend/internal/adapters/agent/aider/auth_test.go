package aider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type aiderInjectedAuthChecker interface {
	authStatusFor(context.Context, ports.AgentAuthCheck, authutil.Dependencies) (ports.AgentAuthStatus, error)
}

func TestAiderAuthStatusForImplementsScopedChecker(t *testing.T) {
	if _, ok := any(&Plugin{resolvedBinary: "aider"}).(ports.AgentScopedAuthChecker); !ok {
		t.Fatal("Aider does not implement ports.AgentScopedAuthChecker")
	}
}

func TestAiderAuthStatusUsesWorkspaceAndGitRootConfigPrecedence(t *testing.T) {
	t.Run("git root config applies from nested workspace", func(t *testing.T) {
		home, root := t.TempDir(), t.TempDir()
		workspace := filepath.Join(root, "nested", "workspace")
		writeAiderPath(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
		writeAiderPath(t, filepath.Join(root, ".aider.conf.yml"), "model: ollama_chat/qwen\n")

		got := runAiderAuth(t, ports.AgentAuthCheck{WorkingDir: workspace}, map[string]string{"HOME": home}, nil)
		if got != ports.AgentAuthStatusNotApplicable {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusNotApplicable)
		}
	})

	t.Run("workspace config overrides git root and home", func(t *testing.T) {
		home, root := t.TempDir(), t.TempDir()
		workspace := filepath.Join(root, "nested", "workspace")
		writeAiderPath(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
		writeAiderPath(t, filepath.Join(home, ".aider.conf.yml"), "model: ollama/local\n")
		writeAiderPath(t, filepath.Join(root, ".aider.conf.yml"), "model: ollama/local\n")
		writeAiderPath(t, filepath.Join(workspace, ".aider.conf.yml"), "model: openai/gpt-5\n")

		got := runAiderAuth(t, ports.AgentAuthCheck{WorkingDir: workspace}, map[string]string{"HOME": home}, nil)
		if got != ports.AgentAuthStatusUnknown {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
		}
	})

	t.Run("scoped config model overrides native files", func(t *testing.T) {
		home, workspace := t.TempDir(), t.TempDir()
		writeAiderPath(t, filepath.Join(workspace, ".aider.conf.yml"), "model: ollama/local\n")
		got := runAiderAuth(t, ports.AgentAuthCheck{
			WorkingDir: workspace,
			Config:     ports.AgentConfig{Model: "openai/gpt-5"},
			Env:        map[string]string{"OPENAI_API_KEY": "scoped-key"},
		}, map[string]string{"HOME": home}, nil)
		if got != ports.AgentAuthStatusConfigured {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
		}
	})
}

func TestAiderAuthStatusUsesExplicitConfigPaths(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		env  map[string]string
	}{
		{name: "CLI config", args: []string{"aider", "--config", "selected.yml"}},
		{name: "inline CLI config", args: []string{"aider", "--config=selected.yml"}},
		{name: "short CLI config", args: []string{"aider", "-c", "selected.yml"}},
		{name: "scoped environment config", env: map[string]string{"AIDER_CONFIG": "selected.yml"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home, workspace := t.TempDir(), t.TempDir()
			writeAiderPath(t, filepath.Join(workspace, ".aider.conf.yml"), "model: openai/gpt-5\nopenai-api-key: unrelated\n")
			writeAiderPath(t, filepath.Join(workspace, "selected.yml"), "model: ollama/qwen\n")
			env := map[string]string{"HOME": home}
			for name, value := range test.env {
				env[name] = value
			}
			got := runAiderAuth(t, ports.AgentAuthCheck{WorkingDir: workspace, Args: test.args}, env, nil)
			if got != ports.AgentAuthStatusNotApplicable {
				t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusNotApplicable)
			}
		})
	}

	t.Run("relative explicit config without workspace is unresolved", func(t *testing.T) {
		got := runAiderAuth(t, ports.AgentAuthCheck{
			Args: []string{"aider", "--config", "selected.yml"},
			Env:  map[string]string{"AIDER_MODEL": "openai/gpt-5", "OPENAI_API_KEY": "must-not-fall-through"},
		}, map[string]string{"HOME": t.TempDir()}, nil)
		if got != ports.AgentAuthStatusUnknown {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
		}
	})
}

func TestAiderAuthStatusUsesDotenvPrecedence(t *testing.T) {
	t.Run("OAuth keys file is loaded before home dotenv", func(t *testing.T) {
		home := t.TempDir()
		writeAiderPath(t, filepath.Join(home, ".aider", "oauth-keys.env"), "OPENROUTER_API_KEY=oauth-key\n")

		got := runAiderAuth(t, ports.AgentAuthCheck{}, map[string]string{"HOME": home}, nil)
		if got != ports.AgentAuthStatusConfigured {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
		}
	})

	t.Run("workspace dotenv overrides git root and home", func(t *testing.T) {
		home, root := t.TempDir(), t.TempDir()
		workspace := filepath.Join(root, "nested", "workspace")
		writeAiderPath(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
		writeAiderPath(t, filepath.Join(home, ".env"), "OPENAI_API_KEY=home-key\n")
		writeAiderPath(t, filepath.Join(root, ".env"), "OPENAI_API_KEY=root-key\n")
		writeAiderPath(t, filepath.Join(workspace, ".env"), "OPENAI_API_KEY=\n")

		got := runAiderAuth(t, ports.AgentAuthCheck{WorkingDir: workspace, Config: ports.AgentConfig{Model: "openai/gpt-5"}}, map[string]string{"HOME": home}, nil)
		if got != ports.AgentAuthStatusUnknown {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
		}
	})

	t.Run("AIDER_ENV_FILE is resolved from workspace", func(t *testing.T) {
		home, workspace := t.TempDir(), t.TempDir()
		writeAiderPath(t, filepath.Join(workspace, "selected.env"), "OPENAI_API_KEY=selected-key\n")
		got := runAiderAuth(t, ports.AgentAuthCheck{
			WorkingDir: workspace,
			Config:     ports.AgentConfig{Model: "openai/gpt-5"},
			Env:        map[string]string{"AIDER_ENV_FILE": "selected.env"},
		}, map[string]string{"HOME": home}, nil)
		if got != ports.AgentAuthStatusConfigured {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
		}
	})

	t.Run("CLI env file overrides AIDER_ENV_FILE", func(t *testing.T) {
		home, workspace := t.TempDir(), t.TempDir()
		writeAiderPath(t, filepath.Join(workspace, "environment.env"), "OPENAI_API_KEY=\n")
		writeAiderPath(t, filepath.Join(workspace, "argument.env"), "OPENAI_API_KEY=argument-key\n")
		got := runAiderAuth(t, ports.AgentAuthCheck{
			WorkingDir: workspace,
			Config:     ports.AgentConfig{Model: "openai/gpt-5"},
			Env:        map[string]string{"AIDER_ENV_FILE": "environment.env"},
			Args:       []string{"aider", "--env-file", "argument.env"},
		}, map[string]string{"HOME": home}, nil)
		if got != ports.AgentAuthStatusConfigured {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
		}
	})

	t.Run("config selects an explicit env file", func(t *testing.T) {
		home, workspace := t.TempDir(), t.TempDir()
		writeAiderPath(t, filepath.Join(workspace, ".aider.conf.yml"), "model: openai/gpt-5\nenv-file: selected.env\n")
		writeAiderPath(t, filepath.Join(workspace, "selected.env"), "OPENAI_API_KEY=selected-key\n")
		got := runAiderAuth(t, ports.AgentAuthCheck{WorkingDir: workspace}, map[string]string{"HOME": home}, nil)
		if got != ports.AgentAuthStatusConfigured {
			t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
		}
	})
}

func TestAiderAuthStatusIgnoresDaemonWorkingDirectory(t *testing.T) {
	daemonDir := t.TempDir()
	writeAiderPath(t, filepath.Join(daemonDir, ".aider.conf.yml"), "model: openai/gpt-5\nopenai-api-key: daemon-secret\n")
	writeAiderPath(t, filepath.Join(daemonDir, ".env"), "OPENAI_API_KEY=daemon-secret\n")
	t.Chdir(daemonDir)

	got := runAiderAuth(t, ports.AgentAuthCheck{}, map[string]string{"HOME": t.TempDir()}, nil)
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestAiderAuthStatusIgnoresRelativeWindowsHome(t *testing.T) {
	daemonDir := t.TempDir()
	writeAiderPath(t, filepath.Join(daemonDir, "relative-home", ".aider.conf.yml"), "model: openai/gpt-5\nopenai-api-key: daemon-secret\n")
	t.Chdir(daemonDir)

	deps := authutil.Dependencies{GOOS: "windows"}
	got := runAiderAuth(t, ports.AgentAuthCheck{}, map[string]string{"USERPROFILE": "relative-home"}, &deps)
	if got != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
	}
}

func TestAiderAuthStatusUsesDocumentedLiteLLMVariablesForSelectedProvider(t *testing.T) {
	tests := []struct {
		provider string
		envName  string
	}{
		{"aleph_alpha", "ALEPH_ALPHA_API_KEY"}, {"aleph_alpha", "ALEPHALPHA_API_KEY"},
		{"anyscale", "ANYSCALE_API_KEY"}, {"volcengine", "ARK_API_KEY"}, {"baseten", "BASETEN_API_KEY"},
		{"bytez", "BYTEZ_API_KEY"}, {"cerebras", "CEREBRAS_API_KEY"}, {"clarifai", "CLARIFAI_API_KEY"},
		{"cloudflare", "CLOUDFLARE_API_KEY"}, {"cohere", "CO_API_KEY"}, {"codestral", "CODESTRAL_API_KEY"},
		{"cohere", "COHERE_API_KEY"}, {"compactifai", "COMPACTIFAI_API_KEY"}, {"dashscope", "DASHSCOPE_API_KEY"},
		{"databricks", "DATABRICKS_API_KEY"}, {"deepinfra", "DEEPINFRA_API_KEY"}, {"deepseek", "DEEPSEEK_API_KEY"},
		{"featherless_ai", "FEATHERLESS_AI_API_KEY"}, {"fireworks_ai", "FIREWORKS_AI_API_KEY"},
		{"fireworks_ai", "FIREWORKS_API_KEY"}, {"fireworks_ai", "FIREWORKSAI_API_KEY"},
		{"gemini", "GEMINI_API_KEY"}, {"gemini", "GOOGLE_API_KEY"}, {"groq", "GROQ_API_KEY"},
		{"huggingface", "HUGGINGFACE_API_KEY"}, {"infinity", "INFINITY_API_KEY"}, {"maritalk", "MARITALK_API_KEY"},
		{"mistral", "MISTRAL_API_KEY"}, {"moonshot", "MOONSHOT_API_KEY"}, {"nebius", "NEBIUS_API_KEY"},
		{"nlp_cloud", "NLP_CLOUD_API_KEY"}, {"novita", "NOVITA_API_KEY"}, {"nvidia_nim", "NVIDIA_NIM_API_KEY"},
		{"openai", "OPENAI_API_KEY"}, {"openai_like", "OPENAI_LIKE_API_KEY"},
		{"openrouter", "OPENROUTER_API_KEY"}, {"openrouter", "OR_API_KEY"}, {"ovhcloud", "OVHCLOUD_API_KEY"},
		{"gemini", "PALM_API_KEY"}, {"perplexity_ai", "PERPLEXITYAI_API_KEY"}, {"predibase", "PREDIBASE_API_KEY"},
		{"provider", "PROVIDER_API_KEY"}, {"replicate", "REPLICATE_API_KEY"}, {"sambanova", "SAMBANOVA_API_KEY"},
		{"together_ai", "TOGETHERAI_API_KEY"}, {"user", "USER_API_KEY"},
		{"vercel_ai_gateway", "VERCEL_AI_GATEWAY_API_KEY"}, {"volcengine", "VOLCENGINE_API_KEY"},
		{"voyage", "VOYAGE_API_KEY"}, {"wandb", "WANDB_API_KEY"}, {"watsonx", "WATSONX_API_KEY"},
		{"watsonx", "WX_API_KEY"}, {"xai", "XAI_API_KEY"}, {"xinference", "XINFERENCE_API_KEY"},
	}
	for _, test := range tests {
		t.Run(test.envName, func(t *testing.T) {
			env := map[string]string{"HOME": t.TempDir(), test.envName: "fixture-key"}
			got := runAiderAuth(t, ports.AgentAuthCheck{Config: ports.AgentConfig{Model: test.provider + "/fixture"}}, env, nil)
			if got != ports.AgentAuthStatusConfigured {
				t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
			}
		})
	}
}

func TestAiderAuthStatusProviderSpecificCredentials(t *testing.T) {
	configuredCloud := authutil.CloudCredential{Token: "fixture-token"}
	tests := []struct {
		name  string
		model string
		env   map[string]string
		deps  authutil.Dependencies
		want  ports.AgentAuthStatus
	}{
		{
			name:  "Bedrock AWS chain",
			model: "bedrock/anthropic.claude",
			env:   map[string]string{"AWS_ACCESS_KEY_ID": "fixture-id", "AWS_SECRET_ACCESS_KEY": "fixture-secret"},
			want:  ports.AgentAuthStatusConfigured,
		},
		{
			name:  "Vertex project location and ADC",
			model: "vertex_ai/gemini",
			env:   map[string]string{"VERTEXAI_PROJECT": "fixture-project", "VERTEXAI_LOCATION": "us-east5"},
			deps: authutil.Dependencies{LoadGoogleADC: func(context.Context) (authutil.CloudCredential, error) {
				return configuredCloud, nil
			}},
			want: ports.AgentAuthStatusConfigured,
		},
		{
			name:  "Vertex missing location",
			model: "vertex_ai/gemini",
			env:   map[string]string{"VERTEXAI_PROJECT": "fixture-project"},
			deps: authutil.Dependencies{LoadGoogleADC: func(context.Context) (authutil.CloudCredential, error) {
				return configuredCloud, nil
			}},
			want: ports.AgentAuthStatusUnknown,
		},
		{
			name:  "Azure API key and endpoint settings",
			model: "azure/deployment",
			env: map[string]string{
				"AZURE_API_KEY": "fixture-key", "AZURE_API_BASE": "https://fixture.openai.azure.com", "AZURE_API_VERSION": "2026-01-01",
			},
			want: ports.AgentAuthStatusConfigured,
		},
		{
			name:  "Azure OpenAI API key",
			model: "azure/deployment",
			env: map[string]string{
				"AZURE_OPENAI_API_KEY": "fixture-key", "AZURE_API_BASE": "https://fixture.openai.azure.com", "AZURE_API_VERSION": "2026-01-01",
			},
			want: ports.AgentAuthStatusConfigured,
		},
		{
			name:  "Azure AI API key",
			model: "azure/deployment",
			env: map[string]string{
				"AZURE_AI_API_KEY": "fixture-key", "AZURE_API_BASE": "https://fixture.openai.azure.com", "AZURE_API_VERSION": "2026-01-01",
			},
			want: ports.AgentAuthStatusConfigured,
		},
		{
			name:  "Azure identity chain",
			model: "azure/deployment",
			env:   map[string]string{"AZURE_API_BASE": "https://fixture.openai.azure.com", "AZURE_API_VERSION": "2026-01-01"},
			deps: authutil.Dependencies{LoadAzure: func(context.Context) (authutil.CloudCredential, error) {
				return configuredCloud, nil
			}},
			want: ports.AgentAuthStatusConfigured,
		},
		{
			name:  "Azure missing endpoint settings",
			model: "azure/deployment",
			env:   map[string]string{"AZURE_API_KEY": "fixture-key"},
			want:  ports.AgentAuthStatusUnknown,
		},
		{
			name:  "GitHub Copilot token for OpenAI model",
			model: "openai/gpt-4o",
			env:   map[string]string{"GITHUB_COPILOT_TOKEN": "fixture-token"},
			want:  ports.AgentAuthStatusConfigured,
		},
		{
			name:  "LM Studio requires dummy key",
			model: "lm_studio/local",
			env:   map[string]string{"LM_STUDIO_API_KEY": "dummy-api-key"},
			want:  ports.AgentAuthStatusConfigured,
		},
		{
			name:  "LM Studio without key",
			model: "lm_studio/local",
			want:  ports.AgentAuthStatusUnknown,
		},
		{
			name:  "Ollama optional key",
			model: "ollama_chat/qwen",
			env:   map[string]string{"OLLAMA_API_KEY": "fixture-key"},
			want:  ports.AgentAuthStatusConfigured,
		},
		{
			name:  "Ollama keyless",
			model: "ollama/qwen",
			want:  ports.AgentAuthStatusNotApplicable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.env == nil {
				test.env = make(map[string]string)
			}
			test.env["HOME"] = t.TempDir()
			got := runAiderAuth(t, ports.AgentAuthCheck{Config: ports.AgentConfig{Model: test.model}}, test.env, &test.deps)
			if got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAiderAuthStatusPairsCredentialsWithSelectedProvider(t *testing.T) {
	for _, test := range []struct {
		name  string
		model string
		env   map[string]string
	}{
		{name: "OpenAI key cannot configure Anthropic", model: "anthropic/claude", env: map[string]string{"OPENAI_API_KEY": "unrelated"}},
		{name: "AWS chain cannot configure OpenAI", model: "openai/gpt-5", env: map[string]string{"AWS_ACCESS_KEY_ID": "id", "AWS_SECRET_ACCESS_KEY": "secret"}},
		{name: "Copilot token cannot configure Anthropic", model: "anthropic/claude", env: map[string]string{"GITHUB_COPILOT_TOKEN": "unrelated"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.env["HOME"] = t.TempDir()
			got := runAiderAuth(t, ports.AgentAuthCheck{Config: ports.AgentConfig{Model: test.model}}, test.env, nil)
			if got != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusUnknown)
			}
		})
	}
}

func TestAiderAuthStatusParsesTypedYAML(t *testing.T) {
	for _, test := range []struct {
		name   string
		config string
		want   ports.AgentAuthStatus
	}{
		{name: "matching generic key", config: "model: openrouter/anthropic/claude\napi-key:\n  - openrouter=fixture-key\n", want: ports.AgentAuthStatusConfigured},
		{name: "matching set env", config: "model: openai/gpt-5\nset-env:\n  - OPENAI_API_KEY=fixture-key\n", want: ports.AgentAuthStatusConfigured},
		{name: "unrelated generic key", config: "model: anthropic/claude\napi-key:\n  - openrouter=unrelated\n", want: ports.AgentAuthStatusUnknown},
		{name: "unrelated list entry containing equals", config: "model: openai/gpt-5\nread:\n  - OPENAI_API_KEY=must-not-count\n", want: ports.AgentAuthStatusUnknown},
		{name: "malformed API key list entry", config: "model: openai/gpt-5\napi-key:\n  - name: OPENAI_API_KEY=must-not-count\n", want: ports.AgentAuthStatusUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			writeAiderPath(t, filepath.Join(home, ".aider.conf.yml"), test.config)
			got := runAiderAuth(t, ports.AgentAuthCheck{}, map[string]string{"HOME": home}, nil)
			if got != test.want {
				t.Fatalf("status = %q, want %q", got, test.want)
			}
		})
	}
}

func TestAiderAuthStatusUsesNativeModelAndKeyArguments(t *testing.T) {
	got := runAiderAuth(t, ports.AgentAuthCheck{Args: []string{
		"aider", "--model", "openrouter/anthropic/claude", "--api-key", "openrouter=argument-key",
	}}, map[string]string{"HOME": t.TempDir(), "AIDER_MODEL": "ollama/local"}, nil)
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want %q", got, ports.AgentAuthStatusConfigured)
	}
}

func TestAiderAuthStatusDoesNotLeakMalformedConfigSecret(t *testing.T) {
	home := t.TempDir()
	secret := "task23-super-secret"
	writeAiderPath(t, filepath.Join(home, ".aider.conf.yml"), "model: openai/gpt-5\napi-key:\n  - name: "+secret+"\n")
	status, err := runAiderAuthResult(context.Background(), ports.AgentAuthCheck{}, map[string]string{"HOME": home}, nil)
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = %q, want %q", status, ports.AgentAuthStatusUnknown)
	}
	if err != nil && strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked secret: %v", err)
	}
}

func TestAiderAuthStatusPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, err := runAiderAuthResult(ctx, ports.AgentAuthCheck{}, map[string]string{"HOME": t.TempDir()}, nil)
	if status != ports.AgentAuthStatusUnknown || !errors.Is(err, context.Canceled) {
		t.Fatalf("status, err = %q, %v; want unknown, canceled", status, err)
	}
}

func runAiderAuth(t *testing.T, check ports.AgentAuthCheck, env map[string]string, supplied *authutil.Dependencies) ports.AgentAuthStatus {
	t.Helper()
	status, err := runAiderAuthResult(context.Background(), check, env, supplied)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func runAiderAuthResult(ctx context.Context, check ports.AgentAuthCheck, env map[string]string, supplied *authutil.Dependencies) (ports.AgentAuthStatus, error) {
	plugin := &Plugin{resolvedBinary: "aider"}
	checker, ok := any(plugin).(aiderInjectedAuthChecker)
	if !ok {
		return ports.AgentAuthStatusUnknown, errors.New("Aider does not expose the injected auth resolver")
	}
	deps := authutil.Dependencies{}
	if supplied != nil {
		deps = *supplied
	}
	deps.Getenv = func(name string) string { return env[name] }
	if deps.Run == nil {
		deps.Run = func(context.Context, string, ...string) ([]byte, error) {
			return nil, errors.New("unexpected command execution")
		}
	}
	return checker.authStatusFor(ctx, check, deps)
}

func writeAiderPath(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
