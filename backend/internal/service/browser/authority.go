package browser

import (
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/pkg/browsercontract"
)

// Authority adapts the shared stateless capability authority to the daemon's
// domain-typed interfaces. All logic lives in pkg/browsercontract.
type Authority struct {
	inner *browsercontract.Authority
}

// NewAuthority returns a stateless browser capability authority.
func NewAuthority() *Authority {
	return &Authority{inner: browsercontract.NewAuthority()}
}

// Issue mints a fresh capability for one worker launch.
func (a *Authority) Issue(sessionID domain.SessionID) (token, verifier string, err error) {
	return a.inner.Issue(string(sessionID))
}

// Valid compares the presented token with a durable verifier in constant time.
func (a *Authority) Valid(sessionID domain.SessionID, token, verifier string) bool {
	return a.inner.Valid(string(sessionID), token, verifier)
}
