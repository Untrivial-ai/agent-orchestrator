package repoowner_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/repoowner"
)

func newClassifier(t *testing.T, handler http.HandlerFunc) *repoowner.GitHubClassifier {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return repoowner.NewGitHubClassifier(repoowner.Options{HTTPClient: srv.Client(), BaseURL: srv.URL})
}

func TestClassifyRepoOwnerMapsGitHubAccountTypes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		body     string
		want     string
		wantPath string
	}{
		{"organization", `{"login":"aoagents","type":"Organization"}`, repoowner.TypeOrganization, "/users/aoagents"},
		{"personal-account", `{"login":"octocat","type":"User"}`, repoowner.TypeUser, "/users/octocat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var gotPath, gotAccept, gotAuth string
			c := newClassifier(t, func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAccept = r.Header.Get("Accept")
				gotAuth = r.Header.Get("Authorization")
				_, _ = w.Write([]byte(tc.body))
			})
			got, err := c.ClassifyRepoOwner(context.Background(), strings.TrimPrefix(tc.wantPath, "/users/"))
			if err != nil {
				t.Fatalf("ClassifyRepoOwner: %v", err)
			}
			if got != tc.want {
				t.Fatalf("owner type = %q, want %q", got, tc.want)
			}
			if gotPath != tc.wantPath {
				t.Fatalf("request path = %q, want %q", gotPath, tc.wantPath)
			}
			if gotAccept != "application/vnd.github+json" {
				t.Fatalf("Accept = %q", gotAccept)
			}
			// PR 1 carries no credentials on purpose: the endpoint is public and
			// keeping it that way is what keeps this half free of PII concerns.
			if gotAuth != "" {
				t.Fatalf("Authorization header sent on a public lookup: %q", gotAuth)
			}
		})
	}
}

// Anything other than the two known account types must be rejected rather than
// forwarded, so a new GitHub account kind cannot widen the exported property's
// value space without a code change here.
func TestClassifyRepoOwnerRejectsUnknownAccountType(t *testing.T) {
	t.Parallel()
	c := newClassifier(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"login":"ghost","type":"Mannequin"}`))
	})
	got, err := c.ClassifyRepoOwner(context.Background(), "ghost")
	if !errors.Is(err, repoowner.ErrUnknownOwnerType) {
		t.Fatalf("error = %v, want ErrUnknownOwnerType", err)
	}
	if got != "" {
		t.Fatalf("owner type = %q, want empty on failure", got)
	}
}

func TestClassifyRepoOwnerFailuresDegradeToAnError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		body   string
		wantIs error
	}{
		{"unknown-owner", http.StatusNotFound, `{"message":"Not Found"}`, repoowner.ErrNotFound},
		{"rate-limited", http.StatusForbidden, `{"message":"API rate limit exceeded"}`, nil},
		{"server-error", http.StatusBadGateway, `bad gateway`, nil},
		{"malformed-body", http.StatusOK, `not json`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newClassifier(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			})
			got, err := c.ClassifyRepoOwner(context.Background(), "octocat")
			if err == nil {
				t.Fatalf("ClassifyRepoOwner = %q, want an error", got)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("error = %v, want %v", err, tc.wantIs)
			}
			if got != "" {
				t.Fatalf("owner type = %q, want empty on failure", got)
			}
		})
	}
}

// The owner is a path segment of a remote URL AO did not author, so a crafted
// remote must not be able to steer the request off /users/{login}.
func TestClassifyRepoOwnerRejectsOwnersThatAreNotLogins(t *testing.T) {
	t.Parallel()
	owners := []string{
		"",
		" ",
		"..",
		"octocat/../orgs",
		"octo cat",
		"-octocat",
		"octocat-",
		"octo@cat",
		strings.Repeat("a", 40),
	}
	for _, owner := range owners {
		t.Run(owner, func(t *testing.T) {
			t.Parallel()
			c := newClassifier(t, func(_ http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected request for %q: %s", owner, r.URL.Path)
			})
			if _, err := c.ClassifyRepoOwner(context.Background(), owner); !errors.Is(err, repoowner.ErrInvalidOwner) {
				t.Fatalf("error = %v, want ErrInvalidOwner", err)
			}
		})
	}
}

func TestClassifyRepoOwnerHonoursContextCancellation(t *testing.T) {
	t.Parallel()
	c := newClassifier(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"type":"User"}`))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ClassifyRepoOwner(ctx, "octocat"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
