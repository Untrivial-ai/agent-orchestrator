// Package sandboxresolve selects the sandbox provider authorized for one
// sandbox row, without leaking provider credentials into domain records.
package sandboxresolve

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	coderprovider "github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/coder"
	"github.com/aoagents/agent-orchestrator/cloud/internal/secrets"
)

// coderConnectionStore reads an organization's encrypted Coder connection with no
// request principal, for the service-context resolver. The returned config is the
// non-secret JSONB; the resolver reads provisioning fields from the immutable
// session profile instead, so config is accepted but unused here.
type coderConnectionStore interface {
	CoderConnectionForService(
		ctx context.Context,
		orgID string,
	) (encrypted, nonce, config []byte, connectionID string, err error)
}

// Resolver maps a sandbox row onto the provider that owns its compute.
type Resolver struct {
	nodeOps          sandbox.Provider
	docker           sandbox.Provider
	coder            sandbox.Provider
	coderConnections coderConnectionStore
	cipher           *secrets.Cipher
}

type sessionScopedProvider interface {
	ForSandbox(domain.Sandbox) (sandbox.Provider, error)
}

// New creates a resolver backed by the providers enabled for this deployment.
// coderConnections and cipher are only needed where organizations bring their own
// Coder deployment (a sandbox row carries a provider_connection_id); both may be
// nil for a deployment that offers only the shared, env-configured Coder.
func New(
	nodeOps, docker, coder sandbox.Provider,
	coderConnections coderConnectionStore,
	cipher *secrets.Cipher,
) *Resolver {
	return &Resolver{
		nodeOps:          nodeOps,
		docker:           docker,
		coder:            coder,
		coderConnections: coderConnections,
		cipher:           cipher,
	}
}

// Resolve returns the provider authorized for sandbox. The reconciler never
// learns which provider it is talking to.
func (r *Resolver) Resolve(ctx context.Context, record domain.Sandbox) (sandbox.Provider, error) {
	switch record.Provider {
	case sandbox.ProviderNodeOps:
		if record.ProviderConnectionID != "" {
			// Bring-your-own-NodeOps credentials live encrypted in
			// ao_provider_connections. Decrypting them needs the secrets
			// cipher, which this slice does not build.
			return nil, fmt.Errorf(
				"per-organization NodeOps credentials are not supported yet (connection %s)",
				record.ProviderConnectionID,
			)
		}
		if r.nodeOps == nil {
			return nil, fmt.Errorf("nodeops sandbox provider is not configured")
		}
		return r.nodeOps, nil
	case sandbox.ProviderDocker:
		if record.ProviderConnectionID != "" {
			return nil, fmt.Errorf("per-organization Docker connections are not supported")
		}
		if r.docker == nil {
			return nil, fmt.Errorf("docker sandbox provider is not configured")
		}
		return r.docker, nil
	case sandbox.ProviderCoder:
		// A bring-your-own-Coder session carries the organization's connection id.
		// Build a fresh client from the org's decrypted token, independent of the
		// (possibly absent) shared deployment client.
		if record.ProviderConnectionID != "" {
			return r.resolveOrgCoder(ctx, record)
		}
		if r.coder == nil {
			return nil, fmt.Errorf("coder sandbox provider is not configured")
		}
		scoped, ok := r.coder.(sessionScopedProvider)
		if !ok {
			return nil, fmt.Errorf("coder sandbox provider does not support durable session profiles")
		}
		return scoped.ForSandbox(record)
	case sandbox.ProviderDaytona, sandbox.ProviderECS:
		return nil, fmt.Errorf("sandbox provider %q is not configured", record.Provider)
	default:
		return nil, fmt.Errorf("unsupported sandbox provider %q", record.Provider)
	}
}

// resolveOrgCoder builds a Coder client for one bring-your-own-Coder session. It
// reads the organization's encrypted token, decrypts it, and pairs it with the
// non-secret deployment contract stamped on the immutable session profile, so the
// token is always bound to the deployment URL recorded for that session — never
// the shared deployment credential.
func (r *Resolver) resolveOrgCoder(ctx context.Context, record domain.Sandbox) (sandbox.Provider, error) {
	if r.coderConnections == nil || r.cipher == nil {
		return nil, fmt.Errorf("per-organization Coder connections are not configured")
	}
	if record.OrgID == "" {
		return nil, fmt.Errorf("coder: session organization is required for a per-organization connection")
	}
	encrypted, nonce, _, connectionID, err := r.coderConnections.CoderConnectionForService(ctx, record.OrgID)
	if err != nil {
		return nil, fmt.Errorf("coder: load per-organization connection: %w", err)
	}
	if connectionID != record.ProviderConnectionID {
		return nil, fmt.Errorf(
			"coder: session connection %q does not match the organization's active connection %q",
			record.ProviderConnectionID, connectionID,
		)
	}
	profile, err := sandbox.DecodeCoderSessionProfile(record.ResourceProfile)
	if err != nil {
		return nil, fmt.Errorf("coder: resolve durable session profile: %w", err)
	}
	// Decrypt the org token and build a fresh client from the immutable session
	// profile. coderprovider.NewForOrg owns the associated-data construction and the
	// token zeroing, shared with the template-list edge.
	client, err := coderprovider.NewForOrg(r.cipher, record.OrgID, encrypted, nonce, coderprovider.Config{
		BaseURL:    profile.BaseURL,
		Owner:      profile.Owner,
		TemplateID: profile.TemplateID,
		AgentName:  profile.AgentName,
		Parameters: profile.Parameters,
	})
	if err != nil {
		return nil, fmt.Errorf("coder: build per-organization client: %w", err)
	}
	// Bind the fresh client to this session's deterministic workspace identity.
	// Its configured BaseURL equals the profile's, so the deployment-equality guard
	// is satisfied by construction.
	return client.ForSandbox(record)
}
