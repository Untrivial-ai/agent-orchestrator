package onedev

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// newObserverProvider builds a provider allowlisted for one httptest server
// and returns the repository identity for a project on it.
func newObserverProvider(t *testing.T, project string, h http.HandlerFunc) (*Provider, ports.SCMRepo, *httptest.Server) {
	t.Helper()
	srv := newTestServer(t, h)
	p, err := NewProvider(ProviderOptions{
		AllowedHosts: []string{srv.URL},
		Token:        StaticTokenSource("od-token"),
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	repo, ok := p.ParseRepository(srv.URL + "/" + project + ".git")
	if !ok {
		t.Fatalf("ParseRepository rejected %s/%s.git", srv.URL, project)
	}
	return p, repo, srv
}

// recorder captures the query strings the provider sent, per API path.
type recorder struct {
	mu sync.Mutex
	// queries maps an API path to every "query" parameter sent to it.
	queries map[string][]string
	// hits counts requests per API path.
	hits map[string]int
}

func newRecorder() *recorder {
	return &recorder{queries: map[string][]string{}, hits: map[string]int{}}
}

func (r *recorder) record(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	path := strings.TrimPrefix(req.URL.Path, APIBasePath)
	r.hits[path]++
	r.queries[path] = append(r.queries[path], req.URL.Query().Get("query"))
}

func (r *recorder) first(path string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queries[path]) == 0 {
		return ""
	}
	return r.queries[path][0]
}

func (r *recorder) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[path]
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return parsed
}

// ---------------------------------------------------------------------------
// Query construction
// ---------------------------------------------------------------------------

func TestQuoteQueryValue(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "productone", want: `"productone"`},
		{name: "nested path", in: "Homelab/curatarr", want: `"Homelab/curatarr"`},
		{name: "embedded quote", in: `odd"name`, want: `"odd\"name"`},
		{name: "embedded backslash", in: `odd\name`, want: `"odd\\name"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := quoteQueryValue(tt.in); got != tt.want {
				t.Fatalf("quoteQueryValue(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

// TestListPRsByRepoQuery pins the three parts of OneDev's query language this
// adapter cannot get wrong: the project criterion, the "since" date operator
// (OneDev rejects "is after" with HTTP 406), and the last-activity ordering
// that RepoPRListGuard's token depends on.
func TestListPRsByRepoQuery(t *testing.T) {
	since := mustTime(t, "2026-08-22T16:55:46Z")
	tests := []struct {
		name         string
		updatedAfter time.Time
		want         string
	}{
		{
			name:         "no cursor lists everything",
			updatedAfter: time.Time{},
			want:         `"Target Project" is "productone" order by "Last Activity Date" desc`,
		},
		{
			name:         "cursor uses the since operator",
			updatedAfter: since,
			want:         `"Target Project" is "productone" and "Last Activity Date" is since "2026-08-22T16:55:46Z" order by "Last Activity Date" desc`,
		},
		{
			// A non-UTC cursor must still be sent as UTC: OneDev parses the
			// value in UTC regardless of any offset, so a local wall-clock
			// time would move the cursor by the offset and drop updates.
			name:         "cursor is normalized to UTC",
			updatedAfter: since.In(time.FixedZone("UTC+5", 5*60*60)),
			want:         `"Target Project" is "productone" and "Last Activity Date" is since "2026-08-22T16:55:46Z" order by "Last Activity Date" desc`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecorder()
			p, repo, _ := newObserverProvider(t, "productone", func(w http.ResponseWriter, r *http.Request) {
				rec.record(r)
				writeJSON(t, w, []restPullRequest{})
			})
			if _, err := p.ListPRsByRepo(context.Background(), repo, tt.updatedAfter); err != nil {
				t.Fatalf("ListPRsByRepo: %v", err)
			}
			if got := rec.first("/pulls"); got != tt.want {
				t.Fatalf("query = %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestListPRsByRepoObservations(t *testing.T) {
	submitted := mustTime(t, "2026-08-20T09:00:00Z")
	activity := mustTime(t, "2026-08-22T16:55:46Z")
	closed := mustTime(t, "2026-08-23T10:00:00Z")

	rec := newRecorder()
	p, repo, srv := newObserverProvider(t, "productone", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			writeJSON(t, w, []restPullRequest{
				{
					ID: 241, Number: 106, Status: "OPEN", Title: "docs: changelog",
					TargetBranch: "main", SourceBranch: "chore/backlog",
					BaseCommitHash: "d3b7cc6", SubmitDate: &submitted,
					LastActivity: &restLastActivity{Date: &activity},
					SubmitterID:  4, TargetProjectID: 33, SourceProjectID: 33,
				},
				{
					ID: 159, Number: 105, Status: "MERGED", Title: "fix: shell",
					TargetBranch: "main", SourceBranch: "fix/shell",
					SubmitDate: &submitted, CloseDate: &closed,
					LastActivity: &restLastActivity{Date: &closed},
					SubmitterID:  4, TargetProjectID: 33, SourceProjectID: 41,
				},
			})
		case strings.HasSuffix(r.URL.Path, "/users/4"):
			writeJSON(t, w, restUser{ID: 4, Name: "johnkattenhorn", FullName: "John Kattenhorn"})
		case strings.HasSuffix(r.URL.Path, "/projects/41"):
			writeJSON(t, w, restProject{ID: 41, Path: "Homelab/fork", Name: "fork"})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	got, err := p.ListPRsByRepo(context.Background(), repo, time.Time{})
	if err != nil {
		t.Fatalf("ListPRsByRepo: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d observations, want 2", len(got))
	}

	open := got[0]
	if open.State != string(domain.PRStateOpen) || open.Merged || open.Closed {
		t.Errorf("open PR = state %q merged %v closed %v", open.State, open.Merged, open.Closed)
	}
	if open.ProviderID != "241" {
		t.Errorf("ProviderID = %q, want 241 (the internal id, not the number)", open.ProviderID)
	}
	if want := srv.URL + "/productone/~pulls/106"; open.URL != want {
		t.Errorf("URL = %q, want %q", open.URL, want)
	}
	if open.Author != "johnkattenhorn" {
		t.Errorf("Author = %q, want johnkattenhorn", open.Author)
	}
	if !open.UpdatedAtProvider.Equal(activity) {
		t.Errorf("UpdatedAtProvider = %v, want %v", open.UpdatedAtProvider, activity)
	}
	if open.HeadRepo != "productone" {
		t.Errorf("HeadRepo = %q, want productone for a same-project PR", open.HeadRepo)
	}

	merged := got[1]
	if merged.State != string(domain.PRStateMerged) || !merged.Merged {
		t.Errorf("merged PR = state %q merged %v", merged.State, merged.Merged)
	}
	if !merged.MergedAtProvider.Equal(closed) || !merged.ClosedAtProvider.IsZero() {
		t.Errorf("merged PR timestamps = merged %v closed %v", merged.MergedAtProvider, merged.ClosedAtProvider)
	}
	// A PR raised from another project in the tree must name that project as
	// its head repo, or branch-prefix attribution would claim it for a session
	// in the target project.
	if merged.HeadRepo != "Homelab/fork" {
		t.Errorf("HeadRepo = %q, want Homelab/fork for a cross-project PR", merged.HeadRepo)
	}

	// The author lookup is memoized: two PRs by the same submitter cost one
	// /users round-trip, not two.
	if n := rec.count("/users/4"); n != 1 {
		t.Errorf("/users/4 fetched %d times, want 1", n)
	}
}

// TestListPRsByRepoTolerablesMissingAuthor: author resolution is display
// metadata. A deleted account (404) or system activity (userId -1) must leave
// the author empty rather than fail the whole listing.
func TestListPRsByRepoToleratesMissingAuthor(t *testing.T) {
	tests := []struct {
		name        string
		submitterID int64
		userStatus  int
		wantHits    int
	}{
		{name: "system activity is not looked up", submitterID: -1, userStatus: http.StatusOK, wantHits: 0},
		{name: "deleted account", submitterID: 7, userStatus: http.StatusNotFound, wantHits: 1},
		{name: "server error", submitterID: 7, userStatus: http.StatusInternalServerError, wantHits: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecorder()
			p, repo, _ := newObserverProvider(t, "productone", func(w http.ResponseWriter, r *http.Request) {
				rec.record(r)
				if strings.Contains(r.URL.Path, "/users/") {
					w.WriteHeader(tt.userStatus)
					return
				}
				writeJSON(t, w, []restPullRequest{{
					ID: 1, Number: 1, Status: "OPEN", SourceBranch: "topic",
					SubmitterID: tt.submitterID,
				}})
			})
			got, err := p.ListPRsByRepo(context.Background(), repo, time.Time{})
			if err != nil {
				t.Fatalf("ListPRsByRepo: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d observations, want 1", len(got))
			}
			if got[0].Author != "" {
				t.Errorf("Author = %q, want empty", got[0].Author)
			}
			if n := rec.count(fmt.Sprintf("/users/%d", tt.submitterID)); n != tt.wantHits {
				t.Errorf("user lookups = %d, want %d", n, tt.wantHits)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Guards — the absent-ETag path
// ---------------------------------------------------------------------------

// TestRepoPRListGuardSynthesisesValidatorWithoutETag is the regression test for
// the guard fallback. OneDev sends neither ETag nor Last-Modified, so the
// guard must synthesise its own token rather than depend on a validator that
// never arrives: with no header at all it still reports changed on a cold
// cache, unchanged when the project's newest activity is identical, and
// changed again the moment that activity moves.
func TestRepoPRListGuardSynthesisesValidatorWithoutETag(t *testing.T) {
	activity := mustTime(t, "2026-08-22T16:55:46Z")
	later := activity.Add(time.Hour)

	var newest []restPullRequest
	var mu sync.Mutex
	rec := newRecorder()
	p, repo, _ := newObserverProvider(t, "productone", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		// Deliberately no ETag and no Last-Modified: this is what a real
		// OneDev instance sends, verified against 16.5.6.
		if got := w.Header().Get("ETag"); got != "" {
			t.Errorf("test server set an ETag (%q); the fallback would not be exercised", got)
		}
		mu.Lock()
		defer mu.Unlock()
		writeJSON(t, w, newest)
	})

	mu.Lock()
	newest = []restPullRequest{{ID: 241, Number: 106, Status: "OPEN", LastActivity: &restLastActivity{Date: &activity}}}
	mu.Unlock()

	// Cold cache: an empty caller token can never match, so the guard reports
	// changed and the observer does a full listing.
	first, err := p.RepoPRListGuard(context.Background(), repo, "")
	if err != nil {
		t.Fatalf("RepoPRListGuard: %v", err)
	}
	if first.ETag == "" {
		t.Fatal("ETag is empty; the guard must synthesise a token when the server sends none")
	}
	if first.NotModified {
		t.Error("NotModified = true on a cold cache, want false")
	}

	// Same data, token round-tripped: nothing can have changed.
	second, err := p.RepoPRListGuard(context.Background(), repo, first.ETag)
	if err != nil {
		t.Fatalf("RepoPRListGuard: %v", err)
	}
	if !second.NotModified || second.ETag != first.ETag {
		t.Errorf("unchanged data = %+v, want NotModified with the same token", second)
	}

	// Any change bumps the newest request's last-activity date, which is
	// exactly what makes the synthesised token move.
	mu.Lock()
	newest = []restPullRequest{{ID: 241, Number: 106, Status: "OPEN", LastActivity: &restLastActivity{Date: &later}}}
	mu.Unlock()
	third, err := p.RepoPRListGuard(context.Background(), repo, second.ETag)
	if err != nil {
		t.Fatalf("RepoPRListGuard: %v", err)
	}
	if third.NotModified {
		t.Error("NotModified = true after activity moved, want false")
	}
	if third.ETag == second.ETag {
		t.Error("token did not change after activity moved")
	}

	// An emptied project is a change too, not an ambiguous no-op.
	mu.Lock()
	newest = nil
	mu.Unlock()
	fourth, err := p.RepoPRListGuard(context.Background(), repo, third.ETag)
	if err != nil {
		t.Fatalf("RepoPRListGuard: %v", err)
	}
	if fourth.NotModified || fourth.ETag == third.ETag {
		t.Errorf("emptied project = %+v, want a changed token", fourth)
	}

	if want := `"Target Project" is "productone" order by "Last Activity Date" desc`; rec.first("/pulls") != want {
		t.Errorf("guard query = %s\nwant %s", rec.first("/pulls"), want)
	}
}

func TestCommitChecksGuardSynthesisesValidatorWithoutETag(t *testing.T) {
	finished := mustTime(t, "2026-08-27T07:38:18Z")

	var builds []restBuild
	var mu sync.Mutex
	rec := newRecorder()
	p, repo, _ := newObserverProvider(t, "productone", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		if got := w.Header().Get("ETag"); got != "" {
			t.Errorf("test server set an ETag (%q); the fallback would not be exercised", got)
		}
		mu.Lock()
		defer mu.Unlock()
		writeJSON(t, w, builds)
	})

	mu.Lock()
	builds = []restBuild{{ID: 1416, Number: 365, JobName: "CI", Status: "RUNNING", CommitHash: "d3b802d"}}
	mu.Unlock()

	first, err := p.CommitChecksGuard(context.Background(), repo, "e638a69", "")
	if err != nil {
		t.Fatalf("CommitChecksGuard: %v", err)
	}
	if first.ETag == "" || first.NotModified {
		t.Fatalf("cold cache = %+v, want a synthesised token and NotModified=false", first)
	}

	second, err := p.CommitChecksGuard(context.Background(), repo, "e638a69", first.ETag)
	if err != nil {
		t.Fatalf("CommitChecksGuard: %v", err)
	}
	if !second.NotModified {
		t.Error("NotModified = false with identical builds, want true")
	}

	// A build finishing is the transition the guard exists to notice.
	mu.Lock()
	builds = []restBuild{{ID: 1416, Number: 365, JobName: "CI", Status: "SUCCESSFUL", CommitHash: "d3b802d", FinishDate: &finished}}
	mu.Unlock()
	third, err := p.CommitChecksGuard(context.Background(), repo, "e638a69", second.ETag)
	if err != nil {
		t.Fatalf("CommitChecksGuard: %v", err)
	}
	if third.NotModified || third.ETag == second.ETag {
		t.Errorf("finished build = %+v, want a changed token", third)
	}

	// The token is scoped to the commit, so two PRs on different heads never
	// share a guard entry.
	other, err := p.CommitChecksGuard(context.Background(), repo, "0000000", "")
	if err != nil {
		t.Fatalf("CommitChecksGuard: %v", err)
	}
	if other.ETag == third.ETag {
		t.Error("token is identical for a different head SHA")
	}

	if want := `"Project" is "productone" order by "Submit Date" desc`; rec.first("/builds") != want {
		t.Errorf("guard query = %s\nwant %s", rec.first("/builds"), want)
	}
}

func TestCommitChecksGuardRejectsEmptyHeadSHA(t *testing.T) {
	p, repo, _ := newObserverProvider(t, "productone", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s: an empty head SHA must not reach the API", r.URL.Path)
	})
	if _, err := p.CommitChecksGuard(context.Background(), repo, "  ", "token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGuardsRejectUnallowlistedHost(t *testing.T) {
	p, repo, _ := newObserverProvider(t, "productone", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s to a host outside the allowlist", r.URL.Path)
	})
	stranger := repo
	stranger.Host = "onedev.stranger.test:6610"

	if _, err := p.RepoPRListGuard(context.Background(), stranger, ""); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("RepoPRListGuard err = %v, want ErrHostNotAllowed", err)
	}
	if _, err := p.CommitChecksGuard(context.Background(), stranger, "abc", ""); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("CommitChecksGuard err = %v, want ErrHostNotAllowed", err)
	}
	if _, err := p.ListPRsByRepo(context.Background(), stranger, time.Time{}); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("ListPRsByRepo err = %v, want ErrHostNotAllowed", err)
	}
}

// ---------------------------------------------------------------------------
// FetchPullRequests
// ---------------------------------------------------------------------------

// prServer serves the endpoints one pull-request fetch touches. Numbers listed
// in broken answer HTTP 500 on their detail fetch.
func prServer(t *testing.T, rec *recorder, broken map[int]bool) http.HandlerFunc {
	t.Helper()
	activity := mustTime(t, "2026-08-22T16:55:46Z")
	return func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		path := strings.TrimPrefix(r.URL.Path, APIBasePath)
		query := r.URL.Query().Get("query")
		switch {
		case path == "/pulls" && strings.Contains(query, `"Number" is`):
			// Number lookup: map 10x -> request id 20x.
			var number int
			if _, err := fmt.Sscanf(query[strings.LastIndex(query, `"Number" is "`)+len(`"Number" is "`):], "%d", &number); err != nil {
				t.Errorf("parse number from %q: %v", query, err)
			}
			writeJSON(t, w, []restPullRequest{{ID: int64(100 + number), Number: number}})
		case strings.HasSuffix(path, "/merge-preview"):
			writeJSON(t, w, restMergePreview{
				TargetHeadCommitHash: "d3b7cc6",
				HeadCommitHash:       "e638a69",
				MergeCommitHash:      "d3b802d",
			})
		case strings.HasSuffix(path, "/reviews"):
			writeJSON(t, w, []restReview{})
		case path == "/builds":
			writeJSON(t, w, []restBuild{{ID: 1416, Number: 365, JobName: "CI", Status: "SUCCESSFUL", CommitHash: "d3b802d"}})
		case strings.HasPrefix(path, "/pulls/"):
			var id int
			if _, err := fmt.Sscanf(strings.TrimPrefix(path, "/pulls/"), "%d", &id); err != nil {
				t.Errorf("parse request id from %q: %v", path, err)
			}
			if broken[id-100] {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("boom"))
				return
			}
			writeJSON(t, w, restPullRequest{
				ID: int64(id), Number: id - 100, Status: "OPEN", Title: "t",
				SourceBranch: "topic", TargetBranch: "main",
				BuildCommitHash: "d3b802d",
				LastActivity:    &restLastActivity{Date: &activity},
			})
		default:
			t.Errorf("unexpected request %s", path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// TestFetchPullRequestsAlignment pins the contract the observer depends on:
// result[i] answers refs[i], and a ref that could not be fetched leaves a
// Fetched=false placeholder carrying its error rather than shifting the
// results of its neighbours.
func TestFetchPullRequestsAlignment(t *testing.T) {
	rec := newRecorder()
	p, repo, _ := newObserverProvider(t, "productone", prServer(t, rec, map[int]bool{2: true}))

	refs := []ports.SCMPRRef{
		{Repo: repo, Number: 1},
		{Repo: repo, Number: 2},
		{Repo: repo, Number: 3},
	}
	got, err := p.FetchPullRequests(context.Background(), refs)
	if err == nil {
		t.Fatal("err = nil, want the failing ref's error surfaced")
	}
	if len(got) != len(refs) {
		t.Fatalf("got %d observations, want %d", len(got), len(refs))
	}
	for i, obs := range got {
		if obs.PR.Number != refs[i].Number {
			t.Fatalf("result[%d] answers PR %d, want %d", i, obs.PR.Number, refs[i].Number)
		}
	}
	if !got[0].Fetched || !got[2].Fetched {
		t.Errorf("healthy refs = %v/%v, want both fetched", got[0].Fetched, got[2].Fetched)
	}
	if got[1].Fetched {
		t.Error("failed ref reported Fetched=true")
	}
	if got[1].Error == nil {
		t.Error("failed ref carries no error")
	}
	if got[1].Provider != ProviderKey || got[1].Host != repo.Host || got[1].Repo != repo.Repo {
		t.Errorf("placeholder lost its repo context: %+v", got[1])
	}
}

func TestFetchPullRequestsObservation(t *testing.T) {
	rec := newRecorder()
	p, repo, srv := newObserverProvider(t, "productone", prServer(t, rec, nil))

	got, err := p.FetchPullRequests(context.Background(), []ports.SCMPRRef{{Repo: repo, Number: 6, URL: "http://stale.example/pr/6"}})
	if err != nil {
		t.Fatalf("FetchPullRequests: %v", err)
	}
	obs := got[0]
	if !obs.Fetched {
		t.Fatalf("Fetched = false: %+v", obs)
	}
	if obs.PR.HeadSHA != "e638a69" {
		t.Errorf("HeadSHA = %q, want e638a69 (the merge preview's head commit)", obs.PR.HeadSHA)
	}
	if obs.PR.MergeCommitSHA != "d3b802d" {
		t.Errorf("MergeCommitSHA = %q, want d3b802d", obs.PR.MergeCommitSHA)
	}
	if obs.PR.URLAlias != "http://stale.example/pr/6" {
		t.Errorf("URLAlias = %q, want the requested URL preserved", obs.PR.URLAlias)
	}
	if want := srv.URL + "/productone/~pulls/6"; obs.PR.URL != want {
		t.Errorf("URL = %q, want %q", obs.PR.URL, want)
	}
	if obs.CI.Summary != string(domain.CIPassing) || obs.CI.HeadSHA != "e638a69" {
		t.Errorf("CI = %+v, want passing at e638a69", obs.CI)
	}
	if len(obs.CI.Checks) != 1 || obs.CI.Checks[0].ProviderID != "1416" {
		t.Errorf("checks = %+v, want one check carrying the build id", obs.CI.Checks)
	}
	if want := srv.URL + "/productone/~builds/365"; obs.CI.Checks[0].URL != want {
		t.Errorf("check URL = %q, want %q", obs.CI.Checks[0].URL, want)
	}
	if obs.Mergeability.State != string(domain.MergeMergeable) || !obs.Mergeability.Mergeable {
		t.Errorf("Mergeability = %+v, want mergeable", obs.Mergeability)
	}
	// The build query must carry a project-qualified pull-request reference; a
	// bare number is rejected by OneDev with "Reference project not specified".
	if want := `"Pull Request" is "productone#6" order by "Submit Date" desc`; rec.first("/builds") != want {
		t.Errorf("build query = %s\nwant %s", rec.first("/builds"), want)
	}
}

func TestFetchPullRequestsRejectsOversizedBatch(t *testing.T) {
	p, repo, _ := newObserverProvider(t, "productone", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s: an oversized batch must be rejected locally", r.URL.Path)
	})
	refs := make([]ports.SCMPRRef, batchLimit+1)
	for i := range refs {
		refs[i] = ports.SCMPRRef{Repo: repo, Number: i + 1}
	}
	if _, err := p.FetchPullRequests(context.Background(), refs); err == nil {
		t.Fatal("err = nil, want an oversized-batch error")
	}
}

// TestFetchPullRequestsFallsBackToUpdatesForHeadSHA: OneDev computes the merge
// preview asynchronously, and its pull-request payload carries no head commit.
// Until the preview exists the head must come from the update history, or the
// observer has nothing to guard CI with.
func TestFetchPullRequestsFallsBackToUpdatesForHeadSHA(t *testing.T) {
	rec := newRecorder()
	p, repo, _ := newObserverProvider(t, "productone", func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		path := strings.TrimPrefix(r.URL.Path, APIBasePath)
		switch {
		case path == "/pulls":
			writeJSON(t, w, []restPullRequest{{ID: 241, Number: 106}})
		case strings.HasSuffix(path, "/merge-preview"):
			// OneDev has not computed one yet.
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(path, "/updates"):
			writeJSON(t, w, []restPullRequestUpdate{
				{ID: 300, HeadCommitHash: "older"},
				{ID: 326, HeadCommitHash: "newest"},
			})
		case strings.HasSuffix(path, "/reviews"):
			writeJSON(t, w, []restReview{})
		case path == "/builds":
			writeJSON(t, w, []restBuild{})
		default:
			writeJSON(t, w, restPullRequest{ID: 241, Number: 106, Status: "OPEN", SourceBranch: "topic"})
		}
	})

	got, err := p.FetchPullRequests(context.Background(), []ports.SCMPRRef{{Repo: repo, Number: 106}})
	if err != nil {
		t.Fatalf("FetchPullRequests: %v", err)
	}
	if got[0].PR.HeadSHA != "newest" {
		t.Errorf("HeadSHA = %q, want newest", got[0].PR.HeadSHA)
	}
	if got[0].Mergeability.State != string(domain.MergeUnknown) {
		t.Errorf("Mergeability = %q, want unknown while no preview exists", got[0].Mergeability.State)
	}
}

// ---------------------------------------------------------------------------
// CI
// ---------------------------------------------------------------------------

func TestFetchCI(t *testing.T) {
	tests := []struct {
		name        string
		builds      []restBuild
		wantSummary domain.CIState
		wantChecks  []string
		wantFailed  []string
	}{
		{
			name:        "no builds",
			builds:      nil,
			wantSummary: domain.CIUnknown,
		},
		{
			name: "stale commits are excluded",
			builds: []restBuild{
				{ID: 2, Number: 2, JobName: "CI", Status: "FAILED", CommitHash: "older"},
				{ID: 1, Number: 1, JobName: "CI", Status: "SUCCESSFUL", CommitHash: "build-commit"},
			},
			wantSummary: domain.CIPassing,
			wantChecks:  []string{"CI"},
		},
		{
			name: "retries collapse to the newest build per job",
			builds: []restBuild{
				{ID: 3, Number: 3, JobName: "CI", Status: "SUCCESSFUL", CommitHash: "build-commit"},
				{ID: 2, Number: 2, JobName: "CI", Status: "FAILED", CommitHash: "build-commit"},
				{ID: 1, Number: 1, JobName: "Lint", Status: "FAILED", CommitHash: "build-commit"},
			},
			wantSummary: domain.CIFailing,
			wantChecks:  []string{"CI", "Lint"},
			wantFailed:  []string{"Lint"},
		},
		{
			name: "a running job keeps the rollup pending",
			builds: []restBuild{
				{ID: 2, Number: 2, JobName: "CI", Status: "RUNNING", CommitHash: "build-commit"},
				{ID: 1, Number: 1, JobName: "Lint", Status: "SUCCESSFUL", CommitHash: "build-commit"},
			},
			wantSummary: domain.CIPending,
			wantChecks:  []string{"CI", "Lint"},
		},
		{
			name: "a timed-out job fails the rollup",
			builds: []restBuild{
				{ID: 1, Number: 1, JobName: "CI", Status: "TIMED_OUT", CommitHash: "build-commit"},
			},
			wantSummary: domain.CIFailing,
			wantChecks:  []string{"CI"},
			wantFailed:  []string{"CI"},
		},
		{
			name: "a build on the head commit counts when the project builds the branch directly",
			builds: []restBuild{
				{ID: 1, Number: 1, JobName: "CI", Status: "SUCCESSFUL", CommitHash: "head-commit"},
			},
			wantSummary: domain.CIPassing,
			wantChecks:  []string{"CI"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, repo, _ := newObserverProvider(t, "productone", func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, tt.builds)
			})
			client, err := p.clientForRepo(repo)
			if err != nil {
				t.Fatalf("clientForRepo: %v", err)
			}
			host, err := p.hostForRepo(repo)
			if err != nil {
				t.Fatalf("hostForRepo: %v", err)
			}
			pr := &restPullRequest{Number: 106, BuildCommitHash: "build-commit"}

			ci, err := p.fetchCI(context.Background(), client, host, repo, pr, "head-commit")
			if err != nil {
				t.Fatalf("fetchCI: %v", err)
			}
			if ci.Summary != string(tt.wantSummary) {
				t.Errorf("Summary = %q, want %q", ci.Summary, tt.wantSummary)
			}
			if got := checkNames(ci.Checks); !reflect.DeepEqual(got, tt.wantChecks) {
				t.Errorf("Checks = %v, want %v", got, tt.wantChecks)
			}
			if got := checkNames(ci.FailedChecks); !reflect.DeepEqual(got, tt.wantFailed) {
				t.Errorf("FailedChecks = %v, want %v", got, tt.wantFailed)
			}
			if len(ci.FailedChecks) == 0 && ci.FailedFingerprint != "" {
				t.Errorf("FailedFingerprint = %q with no failures, want empty", ci.FailedFingerprint)
			}
			if len(ci.FailedChecks) > 0 && ci.FailedFingerprint == "" {
				t.Error("FailedFingerprint is empty despite failing checks")
			}
		})
	}
}

func checkNames(checks []ports.SCMCheckObservation) []string {
	if len(checks) == 0 {
		return nil
	}
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = c.Name
	}
	return out
}

func TestBuildStatusToCheckStatus(t *testing.T) {
	tests := []struct {
		status string
		want   domain.PRCheckStatus
	}{
		{status: "SUCCESSFUL", want: domain.PRCheckPassed},
		{status: "FAILED", want: domain.PRCheckFailed},
		{status: "TIMED_OUT", want: domain.PRCheckFailed},
		{status: "CANCELLED", want: domain.PRCheckCancelled},
		{status: "RUNNING", want: domain.PRCheckInProgress},
		{status: "PENDING", want: domain.PRCheckQueued},
		{status: "WAITING", want: domain.PRCheckQueued},
		{status: "SOMETHING_NEW", want: domain.PRCheckUnknown},
		{status: "", want: domain.PRCheckUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := buildStatusToCheckStatus(tt.status); got != tt.want {
				t.Fatalf("buildStatusToCheckStatus(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestNormalizePRStatus(t *testing.T) {
	tests := []struct {
		status     string
		wantState  domain.PRState
		wantMerged bool
		wantClosed bool
	}{
		{status: "OPEN", wantState: domain.PRStateOpen},
		{status: "MERGED", wantState: domain.PRStateMerged, wantMerged: true},
		{status: "DISCARDED", wantState: domain.PRStateClosed, wantClosed: true},
		// An unknown status must not be mistaken for open: AO acts on open
		// PRs, so guessing open is the more damaging error.
		{status: "SOMETHING_NEW", wantState: domain.PRStateClosed, wantClosed: true},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			state, merged, closed := normalizePRStatus(tt.status)
			if state != tt.wantState || merged != tt.wantMerged || closed != tt.wantClosed {
				t.Fatalf("normalizePRStatus(%q) = %q/%v/%v, want %q/%v/%v",
					tt.status, state, merged, closed, tt.wantState, tt.wantMerged, tt.wantClosed)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Reviews and mergeability
// ---------------------------------------------------------------------------

func TestReviewDecision(t *testing.T) {
	tests := []struct {
		name    string
		reviews []restReview
		want    domain.ReviewDecision
	}{
		{name: "no reviewers", reviews: nil, want: domain.ReviewNone},
		{name: "approved", reviews: []restReview{{Status: "APPROVED"}}, want: domain.ReviewApproved},
		{name: "pending", reviews: []restReview{{Status: "PENDING"}}, want: domain.ReviewRequired},
		{
			name:    "an outstanding reviewer outranks an approval",
			reviews: []restReview{{Status: "APPROVED"}, {Status: "PENDING"}},
			want:    domain.ReviewRequired,
		},
		{
			// A reviewer asking for changes is the signal AO must not lose,
			// so it dominates every other verdict.
			name:    "requested changes dominates",
			reviews: []restReview{{Status: "APPROVED"}, {Status: "PENDING"}, {Status: "REQUESTED_FOR_CHANGES"}},
			want:    domain.ReviewChangesRequest,
		},
		{
			name:    "excluded reviewers carry no signal",
			reviews: []restReview{{Status: "EXCLUDED"}, {Status: "APPROVED"}},
			want:    domain.ReviewApproved,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := reviewDecision(tt.reviews); got != tt.want {
				t.Fatalf("reviewDecision = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMergeability(t *testing.T) {
	tests := []struct {
		name         string
		pr           restPullRequest
		preview      restMergePreview
		hasPreview   bool
		ci           string
		review       string
		wantState    domain.Mergeability
		wantConflict bool
		wantBlockers []string
	}{
		{
			name:       "clean",
			pr:         restPullRequest{Status: "OPEN"},
			preview:    restMergePreview{MergeCommitHash: "abc"},
			hasPreview: true,
			ci:         string(domain.CIPassing),
			review:     string(domain.ReviewApproved),
			wantState:  domain.MergeMergeable,
		},
		{
			name:       "no preview yet",
			pr:         restPullRequest{Status: "OPEN"},
			hasPreview: false,
			wantState:  domain.MergeUnknown,
		},
		{
			// OneDev documents a null mergeCommitHash as "there are conflicts".
			name:         "null merge commit means conflicts",
			pr:           restPullRequest{Status: "OPEN"},
			preview:      restMergePreview{},
			hasPreview:   true,
			wantState:    domain.MergeConflicting,
			wantConflict: true,
			wantBlockers: []string{"conflicts"},
		},
		{
			name:         "failing ci blocks",
			pr:           restPullRequest{Status: "OPEN"},
			preview:      restMergePreview{MergeCommitHash: "abc"},
			hasPreview:   true,
			ci:           string(domain.CIFailing),
			wantState:    domain.MergeBlocked,
			wantBlockers: []string{"ci_failing"},
		},
		{
			name:         "requested changes blocks",
			pr:           restPullRequest{Status: "OPEN"},
			preview:      restMergePreview{MergeCommitHash: "abc"},
			hasPreview:   true,
			ci:           string(domain.CIPassing),
			review:       string(domain.ReviewChangesRequest),
			wantState:    domain.MergeBlocked,
			wantBlockers: []string{"changes_requested"},
		},
		{
			name:         "a failed merge check blocks",
			pr:           restPullRequest{Status: "OPEN", CheckError: "Some builds are required"},
			preview:      restMergePreview{MergeCommitHash: "abc"},
			hasPreview:   true,
			ci:           string(domain.CIPassing),
			wantState:    domain.MergeBlocked,
			wantBlockers: []string{"blocked_by_provider"},
		},
		{
			name:         "a closed request is not mergeable",
			pr:           restPullRequest{Status: "DISCARDED"},
			preview:      restMergePreview{MergeCommitHash: "abc"},
			hasPreview:   true,
			wantState:    domain.MergeBlocked,
			wantBlockers: []string{"blocked_by_provider"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeability(&tt.pr, tt.preview, tt.hasPreview, tt.ci, tt.review)
			if got.State != string(tt.wantState) {
				t.Errorf("State = %q, want %q", got.State, tt.wantState)
			}
			if got.Conflict != tt.wantConflict {
				t.Errorf("Conflict = %v, want %v", got.Conflict, tt.wantConflict)
			}
			if !reflect.DeepEqual(got.Blockers, tt.wantBlockers) {
				t.Errorf("Blockers = %v, want %v", got.Blockers, tt.wantBlockers)
			}
			if got.Mergeable != (tt.wantState == domain.MergeMergeable) {
				t.Errorf("Mergeable = %v for state %q", got.Mergeable, got.State)
			}
		})
	}
}

// TestFetchReviewThreads pins what OneDev can and cannot tell AO about review
// discussion: verdicts and request-level comments are available, inline code
// comments are not reachable per request, and the observation says so by
// reporting itself Partial.
func TestFetchReviewThreads(t *testing.T) {
	submitted := mustTime(t, "2026-08-23T09:00:00Z")
	p, repo, srv := newObserverProvider(t, "productone", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, APIBasePath)
		switch {
		case path == "/pulls":
			writeJSON(t, w, []restPullRequest{{ID: 241, Number: 106}})
		case strings.HasSuffix(path, "/reviews"):
			writeJSON(t, w, []restReview{
				{ID: 9, Status: "APPROVED", UserID: 4, StatusDate: &submitted},
				{ID: 10, Status: "EXCLUDED", UserID: 5},
			})
		case strings.HasSuffix(path, "/comments"):
			writeJSON(t, w, []restPullRequestComment{
				{ID: 1, Content: "Source branch no longer exists", UserID: 4},
				{ID: 2, Content: "beep boop", UserID: 6},
			})
		case strings.HasSuffix(path, "/users/4"):
			writeJSON(t, w, restUser{ID: 4, Name: "johnkattenhorn"})
		case strings.HasSuffix(path, "/users/6"):
			writeJSON(t, w, restUser{ID: 6, Name: "release-bot"})
		default:
			t.Errorf("unexpected request %s", path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	got, err := p.FetchReviewThreads(context.Background(), ports.SCMPRRef{Repo: repo, Number: 106})
	if err != nil {
		t.Fatalf("FetchReviewThreads: %v", err)
	}
	if got.Decision != string(domain.ReviewApproved) {
		t.Errorf("Decision = %q, want approved", got.Decision)
	}
	// An excluded reviewer is not a verdict, so it must not become a summary.
	if len(got.Reviews) != 1 {
		t.Fatalf("Reviews = %+v, want one summary", got.Reviews)
	}
	if got.Reviews[0].ID != "review:9" || got.Reviews[0].Author != "johnkattenhorn" {
		t.Errorf("review summary = %+v", got.Reviews[0])
	}
	if !got.Reviews[0].SubmittedAt.Equal(submitted) {
		t.Errorf("SubmittedAt = %v, want %v", got.Reviews[0].SubmittedAt, submitted)
	}
	if len(got.Threads) != 2 {
		t.Fatalf("Threads = %+v, want two", got.Threads)
	}
	if got.Threads[0].ID != "comment:1" || got.Threads[0].Path != "" {
		t.Errorf("thread = %+v, want an unanchored comment thread", got.Threads[0])
	}
	if want := srv.URL + "/productone/~pulls/106"; got.Threads[0].Comments[0].URL != want {
		t.Errorf("comment URL = %q, want %q", got.Threads[0].Comments[0].URL, want)
	}
	if !got.Threads[1].IsBot || !got.Threads[1].Comments[0].IsBot {
		t.Error("release-bot's comment was not flagged as a bot comment")
	}
	// OneDev exposes code comments only by id, so the thread set is never a
	// complete snapshot and must be merged rather than replace what is stored.
	if !got.Partial {
		t.Error("Partial = false; the thread set cannot include inline comments")
	}
}

// ---------------------------------------------------------------------------
// Build log tail
// ---------------------------------------------------------------------------

// frameStatus encodes OneDev's status marker: a negative big-endian int32
// length followed by that many bytes of status text.
func frameStatus(status string) []byte {
	frame := make([]byte, 4+len(status))
	binary.BigEndian.PutUint32(frame[:4], uint32(int32(-len(status)))) //nolint:gosec // mirrors the wire format
	copy(frame[4:], status)
	return frame
}

// frameEntry encodes one log entry: a positive length followed by that many
// bytes of JSON.
func frameEntry(t *testing.T, text string) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"date":     "2026-08-27T07:38:12.461+00:00",
		"messages": []map[string]any{{"text": text}},
	})
	if err != nil {
		t.Fatalf("marshal log entry: %v", err)
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload))) //nolint:gosec // length fits by construction
	copy(frame[4:], payload)
	return frame
}

func buildLogStream(t *testing.T, status string, lines ...string) []byte {
	t.Helper()
	out := frameStatus(status)
	for _, line := range lines {
		out = append(out, frameEntry(t, line)...)
	}
	return append(out, frameStatus(status)...)
}

func TestTailBuildLog(t *testing.T) {
	tests := []struct {
		name     string
		stream   func(t *testing.T) []byte
		maxLines int
		want     string
		wantErr  bool
	}{
		{
			name:     "status markers are not log lines",
			stream:   func(t *testing.T) []byte { return buildLogStream(t, "SUCCESSFUL", "one", "two") },
			maxLines: 5,
			want:     "one\ntwo",
		},
		{
			name: "only the tail is kept",
			stream: func(t *testing.T) []byte {
				return buildLogStream(t, "FAILED", "one", "two", "three", "four")
			},
			maxLines: 2,
			want:     "three\nfour",
		},
		{
			name:     "an empty log yields an empty tail",
			stream:   func(t *testing.T) []byte { return buildLogStream(t, "SUCCESSFUL") },
			maxLines: 5,
			want:     "",
		},
		{
			// A stream cut mid-frame after usable output (a build still
			// writing, a dropped connection) returns what was decoded.
			name: "truncation after usable output is tolerated",
			stream: func(t *testing.T) []byte {
				full := buildLogStream(t, "FAILED", "one", "two")
				return full[:len(full)-3]
			},
			maxLines: 5,
			want:     "one\ntwo",
		},
		{
			name: "an implausible frame length with no output is an error",
			stream: func(t *testing.T) []byte {
				return []byte{0x7f, 0xff, 0xff, 0xff}
			},
			maxLines: 5,
			wantErr:  true,
		},
		{
			// One unparseable entry does not cost the rest of the log: the
			// frame length says exactly where the next entry begins.
			name: "an unparseable entry is skipped",
			stream: func(t *testing.T) []byte {
				out := frameStatus("FAILED")
				out = append(out, frameEntry(t, "one")...)
				bad := []byte("{not json")
				header := make([]byte, 4)
				binary.BigEndian.PutUint32(header, uint32(len(bad))) //nolint:gosec // test fixture
				out = append(out, header...)
				out = append(out, bad...)
				return append(out, frameEntry(t, "two")...)
			},
			maxLines: 5,
			want:     "one\ntwo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tailBuildLog(strings.NewReader(string(tt.stream(t))), tt.maxLines)
			if tt.wantErr {
				if err == nil {
					t.Fatal("err = nil, want a framing error")
				}
				return
			}
			if err != nil {
				t.Fatalf("tailBuildLog: %v", err)
			}
			if got != tt.want {
				t.Fatalf("tail = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFetchFailedCheckLogTail(t *testing.T) {
	var gotPath string
	p, repo, _ := newObserverProvider(t, "productone", func(w http.ResponseWriter, r *http.Request) {
		gotPath = strings.TrimPrefix(r.URL.Path, APIBasePath)
		_, _ = w.Write(buildLogStream(t, "FAILED", "step one", "step two failed"))
	})

	got, err := p.FetchFailedCheckLogTail(context.Background(), repo, ports.SCMCheckObservation{Name: "CI", ProviderID: "1020"})
	if err != nil {
		t.Fatalf("FetchFailedCheckLogTail: %v", err)
	}
	if got != "step one\nstep two failed" {
		t.Errorf("tail = %q", got)
	}
	if gotPath != "/streaming/build-logs/1020" {
		t.Errorf("path = %q, want /streaming/build-logs/1020", gotPath)
	}
}

func TestFetchFailedCheckLogTailRequiresBuildID(t *testing.T) {
	p, repo, _ := newObserverProvider(t, "productone", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s: a check with no build id must not reach the API", r.URL.Path)
	})
	if _, err := p.FetchFailedCheckLogTail(context.Background(), repo, ports.SCMCheckObservation{Name: "CI"}); err == nil {
		t.Fatal("err = nil, want an error naming the missing build id")
	}
}
