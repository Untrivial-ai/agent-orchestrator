package gitlab

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestParseGLabTokenLine(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name: "real glab output with checkmark",
			output: "Hostname: gitlab.com\n" +
				"✓ Token found: glpat-xxxxxxxxxxxxxxxx\n" +
				"Api Protocol: https\n",
			want: "glpat-xxxxxxxxxxxxxxxx",
		},
		{
			name:   "plain Token: prefix without checkmark",
			output: "Token: glpat-yyyy\n",
			want:   "glpat-yyyy",
		},
		{
			name:   "token line with trailing whitespace",
			output: "✓ Token found: glpat-yyy  \n",
			want:   "glpat-yyy",
		},
		{
			name:   "token line with extra spaces after colon",
			output: "Token:    glpat-spaced\n",
			want:   "glpat-spaced",
		},
		{
			name:   "no token line",
			output: "Hostname: gitlab.com\nApi Protocol: https\n",
			want:   "",
		},
		{
			name:   "empty token value",
			output: "✓ Token found: \n",
			want:   "",
		},
		{
			name:   "empty output",
			output: "",
			want:   "",
		},
		{
			name: "token not on first line",
			output: "Api Protocol: https\n" +
				"Hostname: gitlab.com\n" +
				"✓ Token found: glpat-zzz\n",
			want: "glpat-zzz",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseGLabTokenLine(tt.output)
			if got != tt.want {
				t.Fatalf("ParseGLabTokenLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGLabTokenSourceUsesInjectedHook(t *testing.T) {
	calls := 0
	src := &GLabTokenSource{
		GLab: func(ctx context.Context) (string, error) {
			calls++
			return "glpat-from-hook\n", nil
		},
		TokenTTL: time.Hour,
	}
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "glpat-from-hook" {
		t.Fatalf("token = %q, want glpat-from-hook", tok)
	}
	// Second call must use the cache (no new shell-out).
	tok2, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token (cached): %v", err)
	}
	if tok2 != "glpat-from-hook" {
		t.Fatalf("cached token = %q", tok2)
	}
	if calls != 1 {
		t.Fatalf("hook called %d times, want 1 (cached)", calls)
	}
}

func TestGLabTokenSourceRejectsEmptyOutput(t *testing.T) {
	src := &GLabTokenSource{
		GLab: func(ctx context.Context) (string, error) {
			return "", nil
		},
	}
	_, err := src.Token(context.Background())
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

func TestGLabTokenSourcePropagatesNonNoTokenError(t *testing.T) {
	boom := errors.New("boom")
	src := &GLabTokenSource{
		GLab: func(ctx context.Context) (string, error) {
			return "", boom
		},
	}
	_, err := src.Token(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestGLabTokenSourceInvalidateClearsCache(t *testing.T) {
	calls := 0
	src := &GLabTokenSource{
		GLab: func(ctx context.Context) (string, error) {
			calls++
			return "glpat-aaa\n", nil
		},
		TokenTTL: time.Hour,
	}
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	src.InvalidateToken()
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("hook called %d times, want 2 (cache invalidated)", calls)
	}
}

// TestGLabTokenSourceParsesHookOutput verifies that the GLab hook returns
// just the parsed token (same as glabAuthToken would), not the raw status block.
func TestGLabTokenSourceParsesHookOutput(t *testing.T) {
	src := &GLabTokenSource{
		GLab: func(ctx context.Context) (string, error) {
			// The real glabAuthToken parses `glab auth status --show-token` output
			// and returns just the token. The injected hook mirrors that contract.
			return "glpat-parsed\n", nil
		},
		TokenTTL: time.Hour,
	}
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "glpat-parsed" {
		t.Fatalf("token = %q, want glpat-parsed", tok)
	}
}

// TestHostTokenSourceScopesGLabToHost covers the per-host chain: glab must be
// asked about the specific instance, so a glab authenticated against several
// hosts cannot hand back a token that belongs to a different instance.
func TestHostTokenSourceScopesGLabToHost(t *testing.T) {
	chain, ok := HostTokenSource(" GitLab.Internal:8443 ").(FallbackTokenSource)
	if !ok {
		t.Fatalf("HostTokenSource returned %T, want FallbackTokenSource", HostTokenSource("gitlab.internal"))
	}
	if len(chain) != 2 {
		t.Fatalf("chain length = %d, want scoped glab + env", len(chain))
	}
	scoped, ok := chain[0].(*GLabTokenSource)
	if !ok {
		t.Fatalf("chain[0] = %T, want *GLabTokenSource", chain[0])
	}
	if scoped.Hostname != "gitlab.internal:8443" {
		t.Fatalf("scoped hostname = %q, want the normalized host", scoped.Hostname)
	}
	if _, ok := chain[1].(*EnvTokenSource); !ok {
		t.Fatalf("chain[1] = %T, want *EnvTokenSource", chain[1])
	}
}

// TestHostTokenSourceForDotComUsesTheDefaultChain covers the gitlab.com case:
// gitlab.com is not a self-managed host, so it keeps the documented
// env-vars-first precedence instead of the host-scoped ordering.
func TestHostTokenSourceForDotComUsesTheDefaultChain(t *testing.T) {
	for _, host := range []string{"", "gitlab.com", "www.gitlab.com"} {
		chain, ok := HostTokenSource(host).(FallbackTokenSource)
		if !ok {
			t.Fatalf("HostTokenSource(%q) returned %T, want FallbackTokenSource", host, HostTokenSource(host))
		}
		if len(chain) != 2 {
			t.Fatalf("HostTokenSource(%q) chain length = %d, want env + glab scoped to gitlab.com", host, len(chain))
		}
		if _, ok := chain[0].(*EnvTokenSource); !ok {
			t.Fatalf("HostTokenSource(%q) chain[0] = %T, want *EnvTokenSource", host, chain[0])
		}
		scoped, ok := chain[1].(*GLabTokenSource)
		if !ok || scoped.Hostname != DotComHost {
			t.Fatalf("HostTokenSource(%q) chain[1] = %#v, want glab scoped to gitlab.com", host, chain[1])
		}
	}
}

// TestGLabTokenSourceMemoizesFailures covers the negative cache: a source glab
// cannot satisfy (an unknown --hostname, or a glab too old for the flag) is
// consulted on every token resolution, once per allowlisted host. Without a
// memo each of those forks a process; the failure must be remembered instead.
func TestGLabTokenSourceMemoizesFailures(t *testing.T) {
	for _, tt := range []struct {
		name string
		out  string
		err  error
	}{
		{name: "command failure", err: errors.New("unknown flag: --hostname")},
		{name: "no token printed", out: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			now := time.Now()
			src := &GLabTokenSource{
				Hostname: "gitlab.internal",
				GLab: func(context.Context) (string, error) {
					calls++
					return tt.out, tt.err
				},
				Clock: func() time.Time { return now },
			}
			for i := 0; i < 5; i++ {
				if _, err := src.Token(context.Background()); err == nil {
					t.Fatalf("Token call %d succeeded, want a failure", i)
				}
			}
			if calls != 1 {
				t.Fatalf("hook called %d times, want 1 (failure memoized)", calls)
			}

			// The memo is short-lived: after `glab auth login` the credential
			// must become visible without a restart.
			now = now.Add(defaultGLabFailureCacheTTL + time.Second)
			if _, err := src.Token(context.Background()); err == nil {
				t.Fatal("Token after the failure window succeeded, want a failure")
			}
			if calls != 2 {
				t.Fatalf("hook called %d times, want 2 (failure window elapsed)", calls)
			}
		})
	}
}

// TestGLabTokenSourceInvalidateClearsFailure covers InvalidateToken on a
// memoized failure: the caller believes the credential situation changed, so
// the next call must shell out again rather than replay the cached error.
func TestGLabTokenSourceInvalidateClearsFailure(t *testing.T) {
	calls := 0
	src := &GLabTokenSource{
		GLab: func(context.Context) (string, error) {
			calls++
			if calls == 1 {
				return "", ErrNoToken
			}
			return "glpat-after-login\n", nil
		},
		TokenTTL: time.Hour,
	}
	if _, err := src.Token(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
	src.InvalidateToken()
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token after invalidate: %v", err)
	}
	if tok != "glpat-after-login" {
		t.Fatalf("token = %q, want glpat-after-login", tok)
	}
}

// TestGLabTokenSourceRefetchesAfterFailureWindow covers recovery from a
// transient glab failure: the memo suppresses the shell-out only until the
// failure window elapses, after which the real token is picked up.
func TestGLabTokenSourceRefetchesAfterFailureWindow(t *testing.T) {
	calls := 0
	now := time.Now()
	src := &GLabTokenSource{
		GLab: func(context.Context) (string, error) {
			calls++
			if calls == 1 {
				return "", errors.New("keyring locked")
			}
			return "glpat-recovered\n", nil
		},
		TokenTTL: time.Hour,
		Clock:    func() time.Time { return now },
	}
	if _, err := src.Token(context.Background()); err == nil {
		t.Fatal("first Token succeeded, want the command failure")
	}
	now = now.Add(defaultGLabFailureCacheTTL + time.Second)
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token after the failure window: %v", err)
	}
	if tok != "glpat-recovered" {
		t.Fatalf("token = %q, want glpat-recovered", tok)
	}
}

// TestDotComTokenSourceScopesGLabToDotCom covers the gitlab.com chain: glab
// must be asked about gitlab.com specifically, and no unscoped source may be
// in the chain. An unscoped `glab auth status --show-token` answers with
// whichever host glab lists first, so an unscoped source would send a
// self-managed token to gitlab.com — a disclosure to a third party. The
// scoped source still falls back to unscoped output internally, but only
// accepts the token that output attributes to gitlab.com.
func TestDotComTokenSourceScopesGLabToDotCom(t *testing.T) {
	chain, ok := DotComTokenSource().(FallbackTokenSource)
	if !ok {
		t.Fatalf("DotComTokenSource returned %T, want FallbackTokenSource", DotComTokenSource())
	}
	if len(chain) != 2 {
		t.Fatalf("chain length = %d, want env + glab scoped to gitlab.com", len(chain))
	}
	if _, ok := chain[0].(*EnvTokenSource); !ok {
		t.Fatalf("chain[0] = %T, want *EnvTokenSource", chain[0])
	}
	scoped, ok := chain[1].(*GLabTokenSource)
	if !ok {
		t.Fatalf("chain[1] = %T, want *GLabTokenSource", chain[1])
	}
	if scoped.Hostname != DotComHost {
		t.Fatalf("scoped hostname = %q, want %q", scoped.Hostname, DotComHost)
	}
}

// ---------------------------------------------------------------------------
// Host attribution of `glab auth status --show-token` output
// ---------------------------------------------------------------------------

const glabOldFormatDotCom = "Hostname: gitlab.com\n" +
	"✓ Token found: glpat-dotcom\n" +
	"Api Protocol: https\n"

const glabModernFormatSelfManaged = "gitlab.internal:8443\n" +
	"  ✓ Logged in to gitlab.internal:8443 as alice (keyring)\n" +
	"  ✓ Git operations for gitlab.internal:8443 configured to use https protocol.\n" +
	"  ✓ REST API Endpoint: https://gitlab.internal:8443/api/v4/\n" +
	"  ✓ Token: glpat-internal\n" +
	"  ✓ Token expires: 2027-01-01\n"

const glabModernFormatMultiHost = glabModernFormatSelfManaged + "\n" +
	"gitlab.com\n" +
	"  ✓ Logged in to gitlab.com as alice (keyring)\n" +
	"  ✓ Token: glpat-dotcom\n"

func TestParseGLabHostTokens(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []glabHostToken
	}{
		{
			name:   "old format binds the token to the Hostname line",
			output: glabOldFormatDotCom,
			want:   []glabHostToken{{host: "gitlab.com", token: "glpat-dotcom"}},
		},
		{
			name:   "modern format binds the token to its section host",
			output: glabModernFormatSelfManaged,
			want:   []glabHostToken{{host: "gitlab.internal:8443", token: "glpat-internal"}},
		},
		{
			name:   "multi-instance output keeps every host separate",
			output: glabModernFormatMultiHost,
			want: []glabHostToken{
				{host: "gitlab.internal:8443", token: "glpat-internal"},
				{host: "gitlab.com", token: "glpat-dotcom"},
			},
		},
		{
			name:   "a token with no host before it is not attributed",
			output: "✓ Token found: glpat-orphan\n",
			want:   nil,
		},
		{
			name:   "an expiry line is never mistaken for the token",
			output: "gitlab.com\n  ✓ Token expires: 2027-01-01\n  ✓ Token: glpat-dotcom\n",
			want:   []glabHostToken{{host: "gitlab.com", token: "glpat-dotcom"}},
		},
		{
			name:   "empty output has no pairs",
			output: "",
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseGLabHostTokens(tt.output)
			if len(got) != len(tt.want) {
				t.Fatalf("parseGLabHostTokens() = %+v, want %+v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("pair %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestGLabTokenForHostMatchesNormalizedHost(t *testing.T) {
	if got := GLabTokenForHost(glabModernFormatMultiHost, " GitLab.Internal:8443 "); got != "glpat-internal" {
		t.Fatalf("GLabTokenForHost = %q, want glpat-internal", got)
	}
	if got := GLabTokenForHost(glabModernFormatMultiHost, "gitlab.com"); got != "glpat-dotcom" {
		t.Fatalf("GLabTokenForHost = %q, want glpat-dotcom", got)
	}
	if got := GLabTokenForHost(glabModernFormatSelfManaged, "gitlab.com"); got != "" {
		t.Fatalf("GLabTokenForHost = %q, want no token for an absent host", got)
	}
}

// TestGLabAuthTokenScopesLookupToHost covers the primary path: glab is asked
// about one instance and its answer is that instance's token.
func TestGLabAuthTokenScopesLookupToHost(t *testing.T) {
	var gotArgs []string
	run := func(_ context.Context, args ...string) (string, error) {
		gotArgs = args
		return glabModernFormatSelfManaged, nil
	}
	tok, err := glabAuthTokenWith(context.Background(), "gitlab.internal:8443", run)
	if err != nil {
		t.Fatalf("glabAuthTokenWith: %v", err)
	}
	if tok != "glpat-internal" {
		t.Fatalf("token = %q, want glpat-internal", tok)
	}
	if len(gotArgs) < 2 || gotArgs[len(gotArgs)-2] != "--hostname" || gotArgs[len(gotArgs)-1] != "gitlab.internal:8443" {
		t.Fatalf("args = %v, want the lookup scoped with --hostname", gotArgs)
	}
}

// TestGLabAuthTokenFallsBackToAttributedUnscopedToken covers a glab too old for
// `--hostname`: the scoped call fails, and the unscoped status block is used
// only because it names the host being asked about.
func TestGLabAuthTokenFallsBackToAttributedUnscopedToken(t *testing.T) {
	calls := 0
	run := func(_ context.Context, args ...string) (string, error) {
		calls++
		for _, a := range args {
			if a == "--hostname" {
				return "unknown flag: --hostname\n", errors.New("exit status 1")
			}
		}
		return glabOldFormatDotCom, nil
	}
	tok, err := glabAuthTokenWith(context.Background(), "gitlab.com", run)
	if err != nil {
		t.Fatalf("glabAuthTokenWith: %v", err)
	}
	if tok != "glpat-dotcom" {
		t.Fatalf("token = %q, want glpat-dotcom", tok)
	}
	if calls != 2 {
		t.Fatalf("glab called %d times, want the scoped call then the unscoped fallback", calls)
	}
}

// TestGLabAuthTokenReadsAUsableBlockFromAFailedRun covers a multi-instance
// glab: `glab auth status` exits non-zero when *any* configured instance is
// unauthenticated, while still printing a usable block for the ones that are.
// Gating on the exit code would leave a perfectly good instance without a
// credential because some unrelated host has a stale session.
func TestGLabAuthTokenReadsAUsableBlockFromAFailedRun(t *testing.T) {
	run := func(_ context.Context, args ...string) (string, error) {
		for _, a := range args {
			if a == "--hostname" {
				return "", errors.New("exit status 1")
			}
		}
		return glabModernFormatMultiHost, errors.New("exit status 1")
	}
	tok, err := glabAuthTokenWith(context.Background(), "gitlab.internal:8443", run)
	if err != nil {
		t.Fatalf("glabAuthTokenWith: %v", err)
	}
	if tok != "glpat-internal" {
		t.Fatalf("token = %q, want the host's token despite the non-zero exit", tok)
	}
}

// TestGLabAuthTokenRejectsANonCredentialTokenLine covers the line a status
// block prints next to the credential: "Token expires: <date>" names a date,
// not a token, and sending it as a PRIVATE-TOKEN would 401 every call while
// looking like a rejected credential rather than a parse bug.
func TestGLabAuthTokenRejectsANonCredentialTokenLine(t *testing.T) {
	const expiryFirst = "gitlab.internal\n" +
		"  ✓ Logged in to gitlab.internal as alice (keyring)\n" +
		"  ✓ Token expires: 2026-01-01\n" +
		"  ✓ Token: glpat-internal\n"
	run := func(_ context.Context, _ ...string) (string, error) { return expiryFirst, nil }
	tok, err := glabAuthTokenWith(context.Background(), "gitlab.internal", run)
	if err != nil {
		t.Fatalf("glabAuthTokenWith: %v", err)
	}
	if tok != "glpat-internal" {
		t.Fatalf("token = %q, want the credential rather than the expiry date", tok)
	}
}

// TestGLabAuthTokenRejectsAnotherInstancesToken is the credential boundary: a
// glab whose own default host is a self-managed instance must never hand that
// instance's token to a caller asking about gitlab.com.
func TestGLabAuthTokenRejectsAnotherInstancesToken(t *testing.T) {
	run := func(_ context.Context, args ...string) (string, error) {
		for _, a := range args {
			if a == "--hostname" {
				return "", errors.New("exit status 1")
			}
		}
		return glabModernFormatSelfManaged, nil
	}
	tok, err := glabAuthTokenWith(context.Background(), "gitlab.com", run)
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
	if tok != "" {
		t.Fatalf("token = %q, want no token for an unattributable credential", tok)
	}
}

// TestGLabAuthTokenPicksTheHostFromMultiInstanceOutput covers the multi-host
// case the `--hostname` flag exists for: even unscoped, the token returned is
// the one bound to the requested host, not whichever glab listed first.
func TestGLabAuthTokenPicksTheHostFromMultiInstanceOutput(t *testing.T) {
	run := func(_ context.Context, args ...string) (string, error) {
		for _, a := range args {
			if a == "--hostname" {
				return "", errors.New("exit status 1")
			}
		}
		return glabModernFormatMultiHost, nil
	}
	tok, err := glabAuthTokenWith(context.Background(), "gitlab.com", run)
	if err != nil {
		t.Fatalf("glabAuthTokenWith: %v", err)
	}
	if tok != "glpat-dotcom" {
		t.Fatalf("token = %q, want gitlab.com's own token", tok)
	}
}

// TestHostTokenSourcePrefersHostScopedGLabOverGlobalEnv covers credential
// precedence: a credential the user bound to this instance outranks the global
// default, which would otherwise send a gitlab.com token to a self-managed
// server.
func TestHostTokenSourcePrefersHostScopedGLabOverGlobalEnv(t *testing.T) {
	chain, ok := HostTokenSource("gitlab.internal").(FallbackTokenSource)
	if !ok {
		t.Fatalf("HostTokenSource returned %T, want FallbackTokenSource", HostTokenSource("gitlab.internal"))
	}
	if len(chain) != 2 {
		t.Fatalf("chain length = %d, want scoped glab + env", len(chain))
	}
	scoped, ok := chain[0].(*GLabTokenSource)
	if !ok || scoped.Hostname != "gitlab.internal" {
		t.Fatalf("chain[0] = %#v, want the host-scoped *GLabTokenSource first", chain[0])
	}
	if _, ok := chain[1].(*EnvTokenSource); !ok {
		t.Fatalf("chain[1] = %T, want *EnvTokenSource", chain[1])
	}
}

// TestGLabTokenSourceDoesNotMemoizeACancelledLookup covers the negative cache's
// boundary: a lookup the *caller* ended says nothing about whether glab holds a
// credential, so remembering it as a failure would answer later callers — ones
// with a perfectly good context — out of a stale cache.
func TestGLabTokenSourceDoesNotMemoizeACancelledLookup(t *testing.T) {
	calls := 0
	src := &GLabTokenSource{
		GLab: func(ctx context.Context) (string, error) {
			calls++
			if err := ctx.Err(); err != nil {
				return "", ErrNoToken
			}
			return "glpat-live", nil
		},
		TokenTTL: time.Hour,
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := src.Token(cancelled); !errors.Is(err, ErrNoToken) {
		t.Fatalf("Token(cancelled) err = %v, want ErrNoToken", err)
	}

	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token after the cancelled lookup: %v", err)
	}
	if tok != "glpat-live" {
		t.Fatalf("token = %q, want the source to re-run rather than answer from the failure cache", tok)
	}
	if calls != 2 {
		t.Fatalf("glab called %d times, want the cancelled lookup not to be memoized", calls)
	}
}
