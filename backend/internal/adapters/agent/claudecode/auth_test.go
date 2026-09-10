package claudecode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

			verdict, err := claudeLocalAuthVerdict(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if verdict.State == ports.AgentAuthStatusAuthorized {
				t.Fatalf("%s present reported as authorized; presence is not validity", name)
			}
			if verdict.State != ports.AgentAuthStatusConfigured {
				t.Fatalf("state = %q, want %q", verdict.State, ports.AgentAuthStatusConfigured)
			}
			if verdict.Verified {
				t.Fatal("local evidence must never be marked verified")
			}
			if verdict.Credential != name {
				t.Fatalf("credential = %q, want %q", verdict.Credential, name)
			}
			if verdict.Fingerprint == "" {
				t.Fatal("fingerprint must be set so a credential change invalidates the cache")
			}
		})
	}
}

// Precedence: the reported credential must be the one Claude Code will
// actually send, or the diagnostics point at the wrong variable.
func TestLocalAuthVerdictPrefersOAuthTokenOverAPIKey(t *testing.T) {
	clearClaudeCredentialEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "auth-token")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-token")

	verdict, err := claudeLocalAuthVerdict(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if verdict.Credential != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Fatalf("credential = %q, want CLAUDE_CODE_OAUTH_TOKEN", verdict.Credential)
	}
	if verdict.Fingerprint != ports.CredentialFingerprint("oauth-token") {
		t.Fatalf("fingerprint is not the winning credential's")
	}
}

func TestFingerprintNeverLeaksTheSecret(t *testing.T) {
	const secret = "sk-ant-super-secret-value"
	fingerprint := ports.CredentialFingerprint(secret)
	if len(fingerprint) != 12 {
		t.Fatalf("fingerprint = %q, want 12 hex characters", fingerprint)
	}
	if fingerprint == secret || len(fingerprint) >= len(secret) {
		t.Fatal("fingerprint must be shorter than and unequal to the secret")
	}
	if ports.CredentialFingerprint("  ") != "" {
		t.Fatal("an empty credential has no fingerprint")
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
			verdict, err := claudeConfigAuthVerdict(path)
			if err != nil {
				t.Fatal(err)
			}
			if verdict.State != tc.want {
				t.Fatalf("state = %q, want %q", verdict.State, tc.want)
			}
			if verdict.Verified {
				t.Fatal("a config-file read is never a verified verdict")
			}
		})
	}
}

func TestConfigAuthVerdictMissingFileIsUnknown(t *testing.T) {
	verdict, err := claudeConfigAuthVerdict(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if verdict.State != ports.AgentAuthStatusUnknown {
		t.Fatalf("state = %q, want %q", verdict.State, ports.AgentAuthStatusUnknown)
	}
}

func TestCLIReportVerdict(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		wantParsed   bool
		wantState    ports.AgentAuthStatus
		wantVerified bool
		wantCred     string
	}{
		{
			name:       "logged in is configured, not authorized",
			output:     `{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"pro"}`,
			wantParsed: true, wantState: ports.AgentAuthStatusConfigured, wantCred: "claude.ai",
		},
		{
			name:       "api key source is reported as the credential",
			output:     `{"loggedIn":true,"apiKeySource":"ANTHROPIC_API_KEY","authMethod":"claude.ai"}`,
			wantParsed: true, wantState: ports.AgentAuthStatusConfigured, wantCred: "ANTHROPIC_API_KEY",
		},
		{
			name:       "signed out is a verified rejection",
			output:     `{"loggedIn":false}`,
			wantParsed: true, wantState: ports.AgentAuthStatusUnauthorized, wantVerified: true,
		},
		{
			name:       "warning lines around the json are tolerated",
			output:     "warning: ignored config line\n{\"loggedIn\":true,\"authMethod\":\"oauth_token\"}\n",
			wantParsed: true, wantState: ports.AgentAuthStatusConfigured, wantCred: "oauth_token",
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
			verdict := report.verdict()
			if verdict.State != tc.wantState {
				t.Fatalf("state = %q, want %q", verdict.State, tc.wantState)
			}
			if verdict.Verified != tc.wantVerified {
				t.Fatalf("verified = %v, want %v", verdict.Verified, tc.wantVerified)
			}
			if verdict.Credential != tc.wantCred {
				t.Fatalf("credential = %q, want %q", verdict.Credential, tc.wantCred)
			}
			if verdict.State == ports.AgentAuthStatusAuthorized {
				t.Fatal("the CLI probe cannot prove a credential works")
			}
		})
	}
}

func TestParseAuthReportSurfacesDiagnostics(t *testing.T) {
	report, ok := ParseAuthReport([]byte(
		`{"loggedIn":true,"apiKeySource":"ANTHROPIC_API_KEY","apiProvider":"firstParty","authMethod":"claude.ai","subscriptionType":"pro"}`,
	))
	if !ok {
		t.Fatal("report did not parse")
	}
	if report.APIKeySource != "ANTHROPIC_API_KEY" {
		t.Fatalf("apiKeySource = %q", report.APIKeySource)
	}
	if report.APIProvider != "firstParty" || report.AuthMethod != "claude.ai" || report.SubscriptionType != "pro" {
		t.Fatalf("diagnostics lost: %+v", report)
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
	verdict, _ := claudeConfigAuthVerdict(path)
	if verdict.State == ports.AgentAuthStatusUnauthorized {
		t.Fatal("a parse failure is our bug, not the user's missing credential")
	}
	if verdict.State != ports.AgentAuthStatusUnknown {
		t.Fatalf("state = %q, want %q", verdict.State, ports.AgentAuthStatusUnknown)
	}
}

func TestLocalAuthVerdictHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	verdict, err := claudeLocalAuthVerdict(ctx)
	if err == nil {
		t.Fatal("want the context error")
	}
	if verdict.State != ports.AgentAuthStatusUnknown {
		t.Fatalf("state = %q, want %q", verdict.State, ports.AgentAuthStatusUnknown)
	}
}

func clearClaudeCredentialEnv(t *testing.T) {
	t.Helper()
	for _, name := range claudeCredentialEnv {
		t.Setenv(name, "")
	}
}

// Rung 2 is the only rung permitted to return Authorized, and the only one
// that may mark a verdict verified.
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
			verdict := verdictFromResult(agentcreds.Result{
				State: tc.state, Source: "ANTHROPIC_API_KEY", Fingerprint: "abc123def456",
			})
			if verdict.State != tc.want {
				t.Fatalf("state = %q, want %q", verdict.State, tc.want)
			}
			wantVerified := tc.state != agentcreds.StateUnknown
			if verdict.Verified != wantVerified {
				t.Fatalf("verified = %v, want %v", verdict.Verified, wantVerified)
			}
			if verdict.Verified && verdict.Source != ports.AuthSourceProbe {
				t.Fatalf("source = %q, want %q", verdict.Source, ports.AuthSourceProbe)
			}
		})
	}
}

// An unknown probe result must never be marked verified, or a "we couldn't
// tell" would be presented with the authority of a real answer.
func TestUnknownProbeResultIsNeverVerified(t *testing.T) {
	verdict := verdictFromResult(agentcreds.Result{State: agentcreds.StateUnknown})
	if verdict.Verified {
		t.Fatal("an unknown result must not claim to be verified")
	}
}

// The runtime 401 handler drops the cached verdict; the provider has just
// contradicted it.
func TestInvalidateAuthCacheClearsTheStoredVerdict(t *testing.T) {
	result := agentcreds.Result{State: agentcreds.StateValid, Fingerprint: agentcreds.Fingerprint("k")}
	claudeAuthCache.Put(claudeAgentID, result)
	if _, ok := claudeAuthCache.Get(claudeAgentID, agentcreds.Fingerprint("k")); !ok {
		t.Fatal("expected the verdict to be cached")
	}
	InvalidateAuthCache()
	if _, ok := claudeAuthCache.Get(claudeAgentID, agentcreds.Fingerprint("k")); ok {
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
		return agentcreds.New(agentcreds.WithHTTPClient(server.Client()))
	}
	t.Cleanup(func() { claudeValidator = previous })
	return server
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

	verdict, ok := (&Plugin{}).probeVerdict(context.Background(), claudeAuthReport{APIProvider: "gateway"}, true)
	if !ok {
		t.Fatal("a definite provider answer must stop the ladder")
	}
	if verdict.State != ports.AgentAuthStatusAuthorized {
		t.Fatalf("state = %q, want authorized", verdict.State)
	}
	if !verdict.Verified || verdict.Source != ports.AuthSourceProbe {
		t.Fatalf("a provider acceptance must be a verified probe verdict: %+v", verdict)
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

	verdict, ok := (&Plugin{}).probeVerdict(context.Background(), claudeAuthReport{APIProvider: "gateway"}, true)
	if !ok || verdict.State != ports.AgentAuthStatusUnauthorized {
		t.Fatalf("verdict = %+v, want a verified rejection", verdict)
	}
	if verdict.Credential != "ANTHROPIC_API_KEY" {
		t.Fatalf("credential = %q, want the env var that supplied it", verdict.Credential)
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

	if _, ok := (&Plugin{}).probeVerdict(
		context.Background(), claudeAuthReport{APIProvider: "some-future-provider"}, true,
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

	if _, ok := (&Plugin{}).probeVerdict(
		context.Background(), claudeAuthReport{APIProvider: "gateway"}, true,
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

	claudeAuthCache.Put(claudeAgentID, agentcreds.Result{
		State: agentcreds.StateValid, Source: "ANTHROPIC_API_KEY",
		Fingerprint: agentcreds.Fingerprint("sk-ant-cached"),
	})
	withStubValidator(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("a cache hit must not reach the provider")
	})

	verdict, ok := (&Plugin{}).probeVerdict(context.Background(), claudeAuthReport{APIProvider: "firstParty"}, true)
	if !ok || verdict.State != ports.AgentAuthStatusAuthorized {
		t.Fatalf("verdict = %+v, want the cached acceptance", verdict)
	}
}
