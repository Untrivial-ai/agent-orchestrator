package workerexec

import (
	"errors"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

// harnessCredential encapsulates one coding agent's cloud-credential business
// logic: its default executable, and how a resolved credential is materialized
// for the launched process (environment variables, credential files, or a login
// subprocess). Each harness differs only in this business logic, so adding a new
// harness is a matter of implementing this interface and registering it below --
// there are no credential switch statements elsewhere to keep in sync.
type harnessCredential interface {
	// defaultBinary is the executable name used when no explicit override is
	// configured on the builder.
	defaultBinary() string
	// configure injects the credential into command. It receives the builder so
	// it can reach per-session home directories and shared helpers.
	configure(b HarnessBuilder, command *Command, credential worker.CredentialResponse) error
}

// harnessCredentials is the single registry of supported cloud harnesses. To add
// a harness, implement harnessCredential and add one entry here.
var harnessCredentials = map[string]harnessCredential{
	"claude-code": claudeCredential{},
	"codex":       codexCredential{},
	"cursor":      cursorCredential{},
	"opencode":    opencodeCredential{},
}

func harnessCredentialFor(harness string) (harnessCredential, bool) {
	h, ok := harnessCredentials[harness]
	return h, ok
}

// SupportedHarness reports whether harness is a registered cloud harness and, if
// so, the default executable it launches. It is the single source the worker's
// launch guard uses to gate harness availability, so a newly registered harness
// is covered without editing a second switch (a missing case there silently
// skips launching the agent, leaving a session with no terminal/TUI).
func SupportedHarness(harness string) (binary string, ok bool) {
	h, ok := harnessCredentialFor(harness)
	if !ok {
		return "", false
	}
	return h.defaultBinary(), true
}

type claudeCredential struct{}

func (claudeCredential) defaultBinary() string { return "claude" }

func (claudeCredential) configure(_ HarnessBuilder, command *Command, credential worker.CredentialResponse) error {
	switch credential.CredentialType {
	case "api_key":
		command.Env["ANTHROPIC_API_KEY"] = credential.Secret
	case "oauth_token":
		command.Env["CLAUDE_CODE_OAUTH_TOKEN"] = credential.Secret
	default:
		return errors.New("unsupported Claude Code credential type")
	}
	return nil
}

type codexCredential struct{}

func (codexCredential) defaultBinary() string { return "codex" }

func (codexCredential) configure(b HarnessBuilder, command *Command, credential worker.CredentialResponse) error {
	switch credential.CredentialType {
	case "api_key", "access_token", "auth_json":
		return b.configureCodexCredential(command, credential)
	default:
		return errors.New("unsupported Codex credential type")
	}
}

type cursorCredential struct{}

func (cursorCredential) defaultBinary() string { return "cursor-agent" }

func (cursorCredential) configure(_ HarnessBuilder, command *Command, credential worker.CredentialResponse) error {
	if credential.CredentialType != "api_key" {
		return errors.New("unsupported Cursor credential type")
	}
	command.Env["CURSOR_API_KEY"] = credential.Secret
	return nil
}

// opencodeProviderEnv maps opencode's cloud credential types (one per model
// provider) to the environment variable opencode reads for that provider.
// opencode is multi-provider and stores interactive logins in a local sqlite
// database (not a portable file), so its cloud credential is a provider API key
// injected as the matching env var. Adding a provider = one entry here + the same
// key in opencodeSpec.credentialTypes (control plane) + the dialog's creds list.
var opencodeProviderEnv = map[string]string{
	"opencode_api_key":   "OPENCODE_API_KEY",
	"anthropic_api_key":  "ANTHROPIC_API_KEY",
	"openai_api_key":     "OPENAI_API_KEY",
	"openrouter_api_key": "OPENROUTER_API_KEY",
}

type opencodeCredential struct{}

func (opencodeCredential) defaultBinary() string { return "opencode" }

func (opencodeCredential) configure(_ HarnessBuilder, command *Command, credential worker.CredentialResponse) error {
	env, ok := opencodeProviderEnv[credential.CredentialType]
	if !ok {
		return errors.New("unsupported opencode credential type")
	}
	command.Env[env] = credential.Secret
	return nil
}
