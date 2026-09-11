package sandboxresolve

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
)

type fakeUserConnections struct {
	connection domain.UserProviderConnectionSecret
}

func (f fakeUserConnections) UserProviderConnectionSecretByID(
	_ context.Context, id string,
) (domain.UserProviderConnectionSecret, error) {
	if id != f.connection.ID {
		return domain.UserProviderConnectionSecret{}, errors.New("unexpected connection ID")
	}
	return f.connection, nil
}

type recordingDecrypter struct {
	aad *string
}

func (d recordingDecrypter) Decrypt(_ []byte, _ []byte, aad string) ([]byte, error) {
	*d.aad = aad
	return []byte("coder-token"), nil
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

func TestResolvePersonalCoderConnection(t *testing.T) {
	t.Parallel()
	config, err := json.Marshal(sandbox.CoderConnectionConfig{
		BaseURL: "https://coder.example.com", Owner: "ao-user",
		TemplateID:  "2a2e262c-b31c-4202-946d-a19ad45d1fd2",
		DurableRoot: "/workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	connection := domain.UserProviderConnectionSecret{
		ID: "connection-1", UserID: "user-1", Provider: sandbox.ProviderCoder,
		EncryptedSecret: []byte("encrypted"), Nonce: []byte("nonce"), Config: config,
	}
	var aad string
	resolver := New(nil, nil, nil).WithUserConnections(
		fakeUserConnections{connection: connection}, recordingDecrypter{aad: &aad},
	)
	record := domain.Sandbox{
		SessionID: "session-1", Provider: sandbox.ProviderCoder,
		UserConnectionID: connection.ID,
		ResourceProfile:  json.RawMessage(`{"coder":{"baseUrl":"https://coder.example.com","owner":"ao-user","templateId":"2a2e262c-b31c-4202-946d-a19ad45d1fd2","agentName":"","parameters":{},"durableRoot":"/workspace"}}`),
	}
	provider, err := resolver.Resolve(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.ValueOf(provider).IsNil() {
		t.Fatal("personal Coder provider is nil")
	}
	if aad != "user:user-1|coder|default" {
		t.Fatalf("associated data = %q", aad)
	}
}
