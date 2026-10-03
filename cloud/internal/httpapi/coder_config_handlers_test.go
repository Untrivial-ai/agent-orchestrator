package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/aoagents/agent-orchestrator/cloud/internal/secrets"
	"github.com/go-chi/chi/v5"
)

const (
	coderCfgOrgID    = "00000000-0000-0000-0000-0000000000a1"
	coderCfgUserID   = "00000000-0000-0000-0000-0000000000f6"
	coderCfgTemplate = "2a2e262c-b31c-4202-946d-a19ad45d1fd2"
	coderCfgToken    = "coder-session-token-abcdef123456"
)

// coderConfigFakeStore embeds Store (nil) so it satisfies the full Store
// interface while implementing only the provider-connection methods the coder
// config handlers reach.
type coderConfigFakeStore struct {
	Store
	connections        []domain.ProviderConnection
	upsertErr          error
	upserted           int
	lastProvider       string
	lastLabel          string
	lastEncrypted      []byte
	lastNonce          []byte
	lastConfig         json.RawMessage
	deleteErr          error
	deleted            int
	lastDeleteProvider string
	lastDeleteLabel    string
}

func (s *coderConfigFakeStore) ListProviderConnections(
	context.Context, domain.Principal, string,
) ([]domain.ProviderConnection, error) {
	return s.connections, nil
}

func (s *coderConfigFakeStore) UpsertProviderConnection(
	_ context.Context, _ domain.Principal, orgID, provider, label string,
	encrypted, nonce []byte, config json.RawMessage,
) (domain.ProviderConnection, error) {
	if s.upsertErr != nil {
		return domain.ProviderConnection{}, s.upsertErr
	}
	s.upserted++
	s.lastProvider = provider
	s.lastLabel = label
	s.lastEncrypted = encrypted
	s.lastNonce = nonce
	s.lastConfig = config
	return domain.ProviderConnection{
		ID: "11111111-1111-1111-1111-111111111111", OrgID: orgID,
		Provider: provider, Label: label, Config: config,
		ValidationState: "valid", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}, nil
}

func (s *coderConfigFakeStore) DeleteProviderConnection(
	_ context.Context, _ domain.Principal, _, provider, label string,
) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deleted++
	s.lastDeleteProvider = provider
	s.lastDeleteLabel = label
	return nil
}

func newCoderConfigServer(t *testing.T, store Store, gated bool) (*Server, *secrets.Cipher) {
	t.Helper()
	cipher, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	options := Options{
		Store:                     store,
		SecretCipher:              cipher,
		SandboxProvider:           sandbox.ProviderCoder,
		AvailableSandboxProviders: []string{sandbox.ProviderCoder},
		Logger:                    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if gated {
		options.CapabilityGatedProviders = []string{sandbox.ProviderCoder}
	}
	return New(options), cipher
}

func coderConfigRequest(t *testing.T, method, body string, caps []string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, "/api/cloud/v1/orgs/"+coderCfgOrgID+"/coder-config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("orgId", coderCfgOrgID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, principalKey, domain.Principal{UserID: coderCfgUserID, OrgCapabilities: caps})
	return req.WithContext(ctx)
}

func validCoderConfigBody() string {
	return `{"token":"` + coderCfgToken + `","baseUrl":"https://coder.acme.example.com","owner":"ao-bot","templateId":"` + coderCfgTemplate + `","agentName":"dev","parameters":{"region":"eu"}}`
}

// The happy path stores the token encrypted, never echoes it, and keeps it out of
// the non-secret config JSONB — while filling the durable-root default.
func TestPutOrgCoderConfigStoresEncryptedTokenAndNeverReturnsIt(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{}
	srv, cipher := newCoderConfigServer(t, store, false)

	rec := httptest.NewRecorder()
	srv.putOrgCoderConfig(rec, coderConfigRequest(t, http.MethodPut, validCoderConfigBody(), nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if store.upserted != 1 {
		t.Fatalf("UpsertProviderConnection called %d times, want 1", store.upserted)
	}
	if store.lastProvider != sandbox.ProviderCoder || store.lastLabel != defaultAgentConnectionLabel {
		t.Fatalf("stored under provider=%q label=%q, want coder/default", store.lastProvider, store.lastLabel)
	}
	if strings.Contains(rec.Body.String(), coderCfgToken) {
		t.Fatalf("response leaked the token: %s", rec.Body.String())
	}
	if strings.Contains(string(store.lastConfig), coderCfgToken) {
		t.Fatalf("config JSONB leaked the token: %s", store.lastConfig)
	}
	if len(store.lastEncrypted) == 0 || len(store.lastNonce) == 0 {
		t.Fatal("token was not encrypted")
	}
	// The stored ciphertext must round-trip back to the exact token under the same
	// associated data the resolver will later decrypt with.
	plain, err := cipher.Decrypt(store.lastEncrypted, store.lastNonce, providerSecretAssociatedData(coderCfgOrgID, sandbox.ProviderCoder))
	if err != nil {
		t.Fatalf("decrypt stored token: %v", err)
	}
	if string(plain) != coderCfgToken {
		t.Fatalf("decrypted token = %q, want %q", plain, coderCfgToken)
	}
	cfg, err := domain.DecodeOrgCoderConfig(store.lastConfig)
	if err != nil {
		t.Fatalf("decode stored config: %v", err)
	}
	if cfg.BaseURL != "https://coder.acme.example.com" || cfg.Owner != "ao-bot" ||
		cfg.TemplateID != coderCfgTemplate || cfg.AgentName != "dev" ||
		cfg.Parameters["region"] != "eu" {
		t.Fatalf("stored config = %+v", cfg)
	}
	if cfg.DurableRoot != domain.DefaultOrgCoderDurableRoot {
		t.Fatalf("durableRoot = %q, want default %q", cfg.DurableRoot, domain.DefaultOrgCoderDurableRoot)
	}
}

// A private-IP, non-HTTPS Coder is intentionally allowed: bring-your-own Coder is
// often reached privately, so public HTTPS is not forced.
func TestPutOrgCoderConfigAllowsPrivateIPBaseURL(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{}
	srv, _ := newCoderConfigServer(t, store, false)
	body := `{"token":"` + coderCfgToken + `","baseUrl":"http://10.0.0.5:3000","owner":"ao-bot","templateId":"` + coderCfgTemplate + `"}`

	rec := httptest.NewRecorder()
	srv.putOrgCoderConfig(rec, coderConfigRequest(t, http.MethodPut, body, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
}

// The optional PrivateLink fields store into the non-secret config JSONB and come
// back on the response, while a config that omits them is still accepted.
func TestPutOrgCoderConfigStoresPrivateLinkFields(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{}
	srv, _ := newCoderConfigServer(t, store, false)
	body := `{"token":"` + coderCfgToken + `","baseUrl":"https://coder.acme.example.com","owner":"ao-bot","templateId":"` + coderCfgTemplate +
		`","endpointServiceName":"com.amazonaws.vpce.eu-north-1.vpce-svc-0123456789abcdef0","region":"eu-north-1"}`

	rec := httptest.NewRecorder()
	srv.putOrgCoderConfig(rec, coderConfigRequest(t, http.MethodPut, body, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	cfg, err := domain.DecodeOrgCoderConfig(store.lastConfig)
	if err != nil {
		t.Fatalf("decode stored config: %v", err)
	}
	if cfg.EndpointServiceName != "com.amazonaws.vpce.eu-north-1.vpce-svc-0123456789abcdef0" || cfg.Region != "eu-north-1" {
		t.Fatalf("PrivateLink fields not stored: %+v", cfg)
	}
	var resp orgCoderConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.CoderConfig == nil || resp.CoderConfig.EndpointServiceName != "com.amazonaws.vpce.eu-north-1.vpce-svc-0123456789abcdef0" || resp.CoderConfig.Region != "eu-north-1" {
		t.Fatalf("response missing PrivateLink fields: %+v", resp.CoderConfig)
	}
}

// Blank PrivateLink fields are always allowed: a directly reachable Coder leaves
// them empty and the config stores without them.
func TestPutOrgCoderConfigAllowsBlankPrivateLinkFields(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{}
	srv, _ := newCoderConfigServer(t, store, false)
	body := `{"token":"` + coderCfgToken + `","baseUrl":"https://coder.acme.example.com","owner":"ao-bot","templateId":"` + coderCfgTemplate +
		`","endpointServiceName":"","region":""}`

	rec := httptest.NewRecorder()
	srv.putOrgCoderConfig(rec, coderConfigRequest(t, http.MethodPut, body, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	cfg, err := domain.DecodeOrgCoderConfig(store.lastConfig)
	if err != nil {
		t.Fatalf("decode stored config: %v", err)
	}
	if cfg.EndpointServiceName != "" || cfg.Region != "" {
		t.Fatalf("expected blank PrivateLink fields, got %+v", cfg)
	}
}

func TestPutOrgCoderConfigRejectsMalformedPrivateLinkFields(t *testing.T) {
	t.Parallel()
	base := `{"token":"` + coderCfgToken + `","baseUrl":"https://coder.acme.example.com","owner":"ao-bot","templateId":"` + coderCfgTemplate + `"`
	cases := map[string]string{
		"bad endpoint service name": base + `,"endpointServiceName":"not-a-privatelink-name"}`,
		"bad region":                base + `,"region":"not a region"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := &coderConfigFakeStore{}
			srv, _ := newCoderConfigServer(t, store, false)
			rec := httptest.NewRecorder()
			srv.putOrgCoderConfig(rec, coderConfigRequest(t, http.MethodPut, body, nil))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
			}
			if store.upserted != 0 {
				t.Fatal("a config with a malformed PrivateLink field must not be stored")
			}
		})
	}
}

func TestPutOrgCoderConfigRejectsGarbageURL(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{}
	srv, _ := newCoderConfigServer(t, store, false)
	body := `{"token":"` + coderCfgToken + `","baseUrl":"not a url","owner":"ao-bot","templateId":"` + coderCfgTemplate + `"}`

	rec := httptest.NewRecorder()
	srv.putOrgCoderConfig(rec, coderConfigRequest(t, http.MethodPut, body, nil))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
	if store.upserted != 0 {
		t.Fatal("a config with a bad URL must not be stored")
	}
}

func TestPutOrgCoderConfigRejectsMissingToken(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{}
	srv, _ := newCoderConfigServer(t, store, false)
	body := `{"baseUrl":"https://coder.acme.example.com","owner":"ao-bot","templateId":"` + coderCfgTemplate + `"}`

	rec := httptest.NewRecorder()
	srv.putOrgCoderConfig(rec, coderConfigRequest(t, http.MethodPut, body, nil))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
	if store.upserted != 0 {
		t.Fatal("a config with no token must not be stored")
	}
}

func TestPutOrgCoderConfigRejectsNonUUIDTemplate(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{}
	srv, _ := newCoderConfigServer(t, store, false)
	body := `{"token":"` + coderCfgToken + `","baseUrl":"https://coder.acme.example.com","owner":"ao-bot","templateId":"not-a-uuid"}`

	rec := httptest.NewRecorder()
	srv.putOrgCoderConfig(rec, coderConfigRequest(t, http.MethodPut, body, nil))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body = %s", rec.Code, rec.Body.String())
	}
}

// The organization must hold the coder capability when the provider is gated.
func TestPutOrgCoderConfigRejectsWhenOrgNotEntitled(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{}
	srv, _ := newCoderConfigServer(t, store, true) // coder is capability-gated

	rec := httptest.NewRecorder()
	srv.putOrgCoderConfig(rec, coderConfigRequest(t, http.MethodPut, validCoderConfigBody(), nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if store.upserted != 0 {
		t.Fatal("an unentitled org must not store a coder config")
	}
}

// With the capability present a gated org is allowed through to the store.
func TestPutOrgCoderConfigAllowsEntitledOrg(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{}
	srv, _ := newCoderConfigServer(t, store, true)

	rec := httptest.NewRecorder()
	srv.putOrgCoderConfig(rec, coderConfigRequest(t, http.MethodPut, validCoderConfigBody(), []string{sandbox.ProviderCoder}))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if store.upserted != 1 {
		t.Fatalf("UpsertProviderConnection called %d times, want 1", store.upserted)
	}
}

// A non-admin is rejected by the store's requireOrgAdmin, surfaced as 403.
func TestPutOrgCoderConfigSurfacesAdminForbidden(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{upsertErr: postgres.ErrForbidden}
	srv, _ := newCoderConfigServer(t, store, false)

	rec := httptest.NewRecorder()
	srv.putOrgCoderConfig(rec, coderConfigRequest(t, http.MethodPut, validCoderConfigBody(), nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
}

// GET returns the non-secret config and never the token.
func TestGetOrgCoderConfigReturnsConfigWithoutToken(t *testing.T) {
	t.Parallel()
	config, err := domain.EncodeOrgCoderConfig(domain.OrgCoderConfig{
		BaseURL: "https://coder.acme.example.com", Owner: "ao-bot", TemplateID: coderCfgTemplate,
		AgentName: "dev", DurableRoot: "/home/coder",
		EndpointServiceName: "com.amazonaws.vpce.eu-north-1.vpce-svc-0123456789abcdef0", Region: "eu-north-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &coderConfigFakeStore{connections: []domain.ProviderConnection{
		{ID: "other", Provider: "claude-code", Label: "default", Config: json.RawMessage(`{"credentialType":"api_key"}`)},
		{ID: "coder-1", Provider: sandbox.ProviderCoder, Label: "default", Config: config, ValidationState: "valid"},
	}}
	srv, _ := newCoderConfigServer(t, store, false)

	rec := httptest.NewRecorder()
	srv.getOrgCoderConfig(rec, coderConfigRequest(t, http.MethodGet, "", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), coderCfgToken) || strings.Contains(strings.ToLower(rec.Body.String()), "\"token\"") {
		t.Fatalf("GET leaked a token field: %s", rec.Body.String())
	}
	var resp orgCoderConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Configured || resp.CoderConfig == nil {
		t.Fatalf("expected configured response, got %+v", resp)
	}
	if resp.CoderConfig.BaseURL != "https://coder.acme.example.com" || resp.CoderConfig.TemplateID != coderCfgTemplate {
		t.Fatalf("config = %+v", resp.CoderConfig)
	}
	if resp.CoderConfig.EndpointServiceName != "com.amazonaws.vpce.eu-north-1.vpce-svc-0123456789abcdef0" || resp.CoderConfig.Region != "eu-north-1" {
		t.Fatalf("GET did not return PrivateLink fields: %+v", resp.CoderConfig)
	}
}

// With no coder connection, GET reports it is not configured (and 200, not 404).
func TestGetOrgCoderConfigNotConfigured(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{}
	srv, _ := newCoderConfigServer(t, store, false)

	rec := httptest.NewRecorder()
	srv.getOrgCoderConfig(rec, coderConfigRequest(t, http.MethodGet, "", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var resp orgCoderConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Configured || resp.CoderConfig != nil {
		t.Fatalf("expected not-configured response, got %+v", resp)
	}
}

// DELETE removes the org's coder connection row and returns 204.
func TestDeleteOrgCoderConfigRemovesConnection(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{}
	srv, _ := newCoderConfigServer(t, store, false)

	rec := httptest.NewRecorder()
	srv.deleteOrgCoderConfig(rec, coderConfigRequest(t, http.MethodDelete, "", nil))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}
	if store.deleted != 1 {
		t.Fatalf("DeleteProviderConnection called %d times, want 1", store.deleted)
	}
	if store.lastDeleteProvider != sandbox.ProviderCoder || store.lastDeleteLabel != defaultAgentConnectionLabel {
		t.Fatalf("deleted provider=%q label=%q, want coder/default", store.lastDeleteProvider, store.lastDeleteLabel)
	}
}

// An unentitled org may neither write nor clear a coder connection.
func TestDeleteOrgCoderConfigRejectsWhenOrgNotEntitled(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{}
	srv, _ := newCoderConfigServer(t, store, true) // coder is capability-gated

	rec := httptest.NewRecorder()
	srv.deleteOrgCoderConfig(rec, coderConfigRequest(t, http.MethodDelete, "", nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
	if store.deleted != 0 {
		t.Fatal("an unentitled org must not delete a coder config")
	}
}

// With the capability present a gated org is allowed through to the store.
func TestDeleteOrgCoderConfigAllowsEntitledOrg(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{}
	srv, _ := newCoderConfigServer(t, store, true)

	rec := httptest.NewRecorder()
	srv.deleteOrgCoderConfig(rec, coderConfigRequest(t, http.MethodDelete, "", []string{sandbox.ProviderCoder}))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}
	if store.deleted != 1 {
		t.Fatalf("DeleteProviderConnection called %d times, want 1", store.deleted)
	}
}

// A non-admin is rejected by the store's requireOrgAdmin, surfaced as 403.
func TestDeleteOrgCoderConfigSurfacesAdminForbidden(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{deleteErr: postgres.ErrForbidden}
	srv, _ := newCoderConfigServer(t, store, false)

	rec := httptest.NewRecorder()
	srv.deleteOrgCoderConfig(rec, coderConfigRequest(t, http.MethodDelete, "", nil))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rec.Code, rec.Body.String())
	}
}

// When the org has no connection, the store's ErrNotFound surfaces as 404.
func TestDeleteOrgCoderConfigSurfacesNotFound(t *testing.T) {
	t.Parallel()
	store := &coderConfigFakeStore{deleteErr: postgres.ErrNotFound}
	srv, _ := newCoderConfigServer(t, store, false)

	rec := httptest.NewRecorder()
	srv.deleteOrgCoderConfig(rec, coderConfigRequest(t, http.MethodDelete, "", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}
