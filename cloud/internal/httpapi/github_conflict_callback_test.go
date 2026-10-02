package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// When the store reports that the GitHub account is already connected by another
// AO workspace, the callback must render the specific conflict page (naming the
// GitHub account) rather than the generic "Connection failed" page.
func TestGitHubCallbackErrorRendersConflictPageForCrossOrgOwnership(t *testing.T) {
	srv, _ := newGitHubCallbackTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/cloud/v1/github/oauth/callback", nil)

	srv.githubCallbackError(rec, req, &postgres.InstallationOwnedByAnotherOrgError{
		GitHubInstallationID: 42,
		AccountLogin:         "octo-org",
		OwnerOrgID:           "00000000-0000-0000-0000-000000000001",
	})

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a cross-org conflict", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "octo-org") {
		t.Errorf("conflict page does not name the GitHub account: %q", body)
	}
	if !strings.Contains(body, "already connected to another AO workspace") {
		t.Errorf("conflict page missing the actionable message: %q", body)
	}
	if strings.Contains(body, "Connection failed") {
		t.Error("cross-org conflict rendered the generic failure page")
	}
	// The owning organization is never disclosed across the tenant boundary.
	if strings.Contains(body, "00000000-0000-0000-0000-000000000001") {
		t.Error("conflict page leaked the owning organization id")
	}
}

// A generic conflict (no owner information) still renders the generic failure
// page and is not logged as an unexpected error.
func TestGitHubCallbackErrorGenericConflictUsesFailurePage(t *testing.T) {
	srv, _ := newGitHubCallbackTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/cloud/v1/github/oauth/callback", nil)

	srv.githubCallbackError(rec, req, postgres.ErrConflict)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Connection failed") {
		t.Errorf("generic conflict page = %q", rec.Body.String())
	}
}
