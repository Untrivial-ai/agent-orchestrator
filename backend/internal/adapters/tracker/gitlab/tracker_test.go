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
	f.on("GET", "/projects/octocat/hello-world/issues/42", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tkn-test" {
			t.Errorf("Authorization = %q, want Bearer tkn-test", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"iid": 42,
			"title": "Found a bug",
			"description": "It does not work",
			"state": "opened",
			"web_url": "https://gitlab.com/octocat/hello-world/-/issues/42",
			"labels": ["bug","critical"],
			"assignees": [{"username":"alice"},{"username":"bob"}]
		}`))
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
			payload := map[string]any{
				"iid":     1,
				"title":   "t",
				"state":   tc.glState,
				"web_url": "https://gitlab.com/o/r/-/issues/1",
			}
			b, _ := json.Marshal(payload)
			f.on("GET", "/projects/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(b)
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
	f.on("GET", "/projects/group/sub/project/issues/7", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"iid":7,"title":"nested","description":"d","state":"opened","web_url":"https://gitlab.com/group/sub/project/-/issues/7"}`))
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
	f.on("GET", "/projects/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"404 Not Found"}`, http.StatusNotFound)
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
	f.on("GET", "/projects/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
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
	f.on("GET", "/projects/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
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
	f.on("GET", "/projects/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
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
	f.on("GET", "/projects/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"iid":1,"title":"t","description":"","state":"opened","web_url":"https://gitlab.com/o/r/-/issues/1"}`))
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
	f.on("GET", "/projects/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"iid":1,"title":"t","description":"","state":"opened","web_url":"https://gitlab.com/o/r/-/issues/1"}`))
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
	f.on("GET", "/projects/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("state"); got != "all" {
			t.Errorf("state = %q, want all (default)", got)
		}
		if got := q.Get("per_page"); got != "100" {
			t.Errorf("per_page = %q, want 100 (default)", got)
		}
		_, _ = w.Write([]byte(`[
			{"iid":1,"title":"first","description":"b1","state":"opened","web_url":"https://gitlab.com/o/r/-/issues/1","labels":["bug"]},
			{"iid":2,"title":"second","description":"b2","state":"closed","web_url":"https://gitlab.com/o/r/-/issues/2","assignees":[{"username":"alice"}]}
		]`))
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
	if issues[1].ID.Native != "o/r#2" || issues[1].State != domain.IssueDone || len(issues[1].Assignees) != 1 || issues[1].Assignees[0] != "alice" {
		t.Fatalf("issues[1] = %#v", issues[1])
	}
}

func TestList_QueryEncoding(t *testing.T) {
	cases := []struct {
		name   string
		filter domain.ListFilter
		wantQ  map[string]string
	}{
		{
			name:   "open + labels + assignee + limit",
			filter: domain.ListFilter{State: domain.ListOpen, Labels: []string{"bug", "help wanted"}, Assignee: "alice", Limit: 50},
			wantQ:  map[string]string{"state": "opened", "labels": "bug,help wanted", "assignee_username": "alice", "per_page": "100"},
		},
		{
			name:   "closed only",
			filter: domain.ListFilter{State: domain.ListClosed},
			wantQ:  map[string]string{"state": "closed", "per_page": "100"},
		},
		{
			name:   "large total limit still uses max page size",
			filter: domain.ListFilter{Limit: 9999},
			wantQ:  map[string]string{"state": "all", "per_page": "100"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGL(t)
			f.on("GET", "/projects/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
				got := r.URL.Query()
				for k, want := range tc.wantQ {
					if g := got.Get(k); g != want {
						t.Errorf("query[%q] = %q, want %q", k, g, want)
					}
				}
				_, _ = w.Write([]byte(`[]`))
			})
			tr := newTrackerForTest(t, f)
			if _, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "o/r"}, tc.filter); err != nil {
				t.Fatalf("List: %v", err)
			}
		})
	}
}

func TestList_PaginatesAcrossLinkNext(t *testing.T) {
	f := newFakeGL(t)
	f.on("GET", "/projects/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", `<`+f.server.URL+`/projects/o/r/issues?state=all&per_page=100&page=2>; rel="next"`)
			_, _ = w.Write([]byte(`[{"iid":1,"title":"first","state":"opened","web_url":"https://gitlab.com/o/r/-/issues/1"}]`))
		case "2":
			_, _ = w.Write([]byte(`[{"iid":2,"title":"second","state":"opened","web_url":"https://gitlab.com/o/r/-/issues/2"}]`))
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("page"))
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
}

func TestList_RespectsLimit(t *testing.T) {
	f := newFakeGL(t)
	f.on("GET", "/projects/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<`+f.server.URL+`/projects/o/r/issues?state=all&per_page=100&page=2>; rel="next"`)
		_, _ = w.Write([]byte(`[
			{"iid":1,"title":"first","state":"opened","web_url":"https://gitlab.com/o/r/-/issues/1"},
			{"iid":2,"title":"second","state":"opened","web_url":"https://gitlab.com/o/r/-/issues/2"},
			{"iid":3,"title":"third","state":"opened","web_url":"https://gitlab.com/o/r/-/issues/3"}
		]`))
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
}

func TestList_NestedGroup(t *testing.T) {
	f := newFakeGL(t)
	f.on("GET", "/projects/group/sub/proj/issues", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"iid":1,"title":"nested","state":"opened","web_url":"https://gitlab.com/group/sub/proj/-/issues/1"}]`))
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

func TestList_NotFound(t *testing.T) {
	f := newFakeGL(t)
	f.on("GET", "/projects/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"404 Project Not Found"}`, http.StatusNotFound)
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "o/r"}, domain.ListFilter{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// ParseLinkNext
// ---------------------------------------------------------------------------

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

func TestTrackerIntakeConfig_WithDefaultsStillGitHub(t *testing.T) {
	c := domain.TrackerIntakeConfig{Enabled: true, Assignee: "alice"}
	c = c.WithDefaults()
	if c.Provider != domain.TrackerProviderGitHub {
		t.Fatalf("WithDefaults: Provider = %q, want %q", c.Provider, domain.TrackerProviderGitHub)
	}
}

// ---------------------------------------------------------------------------
// Host-aware tracker (ticket 09)
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
	// The tracker derives self-managed base URLs as https://<host>/api/v4,
	// but for testing we need to point at the httptest server URL.
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
	selfManagedSrv.on("GET", "/projects/group/project/issues/42", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tkn-internal" {
			t.Errorf("Authorization = %q, want Bearer tkn-internal", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"iid": 42,
			"title": "Self-managed issue",
			"description": "d",
			"state": "opened",
			"web_url": "https://gitlab.internal/group/project/-/issues/42"
		}`))
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

	defaultSrv.on("GET", "/projects/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tkn-default" {
			t.Errorf("Authorization = %q, want Bearer tkn-default", got)
		}
		_, _ = w.Write([]byte(`{"iid":1,"title":"default","description":"","state":"opened","web_url":"https://gitlab.com/o/r/-/issues/1"}`))
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

	defaultSrv.on("GET", "/projects/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"iid":1,"title":"t","description":"","state":"opened","web_url":"https://gitlab.com/o/r/-/issues/1"}`))
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

	selfManagedSrv.on("GET", "/projects/group/proj/issues", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tkn-internal" {
			t.Errorf("Authorization = %q, want Bearer tkn-internal", got)
		}
		_, _ = w.Write([]byte(`[{"iid":1,"title":"sm","description":"d","state":"opened","web_url":"https://gitlab.internal/group/proj/-/issues/1"}]`))
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

	selfManagedSrv.on("GET", "/projects/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		// Should use the default token since no per-host token was configured.
		if got := r.Header.Get("Authorization"); got != "Bearer tkn-default" {
			t.Errorf("Authorization = %q, want Bearer tkn-default", got)
		}
		_, _ = w.Write([]byte(`{"iid":1,"title":"t","description":"","state":"opened","web_url":"https://gitlab.internal/o/r/-/issues/1"}`))
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

	selfManagedSrv.on("GET", "/projects/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"iid":1,"title":"t","description":"","state":"opened","web_url":"https://gitlab.internal/o/r/-/issues/1"}`))
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
