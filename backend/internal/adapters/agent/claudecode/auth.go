package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
	"github.com/aoagents/agent-orchestrator/backend/pkg/agentcreds"
)

// The auth ladder.
//
// Every rung answers only what it can prove, and a rung that cannot answer
// hands down rather than guessing. The ladder always terminates in a verdict
// that never blocks a launch.
//
//	rung 0  binary        ResolveBinary        → unavailable (install, not login)
//	rung 1  provider gate apiProvider          → stop unless we can validate it
//	rung 2  network probe GET model list       → authorized or unauthorized
//	rung 3  cli probe     claude auth status   → unauthorized, or configured
//	rung 4  local         ~/.claude.json + env → configured, or unauthorized
//
// Rung 2 is the only rung that can prove a credential works, and so the only
// one permitted to return Authorized. Everything below it reports what is
// configured, which is a different question: presence of a credential was once
// reported as proof of authentication, so a revoked ANTHROPIC_API_KEY in a
// shell profile rendered as "ready" while every turn returned 401. Validity is
// a server-side fact — a credential can be revoked, downgraded, or rate-limited
// with no change on disk — so local evidence yields
// ports.AgentAuthStatusConfigured, which renders neutral, and never Authorized.
//
// The ladder is strictly additive at every step. A rung that cannot answer
// hands down rather than guessing, and the whole thing terminates in a verdict
// that never blocks a launch.
const claudeAuthProbeTimeout = 3 * time.Second

// claudeCredentialEnv is the environment-variable ladder, in the precedence
// Claude Code itself applies. CLAUDE_CODE_OAUTH_TOKEN beats ANTHROPIC_API_KEY:
// the first entry present wins, so the reported credential name matches the
// one the agent will actually send.
var claudeCredentialEnv = []string{
	"CLAUDE_CODE_OAUTH_TOKEN",
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
}

// AuthStatus reports Claude Code's authentication state without starting a
// session. It is the AgentAuthChecker projection of AuthVerdict.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	verdict, err := p.AuthVerdict(ctx)
	return verdict.State, err
}

// AuthVerdict runs the ladder and reports the state together with the
// provenance that produced it, so callers can tell a proven verdict from a
// guessed one.
func (p *Plugin) AuthVerdict(ctx context.Context) (ports.AuthVerdict, error) {
	// Rung 0 — installed? A missing binary is not an auth failure; saying so
	// would point the user at a login when they need an install.
	binary, err := p.claudeBinary(ctx)
	if err != nil {
		if errors.Is(err, ports.ErrAgentBinaryNotFound) {
			return ports.AuthVerdict{
				State: ports.AgentAuthStatusUnavailable, Source: ports.AuthSourceBinary, CheckedAt: time.Now(),
			}, nil
		}
		return unknownVerdict(ports.AuthSourceBinary), err
	}

	// Rung 3 runs first among the evidence rungs, even though rung 2 outranks
	// it, because its output is what rung 2 needs: apiProvider decides whether
	// a first-party probe is even the right thing to send. Inferring the
	// provider from environment variables instead would risk pointing a
	// Bedrock credential at api.anthropic.com.
	//
	// It also carries diagnostics no other rung has — apiKeySource names an
	// env var shadowing a subscription, authMethod separates a claude.ai login
	// from a setup-token — so it runs for those even when rung 2 answers.
	report, cliOK := p.claudeCLIAuthReport(ctx, binary)

	// Rungs 1 and 2 — the provider gate and the network probe. This is the
	// only rung that can prove a credential works, so it is the only one
	// allowed to return Authorized.
	if verdict, ok := p.probeVerdict(ctx, report, cliOK); ok {
		return verdict, nil
	}

	// Rung 3's own verdict. It cannot prove success, but it is the only local
	// check that can observe a definite "signed out".
	if cliOK {
		verdict := report.verdict()
		if verdict.State != ports.AgentAuthStatusUnknown {
			return verdict, nil
		}
	}

	// Rung 4 — local heuristic. Lowest confidence, and the last word only
	// because it never blocks anything.
	return claudeLocalAuthVerdict(ctx)
}

// probeVerdict runs the provider gate and the network probe.
//
// ok=false means the ladder must fall through: the provider is not one this
// build validates, no credential could be resolved, or the probe could not
// reach a conclusion. Only a definite provider answer stops the ladder here,
// which is what keeps the whole check additive — it can convert an Unknown
// into a real verdict, and can never manufacture a worse one.
func (p *Plugin) probeVerdict(ctx context.Context, report claudeAuthReport, cliOK bool) (ports.AuthVerdict, bool) {
	reported := ""
	if cliOK {
		reported = report.APIProvider
	}
	opts := agentcreds.ResolveOptions{
		// Q1 assumption: the Cloud design's keychain prohibition governs
		// shipping local credentials into Cloud sandboxes, not the local
		// daemon reading the local keychain to validate a local login. Every
		// keychain call sits behind this flag, so reversing that reading costs
		// only source 5.
		AllowKeychain: true,
	}
	provider, ok := agentcreds.ResolveProvider(reported, opts)
	if !ok {
		return ports.AuthVerdict{}, false
	}
	cred, found := agentcreds.ResolveLocal(ctx, provider, opts)

	// The cache is read before anything is sent, and is keyed on the
	// credential's fingerprint: a verdict about a credential the agent no
	// longer uses is not evidence about anything.
	if found {
		if cached, hit := p.authCache().Get(claudeAgentID, cred.Fingerprint()); hit {
			return verdictFromResult(cached), true
		}
	}

	probeCtx, cancel := context.WithTimeout(ctx, agentcreds.DefaultTimeout)
	defer cancel()
	result := claudeValidator().ValidateLocal(probeCtx, reported, opts)
	if result.State == agentcreds.StateUnknown {
		return ports.AuthVerdict{}, false
	}
	p.authCache().Put(claudeAgentID, result)
	return verdictFromResult(result), true
}

// verdictFromResult projects a provider verdict onto AO's vocabulary. Only
// this function may produce a verified verdict.
func verdictFromResult(result agentcreds.Result) ports.AuthVerdict {
	verdict := ports.AuthVerdict{
		Source:      ports.AuthSourceProbe,
		Verified:    true,
		Credential:  result.Source,
		Fingerprint: result.Fingerprint,
		CheckedAt:   result.CheckedAt,
	}
	switch result.State {
	case agentcreds.StateValid:
		verdict.State = ports.AgentAuthStatusAuthorized
	case agentcreds.StateInvalid:
		verdict.State = ports.AgentAuthStatusUnauthorized
	default:
		verdict.State = ports.AgentAuthStatusUnknown
		verdict.Verified = false
	}
	return verdict
}

// claudeAgentID keys this adapter's cache entry.
const claudeAgentID = "claude-code"

// claudeValidator builds the credential validator. It is a variable so tests
// can point the probe at a local server: a unit test must never be able to
// reach api.anthropic.com, both because that makes it flaky and because a test
// machine's real credentials are not the test's business.
var claudeValidator = func() *agentcreds.Validator { return agentcreds.New() }

// authCache returns the process-wide verdict cache. It is package-level
// because the verdict is about the machine's credentials, not about any one
// plugin instance, and the readiness coordinator builds fresh adapters.
func (p *Plugin) authCache() *agentcreds.Cache { return claudeAuthCache }

var claudeAuthCache = agentcreds.NewCache(agentcreds.DefaultCacheTTL)

// InvalidateAuthCache drops the cached verdict. The runtime 401 handler calls
// it: the provider has just contradicted whatever was stored.
func InvalidateAuthCache() { claudeAuthCache.Invalidate(claudeAgentID) }

// claudeAuthReport is the parsed shape of `claude auth status --json`. Only
// LoggedIn drives the verdict; the rest is diagnostics.
type claudeAuthReport struct {
	LoggedIn         bool   `json:"loggedIn"`
	APIKeySource     string `json:"apiKeySource"`
	APIProvider      string `json:"apiProvider"`
	AuthMethod       string `json:"authMethod"`
	SubscriptionType string `json:"subscriptionType"`
}

// verdict maps a CLI report onto a verdict. loggedIn:true is credentials
// present, not credentials valid — hence configured, never authorized.
func (r claudeAuthReport) verdict() ports.AuthVerdict {
	verdict := ports.AuthVerdict{
		Source:     ports.AuthSourceCLI,
		Credential: strings.TrimSpace(r.APIKeySource),
		CheckedAt:  time.Now(),
	}
	if verdict.Credential == "" && strings.TrimSpace(r.AuthMethod) != "" {
		verdict.Credential = strings.TrimSpace(r.AuthMethod)
	}
	if r.LoggedIn {
		verdict.State = ports.AgentAuthStatusConfigured
		return verdict
	}
	// A CLI that positively reports signed out is evidence, not a guess: it
	// consulted the same credential sources the agent will use.
	verdict.State = ports.AgentAuthStatusUnauthorized
	verdict.Verified = true
	return verdict
}

// claudeCLIAuthReport runs the CLI probe under a hard timeout. ok=false means
// the probe could not answer — a timeout, an exec failure, or a version whose
// output this build cannot parse — and the caller falls to the next rung.
func (p *Plugin) claudeCLIAuthReport(ctx context.Context, binary string) (claudeAuthReport, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, claudeAuthProbeTimeout)
	defer cancel()

	out, err := aoprocess.CommandContext(probeCtx, binary, "auth", "status").CombinedOutput()
	if probeCtx.Err() != nil {
		return claudeAuthReport{}, false
	}
	// An unfamiliar non-zero result is not affirmative evidence of missing
	// credentials, so the exit code is not consulted: only parsable output is.
	_ = err
	return claudeAuthReportFromOutput(out)
}

// claudeAuthReportFromOutput extracts the JSON object the CLI prints, which may
// be surrounded by human-readable lines.
func claudeAuthReportFromOutput(out []byte) (claudeAuthReport, bool) {
	start := bytes.IndexByte(out, '{')
	end := bytes.LastIndexByte(out, '}')
	if start < 0 || end < start {
		return claudeAuthReport{}, false
	}
	var report claudeAuthReport
	if json.Unmarshal(out[start:end+1], &report) != nil {
		return claudeAuthReport{}, false
	}
	return report, true
}

// claudeLocalAuthVerdict is rung 4: environment variables, then ~/.claude.json.
// It reports what is configured. It can never report authorized.
func claudeLocalAuthVerdict(ctx context.Context) (ports.AuthVerdict, error) {
	if err := ctx.Err(); err != nil {
		return unknownVerdict(ports.AuthSourceLocal), err
	}
	for _, name := range claudeCredentialEnv {
		value := strings.TrimSpace(os.Getenv(name))
		if value == "" {
			continue
		}
		return ports.AuthVerdict{
			State:       ports.AgentAuthStatusConfigured,
			Source:      ports.AuthSourceLocal,
			Credential:  name,
			Fingerprint: ports.CredentialFingerprint(value),
			CheckedAt:   time.Now(),
		}, nil
	}
	cfgPath, err := claudeConfigPath()
	if err != nil {
		return unknownVerdict(ports.AuthSourceLocal), err
	}
	return claudeConfigAuthVerdict(cfgPath)
}

// claudeConfigAuthVerdict reads the durable markers Claude Code writes into
// ~/.claude.json. userID in particular is written once at first login and is
// never removed on logout or revocation, so it says only "this machine has
// signed in at some point" — a configured signal, never an authorized one.
func claudeConfigAuthVerdict(path string) (ports.AuthVerdict, error) {
	configured := ports.AuthVerdict{
		State: ports.AgentAuthStatusConfigured, Source: ports.AuthSourceLocal,
		Credential: "claude-config", CheckedAt: time.Now(),
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return unknownVerdict(ports.AuthSourceLocal), nil
	}
	if err != nil {
		return unknownVerdict(ports.AuthSourceLocal), err
	}
	if strings.TrimSpace(string(data)) == "" {
		return unknownVerdict(ports.AuthSourceLocal), nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return unknownVerdict(ports.AuthSourceLocal), err
	}
	var hasSubscription bool
	if raw := root["hasAvailableSubscription"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &hasSubscription)
	}
	var userID string
	if raw := root["userID"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &userID)
	}
	if strings.TrimSpace(userID) != "" {
		return configured, nil
	}
	var oauthAccount map[string]any
	if raw := root["oauthAccount"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &oauthAccount); err != nil {
			return unknownVerdict(ports.AuthSourceLocal), err
		}
	}
	if len(oauthAccount) == 0 {
		return unknownVerdict(ports.AuthSourceLocal), nil
	}
	if hasSubscription {
		return configured, nil
	}
	if accountUUID, ok := oauthAccount["accountUuid"].(string); ok && strings.TrimSpace(accountUUID) != "" {
		return configured, nil
	}
	return unknownVerdict(ports.AuthSourceLocal), nil
}

func unknownVerdict(source string) ports.AuthVerdict {
	return ports.AuthVerdict{State: ports.AgentAuthStatusUnknown, Source: source, CheckedAt: time.Now()}
}

// AuthReport is the diagnostic half of `claude auth status`, exported for
// `ao doctor`. It says what the CLI believes is configured; it is not a
// statement that the credential works.
type AuthReport struct {
	// LoggedIn is the CLI's own verdict: credentials were found.
	LoggedIn bool
	// APIKeySource is populated only when an environment variable or key
	// helper is overriding a subscription login. It is the single field that
	// identifies a stale key inherited from a shell profile.
	APIKeySource string
	// APIProvider is "firstParty", "bedrock", "vertex", "foundry", …
	APIProvider string
	// AuthMethod distinguishes a claude.ai subscription from a setup-token.
	AuthMethod string
	// SubscriptionType is the plan, when the login is a subscription.
	SubscriptionType string
}

// ParseAuthReport extracts an AuthReport from `claude auth status` output.
// ok=false means the output could not be parsed and nothing may be concluded.
func ParseAuthReport(out []byte) (AuthReport, bool) {
	report, ok := claudeAuthReportFromOutput(out)
	if !ok {
		return AuthReport{}, false
	}
	return AuthReport{
		LoggedIn:         report.LoggedIn,
		APIKeySource:     strings.TrimSpace(report.APIKeySource),
		APIProvider:      strings.TrimSpace(report.APIProvider),
		AuthMethod:       strings.TrimSpace(report.AuthMethod),
		SubscriptionType: strings.TrimSpace(report.SubscriptionType),
	}, true
}
