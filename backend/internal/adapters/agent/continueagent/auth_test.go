package continueagent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestContinueAuthStatusUsesDocumentedLocalEvidence(t *testing.T) {
	for _, tt := range []struct {
		name   string
		auth   string
		config string
		env    map[string]string
		want   ports.AgentAuthStatus
	}{
		{
			name: "browser login",
			auth: `{"userId":"user_1","userEmail":"user@example.com","accessToken":"test-access","refreshToken":"test-refresh","expiresAt":1900000000000}`,
			want: ports.AgentAuthStatusConfigured,
		},
		{
			name: "Continue API key",
			env:  map[string]string{"CONTINUE_API_KEY": "test-continue-key"},
			want: ports.AgentAuthStatusConfigured,
		},
		{
			name: "local Anthropic key",
			env:  map[string]string{"ANTHROPIC_API_KEY": "test-anthropic-key"},
			want: ports.AgentAuthStatusConfigured,
		},
		{
			name: "selected model literal provider key",
			config: `name: Main
version: 1.0.0
schema: v1
models:
  - name: Claude
    provider: anthropic
    model: claude-sonnet-4-6
    apiKey: test-config-key
`,
			want: ports.AgentAuthStatusConfigured,
		},
		{
			name: "selected model environment reference",
			config: `name: Main
version: 1.0.0
schema: v1
models:
  - name: GPT
    provider: openai
    model: gpt-5
    apiKey: ${{ secrets.SELECTED_PROVIDER_KEY }}
`,
			env:  map[string]string{"SELECTED_PROVIDER_KEY": "test-provider-key"},
			want: ports.AgentAuthStatusConfigured,
		},
		{
			name: "unresolved selected model environment reference",
			config: `models:
  - name: GPT
    provider: openai
    model: gpt-5
    apiKey: ${{ secrets.MISSING_PROVIDER_KEY }}
`,
			want: ports.AgentAuthStatusUnknown,
		},
		{
			name: "compact unresolved environment reference",
			config: `models:
  - name: GPT
    provider: openai
    model: gpt-5
    apiKey: ${{secrets.MISSING_PROVIDER_KEY}}
`,
			want: ports.AgentAuthStatusUnknown,
		},
		{
			name: "unsupported template is not a literal key",
			config: `models:
  - name: GPT
    provider: openai
    model: gpt-5
    apiKey: ${{ inputs.NOT_A_SECRET }}
`,
			want: ports.AgentAuthStatusUnknown,
		},
		{
			name: "embedded unresolved template is not a literal key",
			config: `models:
  - name: GPT
    provider: openai
    model: gpt-5
    apiKey: prefix-${{ secrets.MISSING_PROVIDER_KEY }}
`,
			want: ports.AgentAuthStatusUnknown,
		},
		{
			name: "unrelated Anthropic key does not satisfy selected OpenAI model",
			config: `models:
  - name: GPT
    provider: openai
    model: gpt-5
`,
			env:  map[string]string{"ANTHROPIC_API_KEY": "test-anthropic-key"},
			want: ports.AgentAuthStatusUnknown,
		},
		{
			name: "templated provider is not a selected provider",
			config: `models:
  - name: Dynamic
    provider: ${{ secrets.PROVIDER }}
    model: gpt-5
    apiKey: test-config-key
`,
			want: ports.AgentAuthStatusUnknown,
		},
		{
			name: "unselected recursive key",
			config: `models:
  - name: Selected
    provider: openai
    model: gpt-5
  - name: Unselected
    provider: anthropic
    model: claude-sonnet-4-6
    apiKey: ignored-key
metadata:
  apiKey: ignored-recursive-key
`,
			want: ports.AgentAuthStatusUnknown,
		},
		{
			name:   "malformed browser and provider config",
			auth:   `{"accessToken":`,
			config: `models: [`,
			want:   ports.AgentAuthStatusUnknown,
		},
		{
			name: "installed without evidence",
			want: ports.AgentAuthStatusUnknown,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("CONTINUE_GLOBAL_DIR", "")
			for _, name := range []string{"CONTINUE_API_KEY", "ANTHROPIC_API_KEY", "SELECTED_PROVIDER_KEY", "MISSING_PROVIDER_KEY"} {
				t.Setenv(name, "")
			}
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			if tt.auth != "" {
				writeContinueAuthFixture(t, filepath.Join(home, ".continue", "auth.json"), tt.auth)
			}
			if tt.config != "" {
				writeContinueAuthFixture(t, filepath.Join(home, ".continue", "config.yaml"), tt.config)
			}

			got, err := (&Plugin{resolvedBinary: "cn"}).AuthStatus(context.Background())
			if err != nil || got != tt.want {
				t.Fatalf("status = %q, err = %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestContinueSelectedSecretReferenceUsesOfficialDotenvLocations(t *testing.T) {
	for _, tt := range []struct {
		name, relativePath, contents string
		want                         ports.AgentAuthStatus
	}{
		{"global", "global/.env", "SELECTED_PROVIDER_KEY=global-key\n", ports.AgentAuthStatusConfigured},
		{"workspace Continue", "workspace/.continue/.env", "SELECTED_PROVIDER_KEY=workspace-continue-key\n", ports.AgentAuthStatusConfigured},
		{"workspace", "workspace/.env", "export SELECTED_PROVIDER_KEY='workspace-key'\n", ports.AgentAuthStatusConfigured},
		{"unrelated", "workspace/.env", "UNRELATED_KEY=ignored\n", ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			continueHome := filepath.Join(root, "global")
			workspace := filepath.Join(root, "workspace")
			writeContinueAuthFixture(t, filepath.Join(continueHome, "config.yaml"), `models:
  - name: GPT
    provider: openai
    model: gpt-5
    apiKey: ${{ secrets.SELECTED_PROVIDER_KEY }}
`)
			writeContinueAuthFixture(t, filepath.Join(root, filepath.FromSlash(tt.relativePath)), tt.contents)
			t.Setenv("HOME", root)
			t.Setenv("CONTINUE_GLOBAL_DIR", continueHome)
			t.Setenv("CONTINUE_API_KEY", "")
			t.Setenv("ANTHROPIC_API_KEY", "")
			t.Setenv("SELECTED_PROVIDER_KEY", "")

			got, err := (&Plugin{resolvedBinary: "cn"}).AuthStatusFor(context.Background(), ports.AgentAuthCheck{WorkingDir: workspace})
			if err != nil || got != tt.want {
				t.Fatalf("status = %q, err = %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestContinueBrowserLoginRequiresOfficialShape(t *testing.T) {
	for _, auth := range []string{
		`{"accessToken":"test-access"}`,
		`{"userId":"user_1","userEmail":"user@example.com","accessToken":"test-access","refreshToken":"","expiresAt":1900000000000}`,
		`{"userId":"user_1","userEmail":"user@example.com","accessToken":"test-access","refreshToken":"test-refresh","expiresAt":"tomorrow"}`,
		`{"nested":{"userId":"user_1","userEmail":"user@example.com","accessToken":"test-access","refreshToken":"test-refresh","expiresAt":1900000000000}}`,
	} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("CONTINUE_GLOBAL_DIR", "")
		t.Setenv("CONTINUE_API_KEY", "")
		t.Setenv("ANTHROPIC_API_KEY", "")
		writeContinueAuthFixture(t, filepath.Join(home, ".continue", "auth.json"), auth)

		got, err := (&Plugin{resolvedBinary: "cn"}).AuthStatus(context.Background())
		if err != nil || got != ports.AgentAuthStatusUnknown {
			t.Fatalf("status = %q, err = %v; want unknown for %s", got, err, auth)
		}
	}
}

func TestContinueAuthStatusForUsesScopedConfigAndEnvironment(t *testing.T) {
	for _, tt := range []struct {
		name       string
		args       []string
		config     string
		inherited  string
		anthropic  string
		scopedEnv  map[string]string
		defaultKey bool
		want       ports.AgentAuthStatus
	}{
		{
			name:   "absolute config flag",
			args:   []string{"cn", "--config", "CONFIG"},
			config: "models:\n  - name: GPT\n    provider: openai\n    model: gpt-5\n    apiKey: scoped-key\n",
			want:   ports.AgentAuthStatusConfigured,
		},
		{
			name:   "equals config flag",
			args:   []string{"cn", "--config=CONFIG"},
			config: "models:\n  - name: GPT\n    provider: openai\n    model: gpt-5\n    apiKey: scoped-key\n",
			want:   ports.AgentAuthStatusConfigured,
		},
		{
			name:      "scoped environment resolves selected key",
			args:      []string{"cn", "--config", "CONFIG"},
			config:    "models:\n  - name: GPT\n    provider: openai\n    model: gpt-5\n    apiKey: ${{ secrets.SCOPED_PROVIDER_KEY }}\n",
			scopedEnv: map[string]string{"SCOPED_PROVIDER_KEY": "test-key"},
			want:      ports.AgentAuthStatusConfigured,
		},
		{
			name:      "scoped empty key masks inherited key",
			inherited: "inherited-key",
			scopedEnv: map[string]string{"CONTINUE_API_KEY": ""},
			want:      ports.AgentAuthStatusUnknown,
		},
		{
			name:   "config after separator is prompt text",
			args:   []string{"cn", "--", "--config", "CONFIG"},
			config: "models:\n  - name: GPT\n    provider: openai\n    model: gpt-5\n    apiKey: scoped-key\n",
			want:   ports.AgentAuthStatusUnknown,
		},
		{
			name:       "malformed explicit config does not fall back",
			args:       []string{"cn", "--config", "CONFIG"},
			config:     "models: [",
			defaultKey: true,
			want:       ports.AgentAuthStatusUnknown,
		},
		{
			name:       "model flag selects injected model before default config",
			args:       []string{"cn", "--model", "anthropic/claude-sonnet"},
			defaultKey: true,
			want:       ports.AgentAuthStatusUnknown,
		},
		{
			name:      "explicit config does not fall back to Anthropic environment",
			args:      []string{"cn", "--config", "CONFIG"},
			config:    "models:\n  - name: GPT\n    provider: openai\n    model: gpt-5\n",
			anthropic: "test-anthropic-key",
			want:      ports.AgentAuthStatusUnknown,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			workspace := t.TempDir()
			configPath := filepath.Join(workspace, "selected.yaml")
			if tt.config != "" {
				writeContinueAuthFixture(t, configPath, tt.config)
			}
			if tt.defaultKey {
				writeContinueAuthFixture(t, filepath.Join(home, ".continue", "config.yaml"), "models:\n  - name: Default\n    provider: anthropic\n    model: claude-sonnet-4-6\n    apiKey: default-key\n")
			}
			args := append([]string(nil), tt.args...)
			for i := range args {
				args[i] = strings.ReplaceAll(args[i], "CONFIG", configPath)
			}
			t.Setenv("HOME", home)
			t.Setenv("CONTINUE_GLOBAL_DIR", "")
			t.Setenv("CONTINUE_API_KEY", tt.inherited)
			t.Setenv("ANTHROPIC_API_KEY", tt.anthropic)

			checker, ok := any(&Plugin{resolvedBinary: "cn"}).(ports.AgentScopedAuthChecker)
			if !ok {
				t.Fatal("Continue must implement AgentScopedAuthChecker")
			}
			got, err := checker.AuthStatusFor(context.Background(), ports.AgentAuthCheck{
				WorkingDir: workspace,
				Env:        tt.scopedEnv,
				Args:       args,
			})
			if err != nil || got != tt.want {
				t.Fatalf("status = %q, err = %v; want %q", got, err, tt.want)
			}
		})
	}
}

func writeContinueAuthFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
