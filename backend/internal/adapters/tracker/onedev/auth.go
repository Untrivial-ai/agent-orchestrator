package onedev

import (
	"net/http"
	"strings"

	scmonedev "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/onedev"
)

// TokenSource is the OneDev credential contract, re-exported from the SCM
// provider so a deployment configures OneDev credentials once and both
// adapters consume the same sources.
type TokenSource = scmonedev.TokenSource

// Credential is a resolved OneDev credential — either a bearer access token or
// a basic-auth pair. Re-exported for the same reason as TokenSource.
type Credential = scmonedev.Credential

// StaticTokenSource is a literal access token, used for per-host overrides and
// in tests.
type StaticTokenSource = scmonedev.StaticTokenSource

// ErrNoToken re-exports the SCM provider's canonical sentinel so the tracker
// and the SCM adapter share one error identity: callers distinguish "no
// credential" from other failures with errors.Is(err, scmonedev.ErrNoToken)
// regardless of which adapter produced it.
var ErrNoToken = scmonedev.ErrNoToken

// DefaultTokenSource returns the standard OneDev credential chain used by the
// tracker: an explicitly configured token first, then AO_ONEDEV_TOKEN, then
// ONEDEV_TOKEN (EnvTokenSource's own fallback). OneDev ships no first-party
// CLI to borrow a credential from, so unlike GitHub and GitLab there is no
// command fallback here; an operator wanting a keyring lookup configures the
// SCM provider's CommandTokenSource and passes it in explicitly.
func DefaultTokenSource(token string) TokenSource {
	return scmonedev.FallbackTokenSource{
		scmonedev.StaticTokenSource(token),
		scmonedev.EnvTokenSource{EnvVars: []string{"AO_ONEDEV_TOKEN"}},
	}
}

// tokenInvalidator is the optional capability of dropping a cached credential
// so the next call re-resolves it. The tracker invokes it whenever OneDev
// rejects the credential, so a rotated token is picked up without a restart.
type tokenInvalidator interface {
	InvalidateToken()
}

// applyCredential attaches a resolved credential to a request. It mirrors the
// SCM client's unexported Credential.apply: a bearer token wins over a
// basic-auth pair, and an empty credential is an error rather than an
// anonymous request, because OneDev's API has no anonymous mode worth reaching
// and a silent 401 would surface far from its cause.
func applyCredential(req *http.Request, cred Credential) error {
	if tok := strings.TrimSpace(cred.Token); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
		return nil
	}
	if user := strings.TrimSpace(cred.Username); user != "" {
		req.SetBasicAuth(user, cred.Password)
		return nil
	}
	return ErrNoToken
}
