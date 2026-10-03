package sandboxresolve

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	coderprovider "github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/coder"
	"github.com/aoagents/agent-orchestrator/cloud/internal/secrets"
)

const (
	orgCoderOrgID    = "11111111-1111-1111-1111-111111111111"
	orgCoderConnID   = "22222222-2222-2222-2222-222222222222"
	orgCoderToken    = "org-coder-session-token"
	orgCoderTemplate = "2a2e262c-b31c-4202-946d-a19ad45d1fd2"
	orgCoderBaseURL  = "https://org-coder.example.com"
)

type fakeCoderConnectionStore struct {
	encrypted []byte
	nonce     []byte
	connID    string
	err       error
}

func (s *fakeCoderConnectionStore) CoderConnectionForService(
	context.Context, string,
) (encrypted, nonce, config []byte, connectionID string, err error) {
	return s.encrypted, s.nonce, nil, s.connID, s.err
}

func orgCoderResourceProfile() json.RawMessage {
	return json.RawMessage(`{"coder":{"baseUrl":"` + orgCoderBaseURL +
		`","owner":"org-bot","templateId":"` + orgCoderTemplate +
		`","agentName":"dev","parameters":{"region":"eu"},"durableRoot":"/home/coder"}}`)
}

// A per-organization Coder session resolves to a fresh client built from the
// org's decrypted token plus the immutable session profile.
func TestResolveBuildsPerOrgCoderClient(t *testing.T) {
	t.Parallel()
	cipher, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, nonce, err := cipher.Encrypt(
		[]byte(orgCoderToken),
		secrets.ProviderConnectionAssociatedData(orgCoderOrgID, sandbox.ProviderCoder, "default"),
	)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeCoderConnectionStore{encrypted: encrypted, nonce: nonce, connID: orgCoderConnID}
	resolver := New(nil, nil, nil, store, cipher)
	record := domain.Sandbox{
		SessionID: "session-1", OrgID: orgCoderOrgID, Provider: sandbox.ProviderCoder,
		ProviderConnectionID: orgCoderConnID, ResourceProfile: orgCoderResourceProfile(),
	}
	resolved, err := resolver.Resolve(context.Background(), record)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, ok := resolved.(*coderprovider.Client); !ok {
		t.Fatalf("Resolve returned %T, want *coder.Client", resolved)
	}
}

// A stored connection whose id does not match the one stamped on the session row
// is rejected rather than silently used.
func TestResolveRejectsMismatchedOrgCoderConnection(t *testing.T) {
	t.Parallel()
	cipher, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, nonce, err := cipher.Encrypt(
		[]byte(orgCoderToken),
		secrets.ProviderConnectionAssociatedData(orgCoderOrgID, sandbox.ProviderCoder, "default"),
	)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeCoderConnectionStore{encrypted: encrypted, nonce: nonce, connID: "a-different-connection"}
	resolver := New(nil, nil, nil, store, cipher)
	record := domain.Sandbox{
		SessionID: "session-1", OrgID: orgCoderOrgID, Provider: sandbox.ProviderCoder,
		ProviderConnectionID: orgCoderConnID, ResourceProfile: orgCoderResourceProfile(),
	}
	if _, err := resolver.Resolve(context.Background(), record); err == nil {
		t.Fatal("Resolve accepted a connection id that did not match the session row")
	}
}

// Without the connection store and cipher, a per-org session cannot resolve.
func TestResolveRejectsPerOrgCoderWithoutDeps(t *testing.T) {
	t.Parallel()
	resolver := New(nil, nil, nil, nil, nil)
	record := domain.Sandbox{
		SessionID: "session-1", OrgID: orgCoderOrgID, Provider: sandbox.ProviderCoder,
		ProviderConnectionID: orgCoderConnID, ResourceProfile: orgCoderResourceProfile(),
	}
	if _, err := resolver.Resolve(context.Background(), record); err == nil {
		t.Fatal("Resolve built a per-org client with no connection store or cipher")
	}
}

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
	resolver := New(nil, nil, provider, nil, nil)
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
	resolver := New(nil, nil, struct{ sandbox.Provider }{}, nil, nil)
	_, err := resolver.Resolve(context.Background(), domain.Sandbox{Provider: sandbox.ProviderCoder})
	if err == nil {
		t.Fatal("Resolve accepted a Coder provider without durable session scoping")
	}
}
