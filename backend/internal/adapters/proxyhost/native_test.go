package proxyhost

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// onlyThisComputer hides the real machine's agents, keychain included, from a
// test and returns the directories Codex and Claude Code keep their files in.
func onlyThisComputer(t *testing.T) (codexHome, claudeHome string) {
	t.Helper()
	for _, name := range []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN",
		"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "OPENAI_BASE_URL", "OPENAI_API_KEY",
	} {
		t.Setenv(name, "")
	}
	codexHome, claudeHome = t.TempDir(), t.TempDir()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	return codexHome, claudeHome
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func jwtForTest(payload string) string {
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
}

var (
	codexIDToken = jwtForTest(`{"email":"native@example.test","https://api.openai.com/auth":{"chatgpt_account_id":"acct-claim","chatgpt_plan_type":"pro"}}`)
	codexAuth    = `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"access_token":"` + jwtForTest(`{"exp":1893456000}`) + `","refresh_token":"refresh-1","id_token":"` + codexIDToken + `","account_id":"acct-file"},"last_refresh":"2030-01-01T00:00:00Z"}`
	// What the helper is given for codexAuth, and its fingerprint.
	codexCredential  = `{"access_token":"` + jwtForTest(`{"exp":1893456000}`) + `","account_id":"acct-file","email":"native@example.test","expired":"2030-01-01T00:00:00Z","id_token":"` + codexIDToken + `","plan_type":"pro","refresh_token":"refresh-1","type":"codex"}`
	codexFingerprint = "cf607154cc76b791ee4ccdd8f5cbc4547fcaf655abaddb7a434e71e8ff045da4"
)

// A login is named by whose it is, never by its tokens, so the same login is
// recognised after its agent renews it. A known one is not sent to the helper,
// which would renew the copy and sign the agent out.
func TestNativeLoginIsNamedByItsOwnerAndLeftAloneWhenKnown(t *testing.T) {
	claudeFile := `{"claudeAiOauth":{"accessToken":"claude-access","refreshToken":"claude-refresh","expiresAt":1893456000000}}`
	for name, tc := range map[string]struct {
		provider, codexFile, claudeFile, claudeToken, claudeSettings string
		identity                                                     string
		none, fails                                                  bool
	}{
		"Codex signed in with ChatGPT":       {provider: "codex", codexFile: codexAuth, identity: "id:native@example.test"},
		"Codex with opaque tokens":           {provider: "codex", codexFile: `{"tokens":{"access_token":"opaque","refresh_token":"r"}}`},
		"Codex signed in with a key":         {provider: "codex", codexFile: `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-one","tokens":null}`, none: true},
		"Codex not signed in":                {provider: "codex", none: true},
		"Codex with a broken file":           {provider: "codex", codexFile: `{"tokens":`, none: true, fails: true},
		"Claude Code, owner in its settings": {provider: "claude", claudeFile: claudeFile, claudeSettings: `{"oauthAccount":{"emailAddress":" Native@Example.test "}}`, identity: "id:native@example.test"},
		"Claude Code, owner not recorded":    {provider: "claude", claudeFile: claudeFile},
		"Claude Code's token variable":       {provider: "claude", claudeToken: "env-token"},
		"Claude Code not signed in":          {provider: "claude", none: true},
		"Claude Code using an API key":       {provider: "claude", claudeFile: `{"claudeAiOauth":{"accessToken":"claude-access"}}`, claudeToken: "-", none: true},
	} {
		t.Run(name, func(t *testing.T) {
			codexHome, claudeHome := onlyThisComputer(t)
			if tc.codexFile != "" {
				writeFile(t, codexHome, "auth.json", tc.codexFile)
			}
			if tc.claudeFile != "" {
				writeFile(t, claudeHome, ".credentials.json", tc.claudeFile)
			}
			if tc.claudeSettings != "" {
				writeFile(t, claudeHome, ".claude.json", tc.claudeSettings)
			}
			if tc.claudeToken == "-" {
				t.Setenv("ANTHROPIC_API_KEY", "sk-ant-one")
			} else {
				t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", tc.claudeToken)
			}
			c, helper := helperClient(t, func(call) (int, string) { return http.StatusInternalServerError, `{}` })
			asked, askedWith := false, ""
			verified, identity, err := c.ImportNative(ctx, tc.provider, false, func(identity string) bool {
				asked, askedWith = true, identity
				return true
			})
			if (err != nil) != tc.fails || asked == tc.none || askedWith != tc.identity || identity != tc.identity || verified != (ports.VerifiedProviderLogin{}) || len(helper.calls) != 0 {
				t.Fatalf("identity=%q asked=%v with %q err=%v calls=%v", identity, asked, askedWith, err, helper.lines())
			}
		})
	}
}

func TestImportNativeLoginUploadsItOnce(t *testing.T) {
	codexHome, _ := onlyThisComputer(t)
	writeFile(t, codexHome, "auth.json", codexAuth)
	id, misses := "native-codex-"+codexFingerprint, 2
	c, helper := helperClient(t, func(received call) (int, string) {
		if received.Path != "/ao/login-result/"+id {
			return http.StatusOK, `{}`
		}
		// The helper acknowledges the upload before it has loaded the sign-in.
		if misses--; misses >= 0 {
			return http.StatusNotFound, `{}`
		}
		return http.StatusOK, `{"provider":"codex","email":"native@example.test","kind":"imported","credential_ref":"ao-` + id + `.json","auth_id":"auth-1"}`
	})
	want := ports.VerifiedProviderLogin{Provider: "codex", Email: "native@example.test", Kind: "imported", CredentialRef: "ao-" + id + ".json", AuthID: "auth-1"}
	verified, fingerprint, err := c.ImportNative(ctx, "codex", false, func(string) bool { return false })
	if err != nil || verified != want || fingerprint != "id:native@example.test" {
		t.Fatalf("verified=%+v fingerprint=%q err=%v", verified, fingerprint, err)
	}
	result := "GET /ao/login-result/" + id
	if !reflect.DeepEqual(helper.lines(), []string{result, "POST /v8/management/credentials", result, result}) {
		t.Fatalf("calls=%v", helper.lines())
	}
	if upload := helper.calls[1]; upload.Query != "name=ao-"+id+".json" || upload.Body != codexCredential || upload.LoginID != id {
		t.Fatalf("upload query=%q matches=%v", upload.Query, upload.Body == codexCredential)
	}
	// After a crash before the account was saved, the import is found, not repeated:
	// the helper may have renewed the tokens since.
	helper.calls = nil
	if verified, _, err = c.ImportNative(ctx, "codex", false, func(string) bool { return false }); err != nil || verified != want || !reflect.DeepEqual(helper.lines(), []string{result}) {
		t.Fatalf("verified=%+v err=%v calls=%v", verified, err, helper.lines())
	}
}

func TestNativeAPIKeyIsTheOneTheAgentWouldUse(t *testing.T) {
	const helper = "http://127.0.0.1:12345"
	for name, tc := range map[string]struct {
		provider, codexFile, settings string
		env                           map[string]string
		key, base                     string
	}{
		"Claude: an API key":                 {provider: "claude", env: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-one"}, key: "sk-ant-one", base: "https://api.anthropic.com"},
		"Claude: a gateway token":            {provider: "claude", env: map[string]string{"ANTHROPIC_AUTH_TOKEN": "gateway-token", "ANTHROPIC_BASE_URL": "https://gateway.example/anthropic/"}, key: "gateway-token", base: "https://gateway.example/anthropic"},
		"Claude: a key ahead of a token":     {provider: "claude", env: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-one", "ANTHROPIC_AUTH_TOKEN": "gateway-token"}, key: "sk-ant-one", base: "https://api.anthropic.com"},
		"Claude: its own settings":           {provider: "claude", settings: `{"env":{"ANTHROPIC_AUTH_TOKEN":"settings-token","ANTHROPIC_BASE_URL":"https://gateway.example"}}`, key: "settings-token", base: "https://gateway.example"},
		"Claude: nothing set":                {provider: "claude"},
		"Claude: a subscription token":       {provider: "claude", env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "oauth-token", "ANTHROPIC_API_KEY": "sk-ant-one"}},
		"Claude: a cloud provider":           {provider: "claude", env: map[string]string{"CLAUDE_CODE_USE_BEDROCK": "1", "ANTHROPIC_API_KEY": "sk-ant-one"}},
		"Claude: AO's own helper and ticket": {provider: "claude", env: map[string]string{"ANTHROPIC_AUTH_TOKEN": "session-ticket", "ANTHROPIC_BASE_URL": helper}},
		"Claude: an address with a password": {provider: "claude", env: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-one", "ANTHROPIC_BASE_URL": "https://user:pass@gateway.example"}},
		"Codex: signed in with a key":        {provider: "codex", codexFile: `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-one","tokens":null}`, key: "sk-one", base: "https://api.openai.com/v1"},
		"Codex: a key at another address":    {provider: "codex", codexFile: `{"auth_mode":"api_key","OPENAI_API_KEY":"sk-one"}`, env: map[string]string{"OPENAI_BASE_URL": "https://gateway.example/v1/"}, key: "sk-one", base: "https://gateway.example/v1"},
		"Codex: an older file with a key":    {provider: "codex", codexFile: `{"OPENAI_API_KEY":"sk-one","tokens":null}`, key: "sk-one", base: "https://api.openai.com/v1"},
		"Codex: signed in with ChatGPT":      {provider: "codex", codexFile: `{"auth_mode":"chatgpt","OPENAI_API_KEY":"sk-one","tokens":{"access_token":"a"}}`},
		"Codex: an older ChatGPT file":       {provider: "codex", codexFile: `{"OPENAI_API_KEY":"sk-one","tokens":{"access_token":"a"}}`},
		"Codex: no key":                      {provider: "codex", codexFile: codexAuth},
		"Codex: not signed in":               {provider: "codex"},
	} {
		t.Run(name, func(t *testing.T) {
			codexHome, claudeHome := onlyThisComputer(t)
			// A key kept in the environment for other tools is not Codex's.
			t.Setenv("OPENAI_API_KEY", "sk-for-other-tools")
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			if tc.codexFile != "" {
				writeFile(t, codexHome, "auth.json", tc.codexFile)
			}
			if tc.settings != "" {
				writeFile(t, claudeHome, "settings.json", tc.settings)
			}
			key, base, err := nativeAPIKey(ctx, tc.provider, helper)
			if err != nil || key != tc.key || base != tc.base {
				t.Fatalf("found a key=%v base=%q err=%v", key != "", base, err)
			}
		})
	}
}

func TestImportNativeAPIKeyAddsItOnceAndLeavesAHandAddedKeyAlone(t *testing.T) {
	onlyThisComputer(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-one")
	const fingerprint = "33f33ac7dcf94ce4f74500856c50df1c153da27365686302682b4d6bc69e1cc6"
	held, tagged := `[{"api-key":"another","base-url":"https://api.anthropic.com"}]`, false
	c, helper := helperClient(t, func(received call) (int, string) {
		switch {
		case received.Path == "/ao/login-result/native-key-claude-"+fingerprint && tagged:
			return http.StatusOK, `{"provider":"claude","email":"Global API key","kind":"api_key","credential_ref":"config-index:claude:7","auth_id":"key-auth"}`
		case received.Method == http.MethodGet && received.Path == "/v0/management/claude-api-key":
			return http.StatusOK, `{"claude-api-key":` + held + `}`
		case received.Method == http.MethodPut:
			held = received.Body
		case received.Path == "/ao/tag-api-key":
			var tag map[string]string
			_ = json.Unmarshal([]byte(received.Body), &tag)
			tagged = tag["label"] == "Global API key" && tag["id"] == "native-key-claude-"+fingerprint && tag["base_url"] == "https://api.anthropic.com"
		default:
			return http.StatusNotFound, `{}`
		}
		return http.StatusOK, `{}`
	})
	want := ports.VerifiedProviderLogin{Provider: "claude", Email: "Global API key", Kind: "api_key", CredentialRef: "config-index:claude:7", AuthID: "key-auth"}
	for attempt, seen := range []string{"", "", fingerprint} {
		verified, found, err := c.ImportNative(ctx, "claude", true, func(identity string) bool { return identity == seen })
		if err != nil || found != fingerprint || (seen == "") != (verified == want) {
			t.Fatalf("attempt %d: verified=%+v fingerprint=%q err=%v", attempt, verified, found, err)
		}
	}
	puts := 0
	for _, line := range helper.lines() {
		if line == "PUT /v0/management/claude-api-key" {
			puts++
		}
	}
	if puts != 1 || !strings.Contains(held, `"api-key":"another"`) || !strings.Contains(held, `"api-key":"sk-ant-one"`) {
		t.Fatalf("puts=%d, both keys held=%v", puts, strings.Count(held, "api-key") == 2)
	}
	// The same key added by hand is already an account: a conflict, and nothing is written.
	held, tagged, helper.calls = `[{"api-key":"sk-ant-one","base-url":"https://api.anthropic.com/"}]`, false, nil
	_, found, err := c.ImportNative(ctx, "claude", true, func(string) bool { return false })
	if !errors.Is(err, ports.ErrProviderAccountConflict) || found != fingerprint || len(helper.calls) != 2 {
		t.Fatalf("err=%v fingerprint=%q calls=%v", err, found, helper.lines())
	}
}
