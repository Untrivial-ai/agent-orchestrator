package github

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ---------------------------------------------------------------------------
// Test scaffolding: programmable httptest.Server with route-based dispatch.
// Tests register handlers per "METHOD path" key; unmatched requests fail
// loudly so an accidental extra call surfaces immediately.
// ---------------------------------------------------------------------------

type recordedReq struct {
	Method string
	Path   string
	Header http.Header
	Body   string
}

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

// on registers a handler for one METHOD + path tuple. Path is taken
// verbatim (no query string).
func (f *fakeGH) on(method, path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[method+" "+path] = h
}

func (f *fakeGH) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	hdrCopy := r.Header.Clone()
	f.mu.Lock()
	f.requests = append(f.requests, recordedReq{Method: r.Method, Path: r.URL.Path, Header: hdrCopy, Body: string(body)})
	h, ok := f.handlers[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	if !ok {
		f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
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

func (f *fakeGH) callsTo(method, path string) int {
	n := 0
	for _, r := range f.calls() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

// newProviderForTest builds a Provider that talks only to the fake.
func newProviderForTest(t *testing.T, f *fakeGH) *Provider {
	t.Helper()
	p, err := NewProvider(ProviderOptions{
		Token:      StaticTokenSource("tkn-test"),
		HTTPClient: f.server.Client(),
		RESTBase:   f.server.URL,
		GraphQLURL: f.server.URL + "/graphql",
		UserAgent:  "ao-scm-test",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p
}

func ctx() context.Context { return context.Background() }

func TestAuthenticatedIdentityCachesHumanUser(t *testing.T) {
	f := newFakeGH(t)
	f.on(http.MethodGet, "/user", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "Alice", "type": "User"})
	})
	p := newProviderForTest(t, f)

	first, err := p.AuthenticatedIdentity(ctx())
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.AuthenticatedIdentity(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if first != (ports.SCMIdentity{Login: "Alice", Human: true}) || second != first {
		t.Fatalf("identities = %#v, %#v", first, second)
	}
	if got := f.callsTo(http.MethodGet, "/user"); got != 1 {
		t.Fatalf("GET /user calls = %d, want 1", got)
	}
}

func TestAuthenticatedIdentityClassifiesBot(t *testing.T) {
	f := newFakeGH(t)
	f.on(http.MethodGet, "/user", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "ao-bot", "type": "Bot"})
	})
	p := newProviderForTest(t, f)

	got, err := p.AuthenticatedIdentity(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if got != (ports.SCMIdentity{Login: "ao-bot", Human: false}) {
		t.Fatalf("identity = %#v", got)
	}
}

// ---------------------------------------------------------------------------
// Fixture builders. Each test composes a REST pull + GraphQL response so
// it can pin the exact shape it cares about without sharing global state
// with other tests.
// ---------------------------------------------------------------------------

type prFixture struct {
	owner, repo string
	number      int
	rest        map[string]any
	graphql     map[string]any
	jobLogs     map[int64]string // job_id -> log body
}

func basePRFixture() *prFixture {
	return &prFixture{
		owner:  "octocat",
		repo:   "hello",
		number: 42,
		rest: map[string]any{
			"number":             42,
			"title":              "Found a bug",
			"state":              "open",
			"draft":              false,
			"merged":             false,
			"merged_at":          nil,
			"html_url":           "https://github.com/octocat/hello/pull/42",
			"head":               map[string]any{"ref": "feat/x", "sha": "deadbeef"},
			"base":               map[string]any{"ref": "main"},
			"mergeable":          true,
			"rebaseable":         true,
			"mergeable_state":    "clean",
			"merge_state_status": "CLEAN",
		},
		graphql: map[string]any{
			"data": map[string]any{
				"repository": map[string]any{
					"pullRequest": map[string]any{
						"number":           42,
						"url":              "https://github.com/octocat/hello/pull/42",
						"state":            "OPEN",
						"isDraft":          false,
						"merged":           false,
						"closed":           false,
						"mergeable":        "MERGEABLE",
						"mergeStateStatus": "CLEAN",
						"reviewDecision":   "APPROVED",
						"headRefOid":       "deadbeef",
						"commits": map[string]any{"nodes": []any{
							map[string]any{"commit": map[string]any{
								"oid": "deadbeef",
								"statusCheckRollup": map[string]any{
									"state": "SUCCESS",
									"contexts": map[string]any{
										"nodes": []any{
											map[string]any{
												"__typename": "CheckRun",
												"name":       "build",
												"status":     "COMPLETED",
												"conclusion": "SUCCESS",
												"detailsUrl": "https://github.com/octocat/hello/runs/9001",
												"databaseId": float64(9001),
											},
										},
										"pageInfo": map[string]any{"hasNextPage": false},
									},
								},
							}},
						}},
						"reviewThreads": map[string]any{"nodes": []any{}},
					},
				},
			},
		},
	}
}

// install wires REST + GraphQL handlers onto the fake.
func (f *prFixture) install(t *testing.T, fake *fakeGH) {
	restPath := "/repos/" + f.owner + "/" + f.repo + "/pulls/" + strconv.Itoa(f.number)
	fake.on(http.MethodGet, restPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `W/"v1"`)
		_ = json.NewEncoder(w).Encode(f.rest)
	})
	fake.on(http.MethodPost, "/graphql", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.graphql)
	})
	for jobID, body := range f.jobLogs {
		fake.on(http.MethodGet, "/repos/"+f.owner+"/"+f.repo+"/actions/jobs/"+strconv.FormatInt(jobID, 10)+"/logs", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(body))
		})
	}
}

// prData mutates the nested GraphQL pullRequest map.
func (f *prFixture) prData(mut func(pr map[string]any)) *prFixture {
	repoData := f.graphql["data"].(map[string]any)["repository"].(map[string]any)
	pr := repoData["pullRequest"].(map[string]any)
	mut(pr)
	return f
}

func (f *prFixture) prURL() string {
	return "https://github.com/" + f.owner + "/" + f.repo + "/pull/" + strconv.Itoa(f.number)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestParsePRURL(t *testing.T) {
	cases := []struct {
		name       string
		url        string
		wantOwner  string
		wantRepo   string
		wantNumber int
		wantErr    bool
	}{
		{"web url", "https://github.com/o/r/pull/42", "o", "r", 42, false},
		{"api url", "https://api.github.com/repos/o/r/pulls/42", "o", "r", 42, false},
		{"trailing slash", "https://github.com/o/r/pull/42/", "o", "r", 42, false},
		{"empty", "", "", "", 0, true},
		{"not github", "https://example.com/o/r/pull/1", "", "", 0, true},
		{"bad number", "https://github.com/o/r/pull/abc", "", "", 0, true},
		{"zero", "https://github.com/o/r/pull/0", "", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, r, n, err := parsePRURL(tc.url)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %s/%s#%d", o, r, n)
				}
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("err = %v, want wraps ErrNotFound", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if o != tc.wantOwner || r != tc.wantRepo || n != tc.wantNumber {
				t.Fatalf("got %s/%s#%d, want %s/%s#%d", o, r, n, tc.wantOwner, tc.wantRepo, tc.wantNumber)
			}
		})
	}
}

func TestRestListPullToSCMCarriesHeadRepo(t *testing.T) {
	var pull restListPull
	pull.Number = 7
	pull.State = "open"
	pull.Head.Ref = "feat/x"
	pull.Head.SHA = "deadbeef"
	pull.Head.Repo.FullName = "forker/hello"
	pull.Base.Ref = "main"

	obs := restListPullToSCM(pull)
	if obs.SourceBranch != "feat/x" {
		t.Fatalf("SourceBranch = %q, want feat/x", obs.SourceBranch)
	}
	if obs.HeadRepo != "forker/hello" {
		t.Fatalf("HeadRepo = %q, want forker/hello", obs.HeadRepo)
	}
}

func TestObserve_HappyPath(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	fx.install(t, f)
	p := newProviderForTest(t, f)

	obs, err := p.Observe(ctx(), fx.prURL())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.Fetched {
		t.Fatalf("Fetched = false; want true")
	}
	if obs.URL != fx.prURL() {
		t.Errorf("URL = %q, want %q", obs.URL, fx.prURL())
	}
	if obs.Number != 42 {
		t.Errorf("Number = %d, want 42", obs.Number)
	}
	if obs.Draft || obs.Merged || obs.Closed {
		t.Errorf("Draft/Merged/Closed = %v/%v/%v, want all false", obs.Draft, obs.Merged, obs.Closed)
	}
	if obs.CI != domain.CIPassing {
		t.Errorf("CI = %q, want passing", obs.CI)
	}
	if obs.Review != domain.ReviewApproved {
		t.Errorf("Review = %q, want approved", obs.Review)
	}
	if obs.Mergeability != domain.MergeMergeable {
		t.Errorf("Mergeability = %q, want mergeable", obs.Mergeability)
	}
	if len(obs.Checks) != 1 {
		t.Fatalf("Checks = %#v; want 1 entry", obs.Checks)
	}
	if obs.Checks[0].Status != domain.PRCheckPassed {
		t.Errorf("Checks[0].Status = %q, want passed", obs.Checks[0].Status)
	}
	if obs.Checks[0].LogTail != "" {
		t.Errorf("Checks[0].LogTail = %q; want empty on success", obs.Checks[0].LogTail)
	}
	if obs.Checks[0].CommitHash != "deadbeef" {
		t.Errorf("Checks[0].CommitHash = %q; want deadbeef", obs.Checks[0].CommitHash)
	}
	if len(obs.Comments) != 0 {
		t.Errorf("Comments = %#v; want empty", obs.Comments)
	}
}

func TestObserve_DraftPR(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	fx.rest["draft"] = true
	fx.prData(func(pr map[string]any) { pr["isDraft"] = true })
	fx.install(t, f)
	p := newProviderForTest(t, f)

	obs, err := p.Observe(ctx(), fx.prURL())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.Draft {
		t.Errorf("Draft = false; want true")
	}
}

func TestObserve_MergedPR(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	fx.rest["state"] = "closed"
	fx.rest["merged"] = true
	fx.rest["merged_at"] = "2026-05-30T12:00:00Z"
	fx.prData(func(pr map[string]any) {
		pr["state"] = "MERGED"
		pr["merged"] = true
		pr["closed"] = true
	})
	fx.install(t, f)
	p := newProviderForTest(t, f)

	obs, err := p.Observe(ctx(), fx.prURL())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.Merged {
		t.Errorf("Merged = false; want true")
	}
	if obs.Closed {
		t.Errorf("Closed = true; want false (merged is mutually exclusive)")
	}
}

func TestObserve_ClosedNotMerged(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	fx.rest["state"] = "closed"
	fx.rest["merged"] = false
	fx.rest["merged_at"] = nil
	fx.prData(func(pr map[string]any) {
		pr["state"] = "CLOSED"
		pr["closed"] = true
	})
	fx.install(t, f)
	p := newProviderForTest(t, f)

	obs, err := p.Observe(ctx(), fx.prURL())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.Closed {
		t.Errorf("Closed = false; want true")
	}
	if obs.Merged {
		t.Errorf("Merged = true; want false")
	}
}

func TestObserve_CIStates(t *testing.T) {
	cases := []struct {
		name     string
		nodes    []any
		wantCI   domain.CIState
		wantHead domain.PRCheckStatus
	}{
		{
			name: "passing",
			nodes: []any{
				map[string]any{"__typename": "CheckRun", "name": "build", "status": "COMPLETED", "conclusion": "SUCCESS"},
			},
			wantCI:   domain.CIPassing,
			wantHead: domain.PRCheckPassed,
		},
		{
			name: "failing wins over passing",
			nodes: []any{
				map[string]any{"__typename": "CheckRun", "name": "build", "status": "COMPLETED", "conclusion": "SUCCESS"},
				map[string]any{"__typename": "CheckRun", "name": "lint", "status": "COMPLETED", "conclusion": "FAILURE"},
			},
			wantCI: domain.CIFailing,
		},
		{
			name: "pending blocks passing-only",
			nodes: []any{
				map[string]any{"__typename": "CheckRun", "name": "build", "status": "COMPLETED", "conclusion": "SUCCESS"},
				map[string]any{"__typename": "CheckRun", "name": "test", "status": "IN_PROGRESS"},
			},
			wantCI: domain.CIPending,
		},
		{
			name: "cancelled is failing",
			nodes: []any{
				map[string]any{"__typename": "CheckRun", "name": "deploy", "status": "COMPLETED", "conclusion": "CANCELLED"},
			},
			wantCI: domain.CIFailing,
		},
		{
			name: "legacy statuscontext failure",
			nodes: []any{
				map[string]any{"__typename": "StatusContext", "context": "ci/legacy", "state": "FAILURE", "targetUrl": "https://ci"},
			},
			wantCI: domain.CIFailing,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGH(t)
			fx := basePRFixture()
			fx.prData(func(pr map[string]any) {
				commits := pr["commits"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
				commit := commits["commit"].(map[string]any)
				roll := commit["statusCheckRollup"].(map[string]any)
				roll["contexts"].(map[string]any)["nodes"] = tc.nodes
			})
			fx.install(t, f)
			p := newProviderForTest(t, f)
			obs, err := p.Observe(ctx(), fx.prURL())
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if obs.CI != tc.wantCI {
				t.Fatalf("CI = %q, want %q", obs.CI, tc.wantCI)
			}
		})
	}
}

func TestObserve_LogTailOnFailure(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	fx.jobLogs = map[int64]string{
		9001: strings.Repeat("line\n", 30) + strings.Join([]string{
			"01", "02", "03", "04", "05", "06", "07", "08", "09", "10",
			"11", "12", "13", "14", "15", "16", "17", "18", "19", "FAILED-LAST",
		}, "\n"),
	}
	fx.prData(func(pr map[string]any) {
		commits := pr["commits"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
		commit := commits["commit"].(map[string]any)
		roll := commit["statusCheckRollup"].(map[string]any)
		roll["contexts"].(map[string]any)["nodes"] = []any{
			map[string]any{
				"__typename": "CheckRun",
				"name":       "build",
				"status":     "COMPLETED",
				"conclusion": "FAILURE",
				"detailsUrl": "https://github.com/octocat/hello/runs/9001",
				"databaseId": float64(9001),
			},
		}
	})
	fx.install(t, f)
	p := newProviderForTest(t, f)

	obs, err := p.Observe(ctx(), fx.prURL())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.CI != domain.CIFailing {
		t.Fatalf("CI = %q, want failing", obs.CI)
	}
	if len(obs.Checks) != 1 {
		t.Fatalf("Checks = %#v", obs.Checks)
	}
	tail := obs.Checks[0].LogTail
	if tail == "" {
		t.Fatalf("LogTail empty; expected last %d lines", ciFailureLogTailLines)
	}
	lines := strings.Split(tail, "\n")
	if len(lines) > ciFailureLogTailLines {
		t.Fatalf("LogTail has %d lines, want ≤ %d", len(lines), ciFailureLogTailLines)
	}
	if !strings.Contains(tail, "FAILED-LAST") {
		t.Fatalf("LogTail missing the actual tail content: %q", tail)
	}
}

func TestObserve_LogTailFetchFailureIsBestEffort(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	fx.prData(func(pr map[string]any) {
		commits := pr["commits"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
		commit := commits["commit"].(map[string]any)
		roll := commit["statusCheckRollup"].(map[string]any)
		roll["contexts"].(map[string]any)["nodes"] = []any{
			map[string]any{
				"__typename": "CheckRun",
				"name":       "build",
				"status":     "COMPLETED",
				"conclusion": "FAILURE",
				"databaseId": float64(9001),
			},
		}
	})
	fx.install(t, f)
	// Job-log endpoint returns 500 — the observation must still come back
	// Fetched=true with a synthetic LogTail.
	f.on(http.MethodGet, "/repos/octocat/hello/actions/jobs/9001/logs", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"server exploded"}`, http.StatusInternalServerError)
	})
	p := newProviderForTest(t, f)

	obs, err := p.Observe(ctx(), fx.prURL())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.Fetched {
		t.Fatalf("Fetched = false; log-fetch failures must not flip the whole observation")
	}
	if got := obs.Checks[0].LogTail; !strings.HasPrefix(got, "<log fetch failed:") {
		t.Fatalf("LogTail = %q; want a synthetic <log fetch failed:...> sentinel", got)
	}
}

func TestObserve_MergeabilityStates(t *testing.T) {
	cases := []struct {
		name       string
		mutateREST func(map[string]any)
		mutateGQL  func(map[string]any)
		want       domain.Mergeability
	}{
		{
			name: "mergeable",
			// base fixture is the happy path
			mutateREST: func(m map[string]any) {},
			mutateGQL:  func(m map[string]any) {},
			want:       domain.MergeMergeable,
		},
		{
			name: "conflicting via merge_state_status=DIRTY",
			mutateREST: func(m map[string]any) {
				m["mergeable_state"] = "dirty"
			},
			mutateGQL: func(m map[string]any) {
				m["mergeable"] = "CONFLICTING"
				m["mergeStateStatus"] = "DIRTY"
			},
			want: domain.MergeConflicting,
		},
		{
			name: "blocked by review",
			mutateREST: func(m map[string]any) {
				m["mergeable_state"] = "blocked"
			},
			mutateGQL: func(m map[string]any) {
				m["mergeStateStatus"] = "BLOCKED"
				m["reviewDecision"] = "CHANGES_REQUESTED"
			},
			want: domain.MergeBlocked,
		},
		{
			name: "unstable via merge_state_status=UNSTABLE",
			mutateREST: func(m map[string]any) {
				m["mergeable_state"] = "unstable"
			},
			mutateGQL: func(m map[string]any) {
				m["mergeStateStatus"] = "UNSTABLE"
			},
			want: domain.MergeUnstable,
		},
		{
			name: "unknown when github hasn't computed yet",
			mutateREST: func(m map[string]any) {
				m["mergeable"] = nil
				m["mergeable_state"] = "unknown"
			},
			mutateGQL: func(m map[string]any) {
				m["mergeable"] = "UNKNOWN"
				m["mergeStateStatus"] = "UNKNOWN"
			},
			want: domain.MergeUnknown,
		},
		{
			// Load-bearing aa-18 contract: CI failing must force
			// MergeBlocked even when GitHub still reports the rollup
			// as CLEAN (mergeStateStatus has not yet flipped to
			// UNSTABLE). Without this guard the LCM would think a
			// failing-CI PR is ready to merge.
			name: "ci failing forces blocked even when mergeStateStatus is CLEAN",
			mutateREST: func(m map[string]any) {
				m["mergeable_state"] = "clean"
			},
			mutateGQL: func(m map[string]any) {
				m["mergeable"] = "MERGEABLE"
				m["mergeStateStatus"] = "CLEAN"
				commits := m["commits"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
				commit := commits["commit"].(map[string]any)
				roll := commit["statusCheckRollup"].(map[string]any)
				// databaseId=0 so the provider skips the per-job log
				// fetch (this test is about mergeability, not log tail).
				roll["contexts"].(map[string]any)["nodes"] = []any{
					map[string]any{"__typename": "CheckRun", "name": "lint", "status": "COMPLETED", "conclusion": "FAILURE", "databaseId": float64(0)},
				}
			},
			want: domain.MergeBlocked,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGH(t)
			fx := basePRFixture()
			tc.mutateREST(fx.rest)
			fx.prData(tc.mutateGQL)
			fx.install(t, f)
			p := newProviderForTest(t, f)
			obs, err := p.Observe(ctx(), fx.prURL())
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if obs.Mergeability != tc.want {
				t.Fatalf("Mergeability = %q, want %q", obs.Mergeability, tc.want)
			}
		})
	}
}

func TestObserve_ReviewDecisions(t *testing.T) {
	cases := []struct {
		name     string
		decision any
		want     domain.ReviewDecision
	}{
		{"approved", "APPROVED", domain.ReviewApproved},
		{"changes requested", "CHANGES_REQUESTED", domain.ReviewChangesRequest},
		{"review required", "REVIEW_REQUIRED", domain.ReviewRequired},
		{"none / null", nil, domain.ReviewNone},
		{"unrecognized falls to none", "WAT", domain.ReviewNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGH(t)
			fx := basePRFixture()
			fx.prData(func(pr map[string]any) { pr["reviewDecision"] = tc.decision })
			fx.install(t, f)
			p := newProviderForTest(t, f)
			obs, err := p.Observe(ctx(), fx.prURL())
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if obs.Review != tc.want {
				t.Fatalf("Review = %q, want %q", obs.Review, tc.want)
			}
		})
	}
}

func TestObserve_BotAuthorFiltering(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	fx.prData(func(pr map[string]any) {
		pr["reviewThreads"] = map[string]any{"nodes": []any{
			map[string]any{
				"id":         "T1",
				"isResolved": false,
				"comments": map[string]any{"nodes": []any{
					map[string]any{
						"id":     "C1",
						"body":   "real human concern",
						"path":   "foo/bar.go",
						"line":   float64(12),
						"url":    "https://github.com/octocat/hello/pull/42#discussion_r1",
						"author": map[string]any{"login": "alice", "__typename": "User"},
					},
				}},
			},
			// Bot thread — must be filtered out entirely.
			map[string]any{
				"id":         "T2",
				"isResolved": false,
				"comments": map[string]any{"nodes": []any{
					map[string]any{
						"id":     "C2",
						"body":   "dependabot says update",
						"path":   "go.mod",
						"line":   float64(1),
						"author": map[string]any{"login": "dependabot[bot]", "__typename": "Bot"},
					},
				}},
			},
			// Resolved thread — must also be filtered out.
			map[string]any{
				"id":         "T3",
				"isResolved": true,
				"comments": map[string]any{"nodes": []any{
					map[string]any{"id": "C3", "body": "lgtm now", "author": map[string]any{"login": "bob", "__typename": "User"}},
				}},
			},
			// Login like "robothon" — must NOT be treated as a bot (aa-18
			// flagged the strings.Contains(login,"bot") fallback as a
			// false-positive magnet; we use the typed signal only).
			map[string]any{
				"id":         "T4",
				"isResolved": false,
				"comments": map[string]any{"nodes": []any{
					map[string]any{"id": "C4", "body": "actual comment", "path": "a.go", "line": float64(3), "author": map[string]any{"login": "robothon", "__typename": "User"}},
				}},
			},
		}}
	})
	fx.install(t, f)
	p := newProviderForTest(t, f)

	obs, err := p.Observe(ctx(), fx.prURL())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Comments) != 2 {
		t.Fatalf("Comments = %#v; want exactly 2 (alice + robothon)", obs.Comments)
	}
	authors := []string{obs.Comments[0].Author, obs.Comments[1].Author}
	if !contains(authors, "alice") {
		t.Errorf("missing alice's comment: %v", authors)
	}
	if !contains(authors, "robothon") {
		t.Errorf("robothon misclassified as bot: %v", authors)
	}
	for _, c := range obs.Comments {
		if c.Resolved {
			t.Errorf("comment %q marked Resolved=true; observation set is unresolved-only", c.ID)
		}
	}
	if obs.Comments[0].ThreadID != "T1" || obs.Comments[0].URL != "https://github.com/octocat/hello/pull/42#discussion_r1" {
		t.Fatalf("first comment lost URL/thread metadata: %#v", obs.Comments[0])
	}
}

// TestObserve_AllBotThreadsYieldsNilComments pins that a PR whose review
// threads are 100% bot-authored produces Comments == nil but a fully
// fetched observation. The PR Manager downstream must handle a nil
// Comments slice without panicking, and Fetched=true means lifecycle
// can still apply the rest of the observation.
func TestObserve_AllBotThreadsYieldsNilComments(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	fx.prData(func(pr map[string]any) {
		pr["reviewThreads"] = map[string]any{"nodes": []any{
			map[string]any{
				"id":         "T-bot-only",
				"isResolved": false,
				"comments": map[string]any{"nodes": []any{
					map[string]any{"id": "C1", "body": "auto-merged", "author": map[string]any{"login": "dependabot[bot]", "__typename": "Bot"}},
					map[string]any{"id": "C2", "body": "renovate", "author": map[string]any{"login": "renovate[bot]", "__typename": "Bot"}},
				}},
			},
		}}
	})
	fx.install(t, f)
	p := newProviderForTest(t, f)

	obs, err := p.Observe(ctx(), fx.prURL())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !obs.Fetched {
		t.Fatalf("Fetched = false; want true even when all comments are bots")
	}
	if len(obs.Comments) != 0 {
		t.Fatalf("Comments = %#v; want empty (all authors are bots)", obs.Comments)
	}
}

func contains(ss []string, x string) bool {
	for _, s := range ss {
		if s == x {
			return true
		}
	}
	return false
}

func TestObserve_ETag304Cached(t *testing.T) {
	// Second call to the REST pull endpoint must send If-None-Match and
	// reuse the cached body, while still completing the rest of the
	// observation (GraphQL is always re-fetched — there's no cache for it).
	f := newFakeGH(t)
	fx := basePRFixture()
	var restHits int
	restPath := "/repos/" + fx.owner + "/" + fx.repo + "/pulls/" + strconv.Itoa(fx.number)
	f.on(http.MethodGet, restPath, func(w http.ResponseWriter, r *http.Request) {
		restHits++
		if r.Header.Get("If-None-Match") == `W/"v1"` {
			w.Header().Set("ETag", `W/"v1"`)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", `W/"v1"`)
		_ = json.NewEncoder(w).Encode(fx.rest)
	})
	f.on(http.MethodPost, "/graphql", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fx.graphql)
	})
	p := newProviderForTest(t, f)

	first, err := p.Observe(ctx(), fx.prURL())
	if err != nil {
		t.Fatalf("first Observe: %v", err)
	}
	second, err := p.Observe(ctx(), fx.prURL())
	if err != nil {
		t.Fatalf("second Observe: %v", err)
	}
	if first.CI != second.CI || first.Mergeability != second.Mergeability {
		t.Fatalf("304 replay diverged: %#v vs %#v", first, second)
	}
	if !second.Fetched {
		t.Fatalf("second Fetched = false despite 304 hit")
	}
	if restHits != 2 {
		t.Fatalf("expected 2 hits to the REST pull endpoint (one fresh, one 304), got %d", restHits)
	}
	// And: the second call must have actually sent If-None-Match.
	var sentConditional bool
	for _, r := range f.calls() {
		if r.Method == http.MethodGet && r.Path == restPath && r.Header.Get("If-None-Match") != "" {
			sentConditional = true
			break
		}
	}
	if !sentConditional {
		t.Fatalf("second call did not send If-None-Match; ETag cache is broken")
	}
}

func TestObserve_PrimaryRateLimit(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	reset := time.Now().Add(2 * time.Minute).Unix()
	f.on(http.MethodGet, "/repos/octocat/hello/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset, 10))
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
	})
	// GraphQL would never be reached in this scenario.
	p := newProviderForTest(t, f)

	obs, err := p.Observe(ctx(), fx.prURL())
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	if obs.Fetched {
		t.Fatalf("Fetched = true on rate-limit error; want false")
	}
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
	if rle.ResetAt.Unix() != reset {
		t.Fatalf("ResetAt = %d, want %d", rle.ResetAt.Unix(), reset)
	}
}

func TestObserve_SecondaryRateLimit(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	f.on(http.MethodGet, "/repos/octocat/hello/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		http.Error(w, `{"message":"You have exceeded a secondary rate limit"}`, http.StatusForbidden)
	})
	p := newProviderForTest(t, f)

	obs, err := p.Observe(ctx(), fx.prURL())
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	if obs.Fetched {
		t.Fatalf("Fetched = true on rate-limit error")
	}
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
	if rle.RetryAfter != 30*time.Second {
		t.Fatalf("RetryAfter = %v, want 30s", rle.RetryAfter)
	}
}

func TestObserve_AuthFailedSurfacesAsErrAuthFailed(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	f.on(http.MethodGet, "/repos/octocat/hello/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
	})
	p := newProviderForTest(t, f)

	obs, err := p.Observe(ctx(), fx.prURL())
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("err = %v, want ErrAuthFailed", err)
	}
	if obs.Fetched {
		t.Fatalf("Fetched = true on auth-failed; want false")
	}
}

func TestObserve_MalformedJSONIsNotFetched(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	f.on(http.MethodGet, "/repos/octocat/hello/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{not valid json`))
	})
	p := newProviderForTest(t, f)

	obs, err := p.Observe(ctx(), fx.prURL())
	if err == nil {
		t.Fatalf("expected decode error, got nil")
	}
	if obs.Fetched {
		t.Fatalf("Fetched = true on decode failure; want false")
	}
}

func TestObserve_NetworkErrorIsNotFetched(t *testing.T) {
	// Point the provider at a closed server to force a transport error.
	f := newFakeGH(t)
	p, err := NewProvider(ProviderOptions{
		Token:      StaticTokenSource("tkn"),
		HTTPClient: &http.Client{Timeout: 200 * time.Millisecond},
		RESTBase:   "http://127.0.0.1:1", // reserved port; refuses connections
		GraphQLURL: "http://127.0.0.1:1/graphql",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	obs, observeErr := p.Observe(ctx(), "https://github.com/o/r/pull/1")
	if observeErr == nil {
		t.Fatalf("expected network error, got nil")
	}
	if obs.Fetched {
		t.Fatalf("Fetched = true on network error; want false")
	}
	// Reference f so the test linter doesn't flag it; we don't use the
	// fake here but the helper is the canonical way to scope a test.
	_ = f
}

func TestObserve_TokenInjectedAsBearer(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	fx.install(t, f)
	p := newProviderForTest(t, f)
	if _, err := p.Observe(ctx(), fx.prURL()); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	for _, r := range f.calls() {
		if got := r.Header.Get("Authorization"); got != "Bearer tkn-test" {
			t.Fatalf("Authorization header on %s %s = %q, want Bearer tkn-test", r.Method, r.Path, got)
		}
	}
}

func TestStaticTokenSourceRejectsBlank(t *testing.T) {
	if _, err := StaticTokenSource("").Token(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
	if _, err := StaticTokenSource("   ").Token(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("blank-with-spaces: err = %v, want ErrNoToken", err)
	}
}

func TestGHTokenSourceUsesInjectedHook(t *testing.T) {
	calls := 0
	src := &GHTokenSource{
		GH: func(ctx context.Context) (string, error) {
			calls++
			return "from-gh\n", nil
		},
		TokenTTL: time.Hour,
	}
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "from-gh" {
		t.Fatalf("Token = %q, want %q", tok, "from-gh")
	}
	// Second call within TTL must be cached.
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if calls != 1 {
		t.Fatalf("GH called %d times; want 1 (cache miss only)", calls)
	}
	// Invalidate and the next call must re-run.
	src.InvalidateToken()
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("third Token: %v", err)
	}
	if calls != 2 {
		t.Fatalf("after invalidate, GH called %d times; want 2", calls)
	}
}

// TestObserve_CIPaginationDegradesPassingToUnknown pins the safety
// guard for the GraphQL contexts pagination: when GitHub reports
// hasNextPage=true, a visible "all passing" set could be hiding a
// failure on the next page. The provider must degrade Passing /
// Pending / Unknown to CIUnknown so downstream code doesn't treat a
// possibly-broken PR as ready. A FAILING verdict from the visible
// page is still safe (and must NOT degrade).
func TestObserve_CIPaginationDegradesPassingToUnknown(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	fx.prData(func(pr map[string]any) {
		commits := pr["commits"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
		commit := commits["commit"].(map[string]any)
		roll := commit["statusCheckRollup"].(map[string]any)
		ctxs := roll["contexts"].(map[string]any)
		// One visible passing context, but hasNextPage=true so a
		// failure could be hiding in the unseen tail.
		ctxs["nodes"] = []any{
			map[string]any{"__typename": "CheckRun", "name": "build", "status": "COMPLETED", "conclusion": "SUCCESS"},
		}
		ctxs["pageInfo"] = map[string]any{"hasNextPage": true}
	})
	fx.install(t, f)
	p := newProviderForTest(t, f)

	obs, err := p.Observe(ctx(), fx.prURL())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.CI != domain.CIUnknown {
		t.Fatalf("CI = %q, want CIUnknown (hasNextPage must degrade passing)", obs.CI)
	}
}

func TestObserve_CIPaginationDoesNotMaskKnownFailure(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	fx.prData(func(pr map[string]any) {
		commits := pr["commits"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
		commit := commits["commit"].(map[string]any)
		roll := commit["statusCheckRollup"].(map[string]any)
		ctxs := roll["contexts"].(map[string]any)
		ctxs["nodes"] = []any{
			map[string]any{"__typename": "CheckRun", "name": "lint", "status": "COMPLETED", "conclusion": "FAILURE", "databaseId": float64(0)},
		}
		ctxs["pageInfo"] = map[string]any{"hasNextPage": true}
	})
	fx.install(t, f)
	p := newProviderForTest(t, f)

	obs, err := p.Observe(ctx(), fx.prURL())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.CI != domain.CIFailing {
		t.Fatalf("CI = %q, want CIFailing (a known failure on page 1 must NOT degrade)", obs.CI)
	}
}

// TestObserve_StatusContextLegacyHasNoLogTail pins that we do NOT try to
// fetch a job log for a legacy commit-status row (those have no Actions
// job ID, so /actions/jobs/0/logs would 404 if we let the path leak).
func TestObserve_StatusContextLegacyHasNoLogTail(t *testing.T) {
	f := newFakeGH(t)
	fx := basePRFixture()
	fx.prData(func(pr map[string]any) {
		commits := pr["commits"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
		commit := commits["commit"].(map[string]any)
		roll := commit["statusCheckRollup"].(map[string]any)
		roll["contexts"].(map[string]any)["nodes"] = []any{
			map[string]any{"__typename": "StatusContext", "context": "ci/legacy", "state": "FAILURE", "targetUrl": "https://ci"},
		}
	})
	fx.install(t, f)
	p := newProviderForTest(t, f)

	obs, err := p.Observe(ctx(), fx.prURL())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.CI != domain.CIFailing {
		t.Fatalf("CI = %q, want failing", obs.CI)
	}
	if len(obs.Checks) != 1 {
		t.Fatalf("Checks = %#v", obs.Checks)
	}
	if obs.Checks[0].LogTail != "" {
		t.Fatalf("LogTail = %q; want empty (StatusContext has no job log)", obs.Checks[0].LogTail)
	}
	if f.callsTo(http.MethodGet, "/repos/octocat/hello/actions/jobs/0/logs") != 0 {
		t.Fatalf("unexpected attempt to fetch a /actions/jobs/0/logs URL")
	}
}

// TestObserve_AssertsPRObservationShape is a belt-and-braces compile-time
// guard that PRObservation has the fields we depend on. If the port adds
// or renames a field, this test fails to compile rather than failing at
// runtime.
func TestObserve_AssertsPRObservationShape(t *testing.T) {
	var o ports.PRObservation
	o.Fetched = true
	o.URL = ""
	o.Number = 0
	o.Draft = false
	o.Merged = false
	o.Closed = false
	o.CI = domain.CIUnknown
	o.Review = domain.ReviewNone
	o.Mergeability = domain.MergeUnknown
	o.Checks = nil
	o.Comments = nil
	_ = o
}

func TestSCMChecksFromGraphQL_StatusContextUsesState(t *testing.T) {
	pr := map[string]any{
		"commits": map[string]any{"nodes": []any{
			map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{
				"contexts": map[string]any{"nodes": []any{
					map[string]any{"__typename": "StatusContext", "context": "legacy", "state": "FAILURE", "targetUrl": "https://ci/legacy"},
					map[string]any{"__typename": "CheckRun", "name": "actions", "status": "COMPLETED", "conclusion": "SUCCESS", "detailsUrl": "https://ci/actions"},
				}},
			}}},
		}},
	}
	checks := scmChecksFromGraphQL(pr)
	if len(checks) != 2 {
		t.Fatalf("checks = %d, want 2: %+v", len(checks), checks)
	}
	if checks[0].Name != "legacy" || checks[0].Status != string(domain.PRCheckFailed) || checks[0].Conclusion != "failure" {
		t.Fatalf("legacy StatusContext not normalized from state: %+v", checks[0])
	}
	if checks[1].Name != "actions" || checks[1].Status != string(domain.PRCheckPassed) || checks[1].Conclusion != "success" {
		t.Fatalf("CheckRun not normalized from conclusion: %+v", checks[1])
	}
}

func TestSCMThreadFromGraphQLMarksThreadBotOnlyWhenAllCommentsAreBots(t *testing.T) {
	mixed := scmThreadFromGraphQL(map[string]any{
		"id":         "T-mixed",
		"path":       "main.go",
		"line":       float64(12),
		"isResolved": false,
		"comments": map[string]any{"nodes": []any{
			map[string]any{"id": "C-human", "body": "please fix", "author": map[string]any{"login": "alice", "__typename": "User"}},
			map[string]any{"id": "C-bot", "body": "automated note", "author": map[string]any{"login": "review-bot", "__typename": "Bot"}},
		}},
	})
	if mixed.IsBot {
		t.Fatalf("mixed human+bot thread marked as bot: %+v", mixed)
	}
	if len(mixed.Comments) != 2 || mixed.Comments[0].IsBot || !mixed.Comments[1].IsBot {
		t.Fatalf("comment bot flags not preserved on mixed thread: %+v", mixed.Comments)
	}

	allBot := scmThreadFromGraphQL(map[string]any{
		"id":         "T-bot",
		"path":       "main.go",
		"line":       float64(12),
		"isResolved": false,
		"comments": map[string]any{"nodes": []any{
			map[string]any{"id": "C-bot-1", "body": "automated note", "author": map[string]any{"login": "review-bot", "__typename": "Bot"}},
			map[string]any{"id": "C-bot-2", "body": "more automation", "author": map[string]any{"login": "other-bot", "__typename": "Bot"}},
		}},
	})
	if !allBot.IsBot {
		t.Fatalf("all-bot thread not marked as bot: %+v", allBot)
	}
}

func TestSCMObservationUsesRollupStateWhenContextsPaginated(t *testing.T) {
	fx := basePRFixture()
	var pr map[string]any
	fx.prData(func(m map[string]any) {
		pr = m
		commits := m["commits"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
		commit := commits["commit"].(map[string]any)
		roll := commit["statusCheckRollup"].(map[string]any)
		roll["state"] = "FAILURE"
		ctxs := roll["contexts"].(map[string]any)
		ctxs["nodes"] = []any{
			map[string]any{"__typename": "CheckRun", "name": "visible-pass", "status": "COMPLETED", "conclusion": "SUCCESS"},
		}
		ctxs["pageInfo"] = map[string]any{"hasNextPage": true}
	})
	obs := scmObservationFromGraphQL(ports.SCMPRRef{Repo: ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "octocat", Name: "hello", Repo: "octocat/hello"}, Number: 42}, pr)
	if obs.CI.Summary != string(domain.CIFailing) {
		t.Fatalf("observer CI summary = %q, want failing from aggregate rollup state", obs.CI.Summary)
	}
}

func TestSCMBatchQueryRequestsStablePullRequestID(t *testing.T) {
	query, _ := buildSCMBatchQuery([]ports.SCMPRRef{{
		Repo:   ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "octocat", Name: "hello"},
		Number: 42,
	}})
	if !strings.Contains(query, "number id url") {
		t.Fatalf("batch query does not request the stable pull request id:\n%s", query)
	}
}

func TestSCMObservationCarriesStableIDAndRequestedURLAlias(t *testing.T) {
	fx := basePRFixture()
	var pr map[string]any
	fx.prData(func(m map[string]any) {
		pr = m
		m["id"] = "PR_kwDOStable"
		m["url"] = "https://github.com/new-owner/hello/pull/42"
	})
	ref := ports.SCMPRRef{
		Repo:   ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "old-owner", Name: "hello", Repo: "old-owner/hello"},
		Number: 42,
		URL:    "https://github.com/old-owner/hello/pull/42",
	}

	obs := scmObservationFromGraphQL(ref, pr)
	if obs.PR.ProviderID != "PR_kwDOStable" {
		t.Fatalf("ProviderID = %q, want PR_kwDOStable", obs.PR.ProviderID)
	}
	if obs.PR.URLAlias != ref.URL {
		t.Fatalf("URLAlias = %q, want %s", obs.PR.URLAlias, ref.URL)
	}
	if obs.Repo != "new-owner/hello" {
		t.Fatalf("Repo = %q, want canonical new-owner/hello", obs.Repo)
	}
}

func TestSCMMergeabilityBlocksReviewRequiredAndDraft(t *testing.T) {
	cases := []struct {
		name        string
		review      string
		draft       bool
		wantBlocker string
	}{
		{name: "review required", review: string(domain.ReviewRequired), wantBlocker: "review_required"},
		{name: "draft", review: string(domain.ReviewApproved), draft: true, wantBlocker: "draft"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeabilityObservation("MERGEABLE", "CLEAN", string(domain.CIPassing), tc.review, tc.draft)
			if got.State != string(domain.MergeBlocked) || got.Mergeable {
				t.Fatalf("mergeability = %+v, want blocked and not mergeable", got)
			}
			if !contains(got.Blockers, tc.wantBlocker) {
				t.Fatalf("blockers = %v, want %q", got.Blockers, tc.wantBlocker)
			}
		})
	}
}

func TestFetchPullRequestsDoesNotFallbackWhenContextPageComplete(t *testing.T) {
	fake := newFakeGH(t)
	fx := basePRFixture()
	var pr map[string]any
	fx.prData(func(m map[string]any) { pr = m })
	fake.on(http.MethodPost, "/graphql", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "contexts(first:20)") {
			t.Fatalf("batch query should request 20 contexts, body=%s", body)
		}
		if !strings.Contains(string(body), "pageInfo{ hasNextPage endCursor }") {
			t.Fatalf("batch query should request endCursor for fallback, body=%s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"pr0": map[string]any{"pullRequest": pr}},
		})
	})
	p := newProviderForTest(t, fake)
	obs, err := p.FetchPullRequests(ctx(), []ports.SCMPRRef{{Repo: ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "octocat", Name: "hello", Repo: "octocat/hello"}, Number: 42}})
	if err != nil {
		t.Fatalf("FetchPullRequests: %v", err)
	}
	if got := fake.callsTo(http.MethodPost, "/graphql"); got != 1 {
		t.Fatalf("graphql calls = %d, want no fallback", got)
	}
	if len(obs) != 1 || len(obs[0].CI.Checks) != 1 || obs[0].CI.Summary != string(domain.CIPassing) {
		t.Fatalf("observation = %#v", obs)
	}
}

func TestFetchPullRequestsFetchesRemainingCheckContexts(t *testing.T) {
	fake := newFakeGH(t)
	fx := basePRFixture()
	var pr map[string]any
	fx.prData(func(m map[string]any) {
		pr = m
		commits := m["commits"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
		commit := commits["commit"].(map[string]any)
		roll := commit["statusCheckRollup"].(map[string]any)
		roll["state"] = "FAILURE"
		ctxs := roll["contexts"].(map[string]any)
		ctxs["nodes"] = []any{
			map[string]any{"__typename": "CheckRun", "name": "visible-pass", "status": "COMPLETED", "conclusion": "SUCCESS"},
		}
		ctxs["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": "cursor-1"}
	})
	fake.on(http.MethodPost, "/graphql", func(w http.ResponseWriter, r *http.Request) {
		call := fake.callsTo(http.MethodPost, "/graphql")
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch call {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"pr0": map[string]any{"pullRequest": pr}},
			})
		case 2:
			if !strings.Contains(string(body), `after:\"cursor-1\"`) && !strings.Contains(string(body), `after:"cursor-1"`) {
				t.Fatalf("fallback query missing cursor, body=%s", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"repo": map[string]any{"pullRequest": map[string]any{
					"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{
						"contexts": map[string]any{
							"nodes": []any{
								map[string]any{"__typename": "CheckRun", "name": "hidden-fail", "status": "COMPLETED", "conclusion": "FAILURE"},
							},
							"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
						},
					}}}}},
				}}},
			})
		default:
			t.Fatalf("unexpected graphql call %d", call)
		}
	})
	p := newProviderForTest(t, fake)
	obs, err := p.FetchPullRequests(ctx(), []ports.SCMPRRef{{Repo: ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "octocat", Name: "hello", Repo: "octocat/hello"}, Number: 42}})
	if err != nil {
		t.Fatalf("FetchPullRequests: %v", err)
	}
	if got := fake.callsTo(http.MethodPost, "/graphql"); got != 2 {
		t.Fatalf("graphql calls = %d, want batch + fallback", got)
	}
	if len(obs) != 1 {
		t.Fatalf("observations = %#v", obs)
	}
	if obs[0].CI.Summary != string(domain.CIFailing) {
		t.Fatalf("CI summary = %q, want aggregate failing", obs[0].CI.Summary)
	}
	if len(obs[0].CI.Checks) != 2 || len(obs[0].CI.FailedChecks) != 1 || obs[0].CI.FailedChecks[0].Name != "hidden-fail" {
		t.Fatalf("checks not completed from fallback: %#v failed=%#v", obs[0].CI.Checks, obs[0].CI.FailedChecks)
	}
}

func TestFetchPullRequestsFailsWhenCheckContextFallbackFails(t *testing.T) {
	fake := newFakeGH(t)
	fx := basePRFixture()
	var pr map[string]any
	fx.prData(func(m map[string]any) {
		pr = m
		commits := m["commits"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
		commit := commits["commit"].(map[string]any)
		ctxs := commit["statusCheckRollup"].(map[string]any)["contexts"].(map[string]any)
		ctxs["pageInfo"] = map[string]any{"hasNextPage": true, "endCursor": "cursor-1"}
	})
	fake.on(http.MethodPost, "/graphql", func(w http.ResponseWriter, r *http.Request) {
		call := fake.callsTo(http.MethodPost, "/graphql")
		if call == 1 {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"pr0": map[string]any{"pullRequest": pr}},
			})
			return
		}
		http.Error(w, `{"message":"graphql down"}`, http.StatusInternalServerError)
	})
	p := newProviderForTest(t, fake)
	if _, err := p.FetchPullRequests(ctx(), []ports.SCMPRRef{{Repo: ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "octocat", Name: "hello", Repo: "octocat/hello"}, Number: 42}}); err == nil {
		t.Fatal("FetchPullRequests error = nil, want fallback failure")
	}
}

func TestFetchReviewThreadsUsesLatestWindowWithoutFallbackWhenOldestResolved(t *testing.T) {
	fake := newFakeGH(t)
	fake.on(http.MethodPost, "/graphql", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "reviewThreads(last:50, before:null)") {
			t.Fatalf("review query should fetch latest 50, body=%s", body)
		}
		if !strings.Contains(string(body), "reviews(last:20, states:[APPROVED,CHANGES_REQUESTED])") {
			t.Fatalf("review query should fetch decisive review summaries, body=%s", body)
		}
		if !strings.Contains(string(body), "submittedAt body commit") {
			t.Fatalf("review query should request the review body and commit, body=%s", body)
		}
		if !strings.Contains(string(body), "comments(first:5)") {
			t.Fatalf("review query should cap comments per thread, body=%s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"repo": map[string]any{"pullRequest": map[string]any{
				"reviewDecision": "CHANGES_REQUESTED",
				"reviewSummaries": map[string]any{"nodes": []any{map[string]any{
					"id":          "review-1",
					"state":       "CHANGES_REQUESTED",
					"url":         "https://github.com/o/r/pull/1#pullrequestreview-1",
					"submittedAt": "2026-06-15T00:00:00Z",
					"body":        "please address the failing test",
					"commit":      map[string]any{"oid": "head-sha-1"},
					"author":      map[string]any{"login": "alice", "__typename": "User"},
				}}},
				"reviewThreads": map[string]any{
					"nodes": []any{map[string]any{"id": "latest-resolved", "path": "main.go", "line": 1, "isResolved": true, "comments": map[string]any{"nodes": []any{map[string]any{
						"id": "comment-1", "body": "fix", "url": "https://github.com/o/r/pull/1#discussion_r1", "author": map[string]any{"login": "alice", "__typename": "User"},
					}}}}},
					"pageInfo": map[string]any{"hasPreviousPage": true, "startCursor": "latest-start"},
				},
			}}},
		})
	})
	p := newProviderForTest(t, fake)
	review, err := p.FetchReviewThreads(ctx(), ports.SCMPRRef{Repo: ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "o", Name: "r", Repo: "o/r"}, Number: 1})
	if err != nil {
		t.Fatalf("FetchReviewThreads: %v", err)
	}
	if got := fake.callsTo(http.MethodPost, "/graphql"); got != 1 {
		t.Fatalf("graphql calls = %d, want no fallback when oldest latest thread is resolved", got)
	}
	if !review.Partial {
		t.Fatalf("review Partial = false, want true because older pages exist")
	}
	if len(review.Threads) != 1 || review.Threads[0].ID != "latest-resolved" {
		t.Fatalf("threads = %#v", review.Threads)
	}
	if len(review.Reviews) != 1 || review.Reviews[0].Author != "alice" || review.Reviews[0].URL != "https://github.com/o/r/pull/1#pullrequestreview-1" || review.Reviews[0].Body != "please address the failing test" || review.Reviews[0].TargetSHA != "head-sha-1" {
		t.Fatalf("reviews = %#v", review.Reviews)
	}
	if len(review.Threads[0].Comments) != 1 || review.Threads[0].Comments[0].URL != "https://github.com/o/r/pull/1#discussion_r1" {
		t.Fatalf("thread comments = %#v", review.Threads[0].Comments)
	}
}

func TestFetchReviewThreadsFetchesOneOlderPageWhenOldestUnresolved(t *testing.T) {
	fake := newFakeGH(t)
	fake.on(http.MethodPost, "/graphql", func(w http.ResponseWriter, r *http.Request) {
		call := fake.callsTo(http.MethodPost, "/graphql")
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch call {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"repo": map[string]any{"pullRequest": map[string]any{
					"reviewDecision": "CHANGES_REQUESTED",
					"reviewThreads": map[string]any{
						"nodes":    []any{map[string]any{"id": "latest-unresolved", "path": "main.go", "line": 2, "isResolved": false, "comments": map[string]any{"nodes": []any{}}}},
						"pageInfo": map[string]any{"hasPreviousPage": true, "startCursor": "latest-start"},
					},
				}}},
			})
		case 2:
			if !strings.Contains(string(body), `before:\"latest-start\"`) && !strings.Contains(string(body), `before:"latest-start"`) {
				t.Fatalf("older review query missing before cursor, body=%s", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"repo": map[string]any{"pullRequest": map[string]any{
					"reviewDecision": "CHANGES_REQUESTED",
					"reviewThreads": map[string]any{
						"nodes":    []any{map[string]any{"id": "older", "path": "old.go", "line": 1, "isResolved": false, "comments": map[string]any{"nodes": []any{}}}},
						"pageInfo": map[string]any{"hasPreviousPage": true, "startCursor": "older-start"},
					},
				}}},
			})
		default:
			t.Fatalf("unexpected graphql call %d", call)
		}
	})
	p := newProviderForTest(t, fake)
	review, err := p.FetchReviewThreads(ctx(), ports.SCMPRRef{Repo: ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "o", Name: "r", Repo: "o/r"}, Number: 1})
	if err != nil {
		t.Fatalf("FetchReviewThreads: %v", err)
	}
	if got := fake.callsTo(http.MethodPost, "/graphql"); got != githubReviewThreadMaxPages {
		t.Fatalf("graphql calls = %d, want capped at %d", got, githubReviewThreadMaxPages)
	}
	if !review.Partial {
		t.Fatalf("review Partial = false, want true because pagination remains bounded")
	}
	if len(review.Threads) != 2 || review.Threads[0].ID != "older" || review.Threads[1].ID != "latest-unresolved" {
		t.Fatalf("threads order = %#v", review.Threads)
	}
}

// installCheckRunsFake serves the per-commit check-runs endpoint exactly the way
// GitHub does for ETag purposes: 304 when the representation GitHub would return
// for the caller's per_page is unchanged, otherwise 200 with a fresh ETag. The
// authoritative full run list is held behind mu so a test can pivot one run's
// status between calls. This models the real GitHub contract, where the ETag
// covers only the returned page (which is why the old per_page=1 bug existed).
func installCheckRunsFake(t *testing.T, f *fakeGH, owner, repo, sha string, mu *sync.Mutex, runs *[]map[string]any) {
	t.Helper()
	path := repoPath(owner, repo, "commits", sha, "check-runs")
	f.on(http.MethodGet, path, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		perPage := 100
		if q := r.URL.Query().Get("per_page"); q != "" {
			if n, err := strconv.Atoi(q); err == nil && n > 0 {
				perPage = n
			}
		}
		shown := *runs
		if perPage < len(shown) {
			shown = (*runs)[:perPage]
		}
		body, _ := json.Marshal(map[string]any{"total_count": len(*runs), "check_runs": shown})
		sum := sha256.Sum256(body)
		etag := fmt.Sprintf(`W/"%x"`, sum)
		if r.Header.Get("If-None-Match") == etag {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", etag)
		_, _ = w.Write(body)
	})
}

// TestCommitChecksGuard_OtherRunTransitionInvalidatesGuard is a regression test
// for the "completed checks remain pending" bug: the guard used to condition on
// per_page=1, so GitHub's ETag only covered the first returned run. When a
// DIFFERENT workflow finished, the single-item representation was unchanged and
// GitHub answered 304, leaving AO stuck on "Checks running" until the five-minute
// forced refresh. The guard must now track the complete run set.
func TestCommitChecksGuard_OtherRunTransitionInvalidatesGuard(t *testing.T) {
	f := newFakeGH(t)
	const owner, repo, sha = "octocat", "hello", "abc123"
	var mu sync.Mutex
	runs := []map[string]any{
		{"id": 1, "name": "lint", "status": "completed", "conclusion": "success"},
		{"id": 2, "name": "test", "status": "in_progress", "conclusion": ""},
	}
	installCheckRunsFake(t, f, owner, repo, sha, &mu, &runs)
	p := newProviderForTest(t, f)
	repoRef := ports.SCMRepo{Provider: "github", Host: "github.com", Owner: owner, Name: repo, Repo: owner + "/" + repo}

	// First poll: no cached ETag, must be a fresh fetch -> NotModified=false.
	res, err := p.CommitChecksGuard(ctx(), repoRef, sha, "")
	if err != nil {
		t.Fatalf("first CommitChecksGuard: %v", err)
	}
	if res.NotModified {
		t.Fatal("first poll reported NotModified with no cached ETag")
	}
	if res.ETag == "" {
		t.Fatal("first poll returned an empty ETag")
	}

	// Second poll: representation unchanged -> 304 -> NotModified=true.
	res, err = p.CommitChecksGuard(ctx(), repoRef, sha, res.ETag)
	if err != nil {
		t.Fatalf("unchanged CommitChecksGuard: %v", err)
	}
	if !res.NotModified {
		t.Fatal("unchanged check-runs representation reported NotModified=false")
	}

	// Third poll: the FIRST returned run is stable, but a DIFFERENT run
	// transitions in_progress -> completed. With the old per_page=1 guard this
	// returned 304 and AO kept "Checks running". With the complete-representation
	// guard the full body changed, so GitHub sends a fresh ETag -> NotModified=false.
	mu.Lock()
	runs = []map[string]any{
		{"id": 1, "name": "lint", "status": "completed", "conclusion": "success"},
		{"id": 2, "name": "test", "status": "completed", "conclusion": "success"},
	}
	mu.Unlock()
	res, err = p.CommitChecksGuard(ctx(), repoRef, sha, res.ETag)
	if err != nil {
		t.Fatalf("transitioned CommitChecksGuard: %v", err)
	}
	if res.NotModified {
		t.Fatal("guard stayed NotModified after a non-first run transitioned (per_page=1 regression)")
	}
}

// TestCommitChecksGuard_PaginatedFingerprintInvalidatesLaterPageRun verifies the
// paginated path: when a commit carries more than one page of check runs, the
// guard fingerprints the complete set so a transition on a LATER page (invisible
// to the first page's ETag) still invalidates the guard.
func TestCommitChecksGuard_PaginatedFingerprintInvalidatesLaterPageRun(t *testing.T) {
	f := newFakeGH(t)
	const owner, repo, sha = "octocat", "hello", "abc123"
	path := repoPath(owner, repo, "commits", sha, "check-runs")

	var mu sync.Mutex
	// 150 runs: page 1 holds runs 1..100, page 2 holds 101..150. The first-page
	// ETag only reflects runs 1..100; the transition happens on a page-2 run.
	page1 := make([]map[string]any, 0, githubCheckRunsPageSize)
	for i := 1; i <= 100; i++ {
		page1 = append(page1, map[string]any{"id": i, "name": fmt.Sprintf("job-%d", i), "status": "completed", "conclusion": "success"})
	}
	page2 := make([]map[string]any, 0, 50)
	for i := 101; i <= 150; i++ {
		status, conclusion := "completed", "success"
		if i == 150 {
			// The run whose transition the first page's ETag cannot see.
			status, conclusion = "in_progress", ""
		}
		page2 = append(page2, map[string]any{"id": i, "name": fmt.Sprintf("job-%d", i), "status": status, "conclusion": conclusion})
	}
	state := &struct {
		page1 []map[string]any
		page2 []map[string]any
	}{page1: page1, page2: page2}

	f.on(http.MethodGet, path, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		pageNum := 1
		if q := r.URL.Query().Get("page"); q != "" {
			if n, err := strconv.Atoi(q); err == nil && n > 0 {
				pageNum = n
			}
		}
		var shown []map[string]any
		switch pageNum {
		case 1:
			shown = state.page1
		case 2:
			shown = state.page2
		}
		body, _ := json.Marshal(map[string]any{"total_count": 150, "check_runs": shown})
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", fmt.Sprintf(`W/"%x"`, sha256.Sum256(body)))
		_, _ = w.Write(body)
	})
	p := newProviderForTest(t, f)
	repoRef := ports.SCMRepo{Provider: "github", Host: "github.com", Owner: owner, Name: repo, Repo: owner + "/" + repo}

	res, err := p.CommitChecksGuard(ctx(), repoRef, sha, "")
	if err != nil {
		t.Fatalf("first paginated guard: %v", err)
	}
	if res.NotModified {
		t.Fatal("first paginated poll reported NotModified")
	}
	firstFingerprint := res.ETag

	// Unchanged set -> NotModified=true.
	res, err = p.CommitChecksGuard(ctx(), repoRef, sha, firstFingerprint)
	if err != nil {
		t.Fatalf("unchanged paginated guard: %v", err)
	}
	if !res.NotModified {
		t.Fatal("unchanged page-2 content reported NotModified=false")
	}

	// Only a page-2 run transitions; page 1 (whose ETag page-1 callers would see)
	// is untouched. The fingerprint over the whole set must change.
	mu.Lock()
	state.page2[49] = map[string]any{"id": 150, "name": "job-150", "status": "completed", "conclusion": "success"}
	mu.Unlock()
	res, err = p.CommitChecksGuard(ctx(), repoRef, sha, firstFingerprint)
	if err != nil {
		t.Fatalf("transitioned paginated guard: %v", err)
	}
	if res.NotModified {
		t.Fatal("guard stayed NotModified after a page-2 run transitioned (fingerprint bug)")
	}
}
