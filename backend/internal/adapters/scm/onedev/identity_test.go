package onedev

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestProviderSatisfiesIdentityResolvers pins the interfaces the multi
// provider type-asserts on. Without the host-scoped one the daemon logged
// `scm multi: provider "onedev" does not implement AuthenticatedIdentity` and
// the observer silently fell back to branch-based PR discovery.
func TestProviderSatisfiesIdentityResolvers(t *testing.T) {
	p := newTestProvider(t, []string{"od.test:6610"})
	var _ ports.SCMIdentityResolver = p
	var _ interface {
		AuthenticatedIdentityForHost(context.Context, string) (ports.SCMIdentity, error)
	} = p
}

func TestAuthenticatedIdentityForHost(t *testing.T) {
	var calls atomic.Int64
	var path atomic.Value
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		path.Store(r.URL.Path)
		_, _ = w.Write([]byte(`{"id":4,"name":"johnkattenhorn","fullName":"John Kattenhorn","type":"ORDINARY"}`))
	})
	p := newTestProvider(t, []string{srv.URL})
	host := strings.TrimPrefix(srv.URL, "http://")

	ident, err := p.AuthenticatedIdentityForHost(context.Background(), host)
	if err != nil {
		t.Fatalf("AuthenticatedIdentityForHost: %v", err)
	}
	if ident.Login != "johnkattenhorn" {
		t.Errorf("Login = %q, want johnkattenhorn", ident.Login)
	}
	if !ident.Human {
		t.Error("Human = false, want true for an ordinary account")
	}
	if got := path.Load().(string); got != APIBasePath+currentUserPath {
		t.Errorf("probed %q, want %q", got, APIBasePath+currentUserPath)
	}

	// Cached for the provider's lifetime: the observer resolves identity once
	// per poll, and a login only changes when an account is renamed.
	if _, err := p.AuthenticatedIdentityForHost(context.Background(), host); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("API calls = %d, want 1 (identity must be cached)", got)
	}
}

// TestAuthenticatedIdentityResolvesSSHPortHost covers the path the observer
// actually takes: a repository cloned over OneDev's git SSH port carries that
// port in SCMRepo.Host, which must still resolve to the allowlisted HTTP
// instance rather than being rejected.
func TestAuthenticatedIdentityResolvesSSHPortHost(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":4,"name":"ao-bot"}`))
	})
	p := newTestProvider(t, []string{srv.URL})
	hostname := hostnameOf(strings.TrimPrefix(srv.URL, "http://"))

	ident, err := p.AuthenticatedIdentityForHost(context.Background(), hostname+":6611")
	if err != nil {
		t.Fatalf("AuthenticatedIdentityForHost: %v", err)
	}
	if ident.Login != "ao-bot" {
		t.Errorf("Login = %q, want ao-bot", ident.Login)
	}
	if ident.Human {
		t.Error("Human = true, want false for a -bot account")
	}
}

// TestAuthenticatedIdentityIsPerHost: two instances are two unrelated user
// databases, so one host's identity must never be served for another.
func TestAuthenticatedIdentityIsPerHost(t *testing.T) {
	a := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":1,"name":"alice"}`))
	})
	b := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":2,"name":"bob"}`))
	})
	p := newTestProvider(t, []string{a.URL, b.URL})

	identA, err := p.AuthenticatedIdentityForHost(context.Background(), strings.TrimPrefix(a.URL, "http://"))
	if err != nil {
		t.Fatalf("host a: %v", err)
	}
	identB, err := p.AuthenticatedIdentityForHost(context.Background(), strings.TrimPrefix(b.URL, "http://"))
	if err != nil {
		t.Fatalf("host b: %v", err)
	}
	if identA.Login != "alice" || identB.Login != "bob" {
		t.Fatalf("identities = %q/%q, want alice/bob (identity cache is not host-scoped)", identA.Login, identB.Login)
	}
}

// TestAuthenticatedIdentityUnscoped: the host-less form is only meaningful for
// a single-instance deployment. With several configured it must refuse rather
// than attribute one instance's pull requests to another's account.
func TestAuthenticatedIdentityUnscoped(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":4,"name":"solo"}`))
	})
	single := newTestProvider(t, []string{srv.URL})
	ident, err := single.AuthenticatedIdentity(context.Background())
	if err != nil {
		t.Fatalf("AuthenticatedIdentity: %v", err)
	}
	if ident.Login != "solo" {
		t.Errorf("Login = %q, want solo", ident.Login)
	}

	multi := newTestProvider(t, []string{srv.URL, "other.test:6610"})
	if _, err := multi.AuthenticatedIdentity(context.Background()); err == nil {
		t.Fatal("expected an error when several instances are configured, got nil")
	}
}

func TestAuthenticatedIdentityErrors(t *testing.T) {
	t.Run("host not allowed", func(t *testing.T) {
		p := newTestProvider(t, []string{"od.test:6610"})
		_, err := p.AuthenticatedIdentityForHost(context.Background(), "elsewhere.test:6610")
		if !errors.Is(err, ErrHostNotAllowed) {
			t.Fatalf("err = %v, want ErrHostNotAllowed", err)
		}
	})

	t.Run("credential rejected", func(t *testing.T) {
		srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("Invalid account or incorrect credentials"))
		})
		p := newTestProvider(t, []string{srv.URL})
		_, err := p.AuthenticatedIdentityForHost(context.Background(), strings.TrimPrefix(srv.URL, "http://"))
		if !errors.Is(err, ErrAuthFailed) {
			t.Fatalf("err = %v, want ErrAuthFailed", err)
		}
	})

	t.Run("nameless account is not cached as a valid identity", func(t *testing.T) {
		var calls atomic.Int64
		srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			_, _ = w.Write([]byte(`{"id":4,"name":""}`))
		})
		p := newTestProvider(t, []string{srv.URL})
		host := strings.TrimPrefix(srv.URL, "http://")
		if _, err := p.AuthenticatedIdentityForHost(context.Background(), host); err == nil {
			t.Fatal("expected an error for an account with no name, got nil")
		}
		if _, err := p.AuthenticatedIdentityForHost(context.Background(), host); err == nil {
			t.Fatal("second call: expected an error, got nil")
		}
		if got := calls.Load(); got != 2 {
			t.Errorf("API calls = %d, want 2 (a failed lookup must not be cached)", got)
		}
	})
}
