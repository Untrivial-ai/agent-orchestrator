package pi

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestPiAuthJSONStatusConfiguredWithProviderKey(t *testing.T) {
	path := writePiAuthJSON(t, `{"zai":{"type":"api_key","key":"test-key"}}`)

	status, ok, err := piAuthJSONStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
	}
}

func TestPiAuthJSONStatusConfiguredWithResolvedEnvKey(t *testing.T) {
	t.Setenv("PI_TEST_API_KEY", "resolved-key")
	path := writePiAuthJSON(t, `{"zai":{"type":"api_key","key":"$PI_TEST_API_KEY"}}`)

	status, ok, err := piAuthJSONStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = (%q, %v), want (%q, true)", status, ok, ports.AgentAuthStatusConfigured)
	}
}

func TestPiImplementsScopedAuthChecker(t *testing.T) {
	if _, ok := any(&Plugin{resolvedBinary: "pi"}).(ports.AgentScopedAuthChecker); !ok {
		t.Fatal("Pi must implement AgentScopedAuthChecker")
	}
}

func TestPiAuthJSONStatusUnknownWithUnresolvedKey(t *testing.T) {
	t.Setenv("PI_MISSING_API_KEY", "")
	tests := map[string]string{
		"missing environment variable":        `{"zai":{"type":"api_key","key":"$PI_MISSING_API_KEY"}}`,
		"missing braced environment variable": `{"zai":{"type":"api_key","key":"${PI_MISSING_API_KEY}"}}`,
		"unverified command":                  `{"zai":{"type":"api_key","key":"!false"}}`,
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := writePiAuthJSON(t, content)

			status, ok, err := piAuthJSONStatus(path)
			if err != nil {
				t.Fatal(err)
			}
			if ok || status != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
			}
		})
	}
}

func TestPiAuthJSONStatusUnknownWhenEmpty(t *testing.T) {
	path := writePiAuthJSON(t, `{}`)

	status, ok, err := piAuthJSONStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (%q, false)", status, ok, ports.AgentAuthStatusUnknown)
	}
}

func writePiAuthJSON(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPiDocumentedProviderEnvironment(t *testing.T) {
	tests := map[string]string{
		"anthropic": "ANTHROPIC_API_KEY", "ant-ling": "ANT_LING_API_KEY", "openai": "OPENAI_API_KEY",
		"azure-openai-responses": "AZURE_OPENAI_API_KEY", "deepseek": "DEEPSEEK_API_KEY", "nvidia": "NVIDIA_API_KEY",
		"google": "GEMINI_API_KEY", "groq": "GROQ_API_KEY", "cerebras": "CEREBRAS_API_KEY", "xai": "XAI_API_KEY",
		"fireworks": "FIREWORKS_API_KEY", "together": "TOGETHER_API_KEY", "baseten": "BASETEN_API_KEY",
		"openrouter": "OPENROUTER_API_KEY", "vercel-ai-gateway": "AI_GATEWAY_API_KEY", "zai": "ZAI_API_KEY",
		"zai-coding-cn": "ZAI_CODING_CN_API_KEY", "mistral": "MISTRAL_API_KEY", "minimax": "MINIMAX_API_KEY",
		"minimax-cn": "MINIMAX_CN_API_KEY", "moonshotai": "MOONSHOT_API_KEY", "moonshotai-cn": "MOONSHOT_API_KEY",
		"huggingface": "HF_TOKEN", "opencode": "OPENCODE_API_KEY", "opencode-go": "OPENCODE_API_KEY",
		"kimi-coding": "KIMI_API_KEY", "qwen-token-plan": "QWEN_TOKEN_PLAN_API_KEY",
		"qwen-token-plan-individual": "QWEN_TOKEN_PLAN_API_KEY", "qwen-token-plan-cn": "QWEN_TOKEN_PLAN_CN_API_KEY",
		"xiaomi": "XIAOMI_API_KEY", "xiaomi-token-plan-cn": "XIAOMI_TOKEN_PLAN_CN_API_KEY",
		"xiaomi-token-plan-ams": "XIAOMI_TOKEN_PLAN_AMS_API_KEY", "xiaomi-token-plan-sgp": "XIAOMI_TOKEN_PLAN_SGP_API_KEY",
		"github-copilot": "COPILOT_GITHUB_TOKEN", "radius": "RADIUS_API_KEY",
	}
	for provider, variable := range tests {
		t.Run(provider, func(t *testing.T) {
			env := map[string]string{variable: "credential"}
			got := piTestStatus(t, provider, nil, env, authutil.Dependencies{})
			if got != ports.AgentAuthStatusConfigured {
				t.Fatalf("status = %q, want configured for %s", got, variable)
			}
		})
	}
}

func TestPiAdditionalDocumentedProviderEnvironment(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		env      map[string]string
	}{
		{"Anthropic auth token", "anthropic", map[string]string{"ANTHROPIC_AUTH_TOKEN": "token"}},
		{"Anthropic OAuth token", "anthropic", map[string]string{"ANTHROPIC_OAUTH_TOKEN": "token"}},
		{"Bedrock bearer token", "amazon-bedrock", map[string]string{"AWS_BEARER_TOKEN_BEDROCK": "token"}},
		{"Vertex API key", "google-vertex", map[string]string{"GOOGLE_CLOUD_API_KEY": "token"}},
		{"Cloudflare Workers pair", "cloudflare-workers-ai", map[string]string{"CLOUDFLARE_API_KEY": "token", "CLOUDFLARE_ACCOUNT_ID": "account"}},
		{"Cloudflare gateway tuple", "cloudflare-ai-gateway", map[string]string{"CLOUDFLARE_API_KEY": "token", "CLOUDFLARE_ACCOUNT_ID": "account", "CLOUDFLARE_GATEWAY_ID": "gateway"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := piTestStatus(t, tc.provider, nil, tc.env, authutil.Dependencies{}); got != ports.AgentAuthStatusConfigured {
				t.Fatalf("status = %q, want configured", got)
			}
		})
	}
}

func TestPiAuthJSONStatusMalformedOAuthIsUnknown(t *testing.T) {
	path := writePiAuthJSON(t, `{"openai-codex":{"type":"oauth","access":"token"}}`)
	status, ok, err := piAuthJSONStatus(path)
	if err != nil {
		t.Fatal(err)
	}
	if ok || status != ports.AgentAuthStatusUnknown {
		t.Fatalf("status = (%q, %v), want (unknown, false)", status, ok)
	}
}

func TestPiLocalAuthStatusAcceptsZeroDependencies(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	_, _, err := piLocalAuthStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

func TestPiProviderScopedEvidence(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tests := map[string]struct {
		provider string
		auth     string
		models   string
		env      map[string]string
		deps     authutil.Dependencies
		want     ports.AgentAuthStatus
	}{
		"provider env entry resolves only inside its credential": {
			provider: "cloudflare-workers-ai", auth: `{"cloudflare-workers-ai":{"type":"api_key","key":"$TOKEN","env":{"TOKEN":"stored-key","CLOUDFLARE_ACCOUNT_ID":"account"}}}`,
			want: ports.AgentAuthStatusConfigured,
		},
		"OAuth access refresh and millisecond expiry": {
			provider: "openai-codex", auth: `{"openai-codex":{"type":"oauth","access":"access","refresh":"refresh","expires":1800003600000}}`,
			want: ports.AgentAuthStatusConfigured,
		},
		"expired OAuth without refresh is unauthorized": {
			provider: "openai-codex", auth: `{"openai-codex":{"type":"oauth","access":"access","refresh":"","expires":1799999999000}}`,
			want: ports.AgentAuthStatusUnauthorized,
		},
		"unrelated provider entry is ignored": {
			provider: "anthropic", auth: `{"openai":{"type":"api_key","key":"unrelated"}}`,
			want: ports.AgentAuthStatusUnknown,
		},
		"selected provider ignores unrelated process env": {
			provider: "anthropic", env: map[string]string{"OPENAI_API_KEY": "unrelated"}, want: ports.AgentAuthStatusUnknown,
		},
		"Gemini accepts GEMINI_API_KEY": {
			provider: "google", env: map[string]string{"GEMINI_API_KEY": "gemini-key"}, want: ports.AgentAuthStatusConfigured,
		},
		"Gemini rejects GOOGLE_API_KEY": {
			provider: "google", env: map[string]string{"GOOGLE_API_KEY": "wrong-key"}, want: ports.AgentAuthStatusUnknown,
		},
		"AWS default chain": {
			provider: "amazon-bedrock", deps: authutil.Dependencies{LoadAWS: func(context.Context) (authutil.CloudCredential, error) {
				return authutil.CloudCredential{AccessKeyID: "id", SecretAccessKey: "secret"}, nil
			}}, want: ports.AgentAuthStatusConfigured,
		},
		"Vertex ADC requires project and location": {
			provider: "google-vertex", env: map[string]string{"GOOGLE_CLOUD_PROJECT": "project", "GOOGLE_CLOUD_LOCATION": "us-central1"},
			deps: authutil.Dependencies{LoadGoogleADC: func(context.Context) (authutil.CloudCredential, error) {
				return authutil.CloudCredential{Token: "adc-token"}, nil
			}}, want: ports.AgentAuthStatusConfigured,
		},
		"custom models provider key": {
			provider: "custom", models: `{"providers":{"custom":{"baseUrl":"https://models.example/v1","api":"openai-completions","apiKey":"$CUSTOM_API_KEY","models":[{"id":"model"}]}}}`,
			env: map[string]string{"CUSTOM_API_KEY": "custom-key"}, want: ports.AgentAuthStatusConfigured,
		},
		"custom loopback provider needs no auth": {
			provider: "ollama", models: `{"providers":{"ollama":{"baseUrl":"http://127.0.0.1:11434/v1","api":"openai-completions","models":[{"id":"model"}]}}}`,
			want: ports.AgentAuthStatusNotApplicable,
		},
		"custom loopback provider needs an effective API": {
			provider: "ollama", models: `{"providers":{"ollama":{"baseUrl":"http://127.0.0.1:11434/v1","models":[{"id":"model"}]}}}`,
			want: ports.AgentAuthStatusUnknown,
		},
		"custom loopback provider accepts a per-model API": {
			provider: "ollama", models: `{"providers":{"ollama":{"baseUrl":"http://127.0.0.1:11434/v1","models":[{"id":"model","api":"anthropic-messages"}]}}}`,
			want: ports.AgentAuthStatusNotApplicable,
		},
		"custom loopback provider rejects an unsupported API": {
			provider: "ollama", models: `{"providers":{"ollama":{"baseUrl":"http://127.0.0.1:11434/v1","api":"unknown-api","models":[{"id":"model"}]}}}`,
			want: ports.AgentAuthStatusUnknown,
		},
		"built in llama.cpp needs no auth": {provider: "llama.cpp", want: ports.AgentAuthStatusNotApplicable},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			deps := tc.deps
			deps.Now = func() time.Time { return now }
			got := piTestStatus(t, tc.provider, map[string]string{"auth.json": tc.auth, "models.json": tc.models}, tc.env, deps)
			if got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPiNativeProviderCheck(t *testing.T) {
	tests := map[string]struct {
		out     string
		err     error
		timeout bool
		want    ports.AgentAuthStatus
	}{
		"ready":             {out: "ready\n", want: ports.AgentAuthStatusAuthorized},
		"not ready":         {out: "not_ready\n", err: piTestExitError(1), want: ports.AgentAuthStatusUnauthorized},
		"invalid":           {out: "invalid\n", err: piTestExitError(2), want: ports.AgentAuthStatusUnknown},
		"unexpected output": {out: "logged in", want: ports.AgentAuthStatusUnknown},
		"mismatched exit":   {out: "not_ready\n", err: piTestExitError(2), want: ports.AgentAuthStatusUnknown},
		"timeout":           {timeout: true, want: ports.AgentAuthStatusUnknown},
		"oversized output":  {out: strings.Repeat("x", authutil.MaxFileSize+1), want: ports.AgentAuthStatusUnknown},
		"transport absence": {err: errors.New("transport unavailable"), want: ports.AgentAuthStatusConfigured},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writePiTestFile(t, filepath.Join(root, "auth.json"), `{"openai":{"type":"api_key","key":"local-key"}}`)
			var gotName string
			var gotArgs []string
			deps := authutil.Dependencies{
				Getenv: func(key string) string {
					if key == "PI_CODING_AGENT_DIR" {
						return root
					}
					return ""
				},
				Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					gotName, gotArgs = name, append([]string(nil), args...)
					if tc.timeout {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return []byte(tc.out), tc.err
				},
			}
			if tc.timeout {
				deps.Timeout = time.Millisecond
			}
			got, err := piAuthStatus(context.Background(), "/opt/pi", ports.AgentAuthCheck{
				WorkingDir: "/workspace", Config: ports.AgentConfig{Model: "openai/gpt-5"},
			}, deps)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
			if gotName != "/opt/pi" || !reflect.DeepEqual(gotArgs, []string{"auth", "check", "--provider", "openai"}) {
				t.Fatalf("command = %q %q", gotName, gotArgs)
			}
		})
	}
}

func TestPiUnresolvedBareModelDoesNotScanGlobalCredentials(t *testing.T) {
	tests := map[string]struct {
		auth string
		env  map[string]string
	}{
		"unrelated environment credential": {env: map[string]string{"ANTHROPIC_API_KEY": "unrelated"}},
		"unrelated stored credential":      {auth: `{"anthropic":{"type":"api_key","key":"unrelated"}}`},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if tc.auth != "" {
				writePiTestFile(t, filepath.Join(root, "auth.json"), tc.auth)
			}
			values := map[string]string{"PI_CODING_AGENT_DIR": root, "HOME": root}
			for key, value := range tc.env {
				values[key] = value
			}
			got, err := piAuthStatus(context.Background(), "", ports.AgentAuthCheck{
				Args: []string{"pi", "--model", "gpt-5"},
			}, authutil.Dependencies{Getenv: func(key string) string { return values[key] }})
			if err != nil {
				t.Fatal(err)
			}
			if got != ports.AgentAuthStatusUnknown {
				t.Fatalf("status = %q, want unknown", got)
			}
		})
	}
}

func TestPiUnscopedAuthStatusRetainsGlobalCredentialScan(t *testing.T) {
	root := t.TempDir()
	got, err := piAuthStatus(context.Background(), "", ports.AgentAuthCheck{}, authutil.Dependencies{Getenv: func(key string) string {
		if key == "PI_CODING_AGENT_DIR" || key == "HOME" {
			return root
		}
		if key == "ANTHROPIC_API_KEY" {
			return "credential"
		}
		return ""
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want configured", got)
	}
}

func TestPiSelectedLoopbackProviderSkipsNativeAuthCheck(t *testing.T) {
	root := t.TempDir()
	writePiTestFile(t, filepath.Join(root, "models.json"), `{"providers":{"local":{"baseUrl":"http://localhost:8080/v1","api":"openai-completions","models":[{"id":"model"}]}}}`)
	called := false
	got, err := piAuthStatus(context.Background(), "/opt/pi", ports.AgentAuthCheck{
		Config: ports.AgentConfig{Model: "local/model"},
	}, authutil.Dependencies{
		Getenv: func(key string) string {
			if key == "PI_CODING_AGENT_DIR" {
				return root
			}
			return ""
		},
		Run: func(context.Context, string, ...string) ([]byte, error) {
			called = true
			return []byte("not_ready\n"), piTestExitError(1)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusNotApplicable {
		t.Fatalf("status = %q, want not_applicable", got)
	}
	if called {
		t.Fatal("native auth check must not run for a selected loopback provider")
	}
}

func TestPiNativeCheckPrecedesCloudFallback(t *testing.T) {
	cloudCalled := false
	root := t.TempDir()
	got, err := piAuthStatus(context.Background(), "/opt/pi", ports.AgentAuthCheck{
		Config: ports.AgentConfig{Model: "amazon-bedrock/model"},
	}, authutil.Dependencies{
		Getenv: func(key string) string {
			if key == "PI_CODING_AGENT_DIR" {
				return root
			}
			return ""
		},
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("ready\n"), nil
		},
		LoadAWS: func(context.Context) (authutil.CloudCredential, error) {
			cloudCalled = true
			return authutil.CloudCredential{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want authorized", got)
	}
	if cloudCalled {
		t.Fatal("AWS fallback ran before an authoritative native result")
	}
}

func piTestStatus(t *testing.T, provider string, files, env map[string]string, extra authutil.Dependencies) ports.AgentAuthStatus {
	t.Helper()
	root := t.TempDir()
	if files != nil {
		for name, content := range files {
			if content != "" {
				writePiTestFile(t, filepath.Join(root, name), content)
			}
		}
	}
	values := map[string]string{"PI_CODING_AGENT_DIR": root, "HOME": root}
	for key, value := range env {
		values[key] = value
	}
	extra.Getenv = func(key string) string { return values[key] }
	status, err := piAuthStatus(context.Background(), "", ports.AgentAuthCheck{Config: ports.AgentConfig{Model: provider + "/model"}}, extra)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func writePiTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

type piTestExitError int

func (e piTestExitError) Error() string { return "exit" }
func (e piTestExitError) ExitCode() int { return int(e) }

var _ error = piTestExitError(1)
var _ interface{ ExitCode() int } = piTestExitError(1)
