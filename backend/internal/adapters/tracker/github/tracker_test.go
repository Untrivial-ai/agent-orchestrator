package github

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

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// recordedReq captures one inbound HTTP request so tests can assert against
// the exact GitHub API surface the adapter touched.
type recordedReq struct {
	Method      string
	Path        string
	Body        string
	IfNoneMatch string
}

// fakeGH is a programmable httptest.Server that matches requests by
// "METHOD path" and records every call. Unmatched requests fail the test —
// that is the point of TDD here, so an accidental extra call is loud.
type fakeGH struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	requests []recordedReq
	handlers map[string]http.HandlerFunc
}

func newFakeGH(t *testing.T) *fakeGH {
	t.Helper()
	f := &fakeGH{t: t, handlers: map[string]http.HandlerFunc{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGH) on(method, path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method+" "+path] = h
}

func (f *fakeGH) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	key := r.Method + " " + r.URL.Path
	f.mu.Lock()
	f.requests = append(f.requests, recordedReq{Method: r.Method, Path: r.URL.Path, Body: string(body), IfNoneMatch: r.Header.Get("If-None-Match")})
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

func (f *fakeGH) calls() []recordedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedReq, len(f.requests))
	copy(out, f.requests)
	return out
}

// newTrackerForTest constructs an adapter pointed at the fake server with a
// static dev token. Production code uses EnvTokenSource; tests skip that to
// keep the surface tiny.
func newTrackerForTest(t *testing.T, f *fakeGH) *Tracker {
	t.Helper()
	tr, err := New(Options{
		BaseURL:    f.server.URL,
		Token:      StaticTokenSource("tkn-test"),
		HTTPClient: f.server.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tr
}

func ctx() context.Context { return context.Background() }

func TestNewRejectsMissingToken(t *testing.T) {
	if _, err := New(Options{Token: StaticTokenSource("")}); !errors.Is(err, ErrNoToken) {
		t.Fatalf("New with empty token = %v, want ErrNoToken", err)
	}
	if _, err := New(Options{}); !errors.Is(err, ErrNoToken) {
		t.Fatalf("New with no source = %v, want ErrNoToken", err)
	}
}

func TestParseID(t *testing.T) {
	cases := []struct {
		name      string
		native    string
		wantOwner string
		wantRepo  string
		wantNum   int
		wantErr   bool
	}{
		{"happy", "octocat/hello-world#42", "octocat", "hello-world", 42, false},
		{"missing hash", "octocat/hello-world", "", "", 0, true},
		{"missing slash", "octocat#42", "", "", 0, true},
		{"empty owner", "/repo#1", "", "", 0, true},
		{"empty repo", "owner/#1", "", "", 0, true},
		{"embedded slash", "o/r/x#1", "", "", 0, true},
		{"space", "o/r space#1", "", "", 0, true},
		{"non-numeric", "o/r#abc", "", "", 0, true},
		{"zero", "o/r#0", "", "", 0, true},
		{"negative", "o/r#-1", "", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, num, err := parseGitHubID(tc.native)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %s/%s#%d", owner, repo, num)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if owner != tc.wantOwner || repo != tc.wantRepo || num != tc.wantNum {
				t.Fatalf("got %s/%s#%d, want %s/%s#%d", owner, repo, num, tc.wantOwner, tc.wantRepo, tc.wantNum)
			}
		})
	}
}

func TestGet_HappyPath(t *testing.T) {
	f := newFakeGH(t)
	f.on("GET", "/repos/octocat/hello-world/issues/42", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tkn-test" {
			t.Errorf("Authorization = %q, want Bearer tkn-test", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"number": 42,
			"title": "Found a bug",
			"body": "It does not work",
			"state": "open",
			"html_url": "https://github.com/octocat/hello-world/issues/42",
			"labels": [{"name":"bug"},{"name":"in-progress"}],
			"assignees": [{"login":"alice"},{"login":"bob"}]
		}`))
	})
	tr := newTrackerForTest(t, f)

	issue, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "octocat/hello-world#42"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := domain.Issue{
		ID:        domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "octocat/hello-world#42"},
		Title:     "Found a bug",
		Body:      "It does not work",
		State:     domain.IssueInProgress, // the "in-progress" label wins over plain "open"
		URL:       "https://github.com/octocat/hello-world/issues/42",
		Labels:    []string{"bug", "in-progress"},
		Assignees: []string{"alice", "bob"},
	}
	if !reflect.DeepEqual(issue, want) {
		t.Fatalf("issue = %#v\nwant %#v", issue, want)
	}
}

func TestGet_StateMappingFromGitHubFields(t *testing.T) {
	cases := []struct {
		name      string
		ghState   string
		ghReason  string
		labels    []string
		wantState domain.NormalizedIssueState
	}{
		{"plain open", "open", "", nil, domain.IssueOpen},
		{"open with in-progress label", "open", "", []string{"In-Progress"}, domain.IssueInProgress},
		{"open with in-review label", "open", "", []string{"in-review"}, domain.IssueInReview},
		{"review wins over progress when both present", "open", "", []string{"in-progress", "in-review"}, domain.IssueInReview},
		{"closed completed", "closed", "completed", nil, domain.IssueDone},
		{"closed not_planned", "closed", "not_planned", nil, domain.IssueCancelled},
		{"closed unknown reason maps to done", "closed", "", nil, domain.IssueDone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGH(t)
			payload := map[string]any{
				"number":   1,
				"title":    "t",
				"body":     "",
				"state":    tc.ghState,
				"html_url": "https://github.com/o/r/issues/1",
			}
			if tc.ghReason != "" {
				payload["state_reason"] = tc.ghReason
			}
			if tc.labels != nil {
				ls := make([]map[string]string, len(tc.labels))
				for i, n := range tc.labels {
					ls[i] = map[string]string{"name": n}
				}
				payload["labels"] = ls
			}
			b, _ := json.Marshal(payload)
			f.on("GET", "/repos/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(b)
			})
			tr := newTrackerForTest(t, f)
			issue, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"})
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if issue.State != tc.wantState {
				t.Fatalf("state = %q, want %q", issue.State, tc.wantState)
			}
		})
	}
}

func TestGet_NotFound(t *testing.T) {
	f := newFakeGH(t)
	f.on("GET", "/repos/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGet_RateLimited(t *testing.T) {
	f := newFakeGH(t)
	reset := time.Now().Add(2 * time.Minute).Unix()
	f.on("GET", "/repos/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"})
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
}

// TestGet_SecondaryRateLimit covers the GitHub "abuse detection"
// response — it lacks X-RateLimit-Remaining but sets Retry-After, and the
// body mentions the limit. The classifier must still surface this as
// ErrRateLimited rather than mis-categorizing it as auth failure.
func TestGet_SecondaryRateLimit(t *testing.T) {
	f := newFakeGH(t)
	f.on("GET", "/repos/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, `{"message":"You have exceeded a secondary rate limit"}`, http.StatusForbidden)
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
	if rle.RetryAfter != 60*time.Second {
		t.Fatalf("RetryAfter = %v, want 60s", rle.RetryAfter)
	}
}

func TestGet_RejectsWrongProvider(t *testing.T) {
	f := newFakeGH(t)
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProvider("gitlab"), Native: "g/p#1"})
	if !errors.Is(err, ErrWrongProvider) {
		t.Fatalf("err = %v, want ErrWrongProvider", err)
	}
}

func TestGet_RejectsEmptyProvider(t *testing.T) {
	f := newFakeGH(t)
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Native: "o/r#1"})
	if !errors.Is(err, ErrWrongProvider) {
		t.Fatalf("err = %v, want ErrWrongProvider", err)
	}
}

// TestGet_CanonicalizesProviderOnOutput pins the contract that returned
// Issues always carry domain.TrackerProviderGitHub, so callers can re-route
// without inspecting which adapter they originally talked to.
func TestGet_CanonicalizesProviderOnOutput(t *testing.T) {
	f := newFakeGH(t)
	f.on("GET", "/repos/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"number":1,"title":"t","body":"","state":"open","html_url":"https://github.com/o/r/issues/1"}`))
	})
	tr := newTrackerForTest(t, f)
	issue, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if issue.ID.Provider != domain.TrackerProviderGitHub {
		t.Fatalf("issue.ID.Provider = %q, want %q", issue.ID.Provider, domain.TrackerProviderGitHub)
	}
	if issue.ID.Native != "o/r#1" {
		t.Fatalf("issue.ID.Native = %q, want o/r#1", issue.ID.Native)
	}
}

// TestGet_AuthFailed locks in that a 401 (and 403 without rate-limit
// signals) maps to the typed ErrAuthFailed, so callers — especially
// Preflight — can distinguish bad-token from other failures.
func TestGet_AuthFailed(t *testing.T) {
	f := newFakeGH(t)
	f.on("GET", "/repos/o/r/issues/1", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	})
	tr := newTrackerForTest(t, f)
	_, err := tr.Get(ctx(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "o/r#1"})
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("err = %v, want ErrAuthFailed", err)
	}
}

// ---------------------------------------------------------------------------
// Preflight
// ---------------------------------------------------------------------------

func TestPreflight_HappyPath(t *testing.T) {
	f := newFakeGH(t)
	f.on("GET", "/user", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tkn-test" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{"login":"octocat","id":1}`))
	})
	tr := newTrackerForTest(t, f)
	if err := tr.Preflight(ctx()); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
}

func TestPreflight_InvalidToken(t *testing.T) {
	f := newFakeGH(t)
	f.on("GET", "/user", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	})
	tr := newTrackerForTest(t, f)
	err := tr.Preflight(ctx())
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("err = %v, want ErrAuthFailed", err)
	}
}

// TestPreflight_CachesSuccess pins that a successful check is cached so the
// daemon doesn't burn a GET /user on every component start that wants to
// confirm tracker auth.
func TestPreflight_CachesSuccess(t *testing.T) {
	f := newFakeGH(t)
	f.on("GET", "/user", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"login":"octocat","id":1}`))
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

// TestPreflight_RetriesAfterFailure pins the recovery property: failures
// must NOT be cached, otherwise a transient network glitch at startup would
// permanently brick the tracker for the lifetime of the daemon.
func TestPreflight_RetriesAfterFailure(t *testing.T) {
	f := newFakeGH(t)
	var calls int
	f.on("GET", "/user", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, `{"message":"server exploded"}`, http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"login":"octocat","id":1}`))
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

func TestList_HappyPathAndDefaults(t *testing.T) {
	f := newFakeGH(t)
	f.on("GET", "/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if got := q.Get("state"); got != "all" {
			t.Errorf("state = %q, want all (default)", got)
		}
		if got := q.Get("per_page"); got != "100" {
			t.Errorf("per_page = %q, want 100 (default)", got)
		}
		_, _ = w.Write([]byte(`[
			{"number":1,"title":"first","body":"b1","state":"open","html_url":"https://github.com/o/r/issues/1","labels":[{"name":"bug"}],"assignees":[]},
			{"number":2,"title":"second","body":"b2","state":"closed","state_reason":"completed","html_url":"https://github.com/o/r/issues/2","labels":[],"assignees":[{"login":"alice"}]}
		]`))
	})
	tr := newTrackerForTest(t, f)
	issues, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitHub, Native: "o/r"}, domain.ListFilter{})
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

func TestList_FiltersOutPullRequests(t *testing.T) {
	f := newFakeGH(t)
	f.on("GET", "/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		// GitHub's issues endpoint returns PRs too. We must filter them out
		// so the LCM never tries to spawn an agent against a PR number.
		_, _ = w.Write([]byte(`[
			{"number":10,"title":"real issue","state":"open","html_url":"https://github.com/o/r/issues/10"},
			{"number":11,"title":"a PR","state":"open","html_url":"https://github.com/o/r/pull/11","pull_request":{"url":"https://api.github.com/repos/o/r/pulls/11"}},
			{"number":12,"title":"another issue","state":"open","html_url":"https://github.com/o/r/issues/12"}
		]`))
	})
	tr := newTrackerForTest(t, f)
	issues, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitHub, Native: "o/r"}, domain.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("len = %d, want 2 (PR must be filtered out)", len(issues))
	}
	if issues[0].ID.Native != "o/r#10" || issues[1].ID.Native != "o/r#12" {
		t.Fatalf("kept wrong items: %#v", issues)
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
			wantQ:  map[string]string{"state": "open", "labels": "bug,help wanted", "assignee": "alice", "per_page": "100"},
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
			f := newFakeGH(t)
			f.on("GET", "/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
				got := r.URL.Query()
				for k, want := range tc.wantQ {
					if g := got.Get(k); g != want {
						t.Errorf("query[%q] = %q, want %q", k, g, want)
					}
				}
				_, _ = w.Write([]byte(`[]`))
			})
			tr := newTrackerForTest(t, f)
			if _, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitHub, Native: "o/r"}, tc.filter); err != nil {
				t.Fatalf("List: %v", err)
			}
		})
	}
}

func TestList_PaginatesAcrossLinkNext(t *testing.T) {
	f := newFakeGH(t)
	f.on("GET", "/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", `<`+f.server.URL+`/repos/o/r/issues?state=all&per_page=100&page=2>; rel="next"`)
			_, _ = w.Write([]byte(`[{"number":1,"title":"first","state":"open","html_url":"https://github.com/o/r/issues/1"}]`))
		case "2":
			_, _ = w.Write([]byte(`[{"number":2,"title":"second","state":"open","html_url":"https://github.com/o/r/issues/2"}]`))
		default:
			t.Fatalf("unexpected page %q", r.URL.Query().Get("page"))
		}
	})
	tr := newTrackerForTest(t, f)

	issues, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitHub, Native: "o/r"}, domain.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) != 2 || issues[0].ID.Native != "o/r#1" || issues[1].ID.Native != "o/r#2" {
		t.Fatalf("issues = %#v, want both pages in order", issues)
	}
}

func TestList_ConditionalRevalidationContinuesCachedPageChain(t *testing.T) {
	f := newFakeGH(t)
	pageCalls := map[string]int{}
	f.on("GET", "/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pageCalls[page]++
		switch {
		case page == "" && pageCalls[page] == 1:
			if got := r.Header.Get("If-None-Match"); got != "" {
				t.Errorf("first page1 If-None-Match = %q, want empty", got)
			}
			w.Header().Set("ETag", `"e1"`)
			w.Header().Set("Link", `<`+f.server.URL+`/repos/o/r/issues?state=all&per_page=100&page=2>; rel="next"`)
			_, _ = w.Write([]byte(`[{"number":1,"title":"first","state":"open","html_url":"https://github.com/o/r/issues/1"}]`))
		case page == "2" && pageCalls[page] == 1:
			if got := r.Header.Get("If-None-Match"); got != "" {
				t.Errorf("first page2 If-None-Match = %q, want empty", got)
			}
			w.Header().Set("ETag", `"e2"`)
			_, _ = w.Write([]byte(`[{"number":2,"title":"second","state":"open","html_url":"https://github.com/o/r/issues/2"}]`))
		case page == "" && pageCalls[page] == 2:
			if got := r.Header.Get("If-None-Match"); got != `"e1"` {
				t.Errorf("second page1 If-None-Match = %q, want \"e1\"", got)
			}
			w.WriteHeader(http.StatusNotModified)
		case page == "2" && pageCalls[page] == 2:
			if got := r.Header.Get("If-None-Match"); got != `"e2"` {
				t.Errorf("second page2 If-None-Match = %q, want \"e2\"", got)
			}
			w.WriteHeader(http.StatusNotModified)
		default:
			t.Fatalf("unexpected page=%q call=%d", page, pageCalls[page])
		}
	})
	tr := newTrackerForTest(t, f)
	repo := domain.TrackerRepo{Provider: domain.TrackerProviderGitHub, Native: "o/r"}

	first, err := tr.List(ctx(), repo, domain.ListFilter{})
	if err != nil {
		t.Fatalf("first List: %v", err)
	}
	second, err := tr.List(ctx(), repo, domain.ListFilter{})
	if err != nil {
		t.Fatalf("second List: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("second issues = %#v\nwant %#v", second, first)
	}
	if len(second) != 2 {
		t.Fatalf("second len = %d, want both cached pages", len(second))
	}
}

func TestList_PageCountShrinkIgnoresOrphanedCachedPage(t *testing.T) {
	f := newFakeGH(t)
	var page1Calls int
	f.on("GET", "/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "2" {
			w.Header().Set("ETag", `"old-page-2"`)
			_, _ = w.Write([]byte(`[{"number":2,"title":"old second","state":"open","html_url":"https://github.com/o/r/issues/2"}]`))
			return
		}
		page1Calls++
		switch page1Calls {
		case 1:
			w.Header().Set("ETag", `"old-page-1"`)
			w.Header().Set("Link", `<`+f.server.URL+`/repos/o/r/issues?state=all&per_page=100&page=2>; rel="next"`)
			_, _ = w.Write([]byte(`[{"number":1,"title":"old first","state":"open","html_url":"https://github.com/o/r/issues/1"}]`))
		case 2:
			if got := r.Header.Get("If-None-Match"); got != `"old-page-1"` {
				t.Errorf("second page1 If-None-Match = %q, want \"old-page-1\"", got)
			}
			w.Header().Set("ETag", `"new-page-1"`)
			_, _ = w.Write([]byte(`[{"number":3,"title":"only remaining","state":"open","html_url":"https://github.com/o/r/issues/3"}]`))
		default:
			t.Fatalf("unexpected page1 call #%d", page1Calls)
		}
	})
	tr := newTrackerForTest(t, f)
	repo := domain.TrackerRepo{Provider: domain.TrackerProviderGitHub, Native: "o/r"}

	first, err := tr.List(ctx(), repo, domain.ListFilter{})
	if err != nil {
		t.Fatalf("first List: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first len = %d, want two cached pages", len(first))
	}
	second, err := tr.List(ctx(), repo, domain.ListFilter{})
	if err != nil {
		t.Fatalf("second List: %v", err)
	}
	if len(second) != 1 || second[0].ID.Native != "o/r#3" {
		t.Fatalf("second issues = %#v, want only refreshed page 1", second)
	}
}

func TestList_ConditionalRevalidationReturns304CachedIssues(t *testing.T) {
	f := newFakeGH(t)
	var calls int
	f.on("GET", "/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			if got := r.Header.Get("If-None-Match"); got != "" {
				t.Errorf("first If-None-Match = %q, want empty", got)
			}
			w.Header().Set("ETag", `"abc"`)
			_, _ = w.Write([]byte(`[
				{"number":1,"title":"first","body":"b1","state":"open","html_url":"https://github.com/o/r/issues/1"},
				{"number":2,"title":"second","body":"b2","state":"closed","state_reason":"completed","html_url":"https://github.com/o/r/issues/2"}
			]`))
		case 2:
			if got := r.Header.Get("If-None-Match"); got != `"abc"` {
				t.Errorf("second If-None-Match = %q, want \"abc\"", got)
			}
			w.WriteHeader(http.StatusNotModified)
		default:
			t.Fatalf("unexpected List call #%d", calls)
		}
	})
	tr := newTrackerForTest(t, f)
	repo := domain.TrackerRepo{Provider: domain.TrackerProviderGitHub, Native: "o/r"}

	first, err := tr.List(ctx(), repo, domain.ListFilter{})
	if err != nil {
		t.Fatalf("first List: %v", err)
	}
	second, err := tr.List(ctx(), repo, domain.ListFilter{})
	if err != nil {
		t.Fatalf("second List: %v", err)
	}
	if !reflect.DeepEqual(second, first) {
		t.Fatalf("second issues = %#v\nwant %#v", second, first)
	}
	reqs := f.calls()
	if len(reqs) != 2 {
		t.Fatalf("HTTP calls = %d, want 2", len(reqs))
	}
	if got := reqs[1].IfNoneMatch; got != `"abc"` {
		t.Fatalf("recorded If-None-Match = %q, want \"abc\"", got)
	}
}

func TestList_ETagUpdatesWhenIssuesChange(t *testing.T) {
	f := newFakeGH(t)
	var calls int
	f.on("GET", "/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write([]byte(`[{"number":1,"title":"old","state":"open","html_url":"https://github.com/o/r/issues/1"}]`))
		case 2:
			if got := r.Header.Get("If-None-Match"); got != `"v1"` {
				t.Errorf("second If-None-Match = %q, want \"v1\"", got)
			}
			w.Header().Set("ETag", `"v2"`)
			_, _ = w.Write([]byte(`[{"number":2,"title":"new","state":"open","html_url":"https://github.com/o/r/issues/2"}]`))
		case 3:
			if got := r.Header.Get("If-None-Match"); got != `"v2"` {
				t.Errorf("third If-None-Match = %q, want \"v2\"", got)
			}
			w.WriteHeader(http.StatusNotModified)
		default:
			t.Fatalf("unexpected List call #%d", calls)
		}
	})
	tr := newTrackerForTest(t, f)
	repo := domain.TrackerRepo{Provider: domain.TrackerProviderGitHub, Native: "o/r"}

	if _, err := tr.List(ctx(), repo, domain.ListFilter{}); err != nil {
		t.Fatalf("first List: %v", err)
	}
	second, err := tr.List(ctx(), repo, domain.ListFilter{})
	if err != nil {
		t.Fatalf("second List: %v", err)
	}
	if len(second) != 1 || second[0].ID.Native != "o/r#2" || second[0].Title != "new" {
		t.Fatalf("second issues = %#v, want new issue", second)
	}
	if _, err := tr.List(ctx(), repo, domain.ListFilter{}); err != nil {
		t.Fatalf("third List: %v", err)
	}
}

func TestList_SeparateCacheKeyPerFilter(t *testing.T) {
	f := newFakeGH(t)
	var calls int
	f.on("GET", "/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			if got := r.Header.Get("If-None-Match"); got != "" {
				t.Errorf("first If-None-Match = %q, want empty", got)
			}
			w.Header().Set("ETag", `"bug-etag"`)
			_, _ = w.Write([]byte(`[{"number":1,"title":"bug","state":"open","html_url":"https://github.com/o/r/issues/1"}]`))
		case 2:
			if got := r.Header.Get("If-None-Match"); got != "" {
				t.Errorf("second If-None-Match = %q, want empty for different filter", got)
			}
			w.Header().Set("ETag", `"docs-etag"`)
			_, _ = w.Write([]byte(`[{"number":2,"title":"docs","state":"open","html_url":"https://github.com/o/r/issues/2"}]`))
		default:
			t.Fatalf("unexpected List call #%d", calls)
		}
	})
	tr := newTrackerForTest(t, f)
	repo := domain.TrackerRepo{Provider: domain.TrackerProviderGitHub, Native: "o/r"}

	first, err := tr.List(ctx(), repo, domain.ListFilter{Labels: []string{"bug"}})
	if err != nil {
		t.Fatalf("first List: %v", err)
	}
	second, err := tr.List(ctx(), repo, domain.ListFilter{Labels: []string{"docs"}})
	if err != nil {
		t.Fatalf("second List: %v", err)
	}
	if len(first) != 1 || first[0].Title != "bug" {
		t.Fatalf("first issues = %#v, want bug", first)
	}
	if len(second) != 1 || second[0].Title != "docs" {
		t.Fatalf("second issues = %#v, want docs", second)
	}
}

func TestList_NoETagHeaderNotCached(t *testing.T) {
	f := newFakeGH(t)
	var calls int
	f.on("GET", "/repos/o/r/issues", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("If-None-Match"); got != "" {
			t.Errorf("call #%d If-None-Match = %q, want empty", calls, got)
		}
		_, _ = w.Write([]byte(`[{"number":1,"title":"uncached","state":"open","html_url":"https://github.com/o/r/issues/1"}]`))
	})
	tr := newTrackerForTest(t, f)
	repo := domain.TrackerRepo{Provider: domain.TrackerProviderGitHub, Native: "o/r"}

	if _, err := tr.List(ctx(), repo, domain.ListFilter{}); err != nil {
		t.Fatalf("first List: %v", err)
	}
	if _, err := tr.List(ctx(), repo, domain.ListFilter{}); err != nil {
		t.Fatalf("second List: %v", err)
	}
	if got := len(f.calls()); got != 2 {
		t.Fatalf("HTTP calls = %d, want 2", got)
	}
}

func TestList_RejectsWrongProvider(t *testing.T) {
	f := newFakeGH(t)
	tr := newTrackerForTest(t, f)
	_, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProvider("gitlab"), Native: "g/p"}, domain.ListFilter{})
	if !errors.Is(err, ErrWrongProvider) {
		t.Fatalf("err = %v, want ErrWrongProvider", err)
	}
	if calls := f.calls(); len(calls) != 0 {
		t.Fatalf("unexpected HTTP calls: %#v", calls)
	}
}

func TestList_RejectsBadRepo(t *testing.T) {
	cases := []string{
		"",             // empty
		"noseparator",  // missing /
		"/repo",        // empty owner
		"owner/",       // empty repo
		"a/b/c",        // extra slash
		" owner/repo",  // leading whitespace in owner
		"owner/repo ",  // trailing whitespace in repo
		"own er/repo",  // embedded space in owner
		"owner/re#po",  // embedded # in repo
		"\towner/repo", // tab in owner
		"owner/repo\n", // newline in repo
	}
	// Sanity: a benign leading-dot repo (".github" convention) must pass.
	if _, _, err := parseGitHubRepo("octocat/.github"); err != nil {
		t.Fatalf("leading-dot repo rejected unexpectedly: %v", err)
	}
	for _, native := range cases {
		t.Run(native, func(t *testing.T) {
			f := newFakeGH(t)
			tr := newTrackerForTest(t, f)
			_, err := tr.List(ctx(), domain.TrackerRepo{Provider: domain.TrackerProviderGitHub, Native: native}, domain.ListFilter{})
			if !errors.Is(err, ErrBadID) {
				t.Fatalf("native=%q: err = %v, want ErrBadID", native, err)
			}
		})
	}
}
