package gitcode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// newTestProvider builds a Provider against an httptest server serving handler.
func newTestProvider(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	p, err := NewProvider(ProviderOptions{
		Token:    StaticTokenSource("test-token"),
		RESTBase: srv.URL,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p
}

func mustRepo() ports.SCMRepo {
	return ports.SCMRepo{Provider: "gitcode", Host: "gitcode.com", Owner: "Ascend", Name: "model-agent", Repo: "Ascend/model-agent"}
}

func TestParseRepository(t *testing.T) {
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {})
	cases := []struct {
		remote string
		ok     bool
		owner  string
		name   string
	}{
		{"https://gitcode.com/Ascend/model-agent.git", true, "Ascend", "model-agent"},
		{"https://gitcode.com/Ascend/model-agent", true, "Ascend", "model-agent"},
		{"git@gitcode.com:Ascend/model-agent.git", true, "Ascend", "model-agent"},
		{"ssh://git@gitcode.com/Ascend/model-agent.git", true, "Ascend", "model-agent"},
		// Non-GitCode hosts must be rejected so credentials never leak.
		{"https://github.com/Untrivial-ai/agent-orchestrator.git", false, "", ""},
		{"https://gitlab.com/group/repo.git", false, "", ""},
		{"https://gitcode.com/only-one-segment", false, "", ""},
		{"https://gitcode.com/a/b/c.git", false, "", ""},
		{"", false, "", ""},
	}
	for _, tc := range cases {
		repo, ok := p.ParseRepository(tc.remote)
		if ok != tc.ok {
			t.Errorf("ParseRepository(%q) ok = %v, want %v", tc.remote, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if repo.Owner != tc.owner || repo.Name != tc.name || repo.Provider != "gitcode" {
			t.Errorf("ParseRepository(%q) = %+v, want owner=%s name=%s provider=gitcode", tc.remote, repo, tc.owner, tc.name)
		}
	}
}

// prListPayload builds a v5-style pulls list response with pagination headers.
func prListPayload(t *testing.T, w http.ResponseWriter, prs []map[string]any, totalPage int) {
	t.Helper()
	if totalPage > 0 {
		w.Header().Set("total_page", strconv.Itoa(totalPage))
		w.Header().Set("total_count", strconv.Itoa(len(prs)))
	}
	if err := json.NewEncoder(w).Encode(prs); err != nil {
		t.Errorf("encode payload: %v", err)
	}
}

func testPR(number int, state string, updatedAt string) map[string]any {
	return map[string]any{
		"number":     number,
		"title":      "pr " + strconv.Itoa(number),
		"state":      state,
		"draft":      false,
		"html_url":   "https://gitcode.com/Ascend/model-agent/merge_requests/" + strconv.Itoa(number),
		"updated_at": updatedAt,
		"created_at": updatedAt,
		"head":       map[string]any{"ref": "feat/x", "sha": "abc123", "repo": map[string]any{"full_name": "Ascend/model-agent"}},
		"base":       map[string]any{"ref": "master", "sha": "def456"},
		"mergeable":  true,
	}
}

func TestListPRsByRepoPaginatesAndStopsOnCursor(t *testing.T) {
	calls := 0
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.URL.Query().Get("state"); got != "all" {
			t.Errorf("state = %q, want all", got)
		}
		if got := r.URL.Query().Get("sort"); got != "updated" {
			t.Errorf("sort = %q, want updated", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		page := r.URL.Query().Get("page")
		switch page {
		case "1":
			prListPayload(t, w, []map[string]any{testPR(3, "open", "2026-09-12T12:00:00+08:00")}, 2)
		case "2":
			prListPayload(t, w, []map[string]any{testPR(2, "merged", "2026-09-11T12:00:00+08:00")}, 2)
		default:
			t.Errorf("unexpected page %q", page)
		}
	})
	repo := mustRepo()
	got, err := p.ListPRsByRepo(context.Background(), repo, time.Time{})
	if err != nil {
		t.Fatalf("ListPRsByRepo: %v", err)
	}
	if len(got) != 2 || got[0].Number != 3 || got[1].Number != 2 {
		t.Fatalf("got PRs %+v, want [3 2]", got)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (total_page honored)", calls)
	}

	// updatedAfter mid-list stops early: first page newest (12:00 +08:00 = 04:00
	// UTC) passes the 03:00 UTC cursor, the second page (updated Sep 10) is
	// older than the cursor and stops the walk there.
	calls = 0
	cursor := time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)
	p2 := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("page") == "1" {
			prListPayload(t, w, []map[string]any{testPR(3, "open", "2026-09-12T12:00:00+08:00")}, 0)
		} else {
			prListPayload(t, w, []map[string]any{testPR(1, "closed", "2026-09-10T12:00:00+08:00")}, 0)
		}
	})
	got, err = p2.ListPRsByRepo(context.Background(), repo, cursor)
	if err != nil {
		t.Fatalf("ListPRsByRepo(cursor): %v", err)
	}
	if len(got) != 1 || got[0].Number != 3 {
		t.Fatalf("cursor stop got %+v, want only PR 3", got)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestFetchPullRequestsPositionalAlignment(t *testing.T) {
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/pulls/10") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error_message":"not found"}`))
			return
		}
		number := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/repos/Ascend/model-agent/pulls/"), "")
		_ = json.NewEncoder(w).Encode(testPR(mustAtoi(t, number), "open", "2026-09-12T12:00:00+08:00"))
	})
	repo := mustRepo()
	refs := []ports.SCMPRRef{
		{Repo: repo, Number: 1, URL: "https://gitcode.com/Ascend/model-agent/merge_requests/1"},
		{Repo: repo, Number: 10},
		{Repo: repo, Number: 2},
	}
	obs, err := p.FetchPullRequests(context.Background(), refs)
	if err != nil {
		t.Fatalf("FetchPullRequests: %v", err)
	}
	if len(obs) != 3 {
		t.Fatalf("len(obs) = %d, want 3", len(obs))
	}
	if !obs[0].Fetched || obs[0].PR.Number != 1 {
		t.Errorf("obs[0] = fetched:%v number:%d, want fetched 1", obs[0].Fetched, obs[0].PR.Number)
	}
	if obs[1].Fetched || !errors.Is(obs[1].Error, ports.ErrSCMNotFound) {
		t.Errorf("obs[1] fetched:%v err:%v, want Fetched=false ErrSCMNotFound", obs[1].Fetched, obs[1].Error)
	}
	if !obs[2].Fetched || obs[2].PR.Number != 2 {
		t.Errorf("obs[2] = fetched:%v number:%d, want fetched 2", obs[2].Fetched, obs[2].PR.Number)
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("atoi %q: %v", s, err)
	}
	return n
}

func TestFetchPullRequestsNormalizesState(t *testing.T) {
	cases := []struct {
		state    string
		draft    bool
		merged   bool
		closed   bool
		want     string
		mergeSum string
	}{
		{"open", false, false, false, "open", "unknown"},
		{"open", true, false, false, "draft", "unknown"},
		{"merged", false, true, false, "merged", "unknown"},
		{"closed", false, false, true, "closed", "unknown"},
	}
	for _, tc := range cases {
		p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
			pr := testPR(1, tc.state, "2026-09-12T12:00:00+08:00")
			pr["draft"] = tc.draft
			if tc.merged {
				pr["merged_at"] = "2026-09-12T13:00:00+08:00"
			}
			_ = json.NewEncoder(w).Encode(pr)
		})
		repo := mustRepo()
		obs, err := p.FetchPullRequests(context.Background(), []ports.SCMPRRef{{Repo: repo, Number: 1}})
		if err != nil || len(obs) != 1 || !obs[0].Fetched {
			t.Fatalf("state=%s: fetch failed: %v %+v", tc.state, err, obs)
		}
		got := obs[0]
		if got.PR.State != tc.want {
			t.Errorf("state=%s draft=%v: PR.State = %q, want %q", tc.state, tc.draft, got.PR.State, tc.want)
		}
		if got.PR.Merged != tc.merged || got.PR.Closed != tc.closed {
			t.Errorf("state=%s: Merged=%v Closed=%v, want %v/%v", tc.state, got.PR.Merged, got.PR.Closed, tc.merged, tc.closed)
		}
		// CI must be reported unknown, never fabricated passing.
		if got.CI.Summary != tc.mergeSum || len(got.CI.Checks) != 0 {
			t.Errorf("state=%s: CI = %+v, want Summary=%s no checks", tc.state, got.CI, tc.mergeSum)
		}
		if tc.merged && got.PR.MergedAtProvider.IsZero() {
			t.Errorf("state=merged: MergedAtProvider not parsed")
		}
	}
}

func TestFetchReviewThreadsGroupsByDiscussion(t *testing.T) {
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/comments") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		// Out-of-range pages return an empty array (GitCode behavior), which
		// is the pagination terminator when no total_page header is sent.
		if r.URL.Query().Get("page") != "1" {
			_ = json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "discussion_id": "d1", "body": "first", "user": map[string]any{"login": "alice"}},
			{"id": 2, "discussion_id": "d1", "body": "reply", "user": map[string]any{"login": "gitcode-bot"}},
			{"id": 3, "discussion_id": "d2", "body": "second", "user": map[string]any{"login": "gitcode-bot"}},
		})
	})
	repo := mustRepo()
	ref := ports.SCMPRRef{Repo: repo, Number: 1, URL: "https://gitcode.com/Ascend/model-agent/merge_requests/1"}
	obs, err := p.FetchReviewThreads(context.Background(), ref)
	if err != nil {
		t.Fatalf("FetchReviewThreads: %v", err)
	}
	if len(obs.Threads) != 2 {
		t.Fatalf("threads = %d, want 2", len(obs.Threads))
	}
	d1 := obs.Threads[0]
	if d1.ID != "d1" || len(d1.Comments) != 2 || d1.IsBot {
		t.Errorf("thread d1 = %+v, want 2 comments non-bot", d1)
	}
	d2 := obs.Threads[1]
	if d2.ID != "d2" || !d2.IsBot {
		t.Errorf("thread d2 = %+v, want IsBot=true", d2)
	}
	if d1.Comments[0].Author != "alice" || d1.Comments[1].IsBot != true {
		t.Errorf("d1 comments = %+v", d1.Comments)
	}
}

func TestRepoPRListGuardSyntheticETag(t *testing.T) {
	var seenAuth bool
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		seenAuth = true
		if r.URL.Query().Get("per_page") != "1" {
			t.Errorf("guard per_page = %q, want 1", r.URL.Query().Get("per_page"))
		}
		prListPayload(t, w, []map[string]any{testPR(9, "open", "2026-09-12T12:00:00+08:00")}, 0)
	})
	repo := mustRepo()
	res, err := p.RepoPRListGuard(context.Background(), repo, "")
	if err != nil {
		t.Fatalf("RepoPRListGuard: %v", err)
	}
	if res.NotModified || res.ETag == "" || !strings.HasPrefix(res.ETag, "updated:") {
		t.Fatalf("guard = %+v, want synthetic updated: etag", res)
	}
	res2, err := p.RepoPRListGuard(context.Background(), repo, res.ETag)
	if err != nil {
		t.Fatalf("RepoPRListGuard(second): %v", err)
	}
	if !res2.NotModified {
		t.Fatalf("second guard = %+v, want NotModified", res2)
	}
	if !seenAuth {
		t.Error("guard never hit server")
	}
}

func TestAnonymousFallsBackToNoAuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("Authorization = %q, want empty in anonymous mode", auth)
		}
		prListPayload(t, w, []map[string]any{testPR(1, "open", "2026-09-12T12:00:00+08:00")}, 0)
	}))
	defer srv.Close()
	// EnvTokenSource with no matching env vars yields ErrNoToken on this machine.
	p, err := NewProvider(ProviderOptions{
		Token:          EnvTokenSource{EnvVars: []string{"AO_GITCODE_TOKEN_UNIT_TEST_UNSET_VAR"}},
		RESTBase:       srv.URL,
		AllowAnonymous: true,
	})
	if err != nil {
		t.Fatalf("NewProvider(anonymous): %v", err)
	}
	if ok, err := p.SCMCredentialsAvailable(context.Background()); err != nil || !ok {
		t.Fatalf("SCMCredentialsAvailable = %v, %v; want true (anonymous serves public repos)", ok, err)
	}
	repo := mustRepo()
	if _, err := p.ListPRsByRepo(context.Background(), repo, time.Time{}); err != nil {
		t.Fatalf("anonymous ListPRsByRepo: %v", err)
	}
}

func TestNewProviderRequiresTokenWithoutAnonymous(t *testing.T) {
	if _, err := NewProvider(ProviderOptions{}); !errors.Is(err, ErrNoToken) {
		t.Fatalf("NewProvider without token = %v, want ErrNoToken", err)
	}
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		status  int
		body    string
		headers map[string]string
		want    error
	}{
		{http.StatusNotFound, `{"error_message":"not found"}`, nil, ErrNotFound},
		{http.StatusUnauthorized, `{"error_message":"401"}`, nil, ErrAuthFailed},
		{http.StatusForbidden, `{"error_message":"403"}`, nil, ErrAuthFailed},
	}
	for _, tc := range cases {
		resp := &http.Response{StatusCode: tc.status, Header: http.Header{}}
		for k, v := range tc.headers {
			resp.Header.Set(k, v)
		}
		got := classifyError(resp, []byte(tc.body))
		if !errors.Is(got, tc.want) {
			t.Errorf("classifyError(%d) = %v, want %v", tc.status, got, tc.want)
		}
	}

	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{}}
	resp.Header.Set("Retry-After", "30")
	var rl *RateLimitError
	got := classifyError(resp, []byte(`{}`))
	if !errors.As(got, &rl) {
		t.Fatalf("classifyError(429) = %T, want *RateLimitError", got)
	}
	if rl.GetRetryAfter() != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", rl.GetRetryAfter())
	}

	got = classifyError(&http.Response{StatusCode: 500, Header: http.Header{}}, []byte(`{"error_message":"boom"}`))
	if got == nil || !strings.Contains(got.Error(), "boom") {
		t.Errorf("classifyError(500) = %v, want message boom", got)
	}
}

func TestFlexTime(t *testing.T) {
	var ft flexTime
	if err := json.Unmarshal([]byte(`""`), &ft); err != nil || !ft.Time.IsZero() {
		t.Errorf("empty string: %v %v", ft.Time, err)
	}
	if err := json.Unmarshal([]byte(`null`), &ft); err != nil || !ft.Time.IsZero() {
		t.Errorf("null: %v %v", ft.Time, err)
	}
	if err := json.Unmarshal([]byte(`"2026-09-12T12:00:00+08:00"`), &ft); err != nil {
		t.Fatalf("rfc3339: %v", err)
	}
	if ft.Time.Year() != 2026 || ft.Time.Hour() != 12 {
		t.Errorf("rfc3339 parsed to %v, want 2026-09-12 12:00+08:00", ft.Time)
	}
	// Malformed values must not fail the whole observation: they read as zero.
	if err := json.Unmarshal([]byte(`"garbage"`), &ft); err != nil || !ft.Time.IsZero() {
		t.Errorf("garbage: %v %v", ft.Time, err)
	}
}

func TestMergeabilityFromPR(t *testing.T) {
	draft := restPR{Draft: true}
	if m := mergeabilityFromPR(&draft); m.Mergeable || !strings.Contains(strings.Join(m.Blockers, ","), "draft") {
		t.Errorf("draft = %+v, want blocked by draft", m)
	}
	unknown := restPR{}
	if m := mergeabilityFromPR(&unknown); m.State != "unknown" {
		t.Errorf("nil mergeable = %+v, want unknown", m)
	}
	yes := restPR{Mergeable: &[]bool{true}[0]}
	if m := mergeabilityFromPR(&yes); !m.Mergeable {
		t.Errorf("mergeable=true = %+v, want mergeable", m)
	}
	no := restPR{Mergeable: &[]bool{false}[0]}
	if m := mergeabilityFromPR(&no); m.Mergeable || len(m.Blockers) != 1 {
		t.Errorf("mergeable=false = %+v, want blocked", m)
	}
}

func TestIsBotAuthor(t *testing.T) {
	for _, name := range []string{"gitcode-bot", "GitCode-Bot", "dependabot[bot]", "ci_bot"} {
		if !isBotAuthor(name) {
			t.Errorf("isBotAuthor(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"alice", "gcw_93rlw6ed", "renovate"} {
		if isBotAuthor(name) {
			t.Errorf("isBotAuthor(%q) = true, want false", name)
		}
	}
}
