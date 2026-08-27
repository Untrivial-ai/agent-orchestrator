package onedev

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func newTestProvider(t *testing.T, hosts []string) *Provider {
	t.Helper()
	p, err := NewProvider(ProviderOptions{
		AllowedHosts: hosts,
		Token:        StaticTokenSource("od-token"),
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p
}

func TestNewProvider(t *testing.T) {
	tests := []struct {
		name       string
		opts       ProviderOptions
		wantErr    error
		wantHosts  []string
		wantSubstr string
	}{
		{
			// OneDev has no public instance, so an empty allowlist leaves the
			// provider with nowhere to talk to. Fail loudly at construction.
			name:    "no allowed hosts",
			opts:    ProviderOptions{Token: StaticTokenSource("od-token")},
			wantErr: ErrNoAllowedHosts,
		},
		{
			name: "allowlist of only blanks",
			opts: ProviderOptions{
				AllowedHosts: []string{"", "   "},
				Token:        StaticTokenSource("od-token"),
			},
			wantErr: ErrNoAllowedHosts,
		},
		{
			name: "malformed allowlist entry",
			opts: ProviderOptions{
				AllowedHosts: []string{"ssh://od.test:6611"},
				Token:        StaticTokenSource("od-token"),
			},
			wantSubstr: "scheme must be http or https",
		},
		{
			name:    "no credentials",
			opts:    ProviderOptions{AllowedHosts: []string{"od.test:6610"}},
			wantErr: ErrNoToken,
		},
		{
			name: "no credentials is tolerated when the preflight is skipped",
			opts: ProviderOptions{
				AllowedHosts:       []string{"od.test:6610"},
				SkipTokenPreflight: true,
			},
			wantHosts: []string{"https://od.test:6610"},
		},
		{
			// A deployment may configure only per-host tokens.
			name: "per-host token alone is enough",
			opts: ProviderOptions{
				AllowedHosts: []string{"od.test:6610"},
				HostTokens:   map[string]TokenSource{"od.test:6610": StaticTokenSource("od-host")},
			},
			wantHosts: []string{"https://od.test:6610"},
		},
		{
			name: "hosts are normalized, deduped and ordered",
			opts: ProviderOptions{
				AllowedHosts: []string{"  B.test  ", "http://a.test:6610", "b.test", "https://B.TEST"},
				Token:        StaticTokenSource("od-token"),
			},
			wantHosts: []string{"http://a.test:6610", "https://b.test"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewProvider(tt.opts)
			if tt.wantErr != nil || tt.wantSubstr != "" {
				if err == nil {
					t.Fatal("NewProvider succeeded, want error")
				}
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				if tt.wantSubstr != "" && !strings.Contains(err.Error(), tt.wantSubstr) {
					t.Fatalf("err = %v, want it to mention %q", err, tt.wantSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}
			if got := p.AllowedHosts(); !reflect.DeepEqual(got, tt.wantHosts) {
				t.Fatalf("AllowedHosts() = %v, want %v", got, tt.wantHosts)
			}
		})
	}
}

// TestNewProviderSurfacesBrokenCredentialHelper distinguishes "no credential
// configured" from "credential source is broken" — the daemon logs the former
// as disabled and the latter as a failure.
func TestNewProviderSurfacesBrokenCredentialHelper(t *testing.T) {
	boom := errors.New("keyring is locked")
	_, err := NewProvider(ProviderOptions{
		AllowedHosts: []string{"od.test:6610"},
		Token:        errSource{boom},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestParseRepository(t *testing.T) {
	// The estate's real shape: OneDev serves git over SSH on 6611 and HTTP on
	// 6610, and the allowlist names the HTTP API endpoint.
	hosts := []string{"http://192.168.1.30:6610", "https://git.example.com"}

	tests := []struct {
		name   string
		remote string
		want   ports.SCMRepo
		wantOK bool
	}{
		{
			name:   "ssh url with the git ssh port",
			remote: "ssh://git@192.168.1.30:6611/curatarr.git",
			want:   ports.SCMRepo{Provider: "onedev", Host: "192.168.1.30:6610", Owner: "", Name: "curatarr", Repo: "curatarr"},
			wantOK: true,
		},
		{
			name:   "ssh url with a nested project path",
			remote: "ssh://git@192.168.1.30:6611/Homelab/curatarr.git",
			want:   ports.SCMRepo{Provider: "onedev", Host: "192.168.1.30:6610", Owner: "Homelab", Name: "curatarr", Repo: "Homelab/curatarr"},
			wantOK: true,
		},
		{
			name:   "http url with the api port",
			remote: "http://192.168.1.30:6610/curatarr.git",
			want:   ports.SCMRepo{Provider: "onedev", Host: "192.168.1.30:6610", Owner: "", Name: "curatarr", Repo: "curatarr"},
			wantOK: true,
		},
		{
			name:   "http url with a nested project path",
			remote: "http://192.168.1.30:6610/Homelab/curatarr.git",
			want:   ports.SCMRepo{Provider: "onedev", Host: "192.168.1.30:6610", Owner: "Homelab", Name: "curatarr", Repo: "Homelab/curatarr"},
			wantOK: true,
		},
		{
			name:   "deeply nested project path",
			remote: "http://192.168.1.30:6610/Homelab/tools/curatarr.git",
			want:   ports.SCMRepo{Provider: "onedev", Host: "192.168.1.30:6610", Owner: "Homelab/tools", Name: "curatarr", Repo: "Homelab/tools/curatarr"},
			wantOK: true,
		},
		{
			name:   "the .git suffix is optional",
			remote: "http://192.168.1.30:6610/Homelab/curatarr",
			want:   ports.SCMRepo{Provider: "onedev", Host: "192.168.1.30:6610", Owner: "Homelab", Name: "curatarr", Repo: "Homelab/curatarr"},
			wantOK: true,
		},
		{
			name:   "https remote on a default-port host",
			remote: "https://git.example.com/Homelab/curatarr.git",
			want:   ports.SCMRepo{Provider: "onedev", Host: "git.example.com", Owner: "Homelab", Name: "curatarr", Repo: "Homelab/curatarr"},
			wantOK: true,
		},
		{
			name:   "scp-style remote",
			remote: "git@git.example.com:Homelab/curatarr.git",
			want:   ports.SCMRepo{Provider: "onedev", Host: "git.example.com", Owner: "Homelab", Name: "curatarr", Repo: "Homelab/curatarr"},
			wantOK: true,
		},
		{
			name:   "host case is normalized",
			remote: "https://GIT.Example.COM/curatarr.git",
			want:   ports.SCMRepo{Provider: "onedev", Host: "git.example.com", Owner: "", Name: "curatarr", Repo: "curatarr"},
			wantOK: true,
		},
		{
			name:   "trailing slash",
			remote: "http://192.168.1.30:6610/Homelab/curatarr.git/",
			want:   ports.SCMRepo{Provider: "onedev", Host: "192.168.1.30:6610", Owner: "Homelab", Name: "curatarr", Repo: "Homelab/curatarr"},
			wantOK: true,
		},
		{
			name:   "surrounding whitespace",
			remote: "  http://192.168.1.30:6610/curatarr.git  ",
			want:   ports.SCMRepo{Provider: "onedev", Host: "192.168.1.30:6610", Owner: "", Name: "curatarr", Repo: "curatarr"},
			wantOK: true,
		},
		{name: "empty remote", remote: ""},
		{name: "whitespace remote", remote: "   "},
		{name: "host not in the allowlist", remote: "http://onedev.attacker.example:6610/curatarr.git"},
		{name: "github remote", remote: "git@github.com:aoagents/agent-orchestrator.git"},
		{name: "gitlab remote", remote: "https://gitlab.com/group/project.git"},
		{name: "no project path", remote: "http://192.168.1.30:6610/"},
		{name: "only a .git suffix", remote: "http://192.168.1.30:6610/.git"},
		{name: "empty path segment", remote: "http://192.168.1.30:6610/Homelab//curatarr.git"},
		{name: "traversal segment", remote: "http://192.168.1.30:6610/Homelab/../curatarr.git"},
		{name: "dot segment", remote: "http://192.168.1.30:6610/./curatarr.git"},
		{name: "unsupported scheme", remote: "file:///srv/git/curatarr.git"},
		{name: "not a url", remote: "just some text"},
		{name: "local path", remote: "/srv/git/curatarr.git"},
	}

	p := newTestProvider(t, hosts)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := p.ParseRepository(tt.remote)
			if ok != tt.wantOK {
				t.Fatalf("ParseRepository(%q) ok = %v, want %v (repo %+v)", tt.remote, ok, tt.wantOK, got)
			}
			if !tt.wantOK {
				return
			}
			if got != tt.want {
				t.Fatalf("ParseRepository(%q) = %+v, want %+v", tt.remote, got, tt.want)
			}
		})
	}
}

// TestParseRepositoryAmbiguousHostnameRejected covers the one case where the
// SSH-port fallback cannot decide: two allowlisted instances share a hostname
// on different ports, so a remote on a third port matches neither uniquely.
func TestParseRepositoryAmbiguousHostnameRejected(t *testing.T) {
	p := newTestProvider(t, []string{"http://od.test:6610", "http://od.test:7610"})

	if _, ok := p.ParseRepository("ssh://git@od.test:6611/curatarr.git"); ok {
		t.Fatal("ambiguous hostname resolved; want rejection")
	}
	// An exact authority match is still unambiguous.
	got, ok := p.ParseRepository("http://od.test:7610/curatarr.git")
	if !ok {
		t.Fatal("exact authority match rejected")
	}
	if got.Host != "od.test:7610" {
		t.Fatalf("Host = %q, want od.test:7610", got.Host)
	}
}

func TestSCMCredentialsAvailable(t *testing.T) {
	boom := errors.New("keyring is locked")
	tests := []struct {
		name    string
		opts    ProviderOptions
		want    bool
		wantErr error
	}{
		{
			name: "default token",
			opts: ProviderOptions{AllowedHosts: []string{"od.test"}, Token: StaticTokenSource("od-token")},
			want: true,
		},
		{
			name: "per-host token only",
			opts: ProviderOptions{
				AllowedHosts: []string{"od.test"},
				HostTokens:   map[string]TokenSource{"od.test": StaticTokenSource("od-host")},
			},
			want: true,
		},
		{
			name: "nothing configured",
			opts: ProviderOptions{AllowedHosts: []string{"od.test"}, SkipTokenPreflight: true},
			want: false,
		},
		{
			name: "broken source surfaces as an error",
			opts: ProviderOptions{
				AllowedHosts:       []string{"od.test"},
				Token:              errSource{boom},
				SkipTokenPreflight: true,
			},
			wantErr: boom,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewProvider(tt.opts)
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}
			got, err := p.SCMCredentialsAvailable(context.Background())
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SCMCredentialsAvailable: %v", err)
			}
			if got != tt.want {
				t.Fatalf("SCMCredentialsAvailable() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPreflightPerHostCredentials checks that each instance is preflighted
// with its own credential and that a per-host override is not applied to
// another host.
func TestPreflightPerHostCredentials(t *testing.T) {
	seen := map[string]string{}
	handler := func(w http.ResponseWriter, r *http.Request) {
		seen[r.Host] = r.Header.Get("Authorization")
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("[]"))
	}
	a := newTestServer(t, handler)
	b := newTestServer(t, handler)

	p, err := NewProvider(ProviderOptions{
		AllowedHosts: []string{a.URL, b.URL},
		Token:        StaticTokenSource("default-token"),
		HostTokens: map[string]TokenSource{
			b.URL: StaticTokenSource("b-token"),
		},
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if err := p.Preflight(context.Background()); err != nil {
		t.Fatalf("Preflight: %v", err)
	}

	aHost := strings.TrimPrefix(a.URL, "http://")
	bHost := strings.TrimPrefix(b.URL, "http://")
	if got := seen[aHost]; got != "Bearer default-token" {
		t.Errorf("host a Authorization = %q, want Bearer default-token", got)
	}
	if got := seen[bHost]; got != "Bearer b-token" {
		t.Errorf("host b Authorization = %q, want Bearer b-token", got)
	}
}

// TestPreflightJoinsPerHostErrors: one broken instance must not hide another's
// message, and a healthy instance must not mask a broken one.
func TestPreflightJoinsPerHostErrors(t *testing.T) {
	ok := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("[]"))
	})
	bad := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("Invalid account or incorrect credentials"))
	})

	p, err := NewProvider(ProviderOptions{
		AllowedHosts: []string{ok.URL, bad.URL},
		Token:        StaticTokenSource("od-token"),
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	err = p.Preflight(context.Background())
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("err = %v, want ErrAuthFailed", err)
	}
	if !strings.Contains(err.Error(), bad.URL) {
		t.Fatalf("err = %v, want it to name the failing instance %q", err, bad.URL)
	}
	if strings.Contains(err.Error(), ok.URL) {
		t.Fatalf("err = %v, want it to omit the healthy instance %q", err, ok.URL)
	}
}

func TestClientForRepo(t *testing.T) {
	p := newTestProvider(t, []string{"http://192.168.1.30:6610"})

	t.Run("allowed host", func(t *testing.T) {
		repo, ok := p.ParseRepository("ssh://git@192.168.1.30:6611/Homelab/curatarr.git")
		if !ok {
			t.Fatal("ParseRepository rejected a valid remote")
		}
		c, err := p.clientForRepo(repo)
		if err != nil {
			t.Fatalf("clientForRepo: %v", err)
		}
		if got := c.APIBase(); got != "http://192.168.1.30:6610/~api" {
			t.Fatalf("APIBase() = %q, want http://192.168.1.30:6610/~api", got)
		}
		// The client is memoized per instance.
		again, err := p.clientForRepo(repo)
		if err != nil {
			t.Fatalf("clientForRepo (second): %v", err)
		}
		if again != c {
			t.Fatal("clientForRepo returned a fresh client; want the memoized one")
		}
	})

	t.Run("host not in the allowlist", func(t *testing.T) {
		_, err := p.clientForRepo(ports.SCMRepo{Provider: ProviderKey, Host: "onedev.attacker.example:6610"})
		if !errors.Is(err, ErrHostNotAllowed) {
			t.Fatalf("err = %v, want ErrHostNotAllowed", err)
		}
	})
}

// TestNewProviderRejectsConflictingSchemes: the same instance listed twice is
// harmless, but listed twice under different schemes it would let config order
// decide whether the connection is encrypted.
func TestNewProviderRejectsConflictingSchemes(t *testing.T) {
	tests := []struct {
		name       string
		hosts      []string
		wantHosts  []string
		wantSubstr string
	}{
		{
			name:      "exact duplicate is deduped",
			hosts:     []string{"od.test:6610", "od.test:6610"},
			wantHosts: []string{"https://od.test:6610"},
		},
		{
			name:      "duplicate differing only in case",
			hosts:     []string{"OD.test:6610", "od.TEST:6610"},
			wantHosts: []string{"https://od.test:6610"},
		},
		{
			name:      "same scheme written explicitly and implicitly",
			hosts:     []string{"od.test:6610", "https://od.test:6610"},
			wantHosts: []string{"https://od.test:6610"},
		},
		{
			name:       "conflicting schemes",
			hosts:      []string{"od.test:6610", "http://od.test:6610"},
			wantSubstr: "conflicting schemes",
		},
		{
			// The same conflict in the other order must be rejected too —
			// that is the whole point.
			name:       "conflicting schemes, reversed",
			hosts:      []string{"http://od.test:6610", "od.test:6610"},
			wantSubstr: "conflicting schemes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewProvider(ProviderOptions{
				AllowedHosts: tt.hosts,
				Token:        StaticTokenSource("od-token"),
			})
			if tt.wantSubstr != "" {
				if err == nil {
					t.Fatalf("NewProvider(%v) succeeded, want error", tt.hosts)
				}
				if !strings.Contains(err.Error(), tt.wantSubstr) {
					t.Fatalf("err = %v, want it to mention %q", err, tt.wantSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}
			if got := p.AllowedHosts(); !reflect.DeepEqual(got, tt.wantHosts) {
				t.Fatalf("AllowedHosts() = %v, want %v", got, tt.wantHosts)
			}
		})
	}
}

// TestPerHostTokenKeyResolution: a per-host token key is resolved the same way
// a git remote is, so a key that omits the port (or names the SSH port) still
// selects the allowlisted instance instead of being silently dropped.
func TestPerHostTokenKeyResolution(t *testing.T) {
	tests := []struct {
		name       string
		allowed    string
		tokenKey   string
		wantAuth   string
		wantWarn   bool
		wantSubstr string
	}{
		{
			name: "exact authority", allowed: "http://od.test:6610", tokenKey: "od.test:6610",
			wantAuth: "Bearer host-token",
		},
		{
			name: "key omits the port", allowed: "http://od.test:6610", tokenKey: "od.test",
			wantAuth: "Bearer host-token",
		},
		{
			name: "key names the git ssh port", allowed: "http://od.test:6610", tokenKey: "od.test:6611",
			wantAuth: "Bearer host-token",
		},
		{
			name: "scheme-qualified key", allowed: "http://od.test:6610", tokenKey: "http://od.test:6610",
			wantAuth: "Bearer host-token",
		},
		{
			name: "key case is normalized", allowed: "http://od.test:6610", tokenKey: "OD.TEST:6610",
			wantAuth: "Bearer host-token",
		},
		{
			// A surplus entry is not fatal, but it must not pass unremarked:
			// that is exactly what a typo looks like from the outside.
			name: "key names no configured host", allowed: "http://od.test:6610", tokenKey: "other.test",
			wantAuth: "Bearer default-token", wantWarn: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs strings.Builder
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))

			p, err := NewProvider(ProviderOptions{
				AllowedHosts: []string{tt.allowed},
				Logger:       logger,
				Token:        StaticTokenSource("default-token"),
				HostTokens:   map[string]TokenSource{tt.tokenKey: StaticTokenSource("host-token")},
			})
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}

			c, err := p.clientForAuthority("od.test:6610")
			if err != nil {
				t.Fatalf("clientForAuthority: %v", err)
			}
			req, err := c.newRequest(context.Background(), "/projects", nil)
			if err != nil {
				t.Fatalf("newRequest: %v", err)
			}
			if got := req.Header.Get("Authorization"); got != tt.wantAuth {
				t.Fatalf("Authorization = %q, want %q", got, tt.wantAuth)
			}

			warned := strings.Contains(logs.String(), "names no configured host")
			if warned != tt.wantWarn {
				t.Fatalf("warning logged = %v, want %v (logs: %s)", warned, tt.wantWarn, logs.String())
			}
			if tt.wantWarn && !strings.Contains(logs.String(), tt.tokenKey) {
				t.Fatalf("warning does not name the offending key %q: %s", tt.tokenKey, logs.String())
			}
		})
	}
}

// TestNewProviderRejectsCollidingPerHostTokens: two keys resolving to one
// instance means one of the two overrides would be discarded, and which one
// is not something to decide by map order.
func TestNewProviderRejectsCollidingPerHostTokens(t *testing.T) {
	_, err := NewProvider(ProviderOptions{
		AllowedHosts: []string{"http://od.test:6610"},
		HostTokens: map[string]TokenSource{
			"od.test":      StaticTokenSource("a"),
			"od.test:6610": StaticTokenSource("b"),
		},
	})
	if err == nil {
		t.Fatal("NewProvider succeeded, want error")
	}
	if !strings.Contains(err.Error(), "both resolve to host") {
		t.Fatalf("err = %v, want it to report the collision", err)
	}
}

// TestAnyCredentialIsOrderIndependent pins that construction does not depend
// on map iteration order: with one working and one broken per-host source the
// result must be the same every time, and a working source must win.
func TestAnyCredentialIsOrderIndependent(t *testing.T) {
	boom := errors.New("keyring is locked")
	hostTokens := map[string]TokenSource{
		"a.test": errSource{boom},
		"b.test": StaticTokenSource("od-token"),
		"c.test": errSource{boom},
		"d.test": StaticTokenSource(""),
	}
	for i := 0; i < 50; i++ {
		if err := anyCredential(context.Background(), nil, hostTokens); err != nil {
			t.Fatalf("iteration %d: anyCredential = %v, want nil (a working source exists)", i, err)
		}
	}

	// With nothing working, the hard error is what surfaces — every time.
	onlyBroken := map[string]TokenSource{
		"a.test": errSource{boom},
		"b.test": StaticTokenSource(""),
		"c.test": errSource{boom},
	}
	for i := 0; i < 50; i++ {
		if err := anyCredential(context.Background(), nil, onlyBroken); !errors.Is(err, boom) {
			t.Fatalf("iteration %d: anyCredential = %v, want boom", i, err)
		}
	}

	// And with nothing configured at all it is ErrNoToken, not a hard error.
	if err := anyCredential(context.Background(), nil, map[string]TokenSource{
		"a.test": StaticTokenSource(""),
	}); !errors.Is(err, ErrNoToken) {
		t.Fatalf("anyCredential = %v, want ErrNoToken", err)
	}
}

// TestNewProviderFailsWhenOnlyCredentialIsMisKeyed covers the second half of
// the mis-keyed per-host token problem: a deployment whose only credential
// names no configured host must fail at construction, naming the cause, rather
// than constructing fine and returning ErrNoToken on its first request.
func TestNewProviderFailsWhenOnlyCredentialIsMisKeyed(t *testing.T) {
	tests := []struct {
		name     string
		tokenKey string
		wantErr  bool
	}{
		{
			// A key that merely omits the API port is not mis-keyed — it
			// resolves, and construction succeeds.
			name: "port omitted still resolves", tokenKey: "10.0.0.30", wantErr: false,
		},
		{
			name: "key names the git ssh port", tokenKey: "10.0.0.30:6611", wantErr: false,
		},
		{
			name: "key names no configured host", tokenKey: "10.0.0.99:6610", wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs strings.Builder
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))

			_, err := NewProvider(ProviderOptions{
				AllowedHosts: []string{"http://10.0.0.30:6610"},
				Logger:       logger,
				// No default token: the per-host entry is the only credential.
				HostTokens: map[string]TokenSource{tt.tokenKey: StaticTokenSource("host-token")},
			})
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("NewProvider: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrNoToken) {
				t.Fatalf("err = %v, want ErrNoToken at construction", err)
			}
			if !strings.Contains(logs.String(), tt.tokenKey) {
				t.Fatalf("nothing named the offending key %q: %s", tt.tokenKey, logs.String())
			}
		})
	}
}
