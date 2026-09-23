package controllers_test

import (
	"context"

	"encoding/json"

	"io"

	"log/slog"
	"net/url"

	"net/http"

	"net/http/httptest"

	"os"

	"os/exec"

	"path/filepath"

	"strings"

	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"

	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// emptyGetManager returns a GetResult that sets neither Project nor Degraded —

// a Manager-contract violation — so the test can prove the handler answers a

// clean 500 before writing the 200 status.

type emptyGetManager struct{ projectsvc.Manager }

func (emptyGetManager) Get(context.Context, domain.ProjectID) (projectsvc.GetResult, error) {

	return projectsvc.GetResult{}, nil

}

// TestProjectsAPI_GetEmptyResultIs500 locks the fix for the discriminated-union

// invariant: a degenerate GetResult must surface as a parseable 500 envelope,

// not a 200 with truncated JSON.

func TestProjectsAPI_GetEmptyResultIs500(t *testing.T) {

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{

		Projects: emptyGetManager{},
	}, httpd.ControlDeps{}))

	t.Cleanup(srv.Close)

	body, status, headers := doRequest(t, srv, "GET", "/api/v1/projects/whatever", "")

	assertJSON(t, headers)

	assertErrorCode(t, body, status, http.StatusInternalServerError, "INTERNAL_ERROR")

}

func newTestServer(t *testing.T) *httptest.Server {

	t.Helper()
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	store, err := sqlitetest.Open(t.TempDir())

	if err != nil {

		t.Fatalf("open store: %v", err)

	}

	t.Cleanup(func() { _ = store.Close() })

	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{

		Projects: projectsvc.New(store),
	}, httpd.ControlDeps{}))

	t.Cleanup(srv.Close)

	return srv

}

func TestProjectsRoutes_DefaultToStubsWithoutManager(t *testing.T) {

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{}, httpd.ControlDeps{}))

	t.Cleanup(srv.Close)

	body, status, headers := doRequest(t, srv, "GET", "/api/v1/projects", "")

	assertJSON(t, headers)

	assertErrorCode(t, body, status, http.StatusNotImplemented, "NOT_IMPLEMENTED")

}

func TestProjectsAPI_ListAddGet(t *testing.T) {

	srv := newTestServer(t)

	repo := gitRepo(t, "agent-orchestrator")

	body, status, headers := doRequest(t, srv, "GET", "/api/v1/projects", "")

	if status != http.StatusOK {

		t.Fatalf("GET projects = %d, want 200; body=%s", status, body)

	}

	assertJSON(t, headers)

	var list struct {
		Projects []projectSummary `json:"projects"`
	}

	mustJSON(t, body, &list)

	if len(list.Projects) != 0 {

		t.Fatalf("initial project count = %d, want 0", len(list.Projects))

	}

	body, status, _ = doRequest(t, srv, "POST", "/api/v1/projects", `{"path":`+quote(repo)+`,"projectId":"ao","name":"Agent Orchestrator"}`)

	if status != http.StatusCreated {

		t.Fatalf("POST project = %d, want 201; body=%s", status, body)

	}

	var add struct {
		Project projectBody `json:"project"`
	}

	mustJSON(t, body, &add)

	if add.Project.ID != "ao" || add.Project.Name != "Agent Orchestrator" || add.Project.DefaultBranch != domain.DefaultBranchAuto {

		t.Fatalf("created project = %#v", add.Project)

	}

	body, status, _ = doRequest(t, srv, "GET", "/api/v1/projects/ao", "")

	if status != http.StatusOK {

		t.Fatalf("GET project = %d, want 200; body=%s", status, body)

	}

	var get struct {
		Status string `json:"status"`

		Project projectBody `json:"project"`
	}

	mustJSON(t, body, &get)

	if get.Status != "ok" || get.Project.ID != "ao" {

		t.Fatalf("get response = %#v", get)

	}

	body, status, _ = doRequest(t, srv, "GET", "/api/v1/projects", "")
	if status != http.StatusOK {
		t.Fatalf("GET projects after add = %d, want 200; body=%s", status, body)
	}
	mustJSON(t, body, &list)
	if len(list.Projects) != 1 || list.Projects[0].Path != repo {
		t.Fatalf("project summary path = %#v, want path %q", list.Projects, repo)
	}

}

func TestProjectsAPI_UpdateSettings(t *testing.T) {
	srv := newTestServer(t)
	repo := gitRepo(t, "legacy-project")

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/projects", `{"path":`+quote(repo)+`,"projectId":"legacy-project"}`)
	if status != http.StatusCreated {
		t.Fatalf("seed create = %d, want 201; body=%s", status, body)
	}

	body, status, _ = doRequest(t, srv, "PUT", "/api/v1/projects/legacy-project", `{"displayName":"Friendly project","config":{"defaultBranch":"develop"}}`)
	if status != http.StatusOK {
		t.Fatalf("PUT settings = %d, want 200; body=%s", status, body)
	}
	var updated struct {
		Project projectBody `json:"project"`
	}
	mustJSON(t, body, &updated)
	if updated.Project.ID != "legacy-project" || updated.Project.Name != "Friendly project" || updated.Project.DefaultBranch != "develop" {
		t.Fatalf("update response = %#v", updated.Project)
	}

	body, status, _ = doRequest(t, srv, "PUT", "/api/v1/projects/legacy-project", `{"displayName":"  ","config":{}}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "DISPLAY_NAME_REQUIRED")

	body, status, _ = doRequest(t, srv, "PUT", "/api/v1/projects/legacy-project", `{"displayName":"`+strings.Repeat("x", 101)+`","config":{}}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "DISPLAY_NAME_TOO_LONG")

	body, status, _ = doRequest(t, srv, "PUT", "/api/v1/projects/missing", `{"displayName":"Missing","config":{}}`)
	assertErrorCode(t, body, status, http.StatusNotFound, "PROJECT_NOT_FOUND")

	body, status, _ = doRequest(t, srv, "PUT", "/api/v1/projects/legacy-project", `{`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
}

func TestProjectsAPI_AddValidationAndConflicts(t *testing.T) {

	srv := newTestServer(t)

	repoA := gitRepo(t, "repo-a")

	repoB := gitRepo(t, "repo-b")

	notRepo := t.TempDir()

	cases := []struct {
		name, body, wantCode string

		wantStatus int
	}{

		{name: "invalid json", body: `{`, wantStatus: 400, wantCode: "INVALID_JSON"},

		{name: "missing path", body: `{}`, wantStatus: 400, wantCode: "PATH_REQUIRED"},

		{name: "not git", body: `{"path":` + quote(notRepo) + `}`, wantStatus: 400, wantCode: "NOT_A_GIT_REPO"},
	}

	for _, tc := range cases {

		t.Run(tc.name, func(t *testing.T) {

			body, status, _ := doRequest(t, srv, "POST", "/api/v1/projects", tc.body)

			assertErrorCode(t, body, status, tc.wantStatus, tc.wantCode)

		})

	}

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/projects", `{"path":`+quote(repoA)+`,"projectId":"shared"}`)

	if status != http.StatusCreated {

		t.Fatalf("seed create = %d, want 201; body=%s", status, body)

	}

	body, status, _ = doRequest(t, srv, "POST", "/api/v1/projects", `{"path":`+quote(repoA)+`,"projectId":"other"}`)

	assertErrorCode(t, body, status, http.StatusConflict, "PATH_ALREADY_REGISTERED")

	body, status, _ = doRequest(t, srv, "POST", "/api/v1/projects", `{"path":`+quote(repoB)+`,"projectId":"shared"}`)

	assertErrorCode(t, body, status, http.StatusConflict, "ID_ALREADY_REGISTERED")

}

func TestProjectsAPI_Clone(t *testing.T) {
	srv := newTestServer(t)
	source := gitRepo(t, "clone-source")
	remoteURL := (&url.URL{Scheme: "file", Path: source}).String()
	destinationParent := t.TempDir()

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/projects/clone", `{"remoteUrl":`+quote(remoteURL)+`,"destinationParent":`+quote(destinationParent)+`}`)
	if status != http.StatusCreated {
		t.Fatalf("POST clone = %d, want 201; body=%s", status, body)
	}
	var cloned struct {
		Project projectBody `json:"project"`
	}
	mustJSON(t, body, &cloned)
	if cloned.Project.Path != filepath.Join(destinationParent, filepath.Base(source)) || cloned.Project.Repo != remoteURL {
		t.Fatalf("cloned project = %#v", cloned.Project)
	}

	body, status, _ = doRequest(t, srv, "POST", "/api/v1/projects/clone", `{}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_GIT_URL")
	body, status, _ = doRequest(t, srv, "POST", "/api/v1/projects/clone", `{`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")
}

func TestProjectsAPI_InitializeRepository(t *testing.T) {
	srv := newTestServer(t)

	base := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", base)

	plain := filepath.Join(base, "plain")
	if err := os.Mkdir(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	body, status, _ := doRequest(t, srv, "POST", "/api/v1/projects/initialize", `{"path":`+quote(plain)+`}`)
	if status != http.StatusOK {
		t.Fatalf("POST initialize plain = %d, want 200; body=%s", status, body)
	}
	if out, err := exec.Command("git", "-C", plain, "rev-parse", "--verify", "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("plain folder was not committed: %v\\n%s", err, out)
	}

	unborn := filepath.Join(base, "unborn")
	if out, err := exec.Command("git", "init", "-b", "main", unborn).CombinedOutput(); err != nil {
		t.Fatalf("git init unborn fixture: %v\\n%s", err, out)
	}
	body, status, _ = doRequest(t, srv, "POST", "/api/v1/projects/initialize", `{"path":`+quote(unborn)+`}`)
	if status != http.StatusOK {
		t.Fatalf("POST initialize unborn = %d, want 200; body=%s", status, body)
	}
	if out, err := exec.Command("git", "-C", unborn, "rev-parse", "--verify", "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("unborn repo was not committed: %v\\n%s", err, out)
	}

	committed := gitRepo(t, "committed")
	body, status, _ = doRequest(t, srv, "POST", "/api/v1/projects/initialize", `{"path":`+quote(committed)+`}`)
	assertErrorCode(t, body, status, http.StatusConflict, "PROJECT_ALREADY_INITIALIZED")
}
func TestProjectsAPI_Delete(t *testing.T) {

	srv := newTestServer(t)

	repo := gitRepo(t, "repo")

	body, status, _ := doRequest(t, srv, "POST", "/api/v1/projects", `{"path":`+quote(repo)+`,"projectId":"proj"}`)

	if status != http.StatusCreated {

		t.Fatalf("seed create = %d, want 201; body=%s", status, body)

	}

	body, status, _ = doRequest(t, srv, "DELETE", "/api/v1/projects/proj", "")

	if status != http.StatusOK {

		t.Fatalf("DELETE = %d, want 200; body=%s", status, body)

	}

	var removed struct {
		ProjectID string `json:"projectId"`

		RemovedStorageDir bool `json:"removedStorageDir"`
	}

	mustJSON(t, body, &removed)

	if removed.ProjectID != "proj" || removed.RemovedStorageDir {

		t.Fatalf("delete response = %#v", removed)

	}

	body, status, _ = doRequest(t, srv, "GET", "/api/v1/projects/proj", "")

	if status != http.StatusNotFound {

		t.Fatalf("GET archived project = %d, want 404; body=%s", status, body)

	}

	body, status, _ = doRequest(t, srv, "GET", "/api/v1/projects", "")

	if status != http.StatusOK {

		t.Fatalf("GET projects after archive = %d, want 200; body=%s", status, body)

	}

	var list struct {
		Projects []projectSummary `json:"projects"`
	}

	mustJSON(t, body, &list)

	if len(list.Projects) != 0 {

		t.Fatalf("active projects after archive = %d, want 0", len(list.Projects))

	}

}

// TestProjectsAPI_RejectsUnknownConfigKeys locks the strict-decoder gate on the
// project config endpoints: a misspelled or removed field surfaces as a clear
// 400 instead of being silently dropped, so the API cannot accumulate dead
// config the daemon never reads.
func TestProjectsAPI_RejectsUnknownConfigKeys(t *testing.T) {
	srv := newTestServer(t)
	repo := gitRepo(t, "rejects-unknown")
	body, status, _ := doRequest(t, srv, "POST", "/api/v1/projects", `{"path":`+quote(repo)+`,"projectId":"rej"}`)
	if status != http.StatusCreated {
		t.Fatalf("seed create = %d, want 201; body=%s", status, body)
	}

	// PUT settings with an extraneous top-level key.
	body, status, _ = doRequest(t, srv, "PUT", "/api/v1/projects/rej", `{"displayName":"Rejects unknown","config":{"defaultBranch":"develop"},"surprise":"!"}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")

	// Prompt rules are now modeled and accepted in project config.
	body, status, _ = doRequest(t, srv, "PUT", "/api/v1/projects/rej", `{"displayName":"Rejects unknown","config":{"agentRules":"x"}}`)
	if status != http.StatusOK {
		t.Fatalf("agentRules settings = %d, want 200; body=%s", status, body)
	}

	// A still-unknown nested config field is rejected, so misspellings cannot be
	// silently persisted.
	body, status, _ = doRequest(t, srv, "PUT", "/api/v1/projects/rej", `{"displayName":"Rejects unknown","config":{"tracker":{"plugin":"github"}}}`)
	assertErrorCode(t, body, status, http.StatusBadRequest, "INVALID_JSON")

	// POST /projects gets the same gate, so add-time config rides the same rail.
	otherRepo := gitRepo(t, "rejects-unknown-add")
	body, status, _ = doRequest(t, srv, "POST", "/api/v1/projects", `{"path":`+quote(otherRepo)+`,"projectId":"rej2","config":{"orchestratorRules":"x"}}`)
	if status != http.StatusCreated {
		t.Fatalf("orchestratorRules add config = %d, want 201; body=%s", status, body)
	}
}

func TestProjectsRoutes_LegacyUnregistered(t *testing.T) {

	srv := newTestServer(t)

	cases := []struct {
		method, path, wantCode, why string

		wantStatus int
	}{

		{method: "PATCH", path: "/api/v1/projects/p1", wantStatus: 405, wantCode: "METHOD_NOT_ALLOWED", why: "standalone rename is not registered"},
	}

	for _, tc := range cases {

		t.Run(tc.why, func(t *testing.T) {

			body, status, _ := doRequest(t, srv, tc.method, tc.path, "")

			assertErrorCode(t, body, status, tc.wantStatus, tc.wantCode)

		})

	}

}

func TestProjectsRoutes_MissingRoute(t *testing.T) {

	srv := newTestServer(t)

	body, status, headers := doRequest(t, srv, "GET", "/api/v1/projects/p1/does-not-exist", "")

	assertJSON(t, headers)

	assertErrorCode(t, body, status, http.StatusNotFound, "ROUTE_NOT_FOUND")

}

func TestOpenAPIYAMLServed(t *testing.T) {

	srv := newTestServer(t)

	body, status, headers := doRequest(t, srv, "GET", "/api/v1/openapi.yaml", "")

	if status != http.StatusOK {

		t.Fatalf("status = %d, want 200", status)

	}

	if ct := headers.Get("Content-Type"); !strings.HasPrefix(ct, "application/yaml") {

		t.Errorf("Content-Type = %q, want application/yaml*", ct)

	}

	if !strings.Contains(string(body), "openapi: 3.1.0") {

		t.Errorf("served body did not start with an OpenAPI 3.1 doc")

	}

}

type projectSummary struct {
	ID string `json:"id"`

	Name string `json:"name"`

	Path string `json:"path"`

	SessionPrefix string `json:"sessionPrefix"`
}

type projectBody struct {
	ID string `json:"id"`

	Name string `json:"name"`

	Path string `json:"path"`

	Repo string `json:"repo"`

	DefaultBranch string `json:"defaultBranch"`

	Agent string `json:"agent"`
}

type errorBody struct {
	Error string `json:"error"`

	Code string `json:"code"`

	Message string `json:"message"`

	Details map[string]any `json:"details"`
}

func doRequest(t *testing.T, srv *httptest.Server, method, path, body string) ([]byte, int, http.Header) {

	t.Helper()

	var req *http.Request

	var err error

	if body != "" {

		req, err = http.NewRequest(method, srv.URL+path, strings.NewReader(body))

	} else {

		req, err = http.NewRequest(method, srv.URL+path, nil)

	}

	if err != nil {

		t.Fatalf("new request: %v", err)

	}

	if body != "" {

		req.Header.Set("Content-Type", "application/json")

	}

	resp, err := srv.Client().Do(req)

	if err != nil {

		t.Fatalf("do request: %v", err)

	}

	defer resp.Body.Close()

	buf, err := io.ReadAll(resp.Body)

	if err != nil {

		t.Fatalf("read body: %v", err)

	}

	return buf, resp.StatusCode, resp.Header

}

func gitRepo(t *testing.T, name string) string {

	t.Helper()

	dir := filepath.Join(t.TempDir(), name)

	if err := os.MkdirAll(dir, 0o755); err != nil {

		t.Fatalf("create git repo fixture: %v", err)

	}

	if out, err := exec.Command("git", "init", "-b", "main", dir).CombinedOutput(); err != nil {

		t.Fatalf("git init fixture: %v\n%s", err, out)

	}
	if out, err := exec.Command("git", "-C", dir, "-c", "user.email=ao@example.com", "-c", "user.name=AO Test", "commit", "--allow-empty", "-m", "initial").CombinedOutput(); err != nil {
		t.Fatalf("git commit fixture: %v\n%s", err, out)
	}
	return dir

}

func quote(s string) string {

	b, _ := json.Marshal(s)

	return string(b)

}

func mustJSON(t *testing.T, body []byte, out any) {

	t.Helper()

	if err := json.Unmarshal(body, out); err != nil {

		t.Fatalf("unmarshal: %v\nbody=%s", err, body)

	}

}

func assertJSON(t *testing.T, headers http.Header) {

	t.Helper()

	if ct := headers.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {

		t.Fatalf("Content-Type = %q, want JSON", ct)

	}

}

func assertErrorCode(t *testing.T, body []byte, status, wantStatus int, wantCode string) {

	t.Helper()

	if status != wantStatus {

		t.Fatalf("status = %d, want %d\nbody=%s", status, wantStatus, body)

	}

	var got errorBody

	mustJSON(t, body, &got)

	if got.Code != wantCode {

		t.Fatalf("code = %q, want %q\nbody=%s", got.Code, wantCode, body)

	}

}

func TestProjectsAPI_SetPermissions(t *testing.T) {
	srv := newTestServer(t)
	repo := gitRepo(t, "remember")
	body, status, _ := doRequest(t, srv, "POST", "/api/v1/projects", `{"path":`+quote(repo)+`,"projectId":"remember"}`)
	if status != http.StatusCreated {
		t.Fatalf("create %d %s", status, body)
	}
	body, status, _ = doRequest(t, srv, "PATCH", "/api/v1/projects/remember/permissions", `{"permissions":"auto"}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"permissions":"auto"`) {
		t.Fatalf("save %d %s", status, body)
	}
	for _, tc := range []struct{ body, code string }{{`{}`, "INVALID_PERMISSIONS"}, {`{"permissions":"yolo"}`, "INVALID_PERMISSIONS"}, {`{"permissions":"auto","extra":true}`, "INVALID_JSON"}, {`{`, "INVALID_JSON"}} {
		body, status, _ = doRequest(t, srv, "PATCH", "/api/v1/projects/remember/permissions", tc.body)
		assertErrorCode(t, body, status, http.StatusBadRequest, tc.code)
	}
	body, status, _ = doRequest(t, srv, "PATCH", "/api/v1/projects/missing/permissions", `{"permissions":"auto"}`)
	assertErrorCode(t, body, status, http.StatusNotFound, "PROJECT_NOT_FOUND")
}
