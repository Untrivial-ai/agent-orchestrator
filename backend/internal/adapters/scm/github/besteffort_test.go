package github

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCredentialHelperTokenSourceParsesPasswordAndCaches(t *testing.T) {
	calls := 0
	src := &CredentialHelperTokenSource{
		Fill: func(ctx context.Context, host string) (string, error) {
			calls++
			if host != defaultCredentialHost {
				t.Fatalf("host = %q, want %q", host, defaultCredentialHost)
			}
			return "ghp_fromhelper", nil
		},
		TokenTTL: time.Hour,
	}
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok != "ghp_fromhelper" {
		t.Fatalf("Token = %q, want %q", tok, "ghp_fromhelper")
	}
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if calls != 1 {
		t.Fatalf("Fill called %d times; want 1 (cache hit)", calls)
	}
	src.InvalidateToken()
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("third Token: %v", err)
	}
	if calls != 2 {
		t.Fatalf("after invalidate, Fill called %d times; want 2", calls)
	}
}

func TestCredentialHelperTokenSourceEmptyIsErrNoToken(t *testing.T) {
	src := &CredentialHelperTokenSource{
		Fill: func(ctx context.Context, host string) (string, error) { return "   ", nil },
	}
	if _, err := src.Token(context.Background()); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

func TestCredentialHelperTokenSourceHonorsHost(t *testing.T) {
	src := &CredentialHelperTokenSource{
		Host: "ghe.example.com",
		Fill: func(ctx context.Context, host string) (string, error) {
			if host != "ghe.example.com" {
				t.Fatalf("host = %q, want ghe.example.com", host)
			}
			return "t", nil
		},
	}
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
}

func TestParseCredentialPassword(t *testing.T) {
	cases := map[string]string{
		"protocol=https\nhost=github.com\nusername=x\npassword=secret\n": "secret",
		"password=only\n": "only",
		"username=x\n":    "",
		"":                "",
	}
	for in, want := range cases {
		if got := parseCredentialPassword(in); got != want {
			t.Fatalf("parseCredentialPassword(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBestEffortLoginPrefersSSHThenEmail(t *testing.T) {
	r := &BestEffortLoginResolver{
		SSH:   func(ctx context.Context) (string, error) { return "ssh-user", nil },
		Email: func(ctx context.Context) (string, error) { return "email-user", nil },
		TTL:   time.Hour,
	}
	login, err := r.BestEffortLogin(context.Background())
	if err != nil {
		t.Fatalf("BestEffortLogin: %v", err)
	}
	if login != "ssh-user" {
		t.Fatalf("login = %q, want ssh-user", login)
	}
}

func TestBestEffortLoginFallsBackToEmail(t *testing.T) {
	emailCalls := 0
	r := &BestEffortLoginResolver{
		SSH:   func(ctx context.Context) (string, error) { return "", errNoBestEffortLogin },
		Email: func(ctx context.Context) (string, error) { emailCalls++; return "email-user", nil },
		TTL:   time.Hour,
	}
	login, _ := r.BestEffortLogin(context.Background())
	if login != "email-user" {
		t.Fatalf("login = %q, want email-user", login)
	}
	// Second call is served from cache: neither probe re-runs.
	if _, err := r.BestEffortLogin(context.Background()); err != nil {
		t.Fatalf("second: %v", err)
	}
	if emailCalls != 1 {
		t.Fatalf("Email called %d times; want 1 (cached)", emailCalls)
	}
}

func TestBestEffortLoginEmptyWhenNoSignal(t *testing.T) {
	sshCalls := 0
	r := &BestEffortLoginResolver{
		SSH:   func(ctx context.Context) (string, error) { sshCalls++; return "", errNoBestEffortLogin },
		Email: func(ctx context.Context) (string, error) { return "", errNoBestEffortLogin },
		TTL:   time.Hour,
	}
	login, err := r.BestEffortLogin(context.Background())
	if err != nil {
		t.Fatalf("BestEffortLogin returned err %v; want nil (degrade to empty)", err)
	}
	if login != "" {
		t.Fatalf("login = %q, want empty", login)
	}
	// The empty result is cached too, so a machine with no signal is not
	// re-probed on every spawn.
	if _, err := r.BestEffortLogin(context.Background()); err != nil {
		t.Fatalf("second: %v", err)
	}
	if sshCalls != 1 {
		t.Fatalf("SSH called %d times; want 1 (empty result cached)", sshCalls)
	}
}

func TestSSHGreetingRegex(t *testing.T) {
	greeting := "Warning: Permanently added 'github.com' to the list of known hosts.\n" +
		"Hi Pulkit7070! You've successfully authenticated, but GitHub does not provide shell access.\n"
	m := sshGreetingRe.FindStringSubmatch(greeting)
	if len(m) < 2 || m[1] != "Pulkit7070" {
		t.Fatalf("match = %v, want Pulkit7070", m)
	}
	if sshGreetingRe.MatchString("Permission denied (publickey).") {
		t.Fatalf("should not match a denied greeting")
	}
}

func TestNoreplyRegex(t *testing.T) {
	cases := map[string]string{
		"146842937+Pulkit7070@users.noreply.github.com": "Pulkit7070",
		"octocat@users.noreply.github.com":              "octocat",
		"146842937+Pulkit7070@USERS.NOREPLY.GITHUB.COM": "Pulkit7070",
		"prateek.saraf@gmail.com":                       "",
		"someone@example.com":                           "",
	}
	for email, want := range cases {
		m := noreplyRe.FindStringSubmatch(email)
		got := ""
		if len(m) >= 2 {
			got = m[1]
		}
		if got != want {
			t.Fatalf("noreplyRe(%q) = %q, want %q", email, got, want)
		}
	}
}

// On a machine with no credential, gh is missing or logged out and git exits
// 128. Both must read as ErrNoToken, or the provider reports a generic auth
// failure and the best-effort fallback never runs.
func TestTokenChainReportsNoTokenWithoutCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell stub for gh")
	}
	bin := t.TempDir()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	if err := os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	check := func(name string) {
		t.Helper()
		tokens := FallbackTokenSource{&GHTokenSource{}, &CredentialHelperTokenSource{}}
		if _, err := tokens.Token(context.Background()); !errors.Is(err, ErrNoToken) {
			t.Fatalf("%s: token chain err = %v, want ErrNoToken", name, err)
		}
		p, err := NewProvider(ProviderOptions{Token: tokens, SkipTokenPreflight: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.AuthenticatedIdentity(context.Background()); !errors.Is(err, ports.ErrSCMNoCredentials) {
			t.Fatalf("%s: identity err = %v, want ErrSCMNoCredentials", name, err)
		}
	}
	check("gh not installed")

	stub := "#!/bin/sh\necho 'no oauth token found for github.com' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	check("gh logged out")
}

func TestNoTokenUnlessCancelledKeepsTimeouts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	probeErr := errors.New("signal: killed")
	if err := noTokenUnlessCancelled(ctx, probeErr); !errors.Is(err, probeErr) || errors.Is(err, ErrNoToken) {
		t.Fatalf("cancelled probe err = %v, want the original error", err)
	}
	if err := noTokenUnlessCancelled(context.Background(), probeErr); !errors.Is(err, ErrNoToken) {
		t.Fatalf("failed probe err = %v, want ErrNoToken", err)
	}
}

// The probe must never run user ssh_config commands or wait on a security-key
// touch, and must keep the agent so agent-only keys still greet.
func TestSSHProbeArgsStaySilent(t *testing.T) {
	args := strings.Join(sshProbeArgs, " ")
	for _, want := range []string{"-F none", "BatchMode=yes", "PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "PubkeyAcceptedAlgorithms=-sk-", "ControlPath=none", "UserKnownHostsFile=/dev/null"} {
		if !strings.Contains(args, want) {
			t.Errorf("ssh probe args %q missing %q", args, want)
		}
	}
	if strings.Contains(args, "IdentityAgent=none") {
		t.Errorf("ssh probe args %q disable the agent; agent-only keys would never greet", args)
	}
}

// Parallel spawns on a token-less machine must share one probe rather than
// queueing a probe each behind the resolver lock.
func TestBestEffortLoginSharesOneInFlightProbe(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	r := &BestEffortLoginResolver{
		SSH: func(context.Context) (string, error) {
			calls.Add(1)
			<-release
			return "octocat", nil
		},
		Email: func(context.Context) (string, error) { return "", errNoBestEffortLogin },
	}
	var wg sync.WaitGroup
	results := make(chan string, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			login, _ := r.BestEffortLogin(context.Background())
			results <- login
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)
	for login := range results {
		if login != "octocat" {
			t.Fatalf("login = %q, want octocat", login)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("ssh probe ran %d times, want 1", got)
	}
}

// A caller whose context ends stops waiting, but the shared probe still
// finishes and caches its result for the next caller.
func TestBestEffortLoginCallerTimeoutKeepsProbeRunning(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	r := &BestEffortLoginResolver{
		SSH: func(ctx context.Context) (string, error) {
			calls.Add(1)
			select {
			case <-release:
				return "octocat", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
		Email: func(context.Context) (string, error) { return "", errNoBestEffortLogin },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if login, _ := r.BestEffortLogin(ctx); login != "" {
		t.Fatalf("timed-out caller login = %q, want empty", login)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		login, _ := r.BestEffortLogin(context.Background())
		if login == "octocat" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe result never cached; last login = %q", login)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("ssh probe ran %d times, want 1", got)
	}
}
