package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

func TestGitHubRepositoryFullName(t *testing.T) {
	cases := []struct {
		in       string
		want     string
		wantOK   bool
		fullName string
	}{
		{in: "https://github.com/octo/app", want: "octo/app", wantOK: true},
		{in: "https://github.com/octo/app.git", want: "octo/app", wantOK: true},
		{in: "  https://github.com/Octo-Org/my.repo.git  ", want: "Octo-Org/my.repo", wantOK: true},
		{in: "https://github.com/octo/app/", want: "octo/app", wantOK: true},
		{in: "https://github.com/octo", wantOK: false},
		{in: "https://github.com/octo/app/extra", wantOK: false},
		{in: "https://gitlab.com/octo/app", wantOK: false},
		{in: "http://github.com/octo/app", wantOK: false},
		{in: "https://user@github.com/octo/app", wantOK: false},
		{in: "https://github.com/octo/app?x=1", wantOK: false},
		{in: "", wantOK: false},
	}
	for _, tc := range cases {
		got, ok := gitHubRepositoryFullName(tc.in)
		if ok != tc.wantOK {
			t.Errorf("gitHubRepositoryFullName(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("gitHubRepositoryFullName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// newAppTestClient builds a full App-credentialed client pointed at a test
// server. The server never validates the signed JWT, so a throwaway RSA key is
// enough to exercise the installation-token and repository-list paths.
func newAppTestClient(t *testing.T, baseURL string, httpClient *http.Client) *Client {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	client, err := New(Config{
		AppID:         1234,
		AppSlug:       "ao-test",
		ClientID:      "Iv1.testclient",
		ClientSecret:  "secret",
		PrivateKeyPEM: string(pemBytes),
		PublicURL:     "https://api.example.com",
		APIBaseURL:    baseURL,
	}, httpClient)
	if err != nil {
		t.Fatalf("New client: %v", err)
	}
	return client
}

// installationRepositoryServer answers the GitHub endpoints the checkout scope
// path touches: minting installation tokens (capturing the repository_ids of the
// read-scoped mint), listing an installation's repositories, and the per-name
// GET /repos/{owner}/{repo} fallback for repositories missing from the listing.
type installationRepositoryServer struct {
	repos             []Repository
	directRepos       map[string]Repository // fullName -> repo, resolvable only via GET /repos
	mintedRepoIDs     []int64
	mintedPermissions map[string]string
	tokenCalls        int
	listCalls         int
	directGetCalls    int
}

func (h *installationRepositoryServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/1234/access_tokens":
			h.tokenCalls++
			var body struct {
				RepositoryIDs []int64           `json:"repository_ids"`
				Permissions   map[string]string `json:"permissions"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			// The read-scoped checkout mint is the one that carries permissions;
			// the installation-wide list mint sends an empty body.
			if len(body.Permissions) > 0 {
				h.mintedRepoIDs = body.RepositoryIDs
				h.mintedPermissions = body.Permissions
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "ghs_testinstallationtoken",
				"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		case r.Method == http.MethodGet && r.URL.Path == "/installation/repositories":
			h.listCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"repositories": h.repos})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/"):
			h.directGetCalls++
			fullName := strings.ToLower(strings.TrimPrefix(r.URL.Path, "/repos/"))
			// A repo present in the listing is also directly readable; the fallback
			// only exercises repos that are exclusively in directRepos.
			if repo, ok := h.directRepos[fullName]; ok {
				_ = json.NewEncoder(w).Encode(repo)
				return
			}
			// Not granted to the installation: GitHub answers 404.
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func TestResolveInstallationRepositoryIDs(t *testing.T) {
	backend := &installationRepositoryServer{
		repos: []Repository{
			{ID: 1, FullName: "octo/app"},
			{ID: 2, FullName: "octo/lib"},
			{ID: 9, FullName: "octo/unused"},
		},
		// octo/private is granted to the installation but not (yet) in the
		// eventually-consistent listing; it resolves only via the direct GET.
		directRepos: map[string]Repository{
			"octo/private": {ID: 7, FullName: "octo/private", Private: true},
		},
	}
	server := httptest.NewServer(backend.handler(t))
	defer server.Close()
	client := newAppTestClient(t, server.URL, server.Client())

	// A matched extra, a case-mismatched duplicate of it, a private extra missing
	// from the listing (resolved via the GET fallback), and one the App cannot
	// access (404 -> unresolved).
	ids, unresolved, err := client.resolveInstallationRepositoryIDs(context.Background(), 1234, []string{
		"octo/lib", "OCTO/LIB", "octo/lib", "octo/private", "other/nope",
	})
	if err != nil {
		t.Fatalf("resolveInstallationRepositoryIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != 2 || ids[1] != 7 {
		t.Fatalf("resolved ids = %v, want [2 7] (octo/lib deduped + octo/private via fallback)", ids)
	}
	if len(unresolved) != 1 || unresolved[0] != "other/nope" {
		t.Fatalf("unresolved = %v, want [other/nope]", unresolved)
	}
	// Only the two names missing from the listing fall back to a direct GET; the
	// matched name and its duplicate never do.
	if backend.directGetCalls != 2 {
		t.Fatalf("direct GET calls = %d, want 2 (octo/private + other/nope)", backend.directGetCalls)
	}

	// No names means no work and no HTTP call.
	backend.listCalls = 0
	ids, unresolved, err = client.resolveInstallationRepositoryIDs(context.Background(), 1234, nil)
	if err != nil || ids != nil || unresolved != nil {
		t.Fatalf("empty resolve = (%v, %v, %v), want (nil, nil, nil)", ids, unresolved, err)
	}
	if backend.listCalls != 0 {
		t.Fatalf("empty resolve listed repositories %d times, want 0", backend.listCalls)
	}
}

// checkoutStubStore overrides only the two methods IssueCheckoutGrant's happy
// path calls; every other Store method stays nil and would panic if reached,
// which keeps the test honest about the call graph.
type checkoutStubStore struct {
	Store
	primary    domain.GitHubCheckoutContext
	extras     []domain.RepoRef
	extrasErr  error
	extrasSeen int
}

func (s *checkoutStubStore) WorkerGitHubCheckoutContext(
	context.Context, string, string,
) (domain.GitHubCheckoutContext, error) {
	return s.primary, nil
}

func (s *checkoutStubStore) WorkerSessionExtraRepos(
	context.Context, string, string,
) ([]domain.RepoRef, error) {
	s.extrasSeen++
	return s.extras, s.extrasErr
}

func newCheckoutTestService(t *testing.T, store Store, client *Client) *Service {
	t.Helper()
	svc, err := NewService(
		store, client,
		make([]byte, 32), make([]byte, 32),
		"webhook-secret", time.Hour, nil,
	)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func primaryContext() domain.GitHubCheckoutContext {
	return domain.GitHubCheckoutContext{
		OrgID:                "org-1",
		SessionID:            "sess-1",
		ProjectID:            "proj-1",
		GitHubInstallationID: 1234,
		GitHubRepositoryID:   1,
		FullName:             "octo/app",
		CloneURL:             "https://github.com/octo/app.git",
		DefaultBranch:        "main",
	}
}

func TestIssueCheckoutGrantBroadensToExtraRepos(t *testing.T) {
	backend := &installationRepositoryServer{repos: []Repository{
		{ID: 1, FullName: "octo/app"},
		{ID: 2, FullName: "octo/lib"},
	}}
	server := httptest.NewServer(backend.handler(t))
	defer server.Close()
	client := newAppTestClient(t, server.URL, server.Client())
	store := &checkoutStubStore{
		primary: primaryContext(),
		extras: []domain.RepoRef{
			{URL: "https://github.com/octo/lib"},
			// The primary repo listed as an extra must not be double-scoped.
			{URL: "https://github.com/octo/app.git"},
		},
	}
	svc := newCheckoutTestService(t, store, client)

	grant, err := svc.IssueCheckoutGrant(context.Background(), "org-1", "sess-1")
	if err != nil {
		t.Fatalf("IssueCheckoutGrant: %v", err)
	}
	if grant.CloneURL != "https://github.com/octo/app.git" || grant.Token == "" {
		t.Fatalf("grant = %+v, want the primary clone URL and a token", grant)
	}
	got := append([]int64(nil), backend.mintedRepoIDs...)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("minted repository_ids = %v, want [1 2] (primary + octo/lib)", backend.mintedRepoIDs)
	}
	if backend.mintedPermissions["contents"] != "read" || len(backend.mintedPermissions) != 1 {
		t.Fatalf("minted permissions = %v, want contents:read only", backend.mintedPermissions)
	}
}

func TestIssueCheckoutGrantResolvesExtraViaDirectFallback(t *testing.T) {
	backend := &installationRepositoryServer{
		repos: []Repository{{ID: 1, FullName: "octo/app"}},
		// The declared extra is private and missing from the listing, but the App
		// can read it directly (the ChartSnip case): the checkout scope must still
		// include it so the worker can clone it.
		directRepos: map[string]Repository{
			"octo/private": {ID: 5, FullName: "octo/private", Private: true},
		},
	}
	server := httptest.NewServer(backend.handler(t))
	defer server.Close()
	client := newAppTestClient(t, server.URL, server.Client())
	store := &checkoutStubStore{
		primary: primaryContext(),
		extras:  []domain.RepoRef{{URL: "https://github.com/octo/private"}},
	}
	svc := newCheckoutTestService(t, store, client)

	if _, err := svc.IssueCheckoutGrant(context.Background(), "org-1", "sess-1"); err != nil {
		t.Fatalf("IssueCheckoutGrant: %v", err)
	}
	got := append([]int64(nil), backend.mintedRepoIDs...)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if len(got) != 2 || got[0] != 1 || got[1] != 5 {
		t.Fatalf("minted repository_ids = %v, want [1 5] (primary + octo/private via fallback)", backend.mintedRepoIDs)
	}
	if backend.directGetCalls != 1 {
		t.Fatalf("direct GET calls = %d, want 1 (octo/private)", backend.directGetCalls)
	}
}

func TestIssueCheckoutGrantExcludesInaccessibleExtra(t *testing.T) {
	backend := &installationRepositoryServer{repos: []Repository{{ID: 1, FullName: "octo/app"}}}
	server := httptest.NewServer(backend.handler(t))
	defer server.Close()
	client := newAppTestClient(t, server.URL, server.Client())
	store := &checkoutStubStore{
		primary: primaryContext(),
		// Neither in the listing nor readable directly: it must be dropped from the
		// scope without failing the primary checkout (a 422 would have failed all).
		extras: []domain.RepoRef{{URL: "https://github.com/octo/ghost"}},
	}
	svc := newCheckoutTestService(t, store, client)

	if _, err := svc.IssueCheckoutGrant(context.Background(), "org-1", "sess-1"); err != nil {
		t.Fatalf("IssueCheckoutGrant: %v", err)
	}
	if len(backend.mintedRepoIDs) != 1 || backend.mintedRepoIDs[0] != 1 {
		t.Fatalf("minted repository_ids = %v, want [1] (primary only; ghost is 404)", backend.mintedRepoIDs)
	}
	if backend.directGetCalls != 1 {
		t.Fatalf("direct GET calls = %d, want 1 (octo/ghost)", backend.directGetCalls)
	}
}

func TestIssueCheckoutGrantFallsBackToPrimaryOnExtrasError(t *testing.T) {
	backend := &installationRepositoryServer{repos: []Repository{{ID: 1, FullName: "octo/app"}}}
	server := httptest.NewServer(backend.handler(t))
	defer server.Close()
	client := newAppTestClient(t, server.URL, server.Client())
	store := &checkoutStubStore{primary: primaryContext(), extrasErr: errors.New("boom")}
	svc := newCheckoutTestService(t, store, client)

	if _, err := svc.IssueCheckoutGrant(context.Background(), "org-1", "sess-1"); err != nil {
		t.Fatalf("IssueCheckoutGrant: %v", err)
	}
	if len(backend.mintedRepoIDs) != 1 || backend.mintedRepoIDs[0] != 1 {
		t.Fatalf("minted repository_ids = %v, want [1] (primary only on extras error)", backend.mintedRepoIDs)
	}
	if backend.listCalls != 0 {
		t.Fatalf("listed repositories %d times, want 0 (never reached on extras error)", backend.listCalls)
	}
}

func TestIssueCheckoutGrantPrimaryOnlyWhenNoExtras(t *testing.T) {
	backend := &installationRepositoryServer{repos: []Repository{{ID: 1, FullName: "octo/app"}}}
	server := httptest.NewServer(backend.handler(t))
	defer server.Close()
	client := newAppTestClient(t, server.URL, server.Client())
	store := &checkoutStubStore{primary: primaryContext()}
	svc := newCheckoutTestService(t, store, client)

	if _, err := svc.IssueCheckoutGrant(context.Background(), "org-1", "sess-1"); err != nil {
		t.Fatalf("IssueCheckoutGrant: %v", err)
	}
	if len(backend.mintedRepoIDs) != 1 || backend.mintedRepoIDs[0] != 1 {
		t.Fatalf("minted repository_ids = %v, want [1]", backend.mintedRepoIDs)
	}
	if backend.listCalls != 0 {
		t.Fatalf("listed repositories %d times, want 0 (no extras declared)", backend.listCalls)
	}
	if store.extrasSeen != 1 {
		t.Fatalf("WorkerSessionExtraRepos called %d times, want 1", store.extrasSeen)
	}
}
