package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
	"github.com/go-chi/chi/v5"
)

type projectSettingsFakeStore struct {
	Store
	patch domain.ProjectSettingsPatch
	calls int
	err   error
}

func (s *projectSettingsFakeStore) UpdateProjectSettings(_ context.Context, _ domain.Principal, _, _ string, patch domain.ProjectSettingsPatch) (domain.Project, error) {
	s.calls++
	s.patch = patch
	return domain.Project{ID: "project", Config: json.RawMessage(`{"worker":{"agent":"codex"}}`)}, s.err
}

func TestProjectSettingsHandlerPartialPatchAndErrors(t *testing.T) {
	for _, test := range []struct {
		name, body    string
		err           error
		status, calls int
	}{
		{"partial", `{"config":{"autoReview":false}}`, nil, http.StatusOK, 1},
		{"clear worker", `{"config":{"worker":null}}`, nil, http.StatusOK, 1},
		{"clear orchestrator", `{"config":{"orchestrator":null}}`, nil, http.StatusOK, 1},
		{"unknown", `{"config":{"sessionPrefix":"unused"}}`, nil, http.StatusUnprocessableEntity, 0},
		{"invalid", `{"displayName":null}`, nil, http.StatusUnprocessableEntity, 0},
		{"store validation", `{}`, postgres.ErrInvalid, http.StatusUnprocessableEntity, 1},
		{"not found", `{}`, postgres.ErrNotFound, http.StatusNotFound, 1},
		{"forbidden", `{}`, postgres.ErrForbidden, http.StatusForbidden, 1},
		{"failure", `{}`, errors.New("store unavailable"), http.StatusInternalServerError, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &projectSettingsFakeStore{err: test.err}
			s := New(Options{Store: store})
			router := chi.NewRouter()
			router.Patch("/orgs/{orgId}/projects/{projectId}/settings", s.updateProjectSettings)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/orgs/00000000-0000-0000-0000-000000000001/projects/00000000-0000-0000-0000-000000000002/settings", strings.NewReader(test.body)))
			if response.Code != test.status || store.calls != test.calls {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, store.calls, response.Body.String())
			}
			if test.name == "partial" && (store.patch.DisplayName != nil || store.patch.DefaultBranch != nil || string(store.patch.Config) != `{"autoReview":false}`) {
				t.Fatalf("omitted fields were populated: %+v", store.patch)
			}
			if strings.HasPrefix(test.name, "clear ") && !strings.Contains(string(store.patch.Config), ":null") {
				t.Fatalf("role reset was dropped: %+v", store.patch)
			}
		})
	}
}
