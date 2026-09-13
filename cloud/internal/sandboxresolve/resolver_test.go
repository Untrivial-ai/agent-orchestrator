package sandboxresolve

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

type scopedCoderProvider struct {
	sandbox.Provider
	record domain.Sandbox
}

func (p *scopedCoderProvider) ForSandbox(record domain.Sandbox) (sandbox.Provider, error) {
	p.record = record
	return p, nil
}

func TestResolveScopesCoderProviderToDurableSessionProfile(t *testing.T) {
	t.Parallel()
	provider := &scopedCoderProvider{}
	resolver := New(nil, nil, provider)
	record := domain.Sandbox{
		SessionID: "session-1", Provider: sandbox.ProviderCoder,
		ResourceProfile: json.RawMessage(`{"coder":{"owner":"planned-owner"}}`),
	}
	resolved, err := resolver.Resolve(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != provider || provider.record.SessionID != record.SessionID ||
		string(provider.record.ResourceProfile) != string(record.ResourceProfile) {
		t.Fatalf("resolver did not pass the durable session row: %+v", provider.record)
	}
}

func TestResolveRejectsUnscopedCoderProvider(t *testing.T) {
	t.Parallel()
	resolver := New(nil, nil, struct{ sandbox.Provider }{})
	_, err := resolver.Resolve(context.Background(), domain.Sandbox{Provider: sandbox.ProviderCoder})
	if err == nil {
		t.Fatal("Resolve accepted a Coder provider without durable session scoping")
	}
}
