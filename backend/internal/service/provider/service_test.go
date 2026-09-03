package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestClaudeEnvironmentIsFixedAndSelectedOnly(t *testing.T) {
	r := RuntimeConfig{APIProtocol: domain.APIProtocolAnthropicCompatible, BaseURL: "https://provider.example/anthropic", APIKey: "secret", ModelName: "model-selected"}
	env, err := r.ClaudeEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if env["ANTHROPIC_AUTH_TOKEN"] != "secret" || env["ANTHROPIC_MODEL"] != "model-selected" || env["ANTHROPIC_BASE_URL"] != "https://provider.example/anthropic" {
		t.Fatalf("unexpected mapping: %#v", env)
	}
	if len(env) != 14 {
		t.Fatalf("unexpected environment surface: %d", len(env))
	}
}

func TestProviderJSONAndConnectionResultNeverExposeSecret(t *testing.T) {
	const key = "connection-secret-sentinel"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != key {
			t.Error("connection request missing credential")
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	st := &memoryStore{p: domain.Provider{ID: "p", DisplayName: "P", APIProtocol: domain.APIProtocolAnthropicCompatible, BaseURL: server.URL, SecretRef: "provider:p", Enabled: true, SecretConfigured: true}, m: domain.ProviderModel{ID: "m", ProviderID: "p", ModelName: "model", Enabled: true}, secret: append([]byte("encrypted:"), []byte(key)...)}
	svc := New(st, memoryProtector{}, server.Client())
	result, err := svc.TestConnection(context.Background(), "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(struct {
		Provider domain.Provider `json:"provider"`
		Result   TestResult      `json:"result"`
	}{st.p, result})
	if strings.Contains(string(encoded), key) || strings.Contains(string(encoded), st.p.SecretRef) {
		t.Fatalf("response exposed secret material: %s", encoded)
	}
	if result.Category != "authentication" || strings.Contains(result.Message, key) {
		t.Fatalf("unsanitized result: %#v", result)
	}
}

type memoryProtector struct{}

func (memoryProtector) Protect(v []byte) ([]byte, error) {
	return append([]byte("encrypted:"), v...), nil
}
func (memoryProtector) Unprotect(v []byte) ([]byte, error) {
	return append([]byte(nil), v[len("encrypted:"):]...), nil
}

type memoryStore struct {
	p      domain.Provider
	m      domain.ProviderModel
	secret []byte
	audits []domain.ProviderAudit
}

func (s *memoryStore) ListProviders(context.Context) ([]domain.Provider, error) {
	return []domain.Provider{s.p}, nil
}
func (s *memoryStore) GetProvider(_ context.Context, id domain.ProviderID) (domain.Provider, bool, error) {
	return s.p, s.p.ID == id, nil
}
func (s *memoryStore) PutProvider(_ context.Context, p domain.Provider) error { s.p = p; return nil }
func (s *memoryStore) PutProviderSecret(_ context.Context, _ string, v []byte, _ time.Time) error {
	s.secret = append([]byte(nil), v...)
	return nil
}
func (s *memoryStore) GetProviderSecret(context.Context, string) ([]byte, bool, error) {
	return s.secret, len(s.secret) > 0, nil
}
func (s *memoryStore) DeleteProviderSecret(context.Context, string) error { s.secret = nil; return nil }
func (s *memoryStore) ListProviderModels(context.Context, domain.ProviderID) ([]domain.ProviderModel, error) {
	return []domain.ProviderModel{s.m}, nil
}
func (s *memoryStore) GetProviderModel(_ context.Context, id domain.ProviderModelID) (domain.ProviderModel, bool, error) {
	return s.m, s.m.ID == id, nil
}
func (s *memoryStore) PutProviderModel(_ context.Context, m domain.ProviderModel) error {
	s.m = m
	return nil
}
func (s *memoryStore) CreateProviderAudit(_ context.Context, a domain.ProviderAudit) error {
	s.audits = append(s.audits, a)
	return nil
}

func TestSecretUpdateKeepReplaceDeleteAndResolveDisabled(t *testing.T) {
	ctx := context.Background()
	st := &memoryStore{}
	svc := New(st, memoryProtector{}, nil)
	svc.newID = func() string { return "fixed-id" }
	key := "first-secret"
	p, err := svc.Create(ctx, ProviderInput{DisplayName: "DeepSeek", APIProtocol: domain.APIProtocolAnthropicCompatible, BaseURL: "https://api.example/anthropic", Enabled: true, APIKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	if string(st.secret) == key || string(st.secret) != "encrypted:"+key {
		t.Fatal("secret was not protected")
	}
	blank := ""
	_, err = svc.Update(ctx, p.ID, ProviderInput{DisplayName: p.DisplayName, APIProtocol: p.APIProtocol, BaseURL: p.BaseURL, Enabled: true, APIKey: &blank})
	if err != nil {
		t.Fatal(err)
	}
	if string(st.secret) != "encrypted:"+key {
		t.Fatal("blank update replaced secret")
	}
	replacement := "second-secret"
	_, err = svc.Update(ctx, p.ID, ProviderInput{DisplayName: p.DisplayName, APIProtocol: p.APIProtocol, BaseURL: p.BaseURL, Enabled: true, APIKey: &replacement})
	if err != nil {
		t.Fatal(err)
	}
	if string(st.secret) != "encrypted:"+replacement {
		t.Fatal("replacement failed")
	}
	model, err := svc.PutModel(ctx, p.ID, "", ModelInput{DisplayName: "Model", ModelName: "model", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := svc.ResolveRuntimeProvider(ctx, p.ID, model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.APIKey != replacement {
		t.Fatal("resolved wrong secret")
	}
	_, err = svc.Update(ctx, p.ID, ProviderInput{DisplayName: p.DisplayName, APIProtocol: p.APIProtocol, BaseURL: p.BaseURL, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ResolveRuntimeProvider(ctx, p.ID, model.ID); err != ErrDisabled {
		t.Fatalf("disabled selection error=%v", err)
	}
	if resumed, err := svc.ResolveRuntimeProviderForResume(ctx, p.ID, model.ID); err != nil || resumed.APIKey != replacement {
		t.Fatalf("disabled historical resume = (%q, %v)", resumed.APIKey, err)
	}
	_, err = svc.Update(ctx, p.ID, ProviderInput{DisplayName: p.DisplayName, APIProtocol: p.APIProtocol, BaseURL: p.BaseURL, Enabled: true, DeleteAPIKey: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.secret) != 0 {
		t.Fatal("explicit delete retained secret")
	}
	if _, err = svc.ResolveRuntimeProviderForResume(ctx, p.ID, model.ID); err != ErrSecretMissing {
		t.Fatalf("resume after secret deletion error=%v", err)
	}
}

func TestOpenAICompatibleGLMIsStructurallyConfigurableButNotMappedToClaude(t *testing.T) {
	ctx := context.Background()
	st := &memoryStore{}
	svc := New(st, memoryProtector{}, nil)
	svc.newID = func() string { return "glm-id" }
	key := "fake-glm-key"
	p, err := svc.Create(ctx, ProviderInput{DisplayName: "GLM mock", APIProtocol: domain.APIProtocolOpenAICompatible, BaseURL: "https://glm.mock.invalid/v1", Enabled: true, APIKey: &key})
	if err != nil {
		t.Fatal(err)
	}
	m, err := svc.PutModel(ctx, p.ID, "", ModelInput{DisplayName: "GLM mock model", ModelName: "glm-mock", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	r, err := svc.ResolveRuntimeProvider(ctx, p.ID, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.ClaudeEnvironment(); err == nil {
		t.Fatal("openai-compatible provider was incorrectly mapped to Claude Code")
	}
}
