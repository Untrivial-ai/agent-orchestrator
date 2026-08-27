package onedev

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// currentUserPath is OneDev's "who am I" resource. It is the only route that
// answers for the authenticated account without knowing its numeric id:
// /~api/users/{id} needs the id, and GET /~api/users is admin-only (a normal
// account gets HTTP 403), so neither can stand in for it.
const currentUserPath = "/users/me"

// AuthenticatedIdentity resolves the account the configured credential
// belongs to.
//
// OneDev identity is per-instance — two allowlisted hosts are two unrelated
// user databases — so this unscoped form is only meaningful when exactly one
// host is configured, which is the common single-instance deployment. With
// several hosts configured it refuses rather than picking one, because
// silently answering for the wrong instance would attribute another
// instance's pull requests to this account. Callers that know the host (the
// observer, via ports.ScopedIdentityResolver) use
// AuthenticatedIdentityForHost instead.
func (p *Provider) AuthenticatedIdentity(ctx context.Context) (ports.SCMIdentity, error) {
	return p.AuthenticatedIdentityForHost(ctx, "")
}

// AuthenticatedIdentityForHost resolves the account the credential for one
// allowlisted instance belongs to. It is the host-scoped identity method the
// multi provider prefers, and it is what lets the observer attribute a OneDev
// pull request to the authenticated user instead of falling back to
// branch-based discovery.
//
// The host is resolved through the same allowlist as a git remote, so a
// repository's SCMRepo.Host — which may carry OneDev's SSH port rather than
// its HTTP one — selects the right instance and the right credential. A host
// outside the allowlist is rejected before any credential is attached.
//
// Successful results are cached per host for the provider's lifetime, matching
// the GitLab adapter: the observer asks once per poll and a login changes only
// when an account is renamed.
func (p *Provider) AuthenticatedIdentityForHost(ctx context.Context, host string) (ports.SCMIdentity, error) {
	h, err := p.identityHost(host)
	if err != nil {
		return ports.SCMIdentity{}, err
	}

	p.identityMu.Lock()
	defer p.identityMu.Unlock()
	if ident, ok := p.identities[h.authority]; ok {
		return ident, nil
	}

	client, err := p.clientForAuthority(h.authority)
	if err != nil {
		return ports.SCMIdentity{}, err
	}
	resp, err := client.doGET(ctx, currentUserPath, nil)
	if err != nil {
		return ports.SCMIdentity{}, fmt.Errorf("onedev scm: authenticated identity for %s: %w", h.authority, err)
	}
	var user restUser
	if err := json.Unmarshal(resp.Body, &user); err != nil {
		return ports.SCMIdentity{}, fmt.Errorf("onedev scm: decode authenticated user for %s: %w", h.authority, err)
	}
	login := strings.TrimSpace(user.Name)
	if login == "" {
		return ports.SCMIdentity{}, fmt.Errorf("onedev scm: authenticated user for %s has no name", h.authority)
	}

	// Human/bot classification reuses isBotAuthor, the same signal the adapter
	// applies to pull-request and review authors. OneDev exposes no
	// human/bot flag, and using a different rule here than for authors would
	// let the same account read as a bot in one place and a human in another.
	ident := ports.SCMIdentity{Login: login, Human: !isBotAuthor(login)}
	if p.identities == nil {
		p.identities = map[string]ports.SCMIdentity{}
	}
	p.identities[h.authority] = ident
	return ident, nil
}

// identityHost selects the allowlist entry an identity lookup targets. An
// empty host is the unscoped call: it resolves only when the allowlist names a
// single instance.
func (p *Provider) identityHost(host string) (allowedHost, error) {
	if strings.TrimSpace(host) == "" {
		if len(p.order) != 1 {
			return allowedHost{}, fmt.Errorf(
				"onedev scm: authenticated identity needs a host; %d instances are configured (%v)",
				len(p.order), p.AllowedHosts())
		}
		return p.allowed[p.order[0]], nil
	}
	h, ok := p.resolveHost(host)
	if !ok {
		return allowedHost{}, fmt.Errorf("onedev scm: host %q: %w", host, ErrHostNotAllowed)
	}
	return h, nil
}
