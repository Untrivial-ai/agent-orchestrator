package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/go-chi/chi/v5"
)

const projectSettingsTestOrgID = "00000000-0000-0000-0000-0000000000aa"
const projectSettingsTestProjectID = "00000000-0000-0000-0000-0000000000bb"

type projectSettingsStore struct {
	Store
	input domain.UpdateProject
}

func (s *projectSettingsStore) UpdateProject(
	_ context.Context,
	_ domain.Principal,
	_, _ string,
	input domain.UpdateProject,
) (domain.Project, error) {
	s.input = input
	return domain.Project{
		ID: projectSettingsTestProjectID, OrgID: projectSettingsTestOrgID,
		DisplayName: input.DisplayName, DefaultBranch: input.DefaultBranch,
		RepositoryURL: "https://github.com/octo/widgets.git", Config: input.Config,
	}, nil
}

func TestUpdateProjectPersistsCloudProjectSettings(t *testing.T) {
	store := &projectSettingsStore{}
	body := []byte(`{
		"displayName":"Widgets",
		"defaultBranch":"main",
		"config":{"sessionPrefix":"widgets","worker":{"agent":"codex","agentConfig":{"model":"gpt-5","effort":"high"}}}
	}`)
	req := httptest.NewRequest(http.MethodPatch, "/orgs/"+projectSettingsTestOrgID+"/projects/"+projectSettingsTestProjectID, bytes.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("orgId", projectSettingsTestOrgID)
	rctx.URLParams.Add("projectId", projectSettingsTestProjectID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = context.WithValue(ctx, principalKey, domain.Principal{UserID: "00000000-0000-0000-0000-000000000001"})
	w := httptest.NewRecorder()

	(&Server{store: store}).updateProject(w, req.WithContext(ctx))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(store.input.Config, &got); err != nil {
		t.Fatalf("saved config is not JSON: %v", err)
	}
	if got["sessionPrefix"] != "widgets" {
		t.Fatalf("session prefix = %#v", got["sessionPrefix"])
	}
	worker, ok := got["worker"].(map[string]any)
	if !ok || worker["agent"] != "codex" {
		t.Fatalf("worker settings = %#v", got["worker"])
	}
}
