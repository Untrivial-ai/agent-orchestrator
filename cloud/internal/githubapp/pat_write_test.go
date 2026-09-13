package githubapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

type stubPATStore struct {
	createArgs  createRecordArgs
	claimInput  domain.PullRequest
	createCalls int
	claimCalls  int
}

type createRecordArgs struct {
	orgID, sessionID, repository, author, url, source, target, headSHA, title string
	number, additions, deletions, changed                                     int
}

func (s *stubPATStore) CreatePullRequestRecord(
	_ context.Context, orgID, sessionID, provider, repository, author string, number int,
	url, sourceBranch, targetBranch, headSHA, title string, additions, deletions, changedFiles int,
) (domain.PullRequest, error) {
	s.createCalls++
	s.createArgs = createRecordArgs{
		orgID: orgID, sessionID: sessionID, repository: repository, author: author,
		url: url, source: sourceBranch, target: targetBranch, headSHA: headSHA, title: title,
		number: number, additions: additions, deletions: deletions, changed: changedFiles,
	}
	return domain.PullRequest{ID: "pr-rec", Number: number, URL: url, SourceBranch: sourceBranch, TargetBranch: targetBranch}, nil
}

func (s *stubPATStore) ClaimPullRequestRecord(
	_ context.Context, _, _ string, input domain.PullRequest,
) (domain.PullRequest, error) {
	s.claimCalls++
	s.claimInput = input
	return input, nil
}

const testPAT = "ghp_TESTPATtoken000000000000000000000"

// RaisePullRequest must send the user's PAT as the bearer token, resolve the
// default branch when none is given, create the PR at the REST endpoint, and
// record it — the whole point of the fallback.
func TestPATWriteServiceRaisePullRequestUsesPAT(t *testing.T) {
	var seenAuth, seenBase string
	var createdPR bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/widgets":
			_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": "main"})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/octo/widgets/pulls":
			createdPR = true
			var body map[string]any
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &body)
			seenBase, _ = body["base"].(string)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 1, "number": 7, "html_url": "https://github.com/octo/widgets/pull/7",
				"state": "open", "title": "Add logging",
				"user":      map[string]any{"login": "octocat"},
				"additions": 8, "deletions": 0, "changed_files": 1,
				"head": map[string]any{"sha": "abc123", "ref": "feature"},
				"base": map[string]any{"ref": "main"},
			})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := &stubPATStore{}
	svc := NewPATWriteService(NewRESTClient(server.URL, server.Client()), store)

	pr, err := svc.RaisePullRequest(
		context.Background(), "org-1", "sess-1",
		"https://github.com/octo/widgets.git", testPAT,
		domain.RaisePullRequest{Title: "Add logging", HeadBranch: "feature"}, // no base → default
	)
	if err != nil {
		t.Fatalf("RaisePullRequest: %v", err)
	}
	if !createdPR {
		t.Fatal("GitHub create-PR endpoint was never called")
	}
	if seenAuth != "Bearer "+testPAT {
		t.Fatalf("Authorization = %q, want the PAT as bearer", seenAuth)
	}
	if seenBase != "main" {
		t.Fatalf("base branch = %q, want the resolved default 'main'", seenBase)
	}
	if pr.Number != 7 {
		t.Fatalf("returned PR number = %d, want 7", pr.Number)
	}
	if store.createCalls != 1 {
		t.Fatalf("record store called %d times, want 1", store.createCalls)
	}
	if store.createArgs.repository != "octo/widgets" || store.createArgs.author != "octocat" ||
		store.createArgs.headSHA != "abc123" || store.createArgs.target != "main" || store.createArgs.number != 7 {
		t.Fatalf("record args not plumbed: %+v", store.createArgs)
	}
}

// ClaimPullRequest must fetch the PR with the PAT and record it.
func TestPATWriteServiceClaimPullRequestUsesPAT(t *testing.T) {
	var seenAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodGet || r.URL.Path != "/repos/octo/widgets/pulls/42" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": 9, "number": 42, "html_url": "https://github.com/octo/widgets/pull/42",
			"state": "open", "title": "Existing", "user": map[string]any{"login": "octocat"},
			"head": map[string]any{"sha": "def456", "ref": "feature"},
			"base": map[string]any{"ref": "main"},
		})
	}))
	defer server.Close()

	store := &stubPATStore{}
	svc := NewPATWriteService(NewRESTClient(server.URL, server.Client()), store)

	pr, err := svc.ClaimPullRequest(
		context.Background(), "org-1", "sess-1",
		"https://github.com/octo/widgets", testPAT, "42",
	)
	if err != nil {
		t.Fatalf("ClaimPullRequest: %v", err)
	}
	if seenAuth != "Bearer "+testPAT {
		t.Fatalf("Authorization = %q, want the PAT as bearer", seenAuth)
	}
	if store.claimCalls != 1 || store.claimInput.Number != 42 || store.claimInput.State != contract.PRStateOpen {
		t.Fatalf("claim not recorded correctly: calls=%d input=%+v", store.claimCalls, store.claimInput)
	}
	if pr.Number != 42 {
		t.Fatalf("returned PR number = %d, want 42", pr.Number)
	}
}

func TestOwnerRepoFromCloneURL(t *testing.T) {
	cases := []struct {
		in          string
		owner, repo string
		wantErr     bool
	}{
		{"https://github.com/octo/widgets.git", "octo", "widgets", false},
		{"https://github.com/octo/widgets", "octo", "widgets", false},
		{"https://github.com/Octo-Org/my.repo.git", "Octo-Org", "my.repo", false},
		{"https://github.com/octo", "", "", true},
		{"https://github.com/octo/widgets/extra", "", "", true},
		{"", "", "", true},
	}
	for _, tc := range cases {
		owner, repo, err := ownerRepoFromCloneURL(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%q: expected error, got %s/%s", tc.in, owner, repo)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", tc.in, err)
			continue
		}
		if owner != tc.owner || repo != tc.repo {
			t.Errorf("%q: got %s/%s, want %s/%s", tc.in, owner, repo, tc.owner, tc.repo)
		}
	}
}

// A REST-only client keeps the default GitHub API base when constructed with an
// empty URL, and honors an override (used to point at a test server).
func TestNewRESTClientBaseURL(t *testing.T) {
	if got := NewRESTClient("", nil).apiBaseURL; got != defaultAPIBaseURL {
		t.Fatalf("empty base = %q, want %q", got, defaultAPIBaseURL)
	}
	if got := NewRESTClient("https://ghe.example.com/api/v3/", nil).apiBaseURL; got != "https://ghe.example.com/api/v3" {
		t.Fatalf("override base = %q, want trimmed", got)
	}
}
