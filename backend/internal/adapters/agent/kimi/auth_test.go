package kimi

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/authutil"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestKimiCurrentAuthEvidence(t *testing.T) {
	for _, tt := range []struct {
		name, provider string
		env            map[string]string
		want           ports.AgentAuthStatus
	}{
		{name: "inline key", provider: "type='kimi'\napi_key='test-key'", want: ports.AgentAuthStatusConfigured},
		{name: "explicit shell key", provider: "type='kimi'\napi_key_env='KIMI_API_KEY'", env: map[string]string{"KIMI_API_KEY": "test-key"}, want: ports.AgentAuthStatusConfigured},
		{name: "missing explicit shell key", provider: "type='kimi'\napi_key_env='KIMI_API_KEY'", want: ports.AgentAuthStatusUnknown},
		{name: "conflicting explicit keys", provider: "type='kimi'\napi_key='key'\napi_key_env='KIMI_API_KEY'", env: map[string]string{"KIMI_API_KEY": "test"}, want: ports.AgentAuthStatusUnknown},
		{name: "missing binding cannot use fallback", provider: "type='kimi'\napi_key_env='KIMI_API_KEY'\n[providers.active.env]\nKIMI_API_KEY='fallback'", want: ports.AgentAuthStatusUnknown},
		{name: "provider env fallback", provider: "type='kimi'\n[providers.active.env]\nKIMI_API_KEY='test-key'", want: ports.AgentAuthStatusConfigured},
		{name: "unrelated provider env key", provider: "type='kimi'\n[providers.active.env]\nOPENAI_API_KEY='unrelated'", want: ports.AgentAuthStatusUnknown},
		{name: "arbitrary token", provider: "type='kimi'\n[providers.active.env]\nTOOL_TOKEN='unrelated'", want: ports.AgentAuthStatusUnknown},
		{name: "bare kimi environment", provider: "type='kimi'", env: map[string]string{"KIMI_API_KEY": "unrelated"}, want: ports.AgentAuthStatusUnknown},
		{name: "bare openai environment", provider: "type='openai'", env: map[string]string{"OPENAI_API_KEY": "unrelated"}, want: ports.AgentAuthStatusUnknown},
		{name: "bare legacy names", provider: "type='kimi'", env: map[string]string{"KIMI_CODE_API_KEY": "unrelated", "MOONSHOT_API_KEY": "unrelated"}, want: ports.AgentAuthStatusUnknown},
		{name: "authorization header", provider: "type='openai'\n[providers.active.custom_headers]\nAuthorization='Bearer test-header'", want: ports.AgentAuthStatusConfigured},
		{name: "global authorization header", provider: "type='openai'", env: map[string]string{"KIMI_CODE_CUSTOM_HEADERS": "X-Trace: test\nAuthorization: Bearer test-header"}, want: ports.AgentAuthStatusConfigured},
		{name: "global case variant header", provider: "type='openai'", env: map[string]string{"KIMI_CODE_CUSTOM_HEADERS": "authorization: Bearer test-header"}, want: ports.AgentAuthStatusUnknown},
		{name: "empty header overrides key", provider: "type='openai'\napi_key='key'\n[providers.active.custom_headers]\nAuthorization=' ' ", want: ports.AgentAuthStatusUnknown},
		{name: "empty header overrides bound key", provider: "type='openai'\napi_key_env='OPENAI_API_KEY'\n[providers.active.custom_headers]\nAuthorization=' ' ", env: map[string]string{"OPENAI_API_KEY": "test-key"}, want: ports.AgentAuthStatusUnknown},
		{name: "provider header overrides global", provider: "type='openai'\n[providers.active.custom_headers]\nAuthorization=' '", env: map[string]string{"KIMI_CODE_CUSTOM_HEADERS": "Authorization: Bearer test-header"}, want: ports.AgentAuthStatusUnknown},
		{name: "case variant header", provider: "type='openai'\n[providers.active.custom_headers]\nauthorization='Bearer test-header'", want: ports.AgentAuthStatusUnknown},
		{name: "header on wrong protocol", provider: "type='google-genai'\n[providers.active.custom_headers]\nAuthorization='Bearer unrelated'", want: ports.AgentAuthStatusUnknown},
		{name: "unrelated header", provider: "type='openai'\n[providers.active.custom_headers]\nX_API_KEY='unrelated'", want: ports.AgentAuthStatusUnknown},
		{name: "malformed selected provider", provider: "type='kimi'\napi_key=42", want: ports.AgentAuthStatusUnknown},
		{name: "unknown provider type", provider: "type='unsupported'\napi_key='test'", want: ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home, _ := kimiTestHomes(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			writeKimiFile(t, filepath.Join(home, "config.toml"), kimiSelectedConfig(tt.provider))
			assertKimiLocalStatus(t, tt.want)
		})
	}
}

func TestKimiModelEnvironment(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  map[string]string
		want ports.AgentAuthStatus
	}{
		{"complete", map[string]string{"KIMI_MODEL_NAME": "kimi-for-coding", "KIMI_MODEL_API_KEY": "key"}, ports.AgentAuthStatusConfigured},
		{"missing model", map[string]string{"KIMI_MODEL_API_KEY": "key"}, ports.AgentAuthStatusUnknown},
		{"missing key", map[string]string{"KIMI_MODEL_NAME": "kimi-for-coding"}, ports.AgentAuthStatusUnknown},
		{"wrong key", map[string]string{"KIMI_MODEL_NAME": "kimi-for-coding", "KIMI_API_KEY": "wrong"}, ports.AgentAuthStatusUnknown},
		{"unknown protocol", map[string]string{"KIMI_MODEL_NAME": "model", "KIMI_MODEL_API_KEY": "key", "KIMI_MODEL_PROVIDER_TYPE": "unsupported"}, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			kimiTestHomes(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			assertKimiLocalStatus(t, tt.want)
		})
	}
}

func TestKimiActiveProviderAndCurrentPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name, current, legacy string
		want                  ports.AgentAuthStatus
	}{
		{"inactive credential", kimiSelectedConfig("type='kimi'") + "\n[providers.inactive]\ntype='openai'\napi_key='unrelated'", "", ports.AgentAuthStatusUnknown},
		{"current wins", kimiSelectedConfig("type='kimi'"), kimiSelectedConfig("type='kimi'\napi_key='legacy'"), ports.AgentAuthStatusUnknown},
		{"legacy fallback", "", kimiSelectedConfig("type='kimi'\napi_key='legacy'"), ports.AgentAuthStatusConfigured},
		{"malformed permits valid legacy", "[providers", kimiSelectedConfig("type='kimi'\napi_key='legacy'"), ports.AgentAuthStatusConfigured},
		{"no selected model", "[providers.active]\ntype='kimi'\napi_key='unselected'", "", ports.AgentAuthStatusUnknown},
		{"unknown selected model", "default_model='missing'\n[providers.active]\ntype='kimi'\napi_key='unselected'", "", ports.AgentAuthStatusUnknown},
		{"legacy local provider", "", kimiSelectedConfig("type='_echo'"), ports.AgentAuthStatusNotApplicable},
		{"current unknown local type", kimiSelectedConfig("type='_echo'"), "", ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			current, legacy := kimiTestHomes(t)
			if tt.current != "" {
				writeKimiFile(t, filepath.Join(current, "config.toml"), tt.current)
			}
			if tt.legacy != "" {
				writeKimiFile(t, filepath.Join(legacy, "config.toml"), tt.legacy)
			}
			assertKimiLocalStatus(t, tt.want)
		})
	}
}

func TestKimiLegacyJSONAndProviderEnvironment(t *testing.T) {
	_, legacy := kimiTestHomes(t)
	writeKimiFile(t, filepath.Join(legacy, "config.json"), `{"default_model":"selected","models":{"selected":{"provider":"active","model":"test-model","max_context_size":10000}},"providers":{"active":{"type":"openai_legacy","api_key":"","base_url":"https://api.openai.com/v1"}}}`)
	t.Setenv("OPENAI_API_KEY", "legacy-key")
	assertKimiLocalStatus(t, ports.AgentAuthStatusConfigured)
}

func TestKimiVertexADC(t *testing.T) {
	for _, tt := range []struct {
		name, providerEnv, adc string
		want                   ports.AgentAuthStatus
	}{
		{"configured ADC", "GOOGLE_CLOUD_PROJECT='project'\nGOOGLE_CLOUD_LOCATION='us-central1'", `{"type":"authorized_user","client_id":"client","client_secret":"secret","refresh_token":"refresh"}`, ports.AgentAuthStatusConfigured},
		{"incomplete ADC", "GOOGLE_CLOUD_PROJECT='project'\nGOOGLE_CLOUD_LOCATION='us-central1'", `{"type":"authorized_user","refresh_token":"refresh"}`, ports.AgentAuthStatusUnknown},
		{"missing location", "GOOGLE_CLOUD_PROJECT='project'", `{"type":"authorized_user","client_id":"client","client_secret":"secret","refresh_token":"refresh"}`, ports.AgentAuthStatusUnknown},
		{"shell project is unrelated", "", `{"type":"authorized_user","client_id":"client","client_secret":"secret","refresh_token":"refresh"}`, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home, _ := kimiTestHomes(t)
			writeKimiFile(t, filepath.Join(home, "config.toml"), kimiSelectedConfig("type='vertexai'\n[providers.active.env]\n"+tt.providerEnv))
			adc := filepath.Join(t.TempDir(), "adc.json")
			writeKimiFile(t, adc, tt.adc)
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adc)
			t.Setenv("GOOGLE_CLOUD_PROJECT", "shell-project")
			t.Setenv("GOOGLE_CLOUD_LOCATION", "us-central1")
			assertKimiLocalStatus(t, tt.want)
		})
	}
}

func TestKimiOAuthReference(t *testing.T) {
	for _, tt := range []struct {
		name, ref, credentials string
		want                   ports.AgentAuthStatus
	}{
		{"file refresh", "storage='file', key='oauth/kimi-code'", `{"refresh_token":"refresh"}`, ports.AgentAuthStatusConfigured},
		{"file access", "storage='file', key='oauth/kimi-code'", `{"access_token":"access"}`, ports.AgentAuthStatusConfigured},
		{"empty tokens", "storage='file', key='oauth/kimi-code'", `{"access_token":"","refresh_token":""}`, ports.AgentAuthStatusUnknown},
		{"invalid tokens", "storage='file', key='oauth/kimi-code'", `{"access_token":42}`, ports.AgentAuthStatusUnknown},
		{"path traversal", "storage='file', key='oauth/../../kimi-code'", `{"access_token":"unrelated"}`, ports.AgentAuthStatusUnknown},
		{"unknown storage", "storage='other', key='oauth/kimi-code'", `{"access_token":"unrelated"}`, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home, _ := kimiTestHomes(t)
			writeKimiFile(t, filepath.Join(home, "config.toml"), kimiSelectedConfig("type='kimi'\noauth={"+tt.ref+"}"))
			writeKimiFile(t, filepath.Join(home, "credentials", "kimi-code.json"), tt.credentials)
			assertKimiLocalStatus(t, tt.want)
		})
	}
}

func TestKimiScopedModelAndEnvironment(t *testing.T) {
	home, _ := kimiTestHomes(t)
	writeKimiFile(t, filepath.Join(home, "config.toml"), kimiSelectedConfig("type='kimi'\napi_key='global'")+"\n[models.other]\nprovider='other'\nmodel='test-other'\nmax_context_size=10000\n[providers.other]\ntype='openai'\napi_key_env='OPENAI_API_KEY'\n")
	p := &Plugin{}
	checker, ok := any(p).(ports.AgentScopedAuthChecker)
	if !ok {
		t.Fatal("Kimi must support scoped auth checks")
	}
	p.resolvedBinary = "/test/kimi"
	for _, tt := range []struct {
		scope ports.AgentAuthCheck
		want  ports.AgentAuthStatus
	}{
		{ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "other"}}, ports.AgentAuthStatusUnknown},
		{ports.AgentAuthCheck{Config: ports.AgentConfig{Model: "other"}, Env: map[string]string{"OPENAI_API_KEY": "scope"}}, ports.AgentAuthStatusConfigured},
		{ports.AgentAuthCheck{Args: []string{"kimi", "-m", "other"}}, ports.AgentAuthStatusUnknown},
		{ports.AgentAuthCheck{Env: map[string]string{"KIMI_MODEL_NAME": "temporary"}}, ports.AgentAuthStatusUnknown},
		{ports.AgentAuthCheck{Args: []string{"kimi", "--model=selected"}, Env: map[string]string{"KIMI_MODEL_NAME": "temporary", "KIMI_MODEL_API_KEY": "key"}}, ports.AgentAuthStatusConfigured},
		{ports.AgentAuthCheck{}, ports.AgentAuthStatusConfigured},
	} {
		status, err := checker.AuthStatusFor(context.Background(), tt.scope)
		if err != nil || status != tt.want {
			t.Fatalf("scoped status = %q, %v; want %q", status, err, tt.want)
		}
	}
}

func TestKimiAuthCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, _, err := kimiLocalAuthStatus(ctx)
	if status != ports.AgentAuthStatusUnknown || err != context.Canceled {
		t.Fatalf("canceled status = %q, %v", status, err)
	}
}

func TestKimiLegacyKeyring(t *testing.T) {
	for _, tt := range []struct {
		name, key, payload string
		legacy             bool
		want               ports.AgentAuthStatus
		wantCalls          int
	}{
		{"legacy refresh", "oauth/kimi-code", `{"refresh_token":"test-refresh"}`, true, ports.AgentAuthStatusConfigured, 1},
		{"malformed keyring", "oauth/kimi-code", "not-json", true, ports.AgentAuthStatusUnknown, 1},
		{"unrecognized keyring account", "oauth/other", `{"refresh_token":"test-refresh"}`, true, ports.AgentAuthStatusUnknown, 0},
		{"current never probes legacy keyring", "oauth/kimi-code", `{"refresh_token":"test-refresh"}`, false, ports.AgentAuthStatusUnknown, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			d := authutil.Dependencies{GOOS: "darwin", Getenv: func(string) string { return "" }, Run: func(_ context.Context, command string, args ...string) ([]byte, error) {
				calls++
				if command != "/usr/bin/security" || !reflect.DeepEqual(args, []string{"find-generic-password", "-s", "kimi-code", "-a", "oauth/kimi-code", "-w"}) {
					t.Fatal("unexpected credential selector")
				}
				return []byte(tt.payload), nil
			}}
			provider := kimiCredentialSource{Type: "kimi", OAuth: &kimiOAuthRef{Storage: "keyring", Key: tt.key}}
			status := kimiProviderAuthStatus(context.Background(), d, t.TempDir(), provider, tt.legacy)
			if status != tt.want || calls != tt.wantCalls {
				t.Fatalf("status=%q calls=%d, want %q calls=%d", status, calls, tt.want, tt.wantCalls)
			}
		})
	}
}

func TestKimiScopedRelativeHome(t *testing.T) {
	kimiTestHomes(t)
	workspace := t.TempDir()
	writeKimiFile(t, filepath.Join(workspace, "profile", "config.toml"), kimiSelectedConfig("type='kimi'\napi_key='scoped'"))
	status, err := kimiAuthStatus(context.Background(), ports.AgentAuthCheck{WorkingDir: workspace, Env: map[string]string{"KIMI_CODE_HOME": "profile"}})
	if err != nil || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("relative home = %q, %v", status, err)
	}
}

func kimiTestHomes(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range []string{"KIMI_API_KEY", "OPENAI_API_KEY", "KIMI_CODE_API_KEY", "MOONSHOT_API_KEY", "ANTHROPIC_API_KEY", "GOOGLE_API_KEY", "KIMI_MODEL_NAME", "KIMI_MODEL_API_KEY", "KIMI_MODEL_PROVIDER_TYPE", "KIMI_MODEL_BASE_URL", "KIMI_CODE_CUSTOM_HEADERS", "GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_CONFIG", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "APPDATA"} {
		t.Setenv(key, "")
	}
	current, legacy := t.TempDir(), t.TempDir()
	t.Setenv("KIMI_CODE_HOME", current)
	t.Setenv("KIMI_SHARE_DIR", legacy)
	return current, legacy
}

func kimiSelectedConfig(provider string) string {
	return "default_model='selected'\n[models.selected]\nprovider='active'\nmodel='test-model'\nmax_context_size=10000\n[providers.active]\n" + provider + "\n"
}

func writeKimiFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertKimiLocalStatus(t *testing.T, want ports.AgentAuthStatus) {
	t.Helper()
	status, ok, err := kimiLocalAuthStatus(context.Background())
	if err != nil || status != want || ok != (want != ports.AgentAuthStatusUnknown) {
		t.Fatalf("status = (%q, %v, %v), want %q", status, ok, err, want)
	}
}
