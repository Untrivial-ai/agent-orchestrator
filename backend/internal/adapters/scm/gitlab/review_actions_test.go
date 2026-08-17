package gitlab

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func validReviewRequest() ports.SCMReviewRequest {
	return ports.SCMReviewRequest{
		PR: ports.SCMPRRef{
			Repo:   ports.SCMRepo{Provider: "gitlab", Host: "gitlab.com", Owner: "myorg", Name: "myrepo", Repo: "myorg/myrepo"},
			Number: 42,
			URL:    "https://gitlab.com/myorg/myrepo/-/merge_requests/42",
		},
		Reviewer: "@maya",
	}
}

func validResolveRequest() ports.SCMReviewResolveRequest {
	return ports.SCMReviewResolveRequest{
		PR: ports.SCMPRRef{
			Repo:   ports.SCMRepo{Provider: "gitlab", Host: "gitlab.com", Owner: "myorg", Name: "myrepo", Repo: "myorg/myrepo"},
			Number: 42,
			URL:    "https://gitlab.com/myorg/myrepo/-/merge_requests/42",
		},
		ThreadID: "abc123",
	}
}

func TestRequestReview_ResolvesUserAndPreservesExistingReviewers(t *testing.T) {
	calls := []string{}
	_, p := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.String())
		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/api/v4/users":
			if got := r.URL.Query().Get("username"); got != "maya" {
				t.Fatalf("username query = %q, want maya", got)
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 7, "username": "maya"}})
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/api/v4/projects/myorg%2Fmyrepo/merge_requests/42":
			_ = json.NewEncoder(w).Encode(map[string]any{"reviewers": []map[string]any{{"id": 3}}})
		case r.Method == http.MethodPut && r.URL.EscapedPath() == "/api/v4/projects/myorg%2Fmyrepo/merge_requests/42":
			var body struct {
				ReviewerIDs []int `json:"reviewer_ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if len(body.ReviewerIDs) != 2 || body.ReviewerIDs[0] != 3 || body.ReviewerIDs[1] != 7 {
				t.Fatalf("reviewer_ids = %v, want [3 7]", body.ReviewerIDs)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"iid": 42})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	if err := p.RequestReview(ctx(), validReviewRequest()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("calls = %v, want 3 calls", calls)
	}
}

func TestRequestReview_DoesNotDuplicateExistingReviewer(t *testing.T) {
	_, p := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/api/v4/users":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 7, "username": "maya"}})
		case r.Method == http.MethodGet && r.URL.EscapedPath() == "/api/v4/projects/myorg%2Fmyrepo/merge_requests/42":
			_ = json.NewEncoder(w).Encode(map[string]any{"reviewers": []map[string]any{{"id": 7}}})
		case r.Method == http.MethodPut && r.URL.EscapedPath() == "/api/v4/projects/myorg%2Fmyrepo/merge_requests/42":
			var body struct {
				ReviewerIDs []int `json:"reviewer_ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if len(body.ReviewerIDs) != 1 || body.ReviewerIDs[0] != 7 {
				t.Fatalf("reviewer_ids = %v, want [7]", body.ReviewerIDs)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))

	if err := p.RequestReview(ctx(), validReviewRequest()); err != nil {
		t.Fatal(err)
	}
}

func TestRequestReview_MapsGitLabFailures(t *testing.T) {
	t.Run("reviewer not found", func(t *testing.T) {
		_, p := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.EscapedPath() != "/api/v4/users" {
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		}))
		err := p.RequestReview(ctx(), validReviewRequest())
		if !errors.Is(err, ports.ErrSCMNotFound) {
			t.Fatalf("error = %v, want ErrSCMNotFound", err)
		}
	})

	t.Run("merge request not found", func(t *testing.T) {
		_, p := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.EscapedPath() {
			case "/api/v4/users":
				_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 7, "username": "maya"}})
			case "/api/v4/projects/myorg%2Fmyrepo/merge_requests/42":
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": "not found"})
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
			}
		}))
		err := p.RequestReview(ctx(), validReviewRequest())
		if !errors.Is(err, ports.ErrSCMNotFound) {
			t.Fatalf("error = %v, want ErrSCMNotFound", err)
		}
	})
}

func TestRequestReview_RejectsInvalidArgs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(r ports.SCMReviewRequest) ports.SCMReviewRequest
	}{
		{name: "non-positive number", modify: func(r ports.SCMReviewRequest) ports.SCMReviewRequest { r.PR.Number = 0; return r }},
		{name: "missing owner", modify: func(r ports.SCMReviewRequest) ports.SCMReviewRequest { r.PR.Repo.Owner = ""; return r }},
		{name: "missing name", modify: func(r ports.SCMReviewRequest) ports.SCMReviewRequest { r.PR.Repo.Name = ""; return r }},
		{name: "missing reviewer", modify: func(r ports.SCMReviewRequest) ports.SCMReviewRequest { r.Reviewer = " "; return r }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, p := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("HTTP request should not be made for invalid args")
			}))
			err := p.RequestReview(ctx(), tc.modify(validReviewRequest()))
			if err == nil {
				t.Fatal("expected error for invalid args, got nil")
			}
		})
	}
}

func TestResolveReviewThread_ResolvesDiscussion(t *testing.T) {
	_, p := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", r.Method)
		}
		wantPath := "/api/v4/projects/myorg%2Fmyrepo/merge_requests/42/discussions/abc123"
		if r.URL.EscapedPath() != wantPath {
			t.Fatalf("path = %s, want %s", r.URL.EscapedPath(), wantPath)
		}
		if got := r.URL.Query().Get("resolved"); got != "true" {
			t.Fatalf("resolved query = %q, want true", got)
		}
		w.WriteHeader(http.StatusOK)
	}))

	if err := p.ResolveReviewThread(ctx(), validResolveRequest()); err != nil {
		t.Fatal(err)
	}
}

func TestResolveReviewThread_MapsNotFound(t *testing.T) {
	_, p := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "not found"})
	}))

	err := p.ResolveReviewThread(ctx(), validResolveRequest())
	if !errors.Is(err, ports.ErrSCMNotFound) {
		t.Fatalf("error = %v, want ErrSCMNotFound", err)
	}
}

func TestResolveReviewThread_RejectsInvalidArgs(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(r ports.SCMReviewResolveRequest) ports.SCMReviewResolveRequest
	}{
		{name: "non-positive number", modify: func(r ports.SCMReviewResolveRequest) ports.SCMReviewResolveRequest { r.PR.Number = 0; return r }},
		{name: "missing owner", modify: func(r ports.SCMReviewResolveRequest) ports.SCMReviewResolveRequest { r.PR.Repo.Owner = ""; return r }},
		{name: "missing name", modify: func(r ports.SCMReviewResolveRequest) ports.SCMReviewResolveRequest { r.PR.Repo.Name = ""; return r }},
		{name: "missing thread", modify: func(r ports.SCMReviewResolveRequest) ports.SCMReviewResolveRequest { r.ThreadID = " "; return r }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, p := testServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("HTTP request should not be made for invalid args")
			}))
			err := p.ResolveReviewThread(ctx(), tc.modify(validResolveRequest()))
			if err == nil {
				t.Fatal("expected error for invalid args, got nil")
			}
		})
	}
}
