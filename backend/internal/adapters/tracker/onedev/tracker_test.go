package onedev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	scmonedev "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/onedev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// fakeIssue is one issue in the fake instance, in the shape OneDev returns it:
// the list payload plus the separately-served custom fields.
type fakeIssue struct {
	ID      int64
	Number  int
	State   string
	Title   string
	Body    string
	Project string
	Fields  map[string]any
}

// fakeOneDev is a programmable stand-in for a OneDev instance. It records every
// query it was asked, so tests can assert on the exact query DSL the adapter
// emits — the part of this adapter most likely to break silently against a
// real server.
type fakeOneDev struct {
	t      *testing.T
	server *httptest.Server

	mu      sync.Mutex
	queries []string
	paths   []string
	auth    []string
	issues  []fakeIssue
	status  int
	body    string
}

func newFakeOneDev(t *testing.T, issues ...fakeIssue) *fakeOneDev {
	t.Helper()
	f := &fakeOneDev{t: t, issues: issues}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOneDev) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	status, body := f.status, f.body
	f.mu.Unlock()

	if status != 0 {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
		return
	}
	if !strings.HasPrefix(r.URL.Path, apiBasePath) {
		f.t.Errorf("request outside the OneDev API root: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, apiBasePath)

	// GET /~api/issues/{id}/fields
	if id, ok := strings.CutPrefix(rest, "/issues/"); ok && strings.HasSuffix(id, "/fields") {
		raw := strings.TrimSuffix(id, "/fields")
		n, _ := strconv.ParseInt(raw, 10, 64)
		for _, iss := range f.issues {
			if iss.ID != n {
				continue
			}
			// A nil Fields map models an instance that fails the
			// custom-fields route (deleted issue, revoked permission).
			if iss.Fields == nil {
				http.Error(w, "Unable to find issue", http.StatusNotFound)
				return
			}
			writeJSON(w, iss.Fields)
			return
		}
		http.Error(w, "Unable to find issue", http.StatusNotFound)
		return
	}

	if rest != "/issues" {
		f.t.Errorf("unexpected path: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		return
	}

	query := r.URL.Query().Get("query")
	f.mu.Lock()
	f.queries = append(f.queries, query)
	f.mu.Unlock()

	count, _ := strconv.Atoi(r.URL.Query().Get("count"))
	if count > maxPageCount {
		http.Error(w, "Count should not be greater than 100", http.StatusNotAcceptable)
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

	matched := make([]map[string]any, 0, len(f.issues))
	for _, iss := range f.issues {
		matched = append(matched, map[string]any{
			"id":          iss.ID,
			"number":      iss.Number,
			"state":       iss.State,
			"title":       iss.Title,
			"description": iss.Body,
		})
	}
	if offset > len(matched) {
		offset = len(matched)
	}
	end := offset + count
	if end > len(matched) {
		end = len(matched)
	}
	writeJSON(w, matched[offset:end])
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeOneDev) lastQuery() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) == 0 {
		f.t.Fatal("no query was issued")
	}
	return f.queries[len(f.queries)-1]
}

func (f *fakeOneDev) failWith(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

// host returns the fake's authority, scheme-qualified for the allowlist —
// httptest serves plain HTTP, which a bare entry would silently upgrade.
func (f *fakeOneDev) host() string { return f.server.URL }

func (f *fakeOneDev) authority() string {
	u, err := url.Parse(f.server.URL)
	if err != nil {
		f.t.Fatalf("parse fake URL: %v", err)
	}
	return u.Host
}

func newTrackerForTest(t *testing.T, f *fakeOneDev, opts ...func(*Options)) *Tracker {
	t.Helper()
	o := Options{
		Token:        scmonedev.StaticTokenSource("dev-token"),
		AllowedHosts: []string{f.host()},
		HTTPClient:   f.server.Client(),
	}
	for _, fn := range opts {
		fn(&o)
	}
	tracker, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return tracker
}

func sampleIssue() fakeIssue {
	return fakeIssue{
		ID:      424,
		Number:  168,
		State:   "Open",
		Title:   "feat(ci): custom build agent",
		Body:    "## Summary\n\nLift the tests into CI.",
		Project: "productone",
		Fields:  map[string]any{"Type": "Story", "Priority": "Normal", "Assignees": []any{"johnkattenhorn", "marie"}},
	}
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNewRequiresAllowedHosts(t *testing.T) {
	_, err := New(Options{Token: scmonedev.StaticTokenSource("t")})
	if !errors.Is(err, ErrNoAllowedHosts) {
		t.Fatalf("New with no hosts = %v; want ErrNoAllowedHosts", err)
	}
}

func TestNewRequiresTokenSource(t *testing.T) {
	_, err := New(Options{AllowedHosts: []string{"onedev.example.com"}})
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("New with no token source = %v; want ErrNoToken", err)
	}
}

func TestNewRejectsConflictingSchemesForOneHost(t *testing.T) {
	_, err := New(Options{
		Token:        scmonedev.StaticTokenSource("t"),
		AllowedHosts: []string{"http://onedev.example.com:6610", "https://onedev.example.com:6610"},
	})
	if err == nil {
		t.Fatal("New accepted one host under two schemes; want an error")
	}
}

// TestHostRoutingIgnoresPortMismatch pins that a repository recorded against
// OneDev's git SSH port still resolves to the instance allowlisted under its
// HTTP API port.
func TestHostRoutingIgnoresPortMismatch(t *testing.T) {
	tracker, err := New(Options{
		Token:        scmonedev.StaticTokenSource("t"),
		AllowedHosts: []string{"http://10.0.0.30:6610"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := tracker.ConfigForHost("10.0.0.30:6611"); err != nil {
		t.Errorf("ConfigForHost(ssh port) = %v; want nil", err)
	}
	if err := tracker.ConfigForHost("other.example.com"); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("ConfigForHost(unknown) = %v; want ErrHostNotAllowed", err)
	}
}

// TestBlankHostResolvesOnlyForASingleInstance pins that OneDev's missing
// default host is honoured: a blank host is a usable shorthand in the
// single-instance case and a hard error once there is more than one, rather
// than a silent pick.
func TestBlankHostResolvesOnlyForASingleInstance(t *testing.T) {
	one, err := New(Options{Token: scmonedev.StaticTokenSource("t"), AllowedHosts: []string{"a.example.com"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := one.ConfigForHost(""); err != nil {
		t.Errorf("ConfigForHost(\"\") with one instance = %v; want nil", err)
	}
	two, err := New(Options{Token: scmonedev.StaticTokenSource("t"), AllowedHosts: []string{"a.example.com", "b.example.com"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := two.ConfigForHost(""); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("ConfigForHost(\"\") with two instances = %v; want ErrHostNotAllowed", err)
	}
}

func TestPerHostTokenOverrideWins(t *testing.T) {
	f := newFakeOneDev(t, sampleIssue())
	tracker := newTrackerForTest(t, f, func(o *Options) {
		o.HostTokens = map[string]TokenSource{f.authority(): scmonedev.StaticTokenSource("host-token")}
	})
	if _, err := tracker.Get(context.Background(), oneDevID("productone#168", f.authority())); err != nil {
		t.Fatalf("Get: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, got := range f.auth {
		if got != "Bearer host-token" {
			t.Fatalf("Authorization = %q; want the per-host override", got)
		}
	}
}

func oneDevID(native, host string) domain.TrackerID {
	return domain.TrackerID{Provider: domain.TrackerProviderOneDev, Native: native, Host: host}
}

func oneDevRepo(native, host string) domain.TrackerRepo {
	return domain.TrackerRepo{Provider: domain.TrackerProviderOneDev, Native: native, Host: host}
}

// ---------------------------------------------------------------------------
// Get
// ---------------------------------------------------------------------------

func TestGetProjectsIssueOntoDomain(t *testing.T) {
	f := newFakeOneDev(t, sampleIssue())
	tracker := newTrackerForTest(t, f)

	got, err := tracker.Get(context.Background(), oneDevID("productone#168", f.authority()))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := domain.Issue{
		ID:        oneDevID("productone#168", f.authority()),
		Title:     "feat(ci): custom build agent",
		Body:      "## Summary\n\nLift the tests into CI.",
		State:     domain.IssueOpen,
		URL:       f.server.URL + "/productone/~issues/168",
		Labels:    []string{"Priority: Normal", "Type: Story"},
		Assignees: []string{"johnkattenhorn", "marie"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Get() =\n%#v\nwant\n%#v", got, want)
	}
}

// TestGetResolvesNumberThroughTheQueryAPI pins that Get does not treat the
// user-facing issue number as OneDev's internal entity id — /~api/issues/{id}
// is keyed by the latter, so a direct fetch would silently return the wrong
// issue.
func TestGetResolvesNumberThroughTheQueryAPI(t *testing.T) {
	f := newFakeOneDev(t, sampleIssue())
	tracker := newTrackerForTest(t, f)
	if _, err := tracker.Get(context.Background(), oneDevID("Homelab/curatarr#3", f.authority())); err != nil {
		t.Fatalf("Get: %v", err)
	}
	q := f.lastQuery()
	if q != `"Project" is "Homelab/curatarr" and "Number" is "3"` {
		t.Errorf("Get query = %q", q)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.paths[0] != apiBasePath+"/issues" {
		t.Errorf("first request path = %q; want the issue query endpoint", f.paths[0])
	}
	if f.paths[1] != apiBasePath+"/issues/424/fields" {
		t.Errorf("second request path = %q; want the custom-fields endpoint keyed by the internal id", f.paths[1])
	}
}

func TestGetRejectsForeignProviderAndBadNativeIDs(t *testing.T) {
	f := newFakeOneDev(t)
	tracker := newTrackerForTest(t, f)
	ctx := context.Background()

	if _, err := tracker.Get(ctx, domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "a/b#1"}); !errors.Is(err, ErrWrongProvider) {
		t.Errorf("Get with a github id = %v; want ErrWrongProvider", err)
	}
	for _, native := range []string{"productone", "productone#", "productone#0", "productone#abc", "#4", "../etc#4", "a b#4"} {
		if _, err := tracker.Get(ctx, oneDevID(native, f.authority())); !errors.Is(err, ErrBadID) {
			t.Errorf("Get(%q) = %v; want ErrBadID", native, err)
		}
	}
}

// TestGetSingleSegmentProjectPath pins that a root OneDev project — which has
// no owner/repo pair at all — is a valid id here, unlike GitLab.
func TestGetSingleSegmentProjectPath(t *testing.T) {
	f := newFakeOneDev(t, sampleIssue())
	tracker := newTrackerForTest(t, f)
	got, err := tracker.Get(context.Background(), oneDevID("productone#168", f.authority()))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID.Native != "productone#168" {
		t.Errorf("round-tripped native = %q", got.ID.Native)
	}
}

func TestGetMissingIssueIsNotFound(t *testing.T) {
	f := newFakeOneDev(t)
	tracker := newTrackerForTest(t, f)
	if _, err := tracker.Get(context.Background(), oneDevID("productone#999", f.authority())); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get of a missing issue = %v; want ErrNotFound", err)
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusUnauthorized, "Invalid account or incorrect credentials", ErrAuthFailed},
		{http.StatusForbidden, "Not authorized", ErrAuthFailed},
		{http.StatusNotFound, "Unable to find issue", ErrNotFound},
		{http.StatusTooManyRequests, "slow down", ErrRateLimited},
	}
	for _, tc := range cases {
		f := newFakeOneDev(t)
		f.failWith(tc.status, tc.body)
		tracker := newTrackerForTest(t, f)
		_, err := tracker.Get(context.Background(), oneDevID("productone#1", f.authority()))
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d = %v; want %v", tc.status, err, tc.want)
		}
	}
}

// TestInvalidQueryIsReportedVerbatim keeps OneDev's plain-text 406 body — the
// only signal that a generated query is malformed — from being swallowed.
func TestInvalidQueryIsReportedVerbatim(t *testing.T) {
	f := newFakeOneDev(t)
	f.failWith(http.StatusNotAcceptable, "Invalid query")
	tracker := newTrackerForTest(t, f)
	_, err := tracker.List(context.Background(), oneDevRepo("productone", f.authority()), domain.ListFilter{})
	if err == nil || !strings.Contains(err.Error(), "Invalid query") {
		t.Fatalf("List error = %v; want it to carry \"Invalid query\"", err)
	}
}

// TestIssueFieldsFailureFailsTheCall pins that a failed custom-fields fetch is
// not swallowed. Assignees decide intake eligibility, so an issue reported with
// silently-empty assignees would make a project configured with assignee
// "none" spawn a session for every issue on the board.
func TestIssueFieldsFailureFailsTheCall(t *testing.T) {
	// A nil Fields map makes the fake's custom-fields route fail.
	f := newFakeOneDev(t, fakeIssue{ID: 7, Number: 7, State: "Open", Title: "t", Project: "productone"})
	tracker := newTrackerForTest(t, f)
	if _, err := tracker.Get(context.Background(), oneDevID("productone#7", f.authority())); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v; want the custom-fields failure to surface", err)
	}
}

// ---------------------------------------------------------------------------
// State mapping through the adapter
// ---------------------------------------------------------------------------

func TestGetMapsInstanceStates(t *testing.T) {
	cases := []struct {
		native string
		want   domain.NormalizedIssueState
	}{
		{"Open", domain.IssueOpen},
		{"Closed", domain.IssueDone},
		{"In Progress", domain.IssueInProgress},
		{"In Review", domain.IssueInReview},
		// Not in the default map: normalizes to open, never to done.
		{"Blocked", domain.IssueOpen},
	}
	for _, tc := range cases {
		iss := sampleIssue()
		iss.State = tc.native
		f := newFakeOneDev(t, iss)
		tracker := newTrackerForTest(t, f)
		got, err := tracker.Get(context.Background(), oneDevID("productone#168", f.authority()))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.State != tc.want {
			t.Errorf("state %q normalized to %q; want %q", tc.native, got.State, tc.want)
		}
	}
}

func TestConfiguredStateOverrideReachesTheProjection(t *testing.T) {
	iss := sampleIssue()
	iss.State = "Blocked"
	f := newFakeOneDev(t, iss)
	states, err := NewStateMap(map[string]string{"Blocked": "in_progress"})
	if err != nil {
		t.Fatalf("NewStateMap: %v", err)
	}
	tracker := newTrackerForTest(t, f, func(o *Options) { o.States = states })
	got, err := tracker.Get(context.Background(), oneDevID("productone#168", f.authority()))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != domain.IssueInProgress {
		t.Errorf("state = %q; want %q from the configured override", got.State, domain.IssueInProgress)
	}
}

// ---------------------------------------------------------------------------
// List queries
// ---------------------------------------------------------------------------

// TestListStateQueryNeverUsesABareOpenCriteria is the trap this adapter exists
// to avoid: a bare "open" works for OneDev pull requests and is rejected with
// HTTP 406 for issues, so state must always go through the "State" field.
func TestListStateQueryNeverUsesABareOpenCriteria(t *testing.T) {
	cases := []struct {
		name   string
		filter domain.ListFilter
		want   string
	}{
		{
			name:   "all states",
			filter: domain.ListFilter{},
			want:   `"Project" is "productone" order by "Last Activity Date" desc`,
		},
		{
			name:   "open excludes the terminal states",
			filter: domain.ListFilter{State: domain.ListOpen},
			want:   `"Project" is "productone" and "State" is not "Closed" order by "Last Activity Date" desc`,
		},
		{
			name:   "closed enumerates the terminal states",
			filter: domain.ListFilter{State: domain.ListClosed},
			want:   `"Project" is "productone" and ("State" is "Closed") order by "Last Activity Date" desc`,
		},
		{
			name:   "assignee",
			filter: domain.ListFilter{State: domain.ListOpen, Assignee: "johnkattenhorn"},
			want:   `"Project" is "productone" and "State" is not "Closed" and "Assignees" is "johnkattenhorn" order by "Last Activity Date" desc`,
		},
		{
			name:   "assignee wildcard",
			filter: domain.ListFilter{Assignee: "*"},
			want:   `"Project" is "productone" and "Assignees" is not empty order by "Last Activity Date" desc`,
		},
		{
			name:   "assignee none",
			filter: domain.ListFilter{Assignee: "none"},
			want:   `"Project" is "productone" and "Assignees" is empty order by "Last Activity Date" desc`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeOneDev(t)
			tracker := newTrackerForTest(t, f)
			if _, err := tracker.List(context.Background(), oneDevRepo("productone", f.authority()), tc.filter); err != nil {
				t.Fatalf("List: %v", err)
			}
			if got := f.lastQuery(); got != tc.want {
				t.Errorf("query =\n%s\nwant\n%s", got, tc.want)
			}
			if strings.Contains(f.lastQuery(), " open") {
				t.Error("query contains a bare open criteria, which OneDev rejects for issues")
			}
		})
	}
}

// TestListOpenExcludesEveryConfiguredTerminalState pins that the open filter is
// derived from the configured mapping, not from a hardcoded "Closed".
func TestListOpenExcludesEveryConfiguredTerminalState(t *testing.T) {
	f := newFakeOneDev(t)
	states, err := NewStateMap(map[string]string{"Won't Fix": "cancelled", "Blocked": "open"})
	if err != nil {
		t.Fatalf("NewStateMap: %v", err)
	}
	tracker := newTrackerForTest(t, f, func(o *Options) { o.States = states })
	if _, err := tracker.List(context.Background(), oneDevRepo("productone", f.authority()), domain.ListFilter{State: domain.ListOpen}); err != nil {
		t.Fatalf("List: %v", err)
	}
	want := `"Project" is "productone" and "State" is not "Closed" and "State" is not "Won't Fix" order by "Last Activity Date" desc`
	if got := f.lastQuery(); got != want {
		t.Errorf("query =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(f.lastQuery(), `is not "Blocked"`) {
		t.Error("an open-mapped state was excluded from the open filter")
	}
}

// TestListClosedWithNoTerminalStatesReturnsNothing guards the degenerate case:
// emitting no state criteria would return the whole backlog as "closed".
func TestListClosedWithNoTerminalStatesReturnsNothing(t *testing.T) {
	f := newFakeOneDev(t, sampleIssue())
	states, err := NewStateMap(map[string]string{"Closed": "open"})
	if err != nil {
		t.Fatalf("NewStateMap: %v", err)
	}
	tracker := newTrackerForTest(t, f, func(o *Options) { o.States = states })
	got, err := tracker.List(context.Background(), oneDevRepo("productone", f.authority()), domain.ListFilter{State: domain.ListClosed})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List returned %d issues; want none", len(got))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) != 0 {
		t.Errorf("List issued %d queries; want none", len(f.queries))
	}
}

func TestListQuotesQueryValues(t *testing.T) {
	f := newFakeOneDev(t)
	tracker := newTrackerForTest(t, f)
	if _, err := tracker.List(context.Background(), oneDevRepo("productone", f.authority()), domain.ListFilter{Assignee: `we"ird\name`}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !strings.Contains(f.lastQuery(), `"Assignees" is "we\"ird\\name"`) {
		t.Errorf("query did not escape the assignee value: %s", f.lastQuery())
	}
}

func TestListAssigneeFieldIsConfigurable(t *testing.T) {
	f := newFakeOneDev(t)
	tracker := newTrackerForTest(t, f, func(o *Options) { o.AssigneeField = "Owner" })
	if _, err := tracker.List(context.Background(), oneDevRepo("productone", f.authority()), domain.ListFilter{Assignee: "alice"}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !strings.Contains(f.lastQuery(), `"Owner" is "alice"`) {
		t.Errorf("query = %s; want the configured assignee field", f.lastQuery())
	}
}

func TestListRejectsForeignProvider(t *testing.T) {
	f := newFakeOneDev(t)
	tracker := newTrackerForTest(t, f)
	_, err := tracker.List(context.Background(), domain.TrackerRepo{Provider: domain.TrackerProviderGitLab, Native: "g/p"}, domain.ListFilter{})
	if !errors.Is(err, ErrWrongProvider) {
		t.Fatalf("List with a gitlab repo = %v; want ErrWrongProvider", err)
	}
}

// ---------------------------------------------------------------------------
// List pagination and filtering
// ---------------------------------------------------------------------------

func TestListPaginatesAndCapsAtLimit(t *testing.T) {
	issues := make([]fakeIssue, 0, 150)
	for i := 1; i <= 150; i++ {
		issues = append(issues, fakeIssue{
			ID: int64(i), Number: i, State: "Open", Title: "t" + strconv.Itoa(i),
			Project: "productone", Fields: map[string]any{"Assignees": []any{"alice"}},
		})
	}
	f := newFakeOneDev(t, issues...)
	tracker := newTrackerForTest(t, f)

	all, err := tracker.List(context.Background(), oneDevRepo("productone", f.authority()), domain.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 150 {
		t.Errorf("List returned %d issues; want 150 across two pages", len(all))
	}

	limited, err := tracker.List(context.Background(), oneDevRepo("productone", f.authority()), domain.ListFilter{Limit: 5})
	if err != nil {
		t.Fatalf("List with limit: %v", err)
	}
	if len(limited) != 5 {
		t.Errorf("List with Limit=5 returned %d issues", len(limited))
	}
}

// TestListNeverRequestsMoreThanTheServerCeiling pins the clamp: OneDev rejects
// count > 100 with HTTP 406, so a large Limit must not become a large count.
func TestListNeverRequestsMoreThanTheServerCeiling(t *testing.T) {
	f := newFakeOneDev(t)
	tracker := newTrackerForTest(t, f)
	if _, err := tracker.List(context.Background(), oneDevRepo("productone", f.authority()), domain.ListFilter{Limit: 5000}); err != nil {
		t.Fatalf("List: %v", err)
	}
}

func TestListFiltersLabelsClientSide(t *testing.T) {
	bug := sampleIssue()
	bug.ID, bug.Number = 1, 1
	bug.Fields = map[string]any{"Type": "Bug", "Assignees": []any{"alice"}}
	story := sampleIssue()
	story.ID, story.Number = 2, 2
	story.Fields = map[string]any{"Type": "Story", "Assignees": []any{"alice"}}

	f := newFakeOneDev(t, bug, story)
	tracker := newTrackerForTest(t, f)

	for _, want := range []string{"Type: Bug", "bug", "TYPE: BUG"} {
		got, err := tracker.List(context.Background(), oneDevRepo("productone", f.authority()), domain.ListFilter{Labels: []string{want}})
		if err != nil {
			t.Fatalf("List(%q): %v", want, err)
		}
		if len(got) != 1 || got[0].ID.Native != "productone#1" {
			t.Errorf("List(labels=%q) returned %d issues; want just the bug", want, len(got))
		}
	}
	// The label filter is client-side, so it must never leak into the query —
	// OneDev answers an unknown field name with HTTP 500.
	if strings.Contains(f.lastQuery(), "Label") {
		t.Errorf("label filter leaked into the query: %s", f.lastQuery())
	}
}

// ---------------------------------------------------------------------------
// Preflight
// ---------------------------------------------------------------------------

func TestPreflightCachesSuccessNotFailure(t *testing.T) {
	f := newFakeOneDev(t)
	tracker := newTrackerForTest(t, f)
	ctx := context.Background()

	f.failWith(http.StatusUnauthorized, "Invalid account or incorrect credentials")
	if err := tracker.Preflight(ctx); !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("Preflight = %v; want ErrAuthFailed", err)
	}
	f.failWith(0, "")
	if err := tracker.Preflight(ctx); err != nil {
		t.Fatalf("Preflight after recovery = %v; want nil (failures must not be cached)", err)
	}

	f.mu.Lock()
	before := len(f.paths)
	f.mu.Unlock()
	if err := tracker.Preflight(ctx); err != nil {
		t.Fatalf("second Preflight = %v", err)
	}
	f.mu.Lock()
	after := len(f.paths)
	f.mu.Unlock()
	if after != before {
		t.Errorf("Preflight made %d extra requests; success should be cached", after-before)
	}
}
