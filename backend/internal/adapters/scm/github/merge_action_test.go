package github

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func validMergeRequest() ports.SCMMergeRequest {
	return ports.SCMMergeRequest{
		PR: ports.SCMPRRef{
			Repo:   ports.SCMRepo{Provider: "github", Host: "github.com", Owner: "octocat", Name: "hello", Repo: "octocat/hello"},
			Number: 42,
			URL:    "https://github.com/octocat/hello/pull/42",
		},
		ExpectedHeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Method:          ports.SCMMergeSquash,
	}
}

func TestMergePullRequest_UsesSquashAndExpectedHead(t *testing.T) {
	f := newFakeGH(t)
	f.on(http.MethodPut, "/repos/octocat/hello/pulls/42/merge", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			SHA         string `json:"sha"`
			MergeMethod string `json:"merge_method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.SHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || body.MergeMethod != "squash" {
			t.Fatalf("body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": "merge-sha", "merged": true, "message": "merged"})
	})

	got, err := newProviderForTest(t, f).MergePullRequest(ctx(), validMergeRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got.MergeCommitSHA != "merge-sha" {
		t.Fatalf("result = %#v", got)
	}
}

func TestMergePullRequest_MapsGitHubFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   error
	}{
		{name: "not found", status: http.StatusNotFound, want: ports.ErrSCMNotFound},
		{name: "head changed", status: http.StatusConflict, want: ports.ErrSCMHeadChanged},
		{name: "merge blocked", status: http.StatusMethodNotAllowed, want: ports.ErrSCMNotMergeable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGH(t)
			f.on(http.MethodPut, "/repos/octocat/hello/pulls/42/merge", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": tc.name})
			})
			_, err := newProviderForTest(t, f).MergePullRequest(ctx(), validMergeRequest())
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestMergePullRequest_RejectsIncompleteGuard(t *testing.T) {
	request := validMergeRequest()
	request.ExpectedHeadSHA = "abc"
	if _, err := newProviderForTest(t, newFakeGH(t)).MergePullRequest(ctx(), request); err == nil {
		t.Fatal("merge with invalid expected head succeeded")
	}
}

func TestRequestReview_PostsRequestedReviewer(t *testing.T) {
	f := newFakeGH(t)
	f.on(http.MethodPost, "/repos/octocat/hello/pulls/42/requested_reviewers", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reviewers []string `json:"reviewers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Reviewers) != 1 || body.Reviewers[0] != "reviewer" {
			t.Fatalf("body = %#v", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	})

	err := newProviderForTest(t, f).RequestReview(ctx(), ports.SCMReviewRequest{
		PR:       validMergeRequest().PR,
		Reviewer: "@reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestResolveReviewThread_UsesGraphQLMutation(t *testing.T) {
	f := newFakeGH(t)
	f.on(http.MethodPost, "/graphql", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(body.Query, "resolveReviewThread") || body.Variables["threadId"] != "thread-1" {
			t.Fatalf("body = %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"resolveReviewThread": map[string]any{"thread": map[string]any{"id": "thread-1"}}}})
	})

	err := newProviderForTest(t, f).ResolveReviewThread(ctx(), ports.SCMReviewResolveRequest{
		PR:       validMergeRequest().PR,
		ThreadID: "thread-1",
	})
	if err != nil {
		t.Fatal(err)
	}
}
