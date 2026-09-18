package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/processenv"
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
//	rung 0  binary        ResolveBinary        → unknown when not installed
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

// claudeCredentialEnv is the environment-variable ladder in the precedence
// Claude Code itself applies.
var claudeCredentialEnv = []string{
	"CLAUDE_CODE_OAUTH_TOKEN",
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
}

// AuthStatus reports Claude Code's authentication state without starting a
// session.
func (p *Plugin) AuthStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	return p.authStatus(ctx)
}

func (p *Plugin) authStatus(ctx context.Context) (ports.AgentAuthStatus, error) {
	// Rung 0 — installed? A missing binary is not an auth failure; saying so
	// would point the user at a login when they need an install.
	binary, err := p.claudeBinary(ctx)
	if err != nil {
		if errors.Is(err, ports.ErrAgentBinaryNotFound) {
			return ports.AgentAuthStatusUnknown, nil
		}
		return ports.AgentAuthStatusUnknown, err
	}

	// Rung 3 runs first among the evidence rungs, even though rung 2 outranks
	// it, because its output is what rung 2 needs: apiProvider decides whether
	// a first-party probe is even the right thing to send. Inferring the
	// provider from environment variables instead would risk pointing a
	// Bedrock credential at api.anthropic.com.
	//
	opts := (agentcreds.ResolveOptions{AllowKeychain: true}).WithClaudeSettings()
	report, cliOK := p.claudeCLIAuthReport(ctx, binary, opts.WorkingDir, opts.CommandEnv)

	// Rungs 1 and 2 — the provider gate and the network probe. This is the
	// only rung that can prove a credential works, so it is the only one
	// allowed to return Authorized.
	if status, ok := p.probeAuthStatus(ctx, report, cliOK, opts); ok {
		return status, nil
	}

	// Rung 3's own verdict. It cannot prove success, but it is the only local
	// check that can observe a definite "signed out".
	// A first-party account report cannot reject a configured gateway or cloud
	// provider when its own probe was inconclusive.
	provider, _ := agentcreds.ResolveProvider("", opts)
	reportedProvider, _ := agentcreds.ParseProvider(report.APIProvider)
	if cliOK && (provider == agentcreds.ProviderFirstParty || provider == reportedProvider) {
		status := report.status()
		if status != ports.AgentAuthStatusUnknown {
			return status, nil
		}
	}

	// Rung 4 — local heuristic. Lowest confidence, and the last word only
	// because it never blocks anything.
	return claudeLocalAuthStatus(ctx, opts)
}

// probeAuthStatus runs the provider gate and the network probe.
//
// ok=false means the ladder must fall through: the provider is not one this
// build validates, no credential could be resolved, or the probe could not
// reach a conclusion. Only a definite provider answer stops the ladder here,
// which is what keeps the whole check additive — it can convert an Unknown
// into a real verdict, and can never manufacture a worse one.
func (p *Plugin) probeAuthStatus(ctx context.Context, report claudeAuthReport, cliOK bool, opts agentcreds.ResolveOptions) (ports.AgentAuthStatus, bool) {
	reported := ""
	if cliOK {
		reported = report.APIProvider
	}
	provider, ok := agentcreds.ResolveProvider(reported, opts)
	if !ok {
		return ports.AgentAuthStatusUnknown, false
	}
	cred, found := agentcreds.ResolveLocal(ctx, provider, opts)

	// The cache is read before anything is sent, and is keyed on the
	// credential's fingerprint: a verdict about a credential the agent no
	// longer uses is not evidence about anything.
	if found {
		if cached, hit := p.authCache().get(cred.Fingerprint(), provider); hit {
			if cached.State == agentcreds.StateUnknown {
				return ports.AgentAuthStatusUnknown, false
			}
			return authStatusFromResult(cached), true
		}
	}

	probeCtx, cancel := context.WithTimeout(ctx, agentcreds.DefaultTimeout)
	defer cancel()
	result := claudeValidator().ValidateLocal(probeCtx, reported, opts)
	// Cache only decisive verdicts. An inconclusive probe must be retried later
	// instead of turning one transient failure into minutes of stale silence.
	p.authCache().put(result)
	if result.State == agentcreds.StateUnknown {
		return ports.AgentAuthStatusUnknown, false
	}
	return authStatusFromResult(result), true
}

// authStatusFromResult projects a provider verdict onto AO's vocabulary. Only
// this function may produce a verified verdict.
func authStatusFromResult(result agentcreds.Result) ports.AgentAuthStatus {
	switch result.State {
	case agentcreds.StateValid:
		return ports.AgentAuthStatusAuthorized
	case agentcreds.StateInvalid:
		return ports.AgentAuthStatusUnauthorized
	default:
		return ports.AgentAuthStatusUnknown
	}
}

// claudeValidator builds the credential validator. It is a variable so tests
// can point the probe at a local server: a unit test must never be able to
// reach api.anthropic.com, both because that makes it flaky and because a test
// machine's real credentials are not the test's business.
var claudeValidator = func() *agentcreds.Validator { return agentcreds.New(nil) }

// authCache returns the process-wide verdict cache. It is package-level
// because the verdict is about the machine's credentials, not about any one
// plugin instance, and the readiness coordinator builds fresh adapters.
func (p *Plugin) authCache() *authCache { return claudeAuthCache }

var claudeAuthCache = newAuthCache(defaultAuthCacheTTL)

// InvalidateAuthCache drops the cached verdict. The runtime 401 handler calls
// it: the provider has just contradicted whatever was stored.
func InvalidateAuthCache() { claudeAuthCache.invalidate() }

// claudeAuthReport is the parsed shape of `claude auth status --json`. Only
// LoggedIn drives the verdict; the rest is diagnostics.
type claudeAuthReport struct {
	LoggedIn    bool   `json:"loggedIn"`
	APIProvider string `json:"apiProvider"`
}

// verdict maps a CLI report onto a verdict. loggedIn:true is credentials
// present, not credentials valid — hence configured, never authorized.
func (r claudeAuthReport) status() ports.AgentAuthStatus {
	if r.LoggedIn {
		return ports.AgentAuthStatusConfigured
	}
	// A CLI that positively reports signed out is evidence, not a guess: it
	// consulted the same credential sources the agent will use.
	return ports.AgentAuthStatusUnauthorized
}

// claudeCLIAuthReport runs the CLI probe under a hard timeout. ok=false means
// the probe could not answer — a timeout, an exec failure, or a version whose
// output this build cannot parse — and the caller falls to the next rung.
func (p *Plugin) claudeCLIAuthReport(ctx context.Context, binary, workingDir string, env map[string]string) (claudeAuthReport, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, claudeAuthProbeTimeout)
	defer cancel()

	out, err := claudeAuthCommand(probeCtx, binary, workingDir, env)
	if probeCtx.Err() != nil {
		return claudeAuthReport{}, false
	}
	// An unfamiliar non-zero result is not affirmative evidence of missing
	// credentials, so the exit code is not consulted: only parsable output is.
	_ = err
	return claudeAuthReportFromOutput(out.Stdout)
}

type claudeCommandOutput struct {
	Stdout []byte
	Stderr []byte
}

var claudeAuthCommand = func(ctx context.Context, binary, workingDir string, env map[string]string) (claudeCommandOutput, error) {
	cmd := aoprocess.CommandContext(ctx, binary, "auth", "status")
	if strings.TrimSpace(workingDir) != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = processenv.Merge(env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return claudeCommandOutput{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, err
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

// claudeLocalAuthStatus is rung 4: environment variables, then ~/.claude.json.
// It reports what is configured. It can never report authorized.
func claudeLocalAuthStatus(ctx context.Context, opts agentcreds.ResolveOptions) (ports.AgentAuthStatus, error) {
	if err := ctx.Err(); err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	for _, name := range claudeCredentialEnv {
		value := strings.TrimSpace(opts.Env(name))
		if value == "" {
			continue
		}
		return ports.AgentAuthStatusConfigured, nil
	}
	cfgPath, err := claudeConfigPath()
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	return claudeConfigAuthStatus(ctx, cfgPath)
}

// claudeConfigAuthStatus reads the durable markers Claude Code writes into
// ~/.claude.json. userID in particular is written once at first login and is
// never removed on logout or revocation, so it says only "this machine has
// signed in at some point" — a configured signal, never an authorized one.
func claudeConfigAuthStatus(ctx context.Context, path string) (ports.AgentAuthStatus, error) {
	_ = ctx
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ports.AgentAuthStatusUnknown, nil
	}
	if err != nil {
		return ports.AgentAuthStatusUnknown, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return ports.AgentAuthStatusUnknown, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return ports.AgentAuthStatusUnknown, err
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
		return ports.AgentAuthStatusConfigured, nil
	}
	var oauthAccount map[string]any
	if raw := root["oauthAccount"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &oauthAccount); err != nil {
			return ports.AgentAuthStatusUnknown, err
		}
	}
	if len(oauthAccount) == 0 {
		return ports.AgentAuthStatusUnknown, nil
	}
	if hasSubscription {
		return ports.AgentAuthStatusConfigured, nil
	}
	if accountUUID, ok := oauthAccount["accountUuid"].(string); ok && strings.TrimSpace(accountUUID) != "" {
		return ports.AgentAuthStatusConfigured, nil
	}
	return ports.AgentAuthStatusUnknown, nil
}

var claudeModelAuthReport = func(ctx context.Context, binary, workingDir string, env map[string]string) (claudeAuthReport, bool) {
	return (&Plugin{}).claudeCLIAuthReport(ctx, binary, workingDir, env)
}

// ProviderModels returns the Claude model IDs the configured provider actually
// serves, in that provider's own ID format.
//
// It reuses the credential probe rather than adding a second network call: the
// response that proves a credential works is the same response that lists what
// that credential may use. Those two facts are inseparable — an account's model
// list is scoped to its entitlement — so discovering them together is both
// cheaper and more correct than asking twice.
//
// An error means the provider could not be asked. Callers must fall back to
// their static list rather than presenting an empty picker.
func ProviderModels(ctx context.Context, binary, workingDir string, env map[string]string) ([]ports.AgentModelInfo, error) {
	opts := (agentcreds.ResolveOptions{AllowKeychain: true, WorkingDir: workingDir, CommandEnv: env}).WithClaudeSettings()

	probeCtx, cancel := context.WithTimeout(ctx, agentcreds.DefaultTimeout)
	defer cancel()
	reported := ""
	if strings.TrimSpace(binary) != "" {
		if report, ok := claudeModelAuthReport(probeCtx, binary, workingDir, opts.CommandEnv); ok {
			reported = report.APIProvider
		}
	}
	provider, providerOK := agentcreds.ResolveProvider(reported, opts)
	if !providerOK {
		return nil, errors.New("claude-code: model discovery: configured provider is unsupported")
	}
	cred, found := agentcreds.ResolveLocal(probeCtx, provider, opts)
	result := agentcreds.Result{}
	if found {
		if cached, hit := claudeAuthCache.get(cred.Fingerprint(), provider); hit &&
			(cached.State != agentcreds.StateValid || len(cached.Models) > 0) {
			result = cached
		}
	}
	if result.State == "" {
		result = claudeValidator().ValidateResolvedLocal(probeCtx, provider, cred, found, opts)
		claudeAuthCache.put(result)
	}
	if result.State != agentcreds.StateValid && len(result.Models) == 0 {
		return nil, fmt.Errorf("claude-code: model discovery: %s", result.Detail)
	}
	if len(result.Models) == 0 {
		return nil, errors.New("claude-code: provider reported no Claude models")
	}

	models := make([]ports.AgentModelInfo, 0, len(result.Models))
	for _, model := range result.Models {
		models = append(models, ports.AgentModelInfo{
			ID: model.ID, Label: model.DisplayName, Efforts: model.Efforts,
		})
	}
	return models, nil
}
