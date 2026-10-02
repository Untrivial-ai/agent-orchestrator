package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// When the org already has an active project for the repository, the store
// returns a typed ProjectRepositoryConflictError. The handler must render a
// clear, specific 409 that names the repository — never the generic
// "resource conflicts with an existing record" string the UI showed as a raw
// red error.
func TestCreateProjectRendersRepositoryConflictSpecifically(t *testing.T) {
	srv, store := newRepositoryProbeTestServer(t, githubAPIMock(true))
	store.createErr = &postgres.ProjectRepositoryConflictError{
		RepositoryURL: "https://github.com/octo/widgets",
	}

	w := httptest.NewRecorder()
	srv.createProject(w, createProjectRequestFor(t, "https://github.com/octo/widgets.git"))

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, w.Body.String())
	}
	if envelope.Code != "project_repository_exists" {
		t.Fatalf("code = %q, want project_repository_exists", envelope.Code)
	}
	if !strings.Contains(envelope.Message, "octo/widgets") {
		t.Fatalf("message = %q, want it to name the repository octo/widgets", envelope.Message)
	}
	if strings.Contains(envelope.Message, "The resource conflicts with an existing record") {
		t.Fatalf("message = %q, must not be the generic conflict string", envelope.Message)
	}
}

// A store conflict without a URL (the GitHub App path authorizes by repository
// id, so the URL is not available) still renders a clear, repository-aware 409
// rather than the generic conflict string.
func TestCreateProjectRendersRepositoryConflictWithoutURL(t *testing.T) {
	srv, store := newRepositoryProbeTestServer(t, githubAPIMock(true))
	store.createErr = &postgres.ProjectRepositoryConflictError{}

	w := httptest.NewRecorder()
	srv.createProject(w, createProjectRequestFor(t, "https://github.com/octo/widgets.git"))

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, w.Body.String())
	}
	if envelope.Code != "project_repository_exists" {
		t.Fatalf("code = %q, want project_repository_exists", envelope.Code)
	}
	if !strings.Contains(envelope.Message, "already have a project for this repository") {
		t.Fatalf("message = %q, want the repository-agnostic conflict message", envelope.Message)
	}
	if strings.Contains(envelope.Message, "The resource conflicts with an existing record") {
		t.Fatalf("message = %q, must not be the generic conflict string", envelope.Message)
	}
}

// A generic store conflict (not a repository-uniqueness violation) keeps the
// existing generic 409 mapping — the specific message is scoped to the repo case.
func TestCreateProjectGenericConflictKeepsGenericMessage(t *testing.T) {
	srv, store := newRepositoryProbeTestServer(t, githubAPIMock(true))
	store.createErr = postgres.ErrConflict

	w := httptest.NewRecorder()
	srv.createProject(w, createProjectRequestFor(t, "https://github.com/octo/widgets.git"))

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", w.Code, w.Body.String())
	}
	var envelope errorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, w.Body.String())
	}
	if envelope.Code != "conflict" {
		t.Fatalf("code = %q, want conflict", envelope.Code)
	}
}
