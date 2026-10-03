package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/coder"
	"github.com/aoagents/agent-orchestrator/cloud/internal/secrets"
)

// fakeDeploymentTemplateLister stands in for the shared, env-configured Coder
// deployment lister (s.coderTemplates).
type fakeDeploymentTemplateLister struct {
	templates []coder.Template
	err       error
}

func (f *fakeDeploymentTemplateLister) ListTemplates(context.Context) ([]coder.Template, error) {
	return f.templates, f.err
}

// fakeCoderConnStore serves an organization's encrypted Coder connection to the
// template-list edge, exactly as the sandbox resolver reads it.
type fakeCoderConnStore struct {
	Store
	encrypted []byte
	nonce     []byte
	config    []byte
	connID    string
	err       error
}

func (s *fakeCoderConnStore) CoderConnectionForService(
	context.Context, string,
) (encrypted, nonce, config []byte, connectionID string, err error) {
	return s.encrypted, s.nonce, s.config, s.connID, s.err
}

func newCoderTemplatesServer(t *testing.T, cipher *secrets.Cipher, store Store, deployment CoderTemplateLister) *Server {
	t.Helper()
	options := Options{
		Store:                     store,
		SecretCipher:              cipher,
		SandboxProvider:           sandbox.ProviderCoder,
		AvailableSandboxProviders: []string{sandbox.ProviderCoder},
		CoderTemplates:            deployment,
		Logger:                    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return New(options)
}

func newTestCipher(t *testing.T) *secrets.Cipher {
	t.Helper()
	cipher, err := secrets.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	return cipher
}

func decodeTemplatesResponse(t *testing.T, rec *httptest.ResponseRecorder) []coderTemplateResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Templates []coderTemplateResponse `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.Templates
}

// orgCoderServer is a fake of the organization's own Coder deployment, answering
// the two read-only endpoints ListTemplates needs.
func orgCoderServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/templates":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":                "org-tpl-id",
				"name":              "org-workspace",
				"display_name":      "Org Workspace",
				"description":       "from the org deployment",
				"icon":              "",
				"active_version_id": "ver-1",
			}})
		case "/api/v2/templateversions/ver-1/rich-parameters":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "size"}})
		default:
			http.Error(w, "unexpected route "+r.URL.Path, http.StatusNotFound)
		}
	}))
}

// When the org has its own Coder connection, the picker lists the templates on
// THAT deployment, not the shared deployment's.
func TestListCoderTemplatesUsesOrgConnectionWhenPresent(t *testing.T) {
	t.Parallel()
	coderSrv := orgCoderServer(t)
	defer coderSrv.Close()

	config, err := domain.EncodeOrgCoderConfig(domain.OrgCoderConfig{
		BaseURL: coderSrv.URL, Owner: "ao-bot", TemplateID: coderCfgTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	cipher := newTestCipher(t)
	encrypted, nonce, err := cipher.Encrypt(
		[]byte(coderCfgToken),
		secrets.ProviderConnectionAssociatedData(coderCfgOrgID, sandbox.ProviderCoder, coder.OrgConnectionLabel),
	)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeCoderConnStore{encrypted: encrypted, nonce: nonce, config: config, connID: "conn-1"}
	// A deployment lister is present too, so the test proves the ORG connection is
	// preferred — not merely used because nothing else was configured.
	deployment := &fakeDeploymentTemplateLister{templates: []coder.Template{{ID: "deployment-tpl", Name: "deployment"}}}
	srv := newCoderTemplatesServer(t, cipher, store, deployment)

	rec := httptest.NewRecorder()
	srv.listCoderTemplates(rec, coderConfigRequest(t, http.MethodGet, "", []string{sandbox.ProviderCoder}))

	templates := decodeTemplatesResponse(t, rec)
	if len(templates) != 1 || templates[0].ID != "org-tpl-id" {
		t.Fatalf("templates = %+v, want the org deployment's single template", templates)
	}
	if len(templates[0].Parameters) != 1 || templates[0].Parameters[0] != "size" {
		t.Fatalf("template parameters = %v, want [size]", templates[0].Parameters)
	}
}

// With no org connection, the picker falls back to the shared deployment lister.
func TestListCoderTemplatesFallsBackToDeploymentWhenAbsent(t *testing.T) {
	t.Parallel()
	store := &fakeCoderConnStore{err: postgres.ErrNotFound}
	deployment := &fakeDeploymentTemplateLister{templates: []coder.Template{{ID: "deployment-tpl", Name: "deployment"}}}
	srv := newCoderTemplatesServer(t, newTestCipher(t), store, deployment)

	rec := httptest.NewRecorder()
	srv.listCoderTemplates(rec, coderConfigRequest(t, http.MethodGet, "", []string{sandbox.ProviderCoder}))

	templates := decodeTemplatesResponse(t, rec)
	if len(templates) != 1 || templates[0].ID != "deployment-tpl" {
		t.Fatalf("templates = %+v, want the shared deployment's single template", templates)
	}
}

// With neither an org connection nor a deployment lister, the list is empty (and
// 200, not an error) — the picker then shows only "Default".
func TestListCoderTemplatesEmptyWhenNothingConfigured(t *testing.T) {
	t.Parallel()
	store := &fakeCoderConnStore{err: postgres.ErrNotFound}
	srv := newCoderTemplatesServer(t, newTestCipher(t), store, nil)

	rec := httptest.NewRecorder()
	srv.listCoderTemplates(rec, coderConfigRequest(t, http.MethodGet, "", []string{sandbox.ProviderCoder}))

	templates := decodeTemplatesResponse(t, rec)
	if len(templates) != 0 {
		t.Fatalf("templates = %+v, want empty", templates)
	}
}
