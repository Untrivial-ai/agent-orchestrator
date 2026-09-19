package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	scmgitlab "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/gitlab"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// recordedReq captures one inbound HTTP request so tests can assert against
// the exact GitLab API surface the adapter touched.
type recordedReq struct {
	Method string
	Path   string
	Body   string
}

// fakeGL is a programmable httptest.Server that matches requests by
// "METHOD path" and records every call. Unmatched requests fail the test.
type fakeGL struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	requests []recordedReq
	handlers map[string]http.HandlerFunc
}

func newFakeGL(t *testing.T) *fakeGL {
	t.Helper()
	f := &fakeGL{t: t, handlers: map[string]http.HandlerFunc{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGL) on(method, path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method+" "+path] = h
}

func (f *fakeGL) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.Path
	f.mu.Lock()
	f.requests = append(f.requests, recordedReq{Method: r.Method, Path: r.URL.Path, Body: string(body)})
	h, ok := f.handlers[key]
	f.mu.Unlock()
	if !ok {
		f.t.Errorf("unexpected request: %s", key)
		http.Error(w, "no handler", http.StatusNotImplemented)
		return
	}
	r.Body = io.NopCloser(strings.NewReader(string(body)))
	h(w, r)
}

func (f *fakeGL) calls() []recordedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedReq, len(f.requests))
	copy(out, f.requests)
	return out
}

// newTrackerForTest constructs an adapter pointed at the fake server with a
// static dev token.
func newTrackerForTest(t *testing.T, f *fakeGL) *Tracker {
	t.Helper()
	tr, err := New(Options{
		BaseURL:    f.server.URL,
		Token:      scmgitlab.StaticTokenSource("tkn-test"),
		HTTPClient: f.server.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

func ctx() context.Context { return context.Background() }

// gqlVars decodes the GraphQL request body's variables map for assertions.
func gqlVars(t *testing.T, body string) map[string]any {
	t.Helper()
	var req struct {
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("failed to decode graphql request body: %v\nbody: %s", err, body)
	}
	return req.Variables
}

// workItemJSON builds a GraphQL work item node JSON suitable for test responses.
func workItemJSON(iid int, title, desc, state, webURL string, labels []string, assignees []string) string {
	var widgets []string
	if len(assignees) > 0 {
		var nodes []string
		for _, a := range assignees {
			nodes = append(nodes, `{"username":"`+a+`"}`)
		}
		widgets = append(widgets, `{"type":"ASSIGNEES","assignees":{"nodes":[`+strings.Join(nodes, ",")+`]}}`)
	}
	if len(labels) > 0 {
		var nodes []string
		for _, l := range labels {
			nodes = append(nodes, `{"title":"`+l+`"}`)
		}
		widgets = append(widgets, `{"type":"LABELS","labels":{"nodes":[`+strings.Join(nodes, ",")+`]}}`)
	}
	widgetsStr := "[]"
	if len(widgets) > 0 {
		widgetsStr = "[" + strings.Join(widgets, ",") + "]"
	}
	return `{"iid":"` + strconv.Itoa(iid) + `","title":` + jsonStr(title) + `,"description":` + jsonStr(desc) + `,"state":"` + state + `","webUrl":"` + webURL + `","widgets":` + widgetsStr + `}`
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// workItemsListJSON wraps work item nodes into a GraphQL project.workItems response.
func workItemsListJSON(nodes []string, endCursor string, hasNextPage bool) string {
	nodesStr := "[]"
	if len(nodes) > 0 {
		nodesStr = "[" + strings.Join(nodes, ",") + "]"
	}
	return `{"project":{"workItems":{"pageInfo":{"endCursor":"` + endCursor + `","hasNextPage":` + strconv.FormatBool(hasNextPage) + `},"nodes":` + nodesStr + `}}}`
}

// ---------------------------------------------------------------------------
// New / construction
// ---------------------------------------------------------------------------

func TestNewRejectsMissingToken(t *testing.T) {
	if _, err := New(Options{Token: scmgitlab.StaticTokenSource("")}); !errors.Is(err, scmgitlab.ErrNoToken) {
		t.Fatalf("New with empty token = %v, want ErrNoToken", err)
	}
	if _, err := New(Options{}); !errors.Is(err, ErrNoToken) {
		t.Fatalf("New with no source = %v, want ErrNoToken", err)
	}
}

// ---------------------------------------------------------------------------
// ID parsing
// ---------------------------------------------------------------------------

func TestParseID(t *testing.T) {
	cases := []struct {
		name     string
		native   string
		wantPath string
		wantIID  int
		wantErr  bool
	}{
		{"happy", "octocat/hello-world#42", "octocat/hello-world", 42, false},
		{"nested group", "group/subgroup/project#7", "group/subgroup/project", 7, false},
		{"missing hash", "octocat/hello-world", "", 0, true},
		{"missing slash", "octocat#42", "", 0, true},
		{"empty path", "#42", "", 0, true},
		{"non-numeric iid", "o/r#abc", "", 0, true},
		{"zero iid", "o/r#0", "", 0, true},
		{"negative iid", "o/r#-1", "", 0, true},
		{"whitespace in path", "o/r space#1", "", 0, true},
		{"hash in path", "o/r#po#1", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, iid, err := parseGitLabID(tc.native)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %s#%d", path, iid)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if path != tc.wantPath || iid != tc.wantIID {
				t.Fatalf("got %s#%d, want %s#%d", path, iid, tc.wantPath, tc.wantIID)
			}
		})
	}
}

func TestParseRepo(t *testing.T) {
	cases := []struct {
		name    string
		native  string
		wantErr bool
	}{
		{"happy", "group/project", false},
		{"nested", "group/sub/project", false},
		{"empty", "", true},
		{"no separator", "project", true},
		{"whitespace", " group/project", true},
		{"hash", "group/pro#ject", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseGitLabRepo(tc.native)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %q", tc.native)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.native, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestGet_HappyPath(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tkn-test" {
			t.Errorf("Authorization = %q, want Bearer tkn-test", got)
		}
		vars := gqlVars(t, readBody(r))
		if vars["fullPath"] != "octocat/hello-world" {
			t.Errorf("fullPath = %v, want octocat/hello-world", vars["fullPath"])
		}
		if vars["iid"] != "42" {
			t.Errorf("iid = %v, want 42", vars["iid"])
		}
		wi := workItemJSON(42, "Found a bug", "It does not work", "opened",
			"https://gitlab.com/octocat/hello-world/-/issues/42",
			[]string{"bug", "critical"}, []string{"alice", "bob"})
		_, _ = w.Write([]byte(`{"data":{"workItem":` + wi + `}}`))
	})
	tr := newTrackerForTest(t, f)

	issue, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "octocat/hello-world#42"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := domain.Issue{
		ID:        domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "octocat/hello-world#42"},
		Title:     "Found a bug",
		Body:      "It does not work",
		State:     domain.IssueOpen,
		URL:       "https://gitlab.com/octocat/hello-world/-/issues/42",
		Labels:    []string{"bug", "critical"},
		Assignees: []string{"alice", "bob"},
	}
	if !reflect.DeepEqual(issue, want) {
		t.Fatalf("issue = %#v\nwant %#v", issue, want)
	}
}

func TestGet_StateMapping(t *testing.T) {
	cases := []struct {
		name      string
		glState   string
		wantState domain.NormalizedIssueState
	}{
		{"opened", "opened", domain.IssueOpen},
		{"closed", "closed", domain.IssueDone},
		{"opened uppercase", "OPENED", domain.IssueOpen},
		{"closed uppercase", "CLOSED", domain.IssueDone},
		{"unknown defaults to open", "locked", domain.IssueOpen},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGL(t)
			wi := workItemJSON(1, "t", "", tc.glState, "https://gitlab.com/o/r/-/issues/1", nil, nil)
			f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"data":{"workItem":` + wi + `}}`))
			})
			tr := newTrackerForTest(t, f)
			issue, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "o/r#1"})
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if issue.State != tc.wantState {
				t.Fatalf("state = %q, want %q", issue.State, tc.wantState)
			}
		})
	}
}

func TestGet_NestedGroup(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		vars := gqlVars(t, readBody(r))
		if vars["fullPath"] != "group/sub/project" {
			t.Errorf("fullPath = %v, want group/sub/project", vars["fullPath"])
		}
		wi := workItemJSON(7, "nested", "d", "opened", "https://gitlab.com/group/sub/project/-/issues/7", nil, nil)
		_, _ = w.Write([]byte(`{"data":{"workItem":` + wi + `}}`))
	})
	tr := newTrackerForTest(t, f)
	issue, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "group/sub/project#7"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if issue.ID.Native != "group/sub/project#7" {
		t.Fatalf("Native = %q, want group/sub/project#7", issue.ID.Native)
	}
}

func TestGet_NotFound(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"workItem":null}}`))
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "o/r#1"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGet_GraphQLErrorNotFound(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"Not found","extensions":{"code":"NOT_FOUND"}}]}`))
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "o/r#1"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGet_RateLimited(t *testing.T) {
	f := newFakeGL(t)
	reset := time.Now().Add(2 * time.Minute).Unix()
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("RateLimit-Reset", strconv.FormatInt(reset, 10))
		w.Header().Set("Retry-After", "60")
		http.Error(w, `{"message":"Too many requests"}`, http.StatusTooManyRequests)
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "o/r#1"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
	if got := rle.ResetAt.Unix(); got != reset {
		t.Fatalf("ResetAt = %d, want %d", got, reset)
	}
	if rle.RetryAfter != 60*time.Second {
		t.Fatalf("RetryAfter = %v, want 60s", rle.RetryAfter)
	}
}

func TestGet_AuthFailed(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "o/r#1"})
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("err = %v, want ErrAuthFailed", err)
	}
}

func TestGet_ForbiddenAuthFailed(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"403 Forbidden"}`, http.StatusForbidden)
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "o/r#1"})
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("err = %v, want ErrAuthFailed", err)
	}
}

func TestGet_RejectsWrongProvider(t *testing.T) {
	f := newFakeGL(t)
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProvider("github"), Native: "o/r#1"})
	if !errors.Is(err, ErrWrongProvider) {
		t.Fatalf("err = %v, want ErrWrongProvider", err)
	}
}

func TestGet_RejectsEmptyProvider(t *testing.T) {
	f := newFakeGL(t)
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Native: "o/r#1"})
	if !errors.Is(err, ErrWrongProvider) {
		t.Fatalf("err = %v, want ErrWrongProvider", err)
	}
}

func TestGet_CanonicalizesProviderOnOutput(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		wi := workItemJSON(1, "t", "", "opened", "https://gitlab.com/o/r/-/issues/1", nil, nil)
		_, _ = w.Write([]byte(`{"data":{"workItem":` + wi + `}}`))
	})
	tr := newTrackerForTest(t, f)
	issue, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "o/r#1"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if issue.ID.Provider != domain.TrackerProviderGitLab {
		t.Fatalf("issue.ID.Provider = %q, want %q", issue.ID.Provider, domain.TrackerProviderGitLab)
	}
	if issue.ID.Native != "o/r#1" {
		t.Fatalf("issue.ID.Native = %q, want o/r#1", issue.ID.Native)
	}
}

func TestGet_EmptyLabelsAndAssigneesAreNil(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		wi := workItemJSON(1, "t", "", "opened", "https://gitlab.com/o/r/-/issues/1", nil, nil)
		_, _ = w.Write([]byte(`{"data":{"workItem":` + wi + `}}`))
	})
	tr := newTrackerForTest(t, f)
	issue, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "o/r#1"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if issue.Labels != nil {
		t.Fatalf("Labels = %#v, want nil", issue.Labels)
	}
	if issue.Assignees != nil {
		t.Fatalf("Assignees = %#v, want nil", issue.Assignees)
	}
}

func TestGet_NoDescription(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		// Work item with null description
		_, _ = w.Write([]byte(`{"data":{"workItem":{"iid":"1","title":"no desc","description":"","state":"opened","webUrl":"https://gitlab.com/o/r/-/issues/1","widgets":[]}}}`))
	})
	tr := newTrackerForTest(t, f)
	issue, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "o/r#1"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if issue.Body != "" {
		t.Fatalf("Body = %q, want empty", issue.Body)
	}
}

func TestGet_GraphQLErrorGeneric(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"Something went wrong","extensions":{"code":"INTERNAL"}}]}`))
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "o/r#1"})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, should not be ErrNotFound for non-NOT_FOUND code", err)
	}
	if !strings.Contains(err.Error(), "Something went wrong") {
		t.Fatalf("err = %v, want message containing 'Something went wrong'", err)
	}
}

func TestGet_ReversedWidgetOrder(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		// Labels widget before assignees widget — verifies iteration order doesn't matter.
		_, _ = w.Write([]byte(`{"data":{"workItem":{"iid":"1","title":"t","description":"d","state":"opened","webUrl":"https://gitlab.com/o/r/-/issues/1","widgets":[
			{"type":"LABELS","labels":{"nodes":[{"title":"bug"},{"title":"critical"}]}},
			{"type":"ASSIGNEES","assignees":{"nodes":[{"username":"alice"}]}}
		]}}}`))
	})
	tr := newTrackerForTest(t, f)
	issue, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitLab, Native: "o/r#1"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(issue.Labels, []string{"bug", "critical"}) {
		t.Fatalf("Labels = %#v, want [bug critical]", issue.Labels)
	}
	if !reflect.DeepEqual(issue.Assignees, []string{"alice"}) {
		t.Fatalf("Assignees = %#v, want [alice]", issue.Assignees)
	}
}

// ---------------------------------------------------------------------------
// Preflight
// ---------------------------------------------------------------------------

func TestPreflight_HappyPath(t *testing.T) {
	f := newFakeGL(t)
	f.on("GET", "/user", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tkn-test" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"id":1,"username":"octocat"}`))
	})
	tr := newTrackerForTest(t, f)
	if err := tr.Preflight(ctx()); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
}

func TestPreflight_InvalidToken(t *testing.T) {
	f := newFakeGL(t)
	f.on("GET", "/user", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
	})
	tr := newTrackerForTest(t, f)
	err := tr.Preflight(ctx())
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("err = %v, want ErrAuthFailed", err)
	}
}

func TestPreflight_CachesSuccess(t *testing.T) {
	f := newFakeGL(t)
	f.on("GET", "/user", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":1,"username":"octocat"}`))
	})
	tr := newTrackerForTest(t, f)
	for i := 0; i < 5; i++ {
		if err := tr.Preflight(ctx()); err != nil {
			t.Fatalf("Preflight #%d: %v", i, err)
		}
	}
	if got := len(f.calls()); got != 1 {
		t.Fatalf("HTTP calls = %d, want 1 (success should be cached)", got)
	}
}

func TestPreflight_RetriesAfterFailure(t *testing.T) {
	f := newFakeGL(t)
	var calls int
	f.on("GET", "/user", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, `{"message":"server exploded"}`, http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"id":1,"username":"octocat"}`))
	})
	tr := newTrackerForTest(t, f)
	if err := tr.Preflight(ctx()); err == nil {
		t.Fatalf("first Preflight expected to fail")
	}
	if err := tr.Preflight(ctx()); err != nil {
		t.Fatalf("second Preflight: %v", err)
	}
	if got := len(f.calls()); got != 2 {
		t.Fatalf("HTTP calls = %d, want 2 (first fail not cached)", got)
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestList_HappyPath(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		vars := gqlVars(t, readBody(r))
		if vars["fullPath"] != "o/r" {
			t.Errorf("fullPath = %v, want o/r", vars["fullPath"])
		}
		// Default state is "all" — the variable should not be set (omitted).
		if _, ok := vars["state"]; ok {
			t.Errorf("state should be omitted for ListAll, got %v", vars["state"])
		}
		wi1 := workItemJSON(1, "first", "b1", "opened",
			"https://gitlab.com/o/r/-/issues/1", []string{"bug"}, nil)
		wi2 := workItemJSON(2, "second", "b2", "closed",
			"https://gitlab.com/o/r/-/issues/2", nil, []string{"alice"})
		_, _ = w.Write([]byte(`{"data":` + workItemsListJSON([]string{wi1, wi2}, "", false) + `}`))
	})
	tr := newTrackerForTest(t, f)
	issues, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "o/r"}, domain.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("len = %d, want 2", len(issues))
	}
	if issues[0].ID.Native != "o/r#1" || issues[0].State != domain.IssueOpen || issues[0].Title != "first" {
		t.Fatalf("issues[0] = %#v", issues[0])
	}
	if issues[0].Labels == nil || len(issues[0].Labels) != 1 || issues[0].Labels[0] != "bug" {
		t.Fatalf("issues[0].Labels = %#v, want [bug]", issues[0].Labels)
	}
	if issues[1].ID.Native != "o/r#2" || issues[1].State != domain.IssueDone || len(issues[1].Assignees) != 1 || issues[1].Assignees[0] != "alice" {
		t.Fatalf("issues[1] = %#v", issues[1])
	}
}

func TestList_FilterVariables(t *testing.T) {
	cases := []struct {
		name   string
		filter domain.ListFilter
		check  func(t *testing.T, vars map[string]any)
	}{
		{
			name:   "open + labels + assignee",
			filter: domain.ListFilter{State: domain.ListOpen, Labels: []string{"bug", "help wanted"}, Assignee: "alice", Limit: 50},
			check: func(t *testing.T, vars map[string]any) {
				if vars["state"] != "opened" {
					t.Errorf("state = %v, want opened", vars["state"])
				}
				if vars["assigneeUsernames"] == nil {
					t.Fatalf("assigneeUsernames not set")
				}
				if vars["labelName"] == nil {
					t.Fatalf("labelName not set")
				}
			},
		},
		{
			name:   "closed only",
			filter: domain.ListFilter{State: domain.ListClosed},
			check: func(t *testing.T, vars map[string]any) {
				if vars["state"] != "closed" {
					t.Errorf("state = %v, want closed", vars["state"])
				}
			},
		},
		{
			name:   "all state — state var omitted",
			filter: domain.ListFilter{},
			check: func(t *testing.T, vars map[string]any) {
				if _, ok := vars["state"]; ok {
					t.Errorf("state should be omitted for ListAll, got %v", vars["state"])
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGL(t)
			f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
				vars := gqlVars(t, readBody(r))
				tc.check(t, vars)
				_, _ = w.Write([]byte(`{"data":` + workItemsListJSON(nil, "", false) + `}`))
			})
			tr := newTrackerForTest(t, f)
			if _, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "o/r"}, tc.filter); err != nil {
				t.Fatalf("List: %v", err)
			}
		})
	}
}

func TestList_PaginatesAcrossCursors(t *testing.T) {
	f := newFakeGL(t)
	var callCount int
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		callCount++
		vars := gqlVars(t, readBody(r))
		switch callCount {
		case 1:
			if _, ok := vars["after"]; ok {
				t.Errorf("first page should not have after, got %v", vars["after"])
			}
			wi1 := workItemJSON(1, "first", "", "opened", "https://gitlab.com/o/r/-/issues/1", nil, nil)
			_, _ = w.Write([]byte(`{"data":` + workItemsListJSON([]string{wi1}, "cursor-abc", true) + `}`))
		case 2:
			if vars["after"] != "cursor-abc" {
				t.Errorf("second page after = %v, want cursor-abc", vars["after"])
			}
			wi2 := workItemJSON(2, "second", "", "opened", "https://gitlab.com/o/r/-/issues/2", nil, nil)
			_, _ = w.Write([]byte(`{"data":` + workItemsListJSON([]string{wi2}, "", false) + `}`))
		default:
			t.Fatalf("unexpected call #%d", callCount)
		}
	})
	tr := newTrackerForTest(t, f)

	issues, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "o/r"}, domain.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) != 2 || issues[0].ID.Native != "o/r#1" || issues[1].ID.Native != "o/r#2" {
		t.Fatalf("issues = %#v, want both pages in order", issues)
	}
	if callCount != 2 {
		t.Fatalf("callCount = %d, want 2", callCount)
	}
}

func TestList_RespectsLimit(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		wi1 := workItemJSON(1, "first", "", "opened", "https://gitlab.com/o/r/-/issues/1", nil, nil)
		wi2 := workItemJSON(2, "second", "", "opened", "https://gitlab.com/o/r/-/issues/2", nil, nil)
		wi3 := workItemJSON(3, "third", "", "opened", "https://gitlab.com/o/r/-/issues/3", nil, nil)
		_, _ = w.Write([]byte(`{"data":` + workItemsListJSON([]string{wi1, wi2, wi3}, "cursor-next", true) + `}`))
	})
	tr := newTrackerForTest(t, f)
	issues, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "o/r"}, domain.ListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("len = %d, want 2 (limit)", len(issues))
	}
	if issues[0].ID.Native != "o/r#1" || issues[1].ID.Native != "o/r#2" {
		t.Fatalf("issues = %#v, want first 2", issues)
	}
	// Should not fetch a second page since limit was hit.
	if calls := f.calls(); len(calls) != 1 {
		t.Fatalf("HTTP calls = %d, want 1 (limit should stop early)", len(calls))
	}
}

func TestList_NestedGroup(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		vars := gqlVars(t, readBody(r))
		if vars["fullPath"] != "group/sub/proj" {
			t.Errorf("fullPath = %v, want group/sub/proj", vars["fullPath"])
		}
		wi := workItemJSON(1, "nested", "", "opened", "https://gitlab.com/group/sub/proj/-/issues/1", nil, nil)
		_, _ = w.Write([]byte(`{"data":` + workItemsListJSON([]string{wi}, "", false) + `}`))
	})
	tr := newTrackerForTest(t, f)
	issues, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "group/sub/proj"}, domain.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) != 1 || issues[0].ID.Native != "group/sub/proj#1" {
		t.Fatalf("issues = %#v", issues)
	}
}

func TestList_RejectsWrongProvider(t *testing.T) {
	f := newFakeGL(t)
	tr := newTrackerForTest(t, f)
	_, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProvider("github"), Native: "o/r"}, domain.ListFilter{})
	if !errors.Is(err, ErrWrongProvider) {
		t.Fatalf("err = %v, want ErrWrongProvider", err)
	}
	if calls := f.calls(); len(calls) != 0 {
		t.Fatalf("unexpected HTTP calls: %#v", calls)
	}
}

func TestList_RejectsBadRepo(t *testing.T) {
	cases := []string{
		"",            // empty
		"noseparator", // missing /
		" owner/repo", // leading whitespace
		"owner/repo ", // trailing whitespace
		"own er/repo", // embedded space
		"owner/re#po", // embedded #
	}
	for _, native := range cases {
		t.Run(native, func(t *testing.T) {
			f := newFakeGL(t)
			tr := newTrackerForTest(t, f)
			_, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: native}, domain.ListFilter{})
			if !errors.Is(err, ErrBadID) {
				t.Fatalf("native=%q: err = %v, want ErrBadID", native, err)
			}
		})
	}
}

func TestList_NotFound_GraphQLErrors(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"Project not found","extensions":{"code":"NOT_FOUND"}}]}`))
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "o/r"}, domain.ListFilter{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestList_NotFound_NullProject(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"project":null}}`))
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "o/r"}, domain.ListFilter{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestList_AuthFailed(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "o/r"}, domain.ListFilter{})
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("err = %v, want ErrAuthFailed", err)
	}
}

func TestList_RateLimited(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Too many requests"}`, http.StatusTooManyRequests)
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "o/r"}, domain.ListFilter{})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func TestList_EmptyResults(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":` + workItemsListJSON(nil, "", false) + `}`))
	})
	tr := newTrackerForTest(t, f)
	issues, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "o/r"}, domain.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("len = %d, want 0", len(issues))
	}
}

func TestList_NullDescription(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		// Work item with null description in the GraphQL response
		_, _ = w.Write([]byte(`{"data":{"project":{"workItems":{"pageInfo":{"endCursor":"","hasNextPage":false},"nodes":[{"iid":"1","title":"no desc","description":"","state":"opened","webUrl":"https://gitlab.com/o/r/-/issues/1","widgets":[]}]}}}}`))
	})
	tr := newTrackerForTest(t, f)
	issues, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "o/r"}, domain.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("len = %d, want 1", len(issues))
	}
	if issues[0].Body != "" {
		t.Fatalf("Body = %q, want empty string", issues[0].Body)
	}
}

func TestList_BothLabelsAndAssignees(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		wi := workItemJSON(1, "full", "body", "opened",
			"https://gitlab.com/o/r/-/issues/1",
			[]string{"bug", "critical"}, []string{"alice", "bob"})
		_, _ = w.Write([]byte(`{"data":` + workItemsListJSON([]string{wi}, "", false) + `}`))
	})
	tr := newTrackerForTest(t, f)
	issues, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "o/r"}, domain.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("len = %d, want 1", len(issues))
	}
	if len(issues[0].Labels) != 2 || issues[0].Labels[0] != "bug" || issues[0].Labels[1] != "critical" {
		t.Fatalf("Labels = %#v, want [bug critical]", issues[0].Labels)
	}
	if len(issues[0].Assignees) != 2 || issues[0].Assignees[0] != "alice" || issues[0].Assignees[1] != "bob" {
		t.Fatalf("Assignees = %#v, want [alice bob]", issues[0].Assignees)
	}
}

func TestList_FirstStaysAtPageSizeWhenLimitExceeds(t *testing.T) {
	f := newFakeGL(t)
	f.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		vars := gqlVars(t, readBody(r))
		first := vars["first"]
		if first != float64(listPageSize) {
			t.Errorf("first = %v, want %d (listPageSize) even when Limit > 100", first, listPageSize)
		}
		_, _ = w.Write([]byte(`{"data":` + workItemsListJSON(nil, "", false) + `}`))
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "o/r"}, domain.ListFilter{Limit: 9999})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Domain validation
// ---------------------------------------------------------------------------

func TestTrackerIntakeConfig_ValidateAcceptsGitLab(t *testing.T) {
	c := domain.TrackerIntakeConfig{
		Enabled:  true,
		Provider: domain.TrackerProviderGitLab,
		Repo:     "group/project",
		Assignee: "alice",
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestTrackerIntakeConfig_ValidateStillAcceptsGitHub(t *testing.T) {
	c := domain.TrackerIntakeConfig{
		Enabled:  true,
		Provider: domain.TrackerProviderGitHub,
		Repo:     "owner/repo",
		Assignee: "alice",
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestTrackerIntakeConfig_ValidateRejectsUnknownProvider(t *testing.T) {
	c := domain.TrackerIntakeConfig{
		Enabled:  true,
		Provider: domain.TrackerProvider("jira"),
		Repo:     "owner/repo",
		Assignee: "alice",
	}
	if err := c.Validate(); err == nil {
		t.Fatalf("Validate: expected error for unknown provider")
	}
}

func TestTrackerIntakeConfig_WithDefaultsLeavesProviderEmpty(t *testing.T) {
	c := domain.TrackerIntakeConfig{Enabled: true, Assignee: "alice"}
	c = c.WithDefaults()
	if c.Provider != "" {
		t.Fatalf("WithDefaults: Provider = %q, want empty (inferred at use time)", c.Provider)
	}
}

// ---------------------------------------------------------------------------
// Host-aware tracker
// ---------------------------------------------------------------------------

// newHostAwareTrackerForTest constructs a host-aware tracker with a default
// (gitlab.com) fake server and an optional self-managed host fake server.
// The self-managed host's base URL is wired via AllowedHosts + HostTokens.
func newHostAwareTrackerForTest(t *testing.T, defaultSrv *fakeGL, hostEntries map[string]struct {
	server *fakeGL
	token  string
}) *Tracker {
	t.Helper()
	opts := Options{
		BaseURL:    defaultSrv.server.URL,
		Token:      scmgitlab.StaticTokenSource("tkn-default"),
		HTTPClient: defaultSrv.server.Client(),
	}
	for host, he := range hostEntries {
		opts.AllowedHosts = append(opts.AllowedHosts, host)
		if he.token != "" {
			if opts.HostTokens == nil {
				opts.HostTokens = make(map[string]scmgitlab.TokenSource)
			}
			opts.HostTokens[strings.ToLower(host)] = scmgitlab.StaticTokenSource(he.token)
		}
	}
	tr, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Override the per-host base URL to point at the fake server.
	for host, he := range hostEntries {
		lh := strings.ToLower(strings.TrimSpace(host))
		entry := tr.hosts[lh]
		entry.baseURL = he.server.server.URL
		tr.hosts[lh] = entry
	}
	return tr
}

func TestGet_SelfManagedHost_RoutesToCorrectBaseURLAndToken(t *testing.T) {
	defaultSrv := newFakeGL(t)
	selfManagedSrv := newFakeGL(t)

	tr := newHostAwareTrackerForTest(t, defaultSrv, map[string]struct {
		server *fakeGL
		token  string
	}{
		"gitlab.internal": {server: selfManagedSrv, token: "tkn-internal"},
	})

	// Register handler on the self-managed server only.
	selfManagedSrv.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tkn-internal" {
			t.Errorf("Authorization = %q, want Bearer tkn-internal", got)
		}
		wi := workItemJSON(42, "Self-managed issue", "d", "opened",
			"https://gitlab.internal/group/project/-/issues/42", nil, nil)
		_, _ = w.Write([]byte(`{"data":{"workItem":` + wi + `}}`))
	})

	issue, err := tr.Get(ctx(), domain.TrackerID{
		Provider: domain.TrackerProviderGitLab,
		Host:     "gitlab.internal",
		Native:   "group/project#42",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if issue.Title != "Self-managed issue" {
		t.Fatalf("Title = %q, want %q", issue.Title, "Self-managed issue")
	}
	if issue.ID.Host != "gitlab.internal" {
		t.Fatalf("issue.ID.Host = %q, want %q", issue.ID.Host, "gitlab.internal")
	}
	// Ensure the default server was NOT hit.
	if calls := defaultSrv.calls(); len(calls) != 0 {
		t.Fatalf("default server received unexpected calls: %#v", calls)
	}
}

func TestGet_DefaultHost_BackwardCompat(t *testing.T) {
	defaultSrv := newFakeGL(t)
	tr := newHostAwareTrackerForTest(t, defaultSrv, nil)

	defaultSrv.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tkn-default" {
			t.Errorf("Authorization = %q, want Bearer tkn-default", got)
		}
		wi := workItemJSON(1, "default", "", "opened", "https://gitlab.com/o/r/-/issues/1", nil, nil)
		_, _ = w.Write([]byte(`{"data":{"workItem":` + wi + `}}`))
	})

	// Host: "" routes to the default (gitlab.com) server.
	issue, err := tr.Get(ctx(), domain.TrackerID{
		Provider: domain.TrackerProviderGitLab,
		Native:   "o/r#1",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if issue.ID.Host != "" {
		t.Fatalf("issue.ID.Host = %q, want \"\"", issue.ID.Host)
	}
}

func TestGet_GitLabComExplicit_RoutesToDefault(t *testing.T) {
	defaultSrv := newFakeGL(t)
	tr := newHostAwareTrackerForTest(t, defaultSrv, nil)

	defaultSrv.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		wi := workItemJSON(1, "t", "", "opened", "https://gitlab.com/o/r/-/issues/1", nil, nil)
		_, _ = w.Write([]byte(`{"data":{"workItem":` + wi + `}}`))
	})

	// Host: "gitlab.com" should route to the default, same as Host: "".
	issue, err := tr.Get(ctx(), domain.TrackerID{
		Provider: domain.TrackerProviderGitLab,
		Host:     "gitlab.com",
		Native:   "o/r#1",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if issue.Title != "t" {
		t.Fatalf("Title = %q, want %q", issue.Title, "t")
	}
}

func TestGet_UnconfiguredHost_Rejected(t *testing.T) {
	defaultSrv := newFakeGL(t)
	tr := newHostAwareTrackerForTest(t, defaultSrv, nil)

	_, err := tr.Get(ctx(), domain.TrackerID{
		Provider: domain.TrackerProviderGitLab,
		Host:     "gitlab.evil.example",
		Native:   "o/r#1",
	})
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("err = %v, want ErrHostNotAllowed", err)
	}
	// No HTTP call should have been made to any server.
	if calls := defaultSrv.calls(); len(calls) != 0 {
		t.Fatalf("unexpected HTTP calls: %#v", calls)
	}
}

func TestList_SelfManagedHost_RoutesCorrectly(t *testing.T) {
	defaultSrv := newFakeGL(t)
	selfManagedSrv := newFakeGL(t)

	tr := newHostAwareTrackerForTest(t, defaultSrv, map[string]struct {
		server *fakeGL
		token  string
	}{
		"gitlab.internal": {server: selfManagedSrv, token: "tkn-internal"},
	})

	selfManagedSrv.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tkn-internal" {
			t.Errorf("Authorization = %q, want Bearer tkn-internal", got)
		}
		wi := workItemJSON(1, "sm", "d", "opened", "https://gitlab.internal/group/proj/-/issues/1", nil, nil)
		_, _ = w.Write([]byte(`{"data":` + workItemsListJSON([]string{wi}, "", false) + `}`))
	})

	issues, err := tr.List(ctx(), domain.TrackerRepo{
		Provider: domain.TrackerProviderGitLab,
		Host:     "gitlab.internal",
		Native:   "group/proj",
	}, domain.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("len = %d, want 1", len(issues))
	}
	if issues[0].ID.Host != "gitlab.internal" {
		t.Fatalf("issues[0].ID.Host = %q, want %q", issues[0].ID.Host, "gitlab.internal")
	}
	if calls := defaultSrv.calls(); len(calls) != 0 {
		t.Fatalf("default server received unexpected calls: %#v", calls)
	}
}

func TestList_UnconfiguredHost_Rejected(t *testing.T) {
	defaultSrv := newFakeGL(t)
	tr := newHostAwareTrackerForTest(t, defaultSrv, nil)

	_, err := tr.List(ctx(), domain.TrackerRepo{
		Provider: domain.TrackerProviderGitLab,
		Host:     "gitlab.evil.example",
		Native:   "o/r",
	}, domain.ListFilter{})
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("err = %v, want ErrHostNotAllowed", err)
	}
	if calls := defaultSrv.calls(); len(calls) != 0 {
		t.Fatalf("unexpected HTTP calls: %#v", calls)
	}
}

func TestGet_SelfManagedHost_FallsBackToDefaultToken(t *testing.T) {
	defaultSrv := newFakeGL(t)
	selfManagedSrv := newFakeGL(t)

	// Register the self-managed host in AllowedHosts but do NOT provide a
	// HostTokens entry — the default token should be used.
	tr := newHostAwareTrackerForTest(t, defaultSrv, map[string]struct {
		server *fakeGL
		token  string
	}{
		"gitlab.internal": {server: selfManagedSrv},
	})

	selfManagedSrv.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		// Should use the default token since no per-host token was configured.
		if got := r.Header.Get("Authorization"); got != "Bearer tkn-default" {
			t.Errorf("Authorization = %q, want Bearer tkn-default", got)
		}
		wi := workItemJSON(1, "t", "", "opened", "https://gitlab.internal/o/r/-/issues/1", nil, nil)
		_, _ = w.Write([]byte(`{"data":{"workItem":` + wi + `}}`))
	})

	_, err := tr.Get(ctx(), domain.TrackerID{
		Provider: domain.TrackerProviderGitLab,
		Host:     "gitlab.internal",
		Native:   "o/r#1",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
}

func TestGet_HostCaseInsensitive(t *testing.T) {
	defaultSrv := newFakeGL(t)
	selfManagedSrv := newFakeGL(t)

	tr := newHostAwareTrackerForTest(t, defaultSrv, map[string]struct {
		server *fakeGL
		token  string
	}{
		"gitlab.internal": {server: selfManagedSrv, token: "tkn-internal"},
	})

	selfManagedSrv.on("POST", "/graphql", func(w http.ResponseWriter, r *http.Request) {
		wi := workItemJSON(1, "t", "", "opened", "https://gitlab.internal/o/r/-/issues/1", nil, nil)
		_, _ = w.Write([]byte(`{"data":{"workItem":` + wi + `}}`))
	})

	// Upper-case host should match the lower-cased allowlist entry.
	_, err := tr.Get(ctx(), domain.TrackerID{
		Provider: domain.TrackerProviderGitLab,
		Host:     "GitLab.Internal",
		Native:   "o/r#1",
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
}

// readBody is a helper to read and restore the request body for inspection.
func readBody(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(strings.NewReader(string(b)))
	return string(b)
}
