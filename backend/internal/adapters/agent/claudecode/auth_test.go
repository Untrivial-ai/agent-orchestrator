package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
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
