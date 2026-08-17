package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func validMergeRequest() ports.SCMMergeRequest {
	return ports.SCMMergeRequest{
		PR: ports.SCMPRRef{
			Repo:   ports.SCMRepo{Provider: "gitlab", Host: "gitlab.com", Owner: "myorg", Name: "myrepo", Repo: "myorg/myrepo"},
			Number: 42,
			URL:    "https://gitlab.com/myorg/myrepo/-/merge_requests/42",
		},
		ExpectedHeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Method:          ports.SCMMergeSquash,
	}
}

func TestMergePullRequest_UsesSquashAndExpectedHead(t *testing.T) {
	srv, p := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", r.Method)
		}
		// Path: /projects/myorg%2Fmyrepo/merge_requests/42/merge
		wantPath := "/api/v4/projects/myorg%2Fmyrepo/merge_requests/42/merge"
		if r.URL.EscapedPath() != wantPath {
			t.Fatalf("path = %s, want %s", r.URL.EscapedPath(), wantPath)
		}
		q := r.URL.Query()
		if q.Get("sha") != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
			t.Fatalf("sha query = %q", q.Get("sha"))
		}
		if q.Get("squash") != "true" {
			t.Fatalf("squash query = %q", q.Get("squash"))
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"merge_commit_sha": "abc123deadbeef"})
	}))

	got, err := p.MergePullRequest(ctx(), validMergeRequest())
	if err != nil {
		t.Fatal(err)
	}
	if got.MergeCommitSHA != "abc123deadbeef" {
		t.Fatalf("result = %#v", got)
	}
	_ = srv
}

func TestMergePullRequest_MapsGitLabFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   error
	}{
		{name: "not found", status: http.StatusNotFound, want: ports.ErrSCMNotFound},
		{name: "head changed", status: http.StatusConflict, want: ports.ErrSCMHeadChanged},
		{name: "method not allowed", status: http.StatusMethodNotAllowed, want: ports.ErrSCMNotMergeable},
		{name: "not acceptable", status: http.StatusNotAcceptable, want: ports.ErrSCMNotMergeable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, p := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": tc.name})
			}))
			_, err := p.MergePullRequest(ctx(), validMergeRequest())
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestMergePullRequest_RejectsInvalidArgs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(r ports.SCMMergeRequest) ports.SCMMergeRequest
	}{
		{
			name:   "non-positive number",
			modify: func(r ports.SCMMergeRequest) ports.SCMMergeRequest { r.PR.Number = 0; return r },
		},
		{
			name:   "missing owner",
			modify: func(r ports.SCMMergeRequest) ports.SCMMergeRequest { r.PR.Repo.Owner = ""; return r },
		},
		{
			name:   "missing name",
			modify: func(r ports.SCMMergeRequest) ports.SCMMergeRequest { r.PR.Repo.Name = ""; return r },
		},
		{
			name:   "invalid sha",
			modify: func(r ports.SCMMergeRequest) ports.SCMMergeRequest { r.ExpectedHeadSHA = "abc"; return r },
		},
		{
			name:   "unsupported method",
			modify: func(r ports.SCMMergeRequest) ports.SCMMergeRequest { r.Method = "merge"; return r },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, p := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Fatal("HTTP request should not be made for invalid args")
			}))
			req := tc.modify(validMergeRequest())
			_, err := p.MergePullRequest(ctx(), req)
			if err == nil {
				t.Fatal("expected error for invalid args, got nil")
			}
		})
	}
}

func TestMergePullRequest_NilProvider(t *testing.T) {
	var p *Provider
	_, err := p.MergePullRequest(ctx(), validMergeRequest())
	if err == nil {
		t.Fatal("expected error for nil provider, got nil")
	}
}

func ctx() context.Context {
	return context.Background()
}
