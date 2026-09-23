package github

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// sshProbeTimeout bounds the SSH greeting probe so a slow or blocked network
// connection cannot stall the caller (the probe runs on the session-spawn path).
const sshProbeTimeout = 4 * time.Second

// defaultBestEffortTTL caches a best-effort resolution (success or empty) long
// enough that a token-less machine probes at most once an hour, keeping the
// synchronous SSH round trip off all but the occasional session spawn. It is
// deliberately longer than the token cache: the local SSH key and git email
// rarely change, and a stale empty result only delays picking up a newly
// configured signal by up to an hour.
const defaultBestEffortTTL = time.Hour

// errNoBestEffortLogin is returned by a probe that found no usable login.
var errNoBestEffortLogin = errors.New("github scm: no best-effort login")

// sshGreetingRe extracts the username from GitHub's SSH authentication greeting,
// "Hi <user>! You've successfully authenticated, but GitHub does not provide
// shell access." GitHub writes it to stderr and exits non-zero; that exit is
// expected, only the greeting text matters.
var sshGreetingRe = regexp.MustCompile(`Hi ([A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)!`)

// noreplyRe extracts the username from a GitHub noreply commit email, either the
// current "<id>+<username>@users.noreply.github.com" or the legacy
// "<username>@users.noreply.github.com". Match is case-insensitive on the
// domain; the captured username keeps its original case.
var noreplyRe = regexp.MustCompile(`(?i)^(?:[0-9]+\+)?([A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)@users\.noreply\.github\.com$`)

// BestEffortLoginResolver resolves a probable GitHub login from local machine
// signals without any authenticated API call: the SSH auth greeting first (a
// live GitHub round trip that names the account behind the operator's SSH key),
// then the git noreply commit email (offline, parsed from git config).
//
// It is TELEMETRY-ONLY and NOT authoritative: the login is not API-verified, may
// be stale after a rename, and the account type (human vs bot/org) is unknown.
// Callers must never use it for access or attribution decisions.
//
// The resolved value, including the empty "nothing found" result, is memoized
// for TTL so repeated lookups on the spawn hot path do not re-probe the network.
type BestEffortLoginResolver struct {
	// SSH is the SSH-greeting hook. Production leaves it nil and uses
	// sshGreetingLogin; tests inject a fake so ssh is never invoked.
	SSH func(ctx context.Context) (string, error)
	// Email is the git-noreply-email hook. Production leaves it nil and uses
	// gitNoreplyLogin; tests inject a fake so git is never invoked.
	Email func(ctx context.Context) (string, error)
	// TTL is how long a resolution (success or empty) is memoized. Zero means
	// defaultBestEffortTTL.
	TTL time.Duration
	// Clock allows tests to drive expiration. Zero means time.Now.
	Clock func() time.Time

	mu        sync.Mutex
	group     singleflight.Group
	login     string
	resolved  bool
	expiresAt time.Time
}

// BestEffortLogin returns a probable login, or "" when none could be resolved.
// The error is always nil: every failure degrades to an empty result so the
// caller can stay anonymous.
//
// Concurrent callers share one in-flight probe instead of queueing behind a
// lock, so parallel spawns wait at most one probe, and a caller whose context
// ends stops waiting without cancelling the probe for the others.
func (r *BestEffortLoginResolver) BestEffortLogin(ctx context.Context) (string, error) {
	r.mu.Lock()
	now := r.now()
	if r.resolved && now.Before(r.expiresAt) {
		login := r.login
		r.mu.Unlock()
		return login, nil
	}
	r.mu.Unlock()
	ch := r.group.DoChan("probe", func() (any, error) {
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*sshProbeTimeout)
		defer cancel()
		login := r.probe(probeCtx)
		r.mu.Lock()
		r.login, r.resolved, r.expiresAt = login, true, r.now().Add(r.ttl())
		r.mu.Unlock()
		return login, nil
	})
	select {
	case res := <-ch:
		login, _ := res.Val.(string)
		return login, nil
	case <-ctx.Done():
		return "", nil
	}
}

func (r *BestEffortLoginResolver) probe(ctx context.Context) string {
	ssh := r.SSH
	if ssh == nil {
		ssh = sshGreetingLogin
	}
	if login, err := ssh(ctx); err == nil {
		if login = strings.TrimSpace(login); login != "" {
			return login
		}
	}
	email := r.Email
	if email == nil {
		email = gitNoreplyLogin
	}
	if login, err := email(ctx); err == nil {
		if login = strings.TrimSpace(login); login != "" {
			return login
		}
	}
	return ""
}

func (r *BestEffortLoginResolver) now() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now()
}

func (r *BestEffortLoginResolver) ttl() time.Duration {
	if r.TTL > 0 {
		return r.TTL
	}
	return defaultBestEffortTTL
}

// sshProbeArgs keeps the probe silent and side-effect free. -F none skips the
// user's ssh_config, which can run arbitrary commands (ProxyCommand,
// LocalCommand, Match exec), reuse a ControlMaster, or point IdentityAgent at an
// approval-prompting agent such as 1Password. Security-key algorithms are
// excluded because FIDO keys demand a physical touch that BatchMode does not
// suppress. The SSH_AUTH_SOCK agent stays enabled: keys held only in the agent
// are common, and dropping it loses their greeting entirely.
var sshProbeArgs = []string{
	"-F", "none",
	"-o", "BatchMode=yes",
	"-o", "StrictHostKeyChecking=no",
	"-o", "UserKnownHostsFile=/dev/null",
	"-o", "ConnectTimeout=3",
	"-o", "PasswordAuthentication=no",
	"-o", "KbdInteractiveAuthentication=no",
	"-o", "PubkeyAcceptedAlgorithms=-sk-ssh-ed25519@openssh.com,sk-ecdsa-sha2-nistp256@openssh.com",
	"-o", "ControlMaster=no",
	"-o", "ControlPath=none",
	"-o", "ForwardAgent=no",
	"-o", "ClearAllForwardings=yes",
	"-T", "git@github.com",
}

// sshGreetingLogin opens a non-interactive SSH connection to github.com and
// parses the username from the authentication greeting. GitHub always closes
// the session with exit status 1 after the greeting, so a non-nil Run error is
// expected and ignored; only the greeting text is used.
func sshGreetingLogin(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, sshProbeTimeout)
	defer cancel()
	cmd := aoprocess.CommandContext(ctx, "ssh", sshProbeArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	m := sshGreetingRe.FindStringSubmatch(stderr.String())
	if len(m) < 2 {
		return "", errNoBestEffortLogin
	}
	return m[1], nil
}

// gitNoreplyLogin reads git's configured commit email and, when it is a GitHub
// noreply address, extracts the embedded username. Any other email yields no
// login. --global pins the read to the operator's ~/.gitconfig; without it a
// repo-local user.email in the daemon's working directory would win.
func gitNoreplyLogin(ctx context.Context) (string, error) {
	out, err := aoprocess.CommandContext(ctx, "git", "config", "--global", "--get", "user.email").Output()
	if err != nil {
		return "", err
	}
	m := noreplyRe.FindStringSubmatch(strings.TrimSpace(string(out)))
	if len(m) < 2 {
		return "", errNoBestEffortLogin
	}
	return m[1], nil
}
