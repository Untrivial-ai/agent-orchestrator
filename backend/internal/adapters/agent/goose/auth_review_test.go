package goose

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type gooseSecurityExit int

func (e gooseSecurityExit) Error() string { return "security failure" }
func (e gooseSecurityExit) ExitCode() int { return int(e) }

func TestGooseKeyringStorageOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, content string
		err           error
		want          ports.AgentAuthStatus
	}{
		{"empty object blocks stale file", `{}`, nil, ports.AgentAuthStatusUnknown},
		{"malformed JSON blocks stale file", `{`, nil, ports.AgentAuthStatusUnknown},
		{"empty response blocks stale file", ``, nil, ports.AgentAuthStatusUnknown},
		{"wrong value type blocks stale file", `{"OPENAI_API_KEY":42}`, nil, ports.AgentAuthStatusUnknown},
		{"null object blocks stale file", `null`, nil, ports.AgentAuthStatusUnknown},
		{"missing item permits fallback", ``, gooseSecurityExit(44), ports.AgentAuthStatusConfigured},
		{"unavailable storage permits fallback", ``, gooseSecurityExit(37), ports.AgentAuthStatusConfigured},
		{"unclassified error blocks stale file", ``, errors.New("fixture-secret"), ports.AgentAuthStatusUnknown},
		{"cancelled operation blocks stale file", ``, context.Canceled, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			gooseWrite(t, filepath.Join(root, "config", "secrets.yaml"), "OPENAI_API_KEY: stale-fixture\n")
			d := gooseDeps(t, map[string]string{"GOOSE_PATH_ROOT": root, "GOOSE_PROVIDER": "openai"})
			d.GOOS = "darwin"
			d.Run = func(context.Context, string, ...string) ([]byte, error) { return []byte(tc.content), tc.err }
			got, err := gooseAuthStatus(context.Background(), ports.AgentAuthCheck{}, d)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestGooseOAuthRequiredRefreshField(t *testing.T) {
	for _, provider := range []struct{ name, path string }{
		{"xai_oauth", "xai_oauth/tokens.json"},
		{"kimi_code", "kimicode/token.json"},
		{"chatgpt_codex", "chatgpt_codex/tokens.json"},
		{"gemini_oauth", "gemini_oauth/tokens.json"},
	} {
		for _, tc := range []struct {
			name, refresh, expiry string
			want                  ports.AgentAuthStatus
		}{
			{"missing expired", ``, "2020-01-01T00:00:00Z", ports.AgentAuthStatusUnknown},
			{"null expired", `,"refresh_token":null`, "2020-01-01T00:00:00Z", ports.AgentAuthStatusUnknown},
			{"missing valid", ``, "2099-01-01T00:00:00Z", ports.AgentAuthStatusUnknown},
			{"null valid", `,"refresh_token":null`, "2099-01-01T00:00:00Z", ports.AgentAuthStatusUnknown},
			{"explicit empty expired", `,"refresh_token":""`, "2020-01-01T00:00:00Z", ports.AgentAuthStatusUnauthorized},
			{"explicit empty valid", `,"refresh_token":""`, "2099-01-01T00:00:00Z", ports.AgentAuthStatusConfigured},
		} {
			t.Run(provider.name+"/"+tc.name, func(t *testing.T) {
				root := t.TempDir()
				content := `{"access_token":"fixture","expires_at":"` + tc.expiry + `"` + tc.refresh + `}`
				if provider.name == "gemini_oauth" {
					content = `{"project_id":"project","token":` + content + `}`
				}
				gooseWrite(t, filepath.Join(root, "config", provider.path), content)
				got, err := gooseAuthStatus(context.Background(), ports.AgentAuthCheck{}, gooseDeps(t, map[string]string{"GOOSE_PATH_ROOT": root, "GOOSE_PROVIDER": provider.name}))
				if err != nil || got != tc.want {
					t.Fatalf("got %q, %v; want %q", got, err, tc.want)
				}
			})
		}
	}
}

func TestGooseCustomURLTemplates(t *testing.T) {
	for _, tc := range []struct {
		name, baseURL, envVars string
		env                    map[string]string
		want                   ports.AgentAuthStatus
	}{
		{"declared substitution", `${TEAM_HOST}/v1`, `[{"name":"TEAM_HOST","required":true}]`, map[string]string{"TEAM_HOST": "https://models.example"}, ports.AgentAuthStatusConfigured},
		{"declared default", `${TEAM_HOST}/v1`, `[{"name":"TEAM_HOST","required":true,"default":"https://models.example"}]`, nil, ports.AgentAuthStatusConfigured},
		{"empty declared default", `https://models.example/${TEAM_PATH}`, `[{"name":"TEAM_PATH","required":true,"default":""}]`, nil, ports.AgentAuthStatusConfigured},
		{"unreferenced required field", `https://models.example/v1`, `[{"name":"UNUSED","required":true}]`, nil, ports.AgentAuthStatusConfigured},
		{"missing referenced required field", `${TEAM_HOST}/v1`, `[{"name":"TEAM_HOST","required":true}]`, nil, ports.AgentAuthStatusUnknown},
		{"invalid resolved endpoint", `${TEAM_HOST}/v1`, `[{"name":"TEAM_HOST","required":true}]`, map[string]string{"TEAM_HOST": "not-an-endpoint"}, ports.AgentAuthStatusUnknown},
		{"unresolved optional template", `https://models.example/${TEAM_PATH}`, `[{"name":"TEAM_PATH"}]`, nil, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			env := tc.env
			if env == nil {
				env = map[string]string{}
			}
			env["GOOSE_PATH_ROOT"], env["GOOSE_PROVIDER"], env["TEAM_AUTH"] = root, "custom_review", "fixture"
			metadata := `{"name":"custom_review","display_name":"Review","engine":"openai","models":[],"api_key_env":"TEAM_AUTH","base_url":"` + tc.baseURL + `","env_vars":` + tc.envVars + `}`
			gooseWrite(t, filepath.Join(root, "config", "custom_providers", "custom_review.json"), metadata)
			got, err := gooseAuthStatus(context.Background(), ports.AgentAuthCheck{}, gooseDeps(t, env))
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestGooseCustomNativeSchema(t *testing.T) {
	const valid = `{"name":"custom_review","display_name":"Review","engine":"openai","base_url":"https://models.example/v1","models":[{"name":"model","context_limit":8192}],"auth":{"command":"helper","args":[],"refresh_interval":3600,"timeout_seconds":10,"cwd":"/tmp"}}`
	for _, tc := range []struct {
		name, from, to string
		want           ports.AgentAuthStatus
	}{
		{"valid native declaration", "", "", ports.AgentAuthStatusConfigured},
		{"missing display name", `"display_name":"Review",`, ``, ports.AgentAuthStatusUnknown},
		{"null display name", `"display_name":"Review"`, `"display_name":null`, ports.AgentAuthStatusUnknown},
		{"missing models", `"models":[{"name":"model","context_limit":8192}],`, ``, ports.AgentAuthStatusUnknown},
		{"null models", `"models":[{"name":"model","context_limit":8192}]`, `"models":null`, ports.AgentAuthStatusUnknown},
		{"wrong models type", `"models":[{"name":"model","context_limit":8192}]`, `"models":"bad"`, ports.AgentAuthStatusUnknown},
		{"missing model name", `"name":"model",`, ``, ports.AgentAuthStatusUnknown},
		{"null model name", `"name":"model"`, `"name":null`, ports.AgentAuthStatusUnknown},
		{"wrong model limit", `"context_limit":8192`, `"context_limit":"bad"`, ports.AgentAuthStatusUnknown},
		{"negative refresh interval", `"refresh_interval":3600`, `"refresh_interval":-1`, ports.AgentAuthStatusUnknown},
		{"null refresh interval", `"refresh_interval":3600`, `"refresh_interval":null`, ports.AgentAuthStatusUnknown},
		{"text refresh interval", `"refresh_interval":3600`, `"refresh_interval":"bad"`, ports.AgentAuthStatusUnknown},
		{"text timeout", `"timeout_seconds":10`, `"timeout_seconds":"bad"`, ports.AgentAuthStatusUnknown},
		{"negative timeout", `"timeout_seconds":10`, `"timeout_seconds":-1`, ports.AgentAuthStatusUnknown},
		{"wrong cwd type", `"cwd":"/tmp"`, `"cwd":42`, ports.AgentAuthStatusUnknown},
		{"null args", `"args":[]`, `"args":null`, ports.AgentAuthStatusUnknown},
		{"null argument", `"args":[]`, `"args":[null]`, ports.AgentAuthStatusUnknown},
		{"missing command even with no auth", `"auth":{"command":"helper",`, `"requires_auth":false,"auth":{`, ports.AgentAuthStatusUnknown},
		{"null environment boolean", `"engine":"openai"`, `"engine":"openai","env_vars":[{"name":"UNUSED","required":null}]`, ports.AgentAuthStatusUnknown},
		{"null model boolean", `"context_limit":8192`, `"context_limit":8192,"reasoning":null`, ports.AgentAuthStatusUnknown},
		{"wrong headers type", `"engine":"openai"`, `"engine":"openai","headers":{"Authorization":42}`, ports.AgentAuthStatusUnknown},
		{"wrong top timeout", `"engine":"openai"`, `"engine":"openai","timeout_seconds":"bad"`, ports.AgentAuthStatusUnknown},
		{"null requires auth", `"engine":"openai"`, `"engine":"openai","requires_auth":null`, ports.AgentAuthStatusUnknown},
		{"valid thinking preservation format", `"context_limit":8192`, `"context_limit":8192,"thinking_preservation_format":"content_xml"`, ports.AgentAuthStatusConfigured},
		{"invalid thinking preservation format", `"context_limit":8192`, `"context_limit":8192,"thinking_preservation_format":"invalid"`, ports.AgentAuthStatusUnknown},
		{"valid setup metadata", `"engine":"openai"`, `"engine":"openai","setup":{"category":"model","setup_method":"single_api_key","group":"default"}`, ports.AgentAuthStatusConfigured},
		{"malformed setup metadata", `"engine":"openai"`, `"engine":"openai","setup":42`, ports.AgentAuthStatusUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata := valid
			if tc.from != "" {
				metadata = strings.Replace(valid, tc.from, tc.to, 1)
			}
			root := t.TempDir()
			gooseWrite(t, filepath.Join(root, "config", "custom_providers", "custom_review.json"), metadata)
			got, err := gooseAuthStatus(context.Background(), ports.AgentAuthCheck{}, gooseDeps(t, map[string]string{"GOOSE_PATH_ROOT": root, "GOOSE_PROVIDER": "custom_review"}))
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
