package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/pkg/agentcreds"
)

// The regression this whole ladder exists for: a revoked key in the
// environment is a credential that is present, not a credential that works.
// Reporting it as authorized is what let a 401-ing daemon render as ready.
func TestAuthStatusDoesNotTrustUnvalidatedAPIKey(t *testing.T) {
	for _, name := range []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		t.Run(name, func(t *testing.T) {
			clearClaudeCredentialEnv(t)
			t.Setenv(name, "sk-ant-revoked-key")

			status, err := claudeLocalAuthStatus(context.Background(), (agentcreds.ResolveOptions{}).WithClaudeSettings())
			if err != nil {
				t.Fatal(err)
			}
			if status == ports.AgentAuthStatusAuthorized {
				t.Fatalf("%s present reported as authorized; presence is not validity", name)
			}
			if status != ports.AgentAuthStatusConfigured {
				t.Fatalf("state = %q, want %q", status, ports.AgentAuthStatusConfigured)
			}
		})
	}
}

func TestClaudeConfigAuthStatusReadsSynchronously(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"userID":"user-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, err := claudeConfigAuthStatus(ctx, path)
	if err != nil || status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, err = %v", status, err)
	}
}

// Precedence: the reported credential must be the one Claude Code will
// actually send, or the diagnostics point at the wrong variable.
func TestLocalAuthStatusReportsConfiguredForCredentialEnvironment(t *testing.T) {
	clearClaudeCredentialEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "auth-token")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-token")

	status, err := claudeLocalAuthStatus(context.Background(), (agentcreds.ResolveOptions{}).WithClaudeSettings())
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusConfigured {
		t.Fatalf("status = %q, want configured", status)
	}
}

func TestConfigAuthVerdictNeverReportsAuthorized(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    ports.AgentAuthStatus
	}{
		{"user id only", `{"userID":"user-1"}`, ports.AgentAuthStatusConfigured},
		{"oauth account", `{"oauthAccount":{"accountUuid":"account-1"}}`, ports.AgentAuthStatusConfigured},
		{
			"oauth subscription",
			`{"hasAvailableSubscription":true,"oauthAccount":{"accountUuid":"account-1"}}`,
			ports.AgentAuthStatusConfigured,
		},
		{"empty oauth account", `{"oauthAccount":{}}`, ports.AgentAuthStatusUnknown},
		{"no identity at all", `{"theme":"dark"}`, ports.AgentAuthStatusUnknown},
		{"empty file", ``, ports.AgentAuthStatusUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".claude.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			status, err := claudeConfigAuthStatus(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if status != tc.want {
				t.Fatalf("state = %q, want %q", status, tc.want)
			}
		})
	}
}

func TestConfigAuthVerdictMissingFileIsUnknown(t *testing.T) {
	status, err := claudeConfigAuthStatus(context.Background(), filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("state = %q, want %q", status, ports.AgentAuthStatusUnknown)
	}
}

func TestCLIReportVerdict(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		wantParsed bool
		wantState  ports.AgentAuthStatus
	}{
		{
			name:       "logged in is configured, not authorized",
			output:     `{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"pro"}`,
			wantParsed: true, wantState: ports.AgentAuthStatusConfigured,
		},
		{
			name:       "api key source is reported as the credential",
			output:     `{"loggedIn":true,"apiKeySource":"ANTHROPIC_API_KEY","authMethod":"claude.ai"}`,
			wantParsed: true, wantState: ports.AgentAuthStatusConfigured,
		},
		{
			name:       "signed out is a verified rejection",
			output:     `{"loggedIn":false}`,
			wantParsed: true, wantState: ports.AgentAuthStatusUnauthorized,
		},
		{
			name:       "warning lines around the json are tolerated",
			output:     "warning: ignored config line\n{\"loggedIn\":true,\"authMethod\":\"oauth_token\"}\n",
			wantParsed: true, wantState: ports.AgentAuthStatusConfigured,
		},
		{
			name:   "unparsable output concludes nothing",
			output: "unsupported subcommand on this version",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			report, ok := claudeAuthReportFromOutput([]byte(tc.output))
			if ok != tc.wantParsed {
				t.Fatalf("parsed = %v, want %v", ok, tc.wantParsed)
			}
			if !ok {
				return
			}
			status := report.status()
			if status != tc.wantState {
				t.Fatalf("state = %q, want %q", status, tc.wantState)
			}
			if status == ports.AgentAuthStatusAuthorized {
				t.Fatal("the CLI probe cannot prove a credential works")
			}
		})
	}
}

func TestClaudeAuthReportUsesProjectContext(t *testing.T) {
	previous := claudeAuthCommand
	claudeAuthCommand = func(_ context.Context, binary, workingDir string, env map[string]string) (claudeCommandOutput, error) {
		if binary != "/opt/claude" || workingDir != "/project" || env["CLAUDE_CODE_USE_VERTEX"] != "1" {
			t.Fatalf("command context = binary %q dir %q env %#v", binary, workingDir, env)
		}
		return claudeCommandOutput{Stdout: []byte(`{"loggedIn":true,"apiProvider":"vertex"}`)}, nil
	}
	t.Cleanup(func() { claudeAuthCommand = previous })

	report, ok := (&Plugin{}).claudeCLIAuthReport(context.Background(), "/opt/claude", "/project", map[string]string{
		"CLAUDE_CODE_USE_VERTEX": "1",
	})
	if !ok || report.APIProvider != "vertex" {
		t.Fatalf("report = %#v, parsed = %v", report, ok)
	}
}

// I1: the ladder is strictly additive. Anything it cannot resolve degrades to
// unknown, which never blocks a launch — it must not manufacture an
// unauthorized verdict out of a failure to look.
func TestUnreadableLocalStateDegradesToUnknownNotUnauthorized(t *testing.T) {
	clearClaudeCredentialEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, _ := claudeConfigAuthStatus(context.Background(), path)
	if status == ports.AgentAuthStatusUnauthorized {
		t.Fatal("a parse failure is our bug, not the user's missing credential")
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("state = %q, want %q", status, ports.AgentAuthStatusUnknown)
	}
}

func TestLocalAuthVerdictHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, err := claudeLocalAuthStatus(ctx, agentcreds.ResolveOptions{})
	if err == nil {
		t.Fatal("want the context error")
	}
	if status != ports.AgentAuthStatusUnknown {
		t.Fatalf("state = %q, want %q", status, ports.AgentAuthStatusUnknown)
	}
}

func clearClaudeCredentialEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	for _, name := range append(append([]string{}, claudeCredentialEnv...), "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "ANTHROPIC_FOUNDRY_API_KEY", "ANTHROPIC_FOUNDRY_AUTH_TOKEN") {
		t.Setenv(name, "")
	}
}

// Rung 2 is the only rung permitted to return Authorized.
func TestOnlyTheProbeCanAuthorize(t *testing.T) {
	tests := []struct {
		name  string
		state agentcreds.State
		want  ports.AgentAuthStatus
	}{
		{"provider accepted", agentcreds.StateValid, ports.AgentAuthStatusAuthorized},
		{"provider rejected", agentcreds.StateInvalid, ports.AgentAuthStatusUnauthorized},
		{"could not tell", agentcreds.StateUnknown, ports.AgentAuthStatusUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status := authStatusFromResult(agentcreds.Result{
				State: tc.state, Source: "ANTHROPIC_API_KEY", Fingerprint: "abc123def456",
			})
			if status != tc.want {
				t.Fatalf("state = %q, want %q", status, tc.want)
			}
		})
	}
}

// The runtime 401 handler drops the cached verdict; the provider has just
// contradicted it.
func TestInvalidateAuthCacheClearsTheStoredVerdict(t *testing.T) {
	fingerprint := (agentcreds.Credential{Secret: "k"}).Fingerprint()
	result := agentcreds.Result{
		State: agentcreds.StateValid, Provider: agentcreds.ProviderFirstParty, Fingerprint: fingerprint,
	}
	claudeAuthCache.put(result)
	if _, ok := claudeAuthCache.get(fingerprint, agentcreds.ProviderFirstParty); !ok {
		t.Fatal("expected the verdict to be cached")
	}
	InvalidateAuthCache()
	if _, ok := claudeAuthCache.get(fingerprint, agentcreds.ProviderFirstParty); ok {
		t.Fatal("a runtime rejection must clear the cached verdict")
	}
}

// withStubValidator points the probe at a local server for the duration of a
// test, so no unit test can reach a real provider.
func withStubValidator(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	previous := claudeValidator
	claudeValidator = func() *agentcreds.Validator {
		client := server.Client()
		transport := client.Transport
		client.Transport = claudeTestTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.Scheme+"://"+req.URL.Host != server.URL {
				t.Errorf("blocked unexpected provider request to %s", req.URL.Host)
				return nil, errors.New("unexpected provider host")
			}
			return transport.RoundTrip(req)
		})
		return agentcreds.New(client)
	}
	t.Cleanup(func() { claudeValidator = previous })
	return server
}

type claudeTestTransport func(*http.Request) (*http.Response, error)

func (f claudeTestTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func writeClaudeAuthSettings(t *testing.T, path string, env map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"env": env})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeAuthReportIgnoresStderrJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	clearClaudeCredentialEnv(t)
	project := t.TempDir()
	binary := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"loggedIn\":true,\"apiProvider\":\"vertex\"}'\nprintf '%s\\n' 'warning: {\"quotaProject\":\"missing\"}' >&2\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	report, ok := (&Plugin{}).claudeCLIAuthReport(context.Background(), binary, project, nil)
	if !ok || !report.LoggedIn || report.APIProvider != "vertex" {
		t.Fatalf("stdout auth report was lost: %#v, parsed %v", report, ok)
	}
}

func TestRelativeClaudeConfigDirectoryUsesProjectContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	clearClaudeCredentialEnv(t)
	daemonDir, projectDir := t.TempDir(), t.TempDir()
	t.Chdir(daemonDir)
	for _, fixture := range []struct{ dir, model, token string }{
		{daemonDir, "daemon-model", "daemon-fixture-token"},
		{projectDir, "project-model", "project-fixture-token"},
	} {
		configDir := filepath.Join(fixture.dir, ".claude-custom")
		writeClaudeAuthSettings(t, filepath.Join(configDir, "settings.json"), map[string]string{
			"ANTHROPIC_MODEL": fixture.model, "ANTHROPIC_BASE_URL": "https://gateway.example",
		})
		if err := os.WriteFile(filepath.Join(configDir, ".credentials.json"), []byte(`{"accessToken":"`+fixture.token+`"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	explicitEnv := map[string]string{"CLAUDE_CONFIG_DIR": ".claude-custom"}
	options := agentcreds.ResolveOptions{Env: func(string) string { return "" }, GOOS: "linux"}
	settings := agentcreds.ResolveClaudeSettings(projectDir, explicitEnv, options)
	if settings.Model != "project-model" {
		t.Errorf("settings selected %q, want the project model", settings.Model)
	}
	wantConfigDir := filepath.Join(projectDir, ".claude-custom")
	if settings.Env["CLAUDE_CONFIG_DIR"] != wantConfigDir {
		t.Error("resolved settings did not expose the project-absolute config directory")
	}
	options.WorkingDir, options.CommandEnv = projectDir, explicitEnv
	resolved := options.WithClaudeSettings()
	credential, ok := agentcreds.ResolveLocal(context.Background(), agentcreds.ProviderGateway, resolved)
	if !ok || credential.Secret != "project-fixture-token" || credential.Source != "credentials-file" {
		t.Error("credentials reader did not select the project credential file")
	}
	if resolved.CommandEnv["CLAUDE_CONFIG_DIR"] != wantConfigDir {
		t.Error("child command environment did not use the same absolute config directory")
	}
	// Execute a local auth-status fixture so the assertions cover the real child
	// cwd/environment boundary as well as the Go-side readers. No real CLI,
	// credential store, or provider request is involved.
	binary := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
test "$1" = auth && test "$2" = status || exit 2
case "$CLAUDE_CONFIG_DIR" in /*) ;; *) exit 3 ;; esac
test "$CLAUDE_CONFIG_DIR" -ef "$PWD/.claude-custom" || exit 4
test "$ANTHROPIC_MODEL" = project-model || exit 5
settings=$(cat "$CLAUDE_CONFIG_DIR/settings.json") || exit 6
case "$settings" in *'"ANTHROPIC_MODEL":"project-model"'*) ;; *) exit 7 ;; esac
IFS= read -r credential < "$CLAUDE_CONFIG_DIR/.credentials.json" || exit 8
test "$credential" = '{"accessToken":"project-fixture-token"}' || exit 9
printf '%s\n' '{"loggedIn":true,"apiProvider":"gateway"}'
`
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	report, parsed := (&Plugin{}).claudeCLIAuthReport(context.Background(), binary, resolved.WorkingDir, resolved.CommandEnv)
	if !parsed || !report.LoggedIn || report.APIProvider != "gateway" {
		t.Error("auth-status child did not read the same project settings and credentials")
	}
	if explicitEnv["CLAUDE_CONFIG_DIR"] != ".claude-custom" {
		t.Fatal("explicit caller environment was mutated")
	}
}

func TestUserGatewaySettingsOverrideSignedOutFirstPartyAuth(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			clearClaudeCredentialEnv(t)
			InvalidateAuthCache()
			t.Cleanup(InvalidateAuthCache)
			t.Setenv("ANTHROPIC_API_KEY", "daemon-first-party-key")
			requests := 0
			server := withStubValidator(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("x-api-key") != "settings-gateway-key" {
					t.Error("gateway did not receive settings credential")
				}
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = w.Write([]byte(`{"data":[{"id":"kimi-k2"}]}`))
				}
			})
			writeClaudeAuthSettings(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json"), map[string]string{
				"ANTHROPIC_BASE_URL": server.URL, "ANTHROPIC_API_KEY": "settings-gateway-key",
			})
			previous := claudeAuthCommand
			claudeAuthCommand = func(_ context.Context, _, _ string, env map[string]string) (claudeCommandOutput, error) {
				if env["ANTHROPIC_BASE_URL"] != server.URL || env["ANTHROPIC_API_KEY"] != "settings-gateway-key" {
					t.Error("auth status did not receive the settings environment")
				}
				return claudeCommandOutput{Stdout: []byte(`{"loggedIn":false,"apiProvider":"firstParty"}`)}, nil
			}
			t.Cleanup(func() { claudeAuthCommand = previous })
			state, err := (&Plugin{resolvedBinary: "/fixture/claude"}).AuthStatus(context.Background())
			want := ports.AgentAuthStatusAuthorized
			if status == http.StatusNotFound {
				want = ports.AgentAuthStatusConfigured
			}
			if err != nil || state != want || requests != 1 {
				t.Fatalf("state=%q err=%v gateway requests=%d, want %q and one request", state, err, requests, want)
			}
		})
	}
}

func TestProviderModelsUsesMergedProjectSettings(t *testing.T) {
	clearClaudeCredentialEnv(t)
	InvalidateAuthCache()
	t.Cleanup(InvalidateAuthCache)
	t.Setenv("ANTHROPIC_API_KEY", "daemon-key")
	project := t.TempDir()
	requests := 0
	server := withStubValidator(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("x-api-key") != "session-key" {
			t.Error("explicit credential did not win")
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"glm-5"}]}`))
	})
	writeClaudeAuthSettings(t, filepath.Join(project, ".claude", "settings.local.json"), map[string]string{
		"ANTHROPIC_BASE_URL": server.URL, "ANTHROPIC_API_KEY": "settings-key", "ANTHROPIC_MODEL": "glm-5", "SECRET": "must-not-load",
	})
	previous := claudeModelAuthReport
	claudeModelAuthReport = func(_ context.Context, binary, dir string, env map[string]string) (claudeAuthReport, bool) {
		if binary != "/fixture/claude" || dir != project || env["ANTHROPIC_BASE_URL"] != server.URL || env["ANTHROPIC_API_KEY"] != "session-key" || env["ANTHROPIC_MODEL"] != "glm-5" || env["CLOUDSDK_CONFIG"] != "/fixture/gcloud" || env["LAUNCH_ONLY"] != "preserved" {
			t.Error("auth status did not receive the merged launch context")
		}
		if _, ok := env["SECRET"]; ok {
			t.Error("unapproved settings env escaped")
		}
		return claudeAuthReport{LoggedIn: false, APIProvider: "firstParty"}, true
	}
	t.Cleanup(func() { claudeModelAuthReport = previous })
	env := map[string]string{"ANTHROPIC_API_KEY": "session-key", "CLOUDSDK_CONFIG": "/fixture/gcloud", "LAUNCH_ONLY": "preserved"}
	models, err := ProviderModels(context.Background(), "/fixture/claude", project, env)
	if err != nil || len(models) != 1 || models[0].ID != "glm-5" || requests != 1 {
		t.Fatalf("models=%v err=%v requests=%d", models, err, requests)
	}
	if _, changed := env["ANTHROPIC_BASE_URL"]; changed {
		t.Fatal("caller environment was mutated")
	}
}

// Rungs 1 and 2 end to end: the provider accepts, so the ladder returns the
// one state no local rung is allowed to produce.
func TestProbeAuthorizesOnlyOnAProviderAcceptance(t *testing.T) {
	clearClaudeCredentialEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-works")
	InvalidateAuthCache()
	server := withStubValidator(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-4-5-20251101"}]}`))
	})
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)

	status, ok := (&Plugin{}).probeAuthStatus(context.Background(), claudeAuthReport{APIProvider: "gateway"}, true, (agentcreds.ResolveOptions{}).WithClaudeSettings())
	if !ok {
		t.Fatal("a definite provider answer must stop the ladder")
	}
	if status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("state = %q, want authorized", status)
	}
}

// A revoked key is what this whole change exists for: presence resolves it,
// and the provider is what turns it into a rejection.
func TestProbeRejectsARevokedCredential(t *testing.T) {
	clearClaudeCredentialEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-revoked")
	InvalidateAuthCache()
	server := withStubValidator(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"API key is invalid."}}`))
	})
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)

	status, ok := (&Plugin{}).probeAuthStatus(context.Background(), claudeAuthReport{APIProvider: "gateway"}, true, (agentcreds.ResolveOptions{}).WithClaudeSettings())
	if !ok || status != ports.AgentAuthStatusUnauthorized {
		t.Fatalf("status = %q, want a verified rejection", status)
	}
}

// Rung 1 is a gate, not a guess: an apiProvider this build cannot validate
// must stop the probe rather than pick a host.
func TestProbeGateStopsBeforeSendingAnythingForAnUnknownProvider(t *testing.T) {
	clearClaudeCredentialEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-key")
	InvalidateAuthCache()
	withStubValidator(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("an unrecognized provider must not produce any request")
	})

	if _, ok := (&Plugin{}).probeAuthStatus(
		context.Background(), claudeAuthReport{APIProvider: "some-future-provider"}, true, (agentcreds.ResolveOptions{}).WithClaudeSettings(),
	); ok {
		t.Fatal("an unrecognized provider must hand down the ladder, not answer it")
	}
}

// I1 at the probe rung: an inconclusive probe hands down so the lower rungs
// can still speak, and never becomes a rejection of its own.
func TestInconclusiveProbeHandsDownTheLadder(t *testing.T) {
	clearClaudeCredentialEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-key")
	InvalidateAuthCache()
	server := withStubValidator(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)

	if _, ok := (&Plugin{}).probeAuthStatus(
		context.Background(), claudeAuthReport{APIProvider: "gateway"}, true, (agentcreds.ResolveOptions{}).WithClaudeSettings(),
	); ok {
		t.Fatal("a provider outage must not stop the ladder with a verdict")
	}
}

// The cache is consulted before anything is sent, and answers from the stored
// verdict when the credential has not changed.
func TestProbeAnswersFromCacheWithoutReprobing(t *testing.T) {
	clearClaudeCredentialEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-cached")
	InvalidateAuthCache()
	t.Cleanup(InvalidateAuthCache)

	claudeAuthCache.put(agentcreds.Result{
		State: agentcreds.StateValid, Provider: agentcreds.ProviderFirstParty, Source: "ANTHROPIC_API_KEY",
		Fingerprint: (agentcreds.Credential{
			Kind: agentcreds.KindAPIKey, Secret: "sk-ant-cached", Provider: agentcreds.ProviderFirstParty,
		}).Fingerprint(),
	})
	withStubValidator(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("a cache hit must not reach the provider")
	})

	status, ok := (&Plugin{}).probeAuthStatus(context.Background(), claudeAuthReport{APIProvider: "firstParty"}, true, (agentcreds.ResolveOptions{}).WithClaudeSettings())
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want the cached acceptance", status)
	}
}

// The provider response that proves the credential works also owns the model
// and effort catalog. Model discovery must reuse that exact response instead
// of issuing a second validation request that can fail independently.
func TestProviderModelsReuseTheValidatedAuthResponse(t *testing.T) {
	clearClaudeCredentialEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-cached-models")
	InvalidateAuthCache()
	t.Cleanup(InvalidateAuthCache)

	requests := 0
	server := withStubValidator(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-5","display_name":"Claude Opus 5","capabilities":{"effort":{"supported":true,"low":{"supported":true},"medium":{"supported":true},"high":{"supported":true},"xhigh":{"supported":true},"max":{"supported":true}}}}]}`))
	})
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)

	status, ok := (&Plugin{}).probeAuthStatus(
		context.Background(), claudeAuthReport{APIProvider: "gateway"}, true, (agentcreds.ResolveOptions{}).WithClaudeSettings(),
	)
	if !ok || status != ports.AgentAuthStatusAuthorized {
		t.Fatalf("status = %q, want the provider acceptance", status)
	}

	models, err := ProviderModels(context.Background(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("models = %+v, want the validated provider model", models)
	}
	if models[0].ID != "claude-opus-5" || models[0].Label != "Claude Opus 5" {
		t.Fatalf("model = %+v, want the provider identity and label", models[0])
	}
	wantEfforts := []string{"low", "medium", "high", "xhigh", "max"}
	if !slices.Equal(models[0].Efforts, wantEfforts) {
		t.Fatalf("efforts = %v, want %v", models[0].Efforts, wantEfforts)
	}
	if requests != 1 {
		t.Fatalf("provider requests = %d, want one validation response reused for discovery", requests)
	}
}

func TestProviderModelsRefreshesAnAuthOnlyCacheEntry(t *testing.T) {
	clearClaudeCredentialEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-auth-only")
	InvalidateAuthCache()
	t.Cleanup(InvalidateAuthCache)
	requests := 0
	server := withStubValidator(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-5"}]}`))
	})
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)
	credential := agentcreds.Credential{
		Kind: agentcreds.KindAPIKey, Secret: "sk-ant-auth-only", Provider: agentcreds.ProviderGateway,
		BaseURL: server.URL,
	}
	claudeAuthCache.put(agentcreds.Result{
		State: agentcreds.StateValid, Provider: agentcreds.ProviderGateway,
		Fingerprint: credential.Fingerprint(),
	})

	models, err := ProviderModels(context.Background(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || requests != 1 {
		t.Fatalf("models/requests = %+v/%d, want one fresh provider model", models, requests)
	}
}

func TestProviderModelsRunsCLIOncePerDiscoveryAndCachesProviderResult(t *testing.T) {
	clearClaudeCredentialEnv(t)
	InvalidateAuthCache()
	t.Cleanup(InvalidateAuthCache)

	requests := 0
	server := withStubValidator(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-5"}]}`))
	})
	env := map[string]string{
		"ANTHROPIC_API_KEY":  "project-key",
		"ANTHROPIC_BASE_URL": server.URL,
	}
	cliReports := 0
	previous := claudeModelAuthReport
	claudeModelAuthReport = func(context.Context, string, string, map[string]string) (claudeAuthReport, bool) {
		cliReports++
		return claudeAuthReport{APIProvider: "gateway"}, true
	}
	t.Cleanup(func() { claudeModelAuthReport = previous })

	for range 2 {
		models, err := ProviderModels(context.Background(), "/opt/claude", "/workspace", env)
		if err != nil {
			t.Fatal(err)
		}
		if len(models) != 1 || models[0].ID != "claude-opus-5" {
			t.Fatalf("models = %+v, want cached provider catalog", models)
		}
	}
	if requests != 1 {
		t.Fatalf("provider requests = %d, want one", requests)
	}
	if cliReports != 2 {
		t.Fatalf("claude auth status calls = %d, want one per discovery", cliReports)
	}
}

func TestProviderModelsCachesInconclusiveResultBriefly(t *testing.T) {
	clearClaudeCredentialEnv(t)
	InvalidateAuthCache()
	t.Cleanup(InvalidateAuthCache)

	requests := 0
	server := withStubValidator(t, func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	})
	env := map[string]string{
		"ANTHROPIC_API_KEY":  "project-key",
		"ANTHROPIC_BASE_URL": server.URL,
	}
	for range 2 {
		if _, err := ProviderModels(context.Background(), "", "/workspace", env); err == nil {
			t.Fatal("inconclusive provider response should fail discovery")
		}
	}
	if requests != 1 {
		t.Fatalf("provider requests = %d, want one cached inconclusive probe", requests)
	}
}

func TestProviderModelsUsesCLIReportedProvider(t *testing.T) {
	clearClaudeCredentialEnv(t)
	// Keep the preliminary first-party resolution away from the real keychain.
	t.Setenv("ANTHROPIC_API_KEY", "unused-first-party-fixture")
	InvalidateAuthCache()
	t.Cleanup(InvalidateAuthCache)

	server := withStubValidator(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1beta1/publishers/anthropic/models") {
			t.Fatalf("path = %q, want Vertex model catalog", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"publisherModels":[{"name":"publishers/anthropic/models/claude-vertex"}]}`))
	})
	env := map[string]string{
		"ANTHROPIC_API_KEY":         "stale-first-party-key",
		"GOOGLE_OAUTH_ACCESS_TOKEN": "vertex-token",
		"GOOGLE_CLOUD_PROJECT":      "project",
		"ANTHROPIC_VERTEX_BASE_URL": server.URL,
	}
	firstParty := agentcreds.Credential{
		Kind: agentcreds.KindAPIKey, Secret: env["ANTHROPIC_API_KEY"], Provider: agentcreds.ProviderFirstParty,
	}
	claudeAuthCache.put(agentcreds.Result{
		State: agentcreds.StateValid, Provider: agentcreds.ProviderFirstParty,
		Fingerprint: firstParty.Fingerprint(), Models: []agentcreds.Model{{ID: "claude-wrong-provider"}},
	})

	previous := claudeModelAuthReport
	claudeModelAuthReport = func(_ context.Context, binary, workingDir string, gotEnv map[string]string) (claudeAuthReport, bool) {
		if binary != "/opt/claude" {
			t.Fatalf("binary = %q, want /opt/claude", binary)
		}
		if workingDir != "/workspace" || gotEnv["GOOGLE_CLOUD_PROJECT"] != "project" {
			t.Fatalf("discovery context = %q %#v", workingDir, gotEnv)
		}
		return claudeAuthReport{APIProvider: "vertex"}, true
	}
	t.Cleanup(func() { claudeModelAuthReport = previous })

	models, err := ProviderModels(context.Background(), "/opt/claude", "/workspace", env)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "claude-vertex" {
		t.Fatalf("models = %+v, want the CLI-reported Vertex catalog", models)
	}
}

func TestProviderModelsUsesConfiguredFoundryDeployments(t *testing.T) {
	InvalidateAuthCache()
	models, err := ProviderModels(context.Background(), "", "/workspace", map[string]string{
		"CLAUDE_CODE_USE_FOUNDRY":        "1",
		"ANTHROPIC_FOUNDRY_API_KEY":      "key",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "sonnet-deployment",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "sonnet-deployment" {
		t.Fatalf("models = %+v", models)
	}
}
