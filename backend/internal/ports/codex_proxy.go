package ports

import (
	"context"
	"errors"
)

const (
	// CodexProxyProviderName is the provider name written into Codex's
	// invocation-scoped configuration. It deliberately does not collide with
	// Codex's built-in OpenAI provider.
	CodexProxyProviderName = "ao_accounts_manager"
	// CodexProxyTokenEnv is the environment variable used to pass a scoped
	// route capability to a Codex child process. The token never appears in
	// argv, logs, or durable session metadata.
	CodexProxyTokenEnv = "AO_CODEX_PROXY_TOKEN" // #nosec G101 -- public environment variable name, not a credential.
)

var (
	// ErrCodexProxyUnavailable means the daemon could not make the embedded
	// proxy available for a Codex launch.
	ErrCodexProxyUnavailable = errors.New("codex accounts manager unavailable")
	// ErrCodexProxyNoAccounts means no usable Codex credential is registered in
	// the proxy. It is intentionally distinct so the UI can direct the user to
	// log in rather than reporting a generic launch failure.
	ErrCodexProxyNoAccounts = errors.New("no usable Codex accounts are logged in to the accounts manager")
	// ErrCodexProxyAccountUnavailable means a requested account is not currently
	// eligible for new traffic.
	ErrCodexProxyAccountUnavailable = errors.New("codex account is unavailable")
)

// AgentProviderRoute is the non-secret launch material required to route an
// agent through an account manager. Token is kept in memory until the session
// environment is created; callers must not persist or log it.
type AgentProviderRoute struct {
	BaseURL      string
	ProviderName string
	Token        string
	TokenEnv     string
}

// CodexRouteProvider owns account selection for Codex sessions. The route
// token is stable for the lifetime of an AO session; SwitchSessionAccount
// changes the account selected for subsequent requests without restarting the
// Codex process.
type CodexRouteProvider interface {
	RouteForSession(context.Context, string) (AgentProviderRoute, error)
	SwitchSessionAccount(context.Context, string, string) (string, error)
}
