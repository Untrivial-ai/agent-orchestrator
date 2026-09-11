// Package sandboxresolve selects the sandbox provider authorized for one
// sandbox row, without leaking provider credentials into domain records.
package sandboxresolve

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	coderprovider "github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/coder"
)

// Resolver maps a sandbox row onto the provider that owns its compute.
type Resolver struct {
	nodeOps         sandbox.Provider
	docker          sandbox.Provider
	coder           sandbox.Provider
	userConnections userConnectionStore
	secretCipher    secretDecrypter
}

type userConnectionStore interface {
	UserProviderConnectionSecretByID(context.Context, string) (domain.UserProviderConnectionSecret, error)
}

type secretDecrypter interface {
	Decrypt([]byte, []byte, string) ([]byte, error)
}

type sessionScopedProvider interface {
	ForSandbox(domain.Sandbox) (sandbox.Provider, error)
}

// New creates a resolver backed by the providers enabled for this deployment.
func New(nodeOps, docker, coder sandbox.Provider) *Resolver {
	return &Resolver{nodeOps: nodeOps, docker: docker, coder: coder}
}

// WithUserConnections enables personal Coder connections while keeping the
// deployment-scoped providers as the fallback for existing sessions.
func (r *Resolver) WithUserConnections(store userConnectionStore, cipher secretDecrypter) *Resolver {
	r.userConnections = store
	r.secretCipher = cipher
	return r
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
		if record.UserConnectionID != "" {
			return r.resolveUserCoder(ctx, record)
		}
		if record.ProviderConnectionID != "" {
			return nil, fmt.Errorf("per-organization Coder connections are not supported yet")
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

func (r *Resolver) resolveUserCoder(
	ctx context.Context,
	record domain.Sandbox,
) (sandbox.Provider, error) {
	if r.userConnections == nil || r.secretCipher == nil {
		return nil, fmt.Errorf("personal Coder connections are not configured")
	}
	connection, err := r.userConnections.UserProviderConnectionSecretByID(
		ctx, record.UserConnectionID,
	)
	if err != nil {
		return nil, fmt.Errorf("load personal Coder connection: %w", err)
	}
	if connection.Provider != sandbox.ProviderCoder {
		return nil, fmt.Errorf("personal provider connection is %q, not coder", connection.Provider)
	}
	profile, err := sandbox.DecodeCoderSessionProfile(record.ResourceProfile)
	if err != nil {
		return nil, fmt.Errorf("decode durable Coder session profile: %w", err)
	}
	secret, err := r.secretCipher.Decrypt(
		connection.EncryptedSecret,
		connection.Nonce,
		"user:"+connection.UserID+"|"+sandbox.ProviderCoder+"|default",
	)
	if err != nil {
		return nil, fmt.Errorf("decrypt personal Coder connection: %w", err)
	}
	defer clear(secret)
	provider, err := coderprovider.New(coderprovider.Config{
		BaseURL: profile.BaseURL, Token: string(secret), Owner: profile.Owner,
		TemplateID: profile.TemplateID, AgentName: profile.AgentName,
		Parameters: profile.Parameters, HTTPClient: coderprovider.NewPublicHTTPClient(),
	})
	if err != nil {
		return nil, fmt.Errorf("configure personal Coder connection: %w", err)
	}
	return provider.ForSandbox(record)
}
