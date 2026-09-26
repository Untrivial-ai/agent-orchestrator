package onedev

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCredentialApply(t *testing.T) {
	tests := []struct {
		name     string
		cred     Credential
		wantErr  error
		wantAuth string
		wantUser string
		wantPass string
	}{
		{
			name: "bearer token", cred: BearerCredential("od-token"),
			wantAuth: "Bearer od-token",
		},
		{
			name: "bearer token is trimmed", cred: BearerCredential("  od-token\n"),
			wantAuth: "Bearer od-token",
		},
		{
			name: "basic auth", cred: BasicCredential("johnkattenhorn", "s3cret"),
			wantUser: "johnkattenhorn", wantPass: "s3cret",
		},
		{
			// A password may legitimately be blank-looking; only the username
			// decides whether basic auth applies.
			name: "basic auth with empty password", cred: BasicCredential("svc", ""),
			wantUser: "svc", wantPass: "",
		},
		{
			name:     "token wins over basic auth",
			cred:     Credential{Token: "od-token", Username: "svc", Password: "pw"},
			wantAuth: "Bearer od-token",
		},
		{name: "empty credential", cred: Credential{}, wantErr: ErrNoToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "http://example.test/~api/projects", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			err = tt.cred.apply(req)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("apply() err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("apply(): %v", err)
			}
			if tt.wantAuth != "" {
				if got := req.Header.Get("Authorization"); got != tt.wantAuth {
					t.Fatalf("Authorization = %q, want %q", got, tt.wantAuth)
				}
				return
			}
			user, pass, ok := req.BasicAuth()
			if !ok {
				t.Fatalf("BasicAuth not set; Authorization = %q", req.Header.Get("Authorization"))
			}
			if user != tt.wantUser || pass != tt.wantPass {
				t.Fatalf("BasicAuth = (%q, %q), want (%q, %q)", user, pass, tt.wantUser, tt.wantPass)
			}
		})
	}
}

func TestStaticSources(t *testing.T) {
	tests := []struct {
		name    string
		src     TokenSource
		want    Credential
		wantErr error
	}{
		{name: "static token", src: StaticTokenSource("od-abc"), want: Credential{Token: "od-abc"}},
		{name: "static token trims", src: StaticTokenSource("  od-abc  "), want: Credential{Token: "od-abc"}},
		{name: "static token empty", src: StaticTokenSource("   "), wantErr: ErrNoToken},
		{
			name: "static basic auth",
			src:  StaticBasicAuthSource{Username: "svc", Password: "pw"},
			want: Credential{Username: "svc", Password: "pw"},
		},
		{
			name:    "static basic auth without username",
			src:     StaticBasicAuthSource{Password: "pw"},
			wantErr: ErrNoToken,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.src.Token(context.Background())
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Token() err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Token(): %v", err)
			}
			if got != tt.want {
				t.Fatalf("Token() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestEnvTokenSource(t *testing.T) {
	tests := []struct {
		name    string
		envVars []string
		set     map[string]string
		want    string
		wantErr error
	}{
		{
			name: "reads the configured var", envVars: []string{"AO_ONEDEV_TOKEN"},
			set: map[string]string{"AO_ONEDEV_TOKEN": "od-scoped"}, want: "od-scoped",
		},
		{
			name: "scoped var wins over the global default", envVars: []string{"AO_ONEDEV_TOKEN"},
			set:  map[string]string{"AO_ONEDEV_TOKEN": "od-scoped", "ONEDEV_TOKEN": "od-global"},
			want: "od-scoped",
		},
		{
			name: "falls back to ONEDEV_TOKEN", envVars: []string{"AO_ONEDEV_TOKEN"},
			set: map[string]string{"ONEDEV_TOKEN": "od-global"}, want: "od-global",
		},
		{
			name: "first non-empty of several", envVars: []string{"AO_ONEDEV_TOKEN", "AO_ONEDEV_TOKEN_ALT"},
			set:  map[string]string{"AO_ONEDEV_TOKEN": "  ", "AO_ONEDEV_TOKEN_ALT": "od-alt"},
			want: "od-alt",
		},
		{
			name: "whitespace is trimmed", envVars: []string{"AO_ONEDEV_TOKEN"},
			set: map[string]string{"AO_ONEDEV_TOKEN": "  od-padded\n"}, want: "od-padded",
		},
		{
			name: "nothing set", envVars: []string{"AO_ONEDEV_TOKEN"},
			set: map[string]string{}, wantErr: ErrNoToken,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, name := range []string{"AO_ONEDEV_TOKEN", "AO_ONEDEV_TOKEN_ALT", "ONEDEV_TOKEN"} {
				t.Setenv(name, "")
			}
			for name, val := range tt.set {
				t.Setenv(name, val)
			}
			got, err := EnvTokenSource{EnvVars: tt.envVars}.Token(context.Background())
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Token() err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Token(): %v", err)
			}
			if got.Token != tt.want {
				t.Fatalf("token = %q, want %q", got.Token, tt.want)
			}
		})
	}
}

// errSource is a TokenSource that always fails with a non-ErrNoToken error.
type errSource struct{ err error }

func (s errSource) Token(context.Context) (Credential, error) { return Credential{}, s.err }

func TestFallbackTokenSource(t *testing.T) {
	boom := errors.New("credential helper exploded")
	tests := []struct {
		name    string
		src     FallbackTokenSource
		want    Credential
		wantErr error
	}{
		{
			name: "first usable source wins",
			src:  FallbackTokenSource{StaticTokenSource("first"), StaticTokenSource("second")},
			want: Credential{Token: "first"},
		},
		{
			name: "skips sources reporting ErrNoToken",
			src:  FallbackTokenSource{StaticTokenSource(""), StaticTokenSource("second")},
			want: Credential{Token: "second"},
		},
		{
			name: "nil entries are skipped",
			src:  FallbackTokenSource{nil, StaticTokenSource("second")},
			want: Credential{Token: "second"},
		},
		{
			// A broken helper must not mask a working env var later in the chain.
			name: "a later success beats an earlier hard failure",
			src:  FallbackTokenSource{errSource{boom}, StaticTokenSource("second")},
			want: Credential{Token: "second"},
		},
		{
			name:    "hard failure surfaces when nothing else works",
			src:     FallbackTokenSource{errSource{boom}, StaticTokenSource("")},
			wantErr: boom,
		},
		{name: "empty chain", src: FallbackTokenSource{}, wantErr: ErrNoToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.src.Token(context.Background())
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Token() err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Token(): %v", err)
			}
			if got != tt.want {
				t.Fatalf("Token() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCommandTokenSource(t *testing.T) {
	t.Run("disabled without a command", func(t *testing.T) {
		src := &CommandTokenSource{}
		if _, err := src.Token(context.Background()); !errors.Is(err, ErrNoToken) {
			t.Fatalf("err = %v, want ErrNoToken", err)
		}
	})

	t.Run("caches within the TTL", func(t *testing.T) {
		calls := 0
		src := &CommandTokenSource{
			Command:  []string{"helper"},
			TokenTTL: time.Hour,
			Run: func(context.Context, []string) (string, error) {
				calls++
				return "od-from-helper\n", nil
			},
		}
		for i := 0; i < 2; i++ {
			got, err := src.Token(context.Background())
			if err != nil {
				t.Fatalf("Token(): %v", err)
			}
			if got.Token != "od-from-helper" {
				t.Fatalf("token = %q, want od-from-helper", got.Token)
			}
		}
		if calls != 1 {
			t.Fatalf("helper ran %d times, want 1 (cached)", calls)
		}
	})

	t.Run("re-runs after the TTL expires", func(t *testing.T) {
		calls := 0
		now := time.Unix(1_700_000_000, 0)
		src := &CommandTokenSource{
			Command:  []string{"helper"},
			TokenTTL: time.Minute,
			Clock:    func() time.Time { return now },
			Run: func(context.Context, []string) (string, error) {
				calls++
				return "od-from-helper", nil
			},
		}
		if _, err := src.Token(context.Background()); err != nil {
			t.Fatal(err)
		}
		now = now.Add(2 * time.Minute)
		if _, err := src.Token(context.Background()); err != nil {
			t.Fatal(err)
		}
		if calls != 2 {
			t.Fatalf("helper ran %d times, want 2 (TTL expired)", calls)
		}
	})

	t.Run("InvalidateToken clears the cache", func(t *testing.T) {
		calls := 0
		src := &CommandTokenSource{
			Command:  []string{"helper"},
			TokenTTL: time.Hour,
			Run: func(context.Context, []string) (string, error) {
				calls++
				return "od-from-helper", nil
			},
		}
		if _, err := src.Token(context.Background()); err != nil {
			t.Fatal(err)
		}
		src.InvalidateToken()
		if _, err := src.Token(context.Background()); err != nil {
			t.Fatal(err)
		}
		if calls != 2 {
			t.Fatalf("helper ran %d times, want 2 (cache invalidated)", calls)
		}
	})

	t.Run("username makes the output a basic-auth password", func(t *testing.T) {
		src := &CommandTokenSource{
			Command:  []string{"helper"},
			Username: "svc",
			Run:      func(context.Context, []string) (string, error) { return "keyring-secret\n", nil },
		}
		got, err := src.Token(context.Background())
		if err != nil {
			t.Fatalf("Token(): %v", err)
		}
		want := Credential{Username: "svc", Password: "keyring-secret"}
		if got != want {
			t.Fatalf("Token() = %+v, want %+v", got, want)
		}
	})

	t.Run("empty output is ErrNoToken", func(t *testing.T) {
		src := &CommandTokenSource{
			Command: []string{"helper"},
			Run:     func(context.Context, []string) (string, error) { return "  \n", nil },
		}
		if _, err := src.Token(context.Background()); !errors.Is(err, ErrNoToken) {
			t.Fatalf("err = %v, want ErrNoToken", err)
		}
	})

	t.Run("run error propagates", func(t *testing.T) {
		boom := errors.New("boom")
		src := &CommandTokenSource{
			Command: []string{"helper"},
			Run:     func(context.Context, []string) (string, error) { return "", boom },
		}
		if _, err := src.Token(context.Background()); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
	})
}

// TestRunCredentialCommandMissingBinary pins the "disable, don't error"
// contract: an unavailable helper must look like an absent credential so the
// provider goes quiet rather than failing every poll.
func TestRunCredentialCommandMissingBinary(t *testing.T) {
	_, err := runCredentialCommand(context.Background(), []string{"ao-onedev-no-such-helper-binary"})
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

// newTestServer is shared by the client and provider tests: it records the
// last request and replies with the supplied handler.
func newTestServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}
