package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/codex"
	scmgitlab "github.com/aoagents/agent-orchestrator/backend/internal/adapters/scm/gitlab"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

type doctorLevel string

const (
	doctorPass doctorLevel = "PASS"
	doctorWarn doctorLevel = "WARN"
	doctorFail doctorLevel = "FAIL"
)

type doctorCheck struct {
	Level   doctorLevel `json:"level"`
	Section string      `json:"section,omitempty"`
	Name    string      `json:"name"`
	Message string      `json:"message"`
}

type doctorReport struct {
	OK       bool          `json:"ok"`
	Failures int           `json:"failures"`
	Checks   []doctorCheck `json:"checks"`
}

const (
	doctorSectionCore           = "Core"
	doctorSectionTools          = "Tools"
	doctorSectionAgents         = "Agent harnesses"
	doctorSectionGitHub         = "GitHub"
	doctorSectionGitLab         = "GitLab"
	minGitVersion               = "2.25.0"
	githubDoctorUserAgent       = "ao-agent-orchestrator/doctor"
	gitlabDoctorUserAgent       = "ao-agent-orchestrator/doctor"
	defaultDoctorGitHubRESTBase = "https://api.github.com"
	defaultDoctorGitLabRESTBase = "https://gitlab.com/api/v4"
)

type harnessProbe struct {
	Name                  string
	BinaryName            string
	VersionArg            string
	ExpectedVersionPrefix string
}

var doctorHarnesses = []harnessProbe{
	{Name: "claude-code", BinaryName: "claude", VersionArg: "--version"},
	{Name: "codex", BinaryName: "codex", VersionArg: "--version"},
	{Name: "muse", BinaryName: "muse", VersionArg: "--version", ExpectedVersionPrefix: "Muse Code "},
}

func newDoctorCommand(ctx *commandContext) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run local AO health checks",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			checks := ctx.runDoctor(cmd.Context())
			failures := 0
			for _, check := range checks {
				if check.Level == doctorFail {
					failures++
				}
			}

			if asJSON {
				if err := writeJSON(cmd.OutOrStdout(), doctorReport{
					OK: failures == 0, Failures: failures, Checks: checks,
				}); err != nil {
					return err
				}
			} else {
				if err := writeDoctorText(cmd, checks); err != nil {
					return err
				}
			}

			if failures > 0 {
				return fmt.Errorf("doctor found %d failing check(s)", failures)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output health checks as JSON")
	return cmd
}

func writeDoctorText(cmd *cobra.Command, checks []doctorCheck) error {
	var lastSection string
	for _, check := range checks {
		if check.Section != "" && check.Section != lastSection {
			if lastSection != "" {
				if _, err := fmt.Fprintln(cmd.OutOrStdout()); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s:\n", check.Section); err != nil {
				return err
			}
			lastSection = check.Section
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", check.Level, check.Name, check.Message); err != nil {
			return err
		}
	}
	return nil
}

func (c *commandContext) runDoctor(ctx context.Context) []doctorCheck {
	checks := []doctorCheck{}

	cfg, err := config.Load()
	if err != nil {
		return append(checks, doctorCheck{Level: doctorFail, Section: doctorSectionCore, Name: "config", Message: err.Error()})
	}
	checks = append(checks, doctorCheck{
		Level: doctorPass, Section: doctorSectionCore, Name: "config",
		Message: fmt.Sprintf("runFile=%s dataDir=%s port=%d", cfg.RunFilePath, cfg.DataDir, cfg.Port),
	})

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		checks = append(checks, doctorCheck{Level: doctorFail, Section: doctorSectionCore, Name: "data-dir", Message: err.Error()})
	} else {
		checks = append(checks,
			doctorCheck{Level: doctorPass, Section: doctorSectionCore, Name: "data-dir", Message: cfg.DataDir},
			checkDataDirWritable(cfg.DataDir),
		)
	}

	checks = append(checks, checkStore(cfg.DataDir), checkHooksLog(cfg.DataDir, time.Now()))

	st, err := c.inspectDaemon(ctx)
	if err != nil {
		checks = append(checks, doctorCheck{Level: doctorFail, Section: doctorSectionCore, Name: "daemon", Message: err.Error()})
	} else {
		level := doctorPass
		switch st.State {
		case stateStale, stateNotReady:
			level = doctorWarn
		case stateUnhealthy:
			level = doctorFail
		}
		msg := string(st.State)
		if st.PID != 0 {
			msg = fmt.Sprintf("%s pid=%d port=%d", msg, st.PID, st.Port)
		}
		if st.Error != "" {
			msg += " (" + st.Error + ")"
		}
		checks = append(checks, doctorCheck{Level: level, Section: doctorSectionCore, Name: "daemon", Message: msg})
	}

	checks = append(checks,
		c.checkGit(ctx),
		c.checkTerminalRuntime(ctx),
		c.checkAOBinary(),
	)
	for _, harness := range doctorHarnesses {
		checks = append(checks, c.checkHarness(ctx, harness))
	}
	checks = append(checks, c.checkCodexLaunchFlags(ctx), c.checkGitHubToken(ctx))
	checks = append(checks, c.checkGitLabTokens(ctx, cfg.GitLab)...)
	return checks
}

// checkStore inspects the SQLite store WITHOUT opening or migrating it. The
// daemon is the sole writer and migrator of the database (architecture.md §7);
// the CLI must never run migrations or open a second writer against a database
// a live daemon may already own. Migrations are validated by the daemon at
// startup and surfaced through /readyz, so doctor only confirms whether the
// database file exists yet.
func checkStore(dataDir string) doctorCheck {
	dbPath := filepath.Join(dataDir, "ao.db")
	info, err := os.Stat(dbPath)
	switch {
	case err == nil:
		return doctorCheck{
			Level: doctorPass, Section: doctorSectionCore, Name: "sqlite",
			Message: fmt.Sprintf("%s (%d bytes); migrations are applied by the daemon at startup", dbPath, info.Size()),
		}
	case errors.Is(err, fs.ErrNotExist):
		return doctorCheck{
			Level: doctorWarn, Section: doctorSectionCore, Name: "sqlite",
			Message: "database not created yet; run `ao start` to initialize and migrate it",
		}
	default:
		return doctorCheck{Level: doctorFail, Section: doctorSectionCore, Name: "sqlite", Message: err.Error()}
	}
}

func checkDataDirWritable(dataDir string) doctorCheck {
	f, err := os.CreateTemp(dataDir, ".ao-doctor-write-*")
	if err != nil {
		return doctorCheck{Level: doctorFail, Section: doctorSectionCore, Name: "data-dir-write", Message: err.Error()}
	}
	name := f.Name()
	if _, err := f.WriteString("ok\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return doctorCheck{Level: doctorFail, Section: doctorSectionCore, Name: "data-dir-write", Message: err.Error()}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return doctorCheck{Level: doctorFail, Section: doctorSectionCore, Name: "data-dir-write", Message: err.Error()}
	}
	if err := os.Remove(name); err != nil {
		return doctorCheck{Level: doctorWarn, Section: doctorSectionCore, Name: "data-dir-write", Message: fmt.Sprintf("write probe succeeded but cleanup failed: %v", err)}
	}
	return doctorCheck{Level: doctorPass, Section: doctorSectionCore, Name: "data-dir-write", Message: "write probe succeeded"}
}

// checkAOBinary verifies the `ao` that workspace hooks would invoke. Agent
// adapters install hook commands as a bare `ao hooks <agent> <event>`, so an
// `ao` earlier on PATH that is not this binary (e.g. a legacy CLI without the
// hooks command) fails every callback and silently kills activity tracking.
// The daemon pins PATH inside the sessions it spawns, so a mismatch here is a
// warning about every other context (manual runs, foreign panes), not a hard
// failure.
func (c *commandContext) checkAOBinary() doctorCheck {
	const name = "ao-binary"
	self, err := c.deps.Executable()
	if err != nil {
		return doctorCheck{Level: doctorWarn, Section: doctorSectionTools, Name: name, Message: fmt.Sprintf("could not resolve the running executable: %v", err)}
	}
	onPath, err := c.deps.LookPath("ao")
	if err != nil || onPath == "" {
		return doctorCheck{
			Level: doctorWarn, Section: doctorSectionTools, Name: name,
			Message: "ao not found in PATH; workspace hooks invoke `ao hooks <agent> <event>` (daemon-spawned sessions pin PATH to the daemon binary and are unaffected)",
		}
	}
	if sameBinary(self, onPath) {
		return doctorCheck{Level: doctorPass, Section: doctorSectionTools, Name: name, Message: fmt.Sprintf("ao in PATH is this binary (%s)", onPath)}
	}
	return doctorCheck{
		Level: doctorWarn, Section: doctorSectionTools, Name: name,
		Message: fmt.Sprintf("ao in PATH is %s, not this binary (%s); workspace hooks run `ao hooks` and a foreign ao breaks activity tracking outside daemon-spawned sessions", onPath, self),
	}
}

// sameBinary reports whether two paths name the same file, tolerating symlinks
// via os.SameFile and falling back to cleaned-path equality when either stat
// fails.
func sameBinary(a, b string) bool {
	ai, aErr := os.Stat(a)
	bi, bErr := os.Stat(b)
	if aErr == nil && bErr == nil {
		return os.SameFile(ai, bi)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func (c *commandContext) checkGit(ctx context.Context) doctorCheck {
	path, err := c.deps.LookPath("git")
	if err != nil || path == "" {
		return doctorCheck{Level: doctorFail, Section: doctorSectionTools, Name: "git", Message: "not found in PATH"}
	}
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := c.deps.CommandOutput(reqCtx, path, "--version")
	if err != nil {
		return doctorCheck{Level: doctorFail, Section: doctorSectionTools, Name: "git", Message: fmt.Sprintf("%s: %v", path, err)}
	}
	version, err := parseGitVersion(string(out))
	if err != nil {
		return doctorCheck{Level: doctorWarn, Section: doctorSectionTools, Name: "git", Message: fmt.Sprintf("%s (version unknown: %s)", path, firstOutputLine(out))}
	}
	cmp, err := compareDottedVersion(version, minGitVersion)
	if err != nil {
		return doctorCheck{Level: doctorWarn, Section: doctorSectionTools, Name: "git", Message: fmt.Sprintf("%s (version unknown: %s)", path, firstOutputLine(out))}
	}
	if cmp < 0 {
		return doctorCheck{Level: doctorWarn, Section: doctorSectionTools, Name: "git", Message: fmt.Sprintf("%s (version %s; AO expects >= %s for worktrees)", path, version, minGitVersion)}
	}
	return doctorCheck{Level: doctorPass, Section: doctorSectionTools, Name: "git", Message: fmt.Sprintf("%s (version %s; supports worktrees)", path, version)}
}

// checkTerminalRuntime checks the runtime multiplexer used on this platform:
// tmux on Darwin/Linux, ConPTY (built-in) on Windows.
func (c *commandContext) checkTerminalRuntime(ctx context.Context) doctorCheck {
	if runtime.GOOS == "windows" {
		return doctorCheck{
			Level:   doctorPass,
			Section: doctorSectionTools,
			Name:    "conpty",
			Message: "ConPTY (built-in): no external terminal multiplexer required on Windows",
		}
	}
	return c.checkTmux(ctx)
}

func (c *commandContext) checkTmux(ctx context.Context) doctorCheck {
	path, err := c.deps.LookPath("tmux")
	if err != nil || path == "" {
		return doctorCheck{Level: doctorWarn, Section: doctorSectionTools, Name: "tmux", Message: "not found in PATH; required on macOS/Linux to start sessions"}
	}
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := c.deps.CommandOutput(reqCtx, path, "-V")
	if err != nil {
		return doctorCheck{Level: doctorFail, Section: doctorSectionTools, Name: "tmux", Message: fmt.Sprintf("%s: %v", path, err)}
	}
	version := firstOutputLine(out)
	if version == "" {
		version = "version unknown"
	}
	return doctorCheck{Level: doctorPass, Section: doctorSectionTools, Name: "tmux", Message: fmt.Sprintf("%s (%s)", path, version)}
}

// checkHooksLog surfaces recent agent hook delivery failures. `ao hooks`
// callbacks deliberately swallow errors (a hook must never break the user's
// agent), so $AO_DATA_DIR/hooks.log is the only place a dead activity feed
// becomes visible. Lines start with an RFC3339 timestamp (see appendHooksLog).
func checkHooksLog(dataDir string, now time.Time) doctorCheck {
	const name = "hooks-log"
	path := filepath.Join(dataDir, hooksLogName)
	data, err := os.ReadFile(path) //nolint:gosec // path rooted in AO's own data dir
	if errors.Is(err, fs.ErrNotExist) {
		return doctorCheck{Level: doctorPass, Section: doctorSectionCore, Name: name, Message: "no hook delivery failures recorded"}
	}
	if err != nil {
		return doctorCheck{Level: doctorWarn, Section: doctorSectionCore, Name: name, Message: err.Error()}
	}

	recent := 0
	latest := ""
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		stamp, _, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		ts, err := time.Parse(time.RFC3339, stamp)
		if err != nil || now.Sub(ts) > 24*time.Hour {
			continue
		}
		recent++
		latest = line
	}
	if recent == 0 {
		return doctorCheck{Level: doctorPass, Section: doctorSectionCore, Name: name, Message: fmt.Sprintf("no hook delivery failures in the last 24h (%s)", path)}
	}
	return doctorCheck{
		Level: doctorWarn, Section: doctorSectionCore, Name: name,
		Message: fmt.Sprintf("%d hook delivery failure(s) in the last 24h — activity tracking may be degraded; latest: %s (full log: %s)", recent, latest, path),
	}
}

func (c *commandContext) checkHarness(ctx context.Context, harness harnessProbe) doctorCheck {
	path, err := c.deps.LookPath(harness.BinaryName)
	if err != nil || path == "" {
		return doctorCheck{
			Level: doctorWarn, Section: doctorSectionAgents, Name: harness.Name,
			Message: fmt.Sprintf("%s not found in PATH", harness.BinaryName),
		}
	}
	if harness.VersionArg == "" {
		return doctorCheck{Level: doctorPass, Section: doctorSectionAgents, Name: harness.Name, Message: fmt.Sprintf("%s resolves to %s", harness.BinaryName, path)}
	}
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := c.deps.CommandOutput(reqCtx, path, harness.VersionArg)
	if err != nil {
		return doctorCheck{
			Level: doctorWarn, Section: doctorSectionAgents, Name: harness.Name,
			Message: fmt.Sprintf("%s resolves to %s, but `%s %s` failed: %v", harness.BinaryName, path, harness.BinaryName, harness.VersionArg, err),
		}
	}
	version := firstOutputLine(out)
	if version == "" {
		version = "version output was empty"
	}
	if harness.ExpectedVersionPrefix != "" && !strings.HasPrefix(version, harness.ExpectedVersionPrefix) {
		return doctorCheck{
			Level: doctorWarn, Section: doctorSectionAgents, Name: harness.Name,
			Message: fmt.Sprintf("%s resolves to %s, but its version output %q does not identify the expected CLI (%q prefix)", harness.BinaryName, path, version, harness.ExpectedVersionPrefix),
		}
	}
	return doctorCheck{Level: doctorPass, Section: doctorSectionAgents, Name: harness.Name, Message: fmt.Sprintf("%s resolves to %s (%s)", harness.BinaryName, path, version)}
}

// checkCodexLaunchFlags smoke-tests AO's codex launch surface against the
// installed binary: the hook-trust bypass flag and the `-c` session-flag
// config AO injects at spawn (activity hooks, worktree trust, nudge
// suppression). Codex has no stable hook-config contract, so a codex upgrade
// can silently break activity tracking; this canary turns that breakage into
// a doctor warning. The probes come from the codex adapter itself so they
// cannot drift from the real spawn argv.
func (c *commandContext) checkCodexLaunchFlags(ctx context.Context) doctorCheck {
	const name = "codex-launch-flags"
	path, err := c.deps.LookPath("codex")
	if err != nil || path == "" {
		return doctorCheck{Level: doctorPass, Section: doctorSectionAgents, Name: name, Message: "skipped: codex not found in PATH"}
	}
	for _, probe := range codex.DoctorLaunchProbes() {
		reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		out, err := c.deps.CommandOutput(reqCtx, path, probe...)
		cancel()
		if err != nil {
			return doctorCheck{
				Level: doctorWarn, Section: doctorSectionAgents, Name: name,
				Message: fmt.Sprintf("codex rejected AO's launch flags (`codex %s`: %v) — codex sessions may spawn without activity hooks; a codex CLI update likely changed its flag/config surface", strings.Join(probe, " "), err),
			}
		}
		if strings.Contains(string(out), "unknown configuration field") {
			return doctorCheck{
				Level: doctorWarn, Section: doctorSectionAgents, Name: name,
				Message: fmt.Sprintf("codex no longer recognizes one of AO's config overrides (%s) — codex sessions may spawn without activity hooks", firstOutputLine(out)),
			}
		}
	}
	return doctorCheck{Level: doctorPass, Section: doctorSectionAgents, Name: name, Message: "codex accepts AO's hook/trust launch flags"}
}

func (c *commandContext) checkGitHubToken(ctx context.Context) doctorCheck {
	token, source, err := c.githubToken(ctx)
	if err != nil {
		return doctorCheck{Level: doctorWarn, Section: doctorSectionGitHub, Name: "github-token", Message: err.Error()}
	}

	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, strings.TrimRight(c.deps.DoctorGitHubRESTBase, "/")+"/user", http.NoBody)
	if err != nil {
		return doctorCheck{Level: doctorFail, Section: doctorSectionGitHub, Name: "github-token", Message: err.Error()}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", githubDoctorUserAgent)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.deps.HTTPClient.Do(req)
	if err != nil {
		return doctorCheck{Level: doctorFail, Section: doctorSectionGitHub, Name: "github-token", Message: fmt.Sprintf("%s token validation failed: %v", source, err)}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return doctorCheck{Level: doctorFail, Section: doctorSectionGitHub, Name: "github-token", Message: fmt.Sprintf("%s token rejected by GitHub (HTTP %d)", source, resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return doctorCheck{Level: doctorWarn, Section: doctorSectionGitHub, Name: "github-token", Message: fmt.Sprintf("%s token probe returned HTTP %d", source, resp.StatusCode)}
	}

	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return doctorCheck{Level: doctorFail, Section: doctorSectionGitHub, Name: "github-token", Message: fmt.Sprintf("%s token probe decode failed: %v", source, err)}
	}
	login := user.Login
	if login == "" {
		login = "unknown user"
	}
	scopes := strings.TrimSpace(resp.Header.Get("X-OAuth-Scopes"))
	scopeMsg := "scopes unavailable"
	if scopes != "" {
		scopeMsg = "scopes: " + scopes
	}
	return doctorCheck{Level: doctorPass, Section: doctorSectionGitHub, Name: "github-token", Message: fmt.Sprintf("%s token valid for %s (%s)", source, login, scopeMsg)}
}

func (c *commandContext) githubToken(ctx context.Context) (token, source string, err error) {
	for _, name := range []string{"AO_GITHUB_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v, name, nil
		}
	}
	path, lookErr := c.deps.LookPath("gh")
	if lookErr != nil || path == "" {
		return "", "", errors.New("no GitHub token found (set AO_GITHUB_TOKEN/GITHUB_TOKEN or run `gh auth login`)")
	}
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, cmdErr := c.deps.CommandOutput(reqCtx, path, "auth", "token")
	if cmdErr != nil {
		return "", "", fmt.Errorf("gh is installed but no token was available (`gh auth token` failed: %w)", cmdErr)
	}
	token = strings.TrimSpace(string(out))
	if token == "" {
		return "", "", errors.New("gh is installed but returned an empty auth token")
	}
	return token, "gh", nil
}

// checkGitLabTokens probes every GitLab instance AO is configured to talk to,
// not just gitlab.com. A self-managed deployment (AO_GITLAB_ALLOWED_HOSTS)
// never reaches gitlab.com, so a gitlab.com-only probe reports a token verdict
// about a host AO will never call.
//
// A credential is only ever sent to the instance it belongs to. Sending an
// internal instance's token to gitlab.com would disclose it to a third party,
// and sending gitlab.com's token to a self-managed instance hands it to whoever
// runs that server — `ao doctor` must never do either. gitlabToken resolves
// each probe's credential from that instance's own sources, and every token is
// resolved before any request is issued, so the one credential that could still
// be ambiguous — a global default shared with an allowlisted host — can be
// spotted and its probe skipped (checkGitLabDotComToken).
//
// Host -> REST base + token resolution mirrors the SCM provider's
// clientForHost (adapters/scm/gitlab/provider.go).
func (c *commandContext) checkGitLabTokens(ctx context.Context, gitlabCfg config.GitLabConfig) []doctorCheck {
	hosts := gitlabDoctorHosts(gitlabCfg.AllowedHosts)
	hostTokens := gitlabDoctorHostTokens(gitlabCfg.HostTokens)
	// One glab invocation per hostname per doctor run: without the memo every
	// allowlisted host would respawn the unscoped fallback lookup.
	glabRuns := newGLabStatusCache()

	// Every host's credential resolution and every host's probe is independent
	// of the others, and each carries its own probeTimeout: an off-VPN instance
	// costs one glab lookup plus one unreachable HTTPS request. Run them
	// concurrently so `ao doctor` waits for the slowest host rather than for
	// the sum of all of them.
	probes := make([]gitlabProbe, len(hosts))
	creds := make([]gitlabCredential, len(hosts))
	var wg sync.WaitGroup
	for i, host := range hosts {
		probes[i] = gitlabProbe{
			name:      "gitlab-token:" + host,
			host:      host,
			tokenHost: host,
			restBase:  c.deps.DoctorGitLabHostRESTBase(host),
			hostToken: hostTokens[host],
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			creds[i] = c.gitlabCredential(ctx, probes[i], glabRuns)
		}(i)
	}
	wg.Wait()

	defaultProbe := gitlabProbe{
		name:      "gitlab-token",
		tokenHost: scmgitlab.DotComHost,
		restBase:  c.deps.DoctorGitLabRESTBase,
	}
	// Resolved after the hosts: the gitlab.com verdict depends on whether any
	// host turned out to authenticate with the same credential.
	defaultCred := c.gitlabCredential(ctx, defaultProbe, glabRuns)

	hostChecks := make([]doctorCheck, len(hosts))
	for i := range probes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hostChecks[i] = c.checkGitLabToken(ctx, probes[i], creds[i])
		}(i)
	}
	dotCom := c.checkGitLabDotComToken(ctx, defaultProbe, defaultCred, hosts, creds)
	wg.Wait()

	checks := make([]doctorCheck, 0, len(hosts)+2)
	if check, ok := checkGitLabUnusedHostTokens(gitlabCfg.HostTokens, hosts); ok {
		checks = append(checks, check)
	}
	checks = append(checks, dotCom)
	return append(checks, hostChecks...)
}

// checkGitLabUnusedHostTokens reports AO_GITLAB_HOST_TOKENS entries that no
// GitLab client will ever read, and returns false when there are none — a
// correct configuration must not add noise to `ao doctor`.
//
// An entry is silently inert in three ways, and all of them leave the user with
// a credential they believe is in use: a host missing from
// AO_GITLAB_ALLOWED_HOSTS is rejected before any credential is attached
// (isHostAllowed in adapters/scm/gitlab/provider.go), so its merge requests are
// skipped while every other check reads green; gitlab.com is served by the
// default token chain, which never consults the per-host map; and an entry with
// no token value (`host=`, typically an unset shell variable that expanded to
// nothing) is treated as "no override" by every reader.
//
// It takes the raw configured map, not the filtered one gitlabDoctorHostTokens
// produces, so the entries that filtering drops are exactly the ones reported.
func checkGitLabUnusedHostTokens(hostTokens map[string]string, hosts []string) (doctorCheck, bool) {
	allowed := make(map[string]bool, len(hosts))
	for _, host := range hosts {
		allowed[host] = true
	}
	unused := make([]string, 0, len(hostTokens))
	for rawHost, token := range hostTokens {
		host := scmgitlab.NormalizeHost(rawHost)
		switch {
		case host == "":
		case strings.TrimSpace(token) == "":
			unused = append(unused, host+" (no token value, so AO ignores the entry and falls back to glab or AO_GITLAB_TOKEN/GITLAB_TOKEN)")
		case allowed[host]:
		case scmgitlab.IsGitLabDotCom(host):
			unused = append(unused, host+" (the default instance authenticates with AO_GITLAB_TOKEN/GITLAB_TOKEN or glab)")
		default:
			unused = append(unused, host+" (not in AO_GITLAB_ALLOWED_HOSTS, so AO rejects the host before attaching any credential)")
		}
	}
	if len(unused) == 0 {
		return doctorCheck{}, false
	}
	slices.Sort(unused)
	return doctorCheck{
		Level: doctorWarn, Section: doctorSectionGitLab, Name: "gitlab-host-tokens",
		Message: fmt.Sprintf("unused AO_GITLAB_HOST_TOKENS entries: %s", strings.Join(unused, "; ")),
	}, true
}

// checkGitLabDotComToken runs the default-instance probe unless its credential
// is also what authenticates an allowlisted self-managed host. A token the user
// declared as the global default (AO_GITLAB_TOKEN/GITLAB_TOKEN) belongs to
// every instance by configuration, so gitlabToken hands it over for gitlab.com;
// but when an allowlisted host authenticates with that same value, doctor
// cannot tell whether it is gitlab.com's token or the internal one, and probing
// would ship a possibly-internal credential to a third party — so the check
// reports what it skipped and why instead.
//
// This is the one credential doctor holds back that the daemon would still use:
// AO does send the global default to gitlab.com. It sends it there only for a
// gitlab.com repository, though, while doctor probes gitlab.com on every run.
func (c *commandContext) checkGitLabDotComToken(ctx context.Context, probe gitlabProbe, cred gitlabCredential, hosts []string, hostCreds []gitlabCredential) doctorCheck {
	if shared := gitlabHostsSharingToken(cred, hosts, hostCreds); len(shared) > 0 {
		return doctorCheck{
			Level: doctorWarn, Section: doctorSectionGitLab, Name: probe.name,
			Message: fmt.Sprintf("not probed: the %s token also authenticates %s, and doctor will not send a self-managed credential to gitlab.com; set AO_GITLAB_HOST_TOKENS for those hosts to validate gitlab.com separately", cred.source, strings.Join(shared, ", ")),
		}
	}
	return c.checkGitLabToken(ctx, probe, cred)
}

// gitlabHostsSharingToken returns the allowlisted hosts whose credential is
// byte-identical to the default one. A shared value means the token cannot be
// attributed to gitlab.com, so it must not be sent there.
//
// Only a globally-configured default token is checked this way. A token glab
// reported for gitlab.com specifically is attributed to gitlab.com even if an
// internal instance happens to accept the same value.
//
// A host whose own credential is that same global default is not evidence: it
// has nothing bound to it, so the shared value is simply the only thing left
// to offer it, and that says nothing about who the token belongs to. Counting
// it would leave gitlab.com unvalidated for every self-managed setup that has
// not bound a per-host credential yet.
func gitlabHostsSharingToken(cred gitlabCredential, hosts []string, hostCreds []gitlabCredential) []string {
	if cred.token == "" || !cred.shared {
		return nil
	}
	shared := make([]string, 0, len(hosts))
	for i, host := range hosts {
		if i >= len(hostCreds) || hostCreds[i].shared {
			continue
		}
		if hostCreds[i].token == cred.token {
			shared = append(shared, host)
		}
	}
	return shared
}

// gitlabDoctorHosts normalizes and de-duplicates the configured allowlist,
// preserving configuration order so doctor output is stable. gitlab.com is
// dropped: it is always allowed and is covered by the default check.
func gitlabDoctorHosts(allowed []string) []string {
	hosts := make([]string, 0, len(allowed))
	seen := make(map[string]bool, len(allowed))
	for _, raw := range allowed {
		host := scmgitlab.NormalizeHost(raw)
		if host == "" || seen[host] || scmgitlab.IsGitLabDotCom(host) {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	return hosts
}

// gitlabDoctorHostTokens re-keys the configured per-host token overrides by
// normalized host. config.Load stores the keys exactly as written in
// AO_GITLAB_HOST_TOKENS, while the provider normalizes them before lookup
// (NormalizeHost in adapters/scm/gitlab/provider.go); doctor must match the
// provider or it validates a different credential than the daemon uses.
//
// Entries with an empty token (`host=` in AO_GITLAB_HOST_TOKENS) are dropped:
// the daemon treats them as "no override" and falls back to the default token
// chain (gitlabHostTokenSources in daemon/scm_wiring.go), so doctor must too.
func gitlabDoctorHostTokens(hostTokens map[string]string) map[string]string {
	normalized := make(map[string]string, len(hostTokens))
	for host, token := range hostTokens {
		host = scmgitlab.NormalizeHost(host)
		if host == "" || strings.TrimSpace(token) == "" {
			continue
		}
		normalized[host] = token
	}
	return normalized
}

// gitlabProbe describes one GitLab instance doctor validates a token against.
type gitlabProbe struct {
	// name is the doctor check name ("gitlab-token" for the default instance,
	// "gitlab-token:<host>" for an allowlisted self-managed host).
	name string
	// host is the self-managed GitLab host this probe validates, empty for the
	// default instance. It distinguishes "unreachable is a warning" (a
	// self-managed instance may simply be off-VPN) from the gitlab.com probe.
	host string
	// tokenHost is the instance a credential must be attributable to before it
	// may be sent here: host for a self-managed probe, gitlab.com for the
	// default one. It also scopes the glab lookup.
	tokenHost string
	// restBase is the API base the /user probe is issued against.
	restBase string
	// hostToken is the AO_GITLAB_HOST_TOKENS override for this host; empty
	// means the default token (AO_GITLAB_TOKEN / GITLAB_TOKEN / glab) applies.
	hostToken string
}

// gitlabCredential is the token doctor validates for one probe, plus where it
// came from. Credentials are resolved for every probe before any request is
// issued so a probe can be skipped when its token turns out to belong to
// another instance.
//
// Every credential here belongs to the instance it was resolved for: an
// AO_GITLAB_HOST_TOKENS entry and a glab lookup are both host-attributed
// (gitlabToken), and the env vars are the user's declared default for every
// instance AO talks to.
type gitlabCredential struct {
	token  string
	source string
	// shared marks the user-configured global default token
	// (AO_GITLAB_TOKEN/GITLAB_TOKEN). The daemon sends it to every instance it
	// talks to, so doctor may validate it against any of them.
	shared bool
	err    error
}

// gitlabCredential resolves the credential for one probe: the explicit
// per-host override when configured, otherwise the default chain for the
// probe's instance.
func (c *commandContext) gitlabCredential(ctx context.Context, probe gitlabProbe, glabRuns *glabStatusCache) gitlabCredential {
	if token := strings.TrimSpace(probe.hostToken); token != "" {
		return gitlabCredential{token: token, source: "AO_GITLAB_HOST_TOKENS"}
	}
	return c.gitlabToken(ctx, probe, glabRuns)
}

// checkGitLabToken probes one GitLab instance described by probe, using the
// credential already resolved for it.
func (c *commandContext) checkGitLabToken(ctx context.Context, probe gitlabProbe, cred gitlabCredential) doctorCheck {
	name, restBase := probe.name, probe.restBase
	token, source := cred.token, cred.source
	if cred.err != nil {
		return doctorCheck{Level: doctorWarn, Section: doctorSectionGitLab, Name: name, Message: cred.err.Error()}
	}
	// A self-managed host reached only by the global default token gets nothing
	// sent to it. Nothing attributes that value to this instance — it is most
	// likely gitlab.com's — and probing would hand it to whoever operates the
	// server, then report the inevitable 401 as a credential failure. The
	// daemon still offers it at runtime; doctor does not validate it here.
	if probe.host != "" && cred.shared {
		return doctorCheck{
			Level: doctorWarn, Section: doctorSectionGitLab, Name: name,
			Message: fmt.Sprintf("not probed: %s has no credential of its own, and doctor will not send the global %s to it; run `glab auth login --hostname %s` or set AO_GITLAB_HOST_TOKENS for it", probe.host, source, probe.host),
		}
	}

	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, strings.TrimRight(restBase, "/")+"/user", http.NoBody)
	if err != nil {
		return doctorCheck{Level: doctorFail, Section: doctorSectionGitLab, Name: name, Message: err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", gitlabDoctorUserAgent)
	req.Header.Set("PRIVATE-TOKEN", token)
	resp, err := c.deps.HTTPClient.Do(req)
	if err != nil {
		// A self-managed instance is routinely unreachable from wherever the
		// user happens to be (VPN down, split DNS). That says nothing about
		// the credential, so it must not fail `ao doctor`.
		if probe.host != "" {
			return doctorCheck{Level: doctorWarn, Section: doctorSectionGitLab, Name: name, Message: fmt.Sprintf("%s token not validated: %s is unreachable from this machine (%v)", source, probe.host, err)}
		}
		return doctorCheck{Level: doctorFail, Section: doctorSectionGitLab, Name: name, Message: fmt.Sprintf("%s token validation failed: %v", source, err)}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// No leniency here: a probe only runs against an instance whose
		// credential doctor could attribute to it, so a rejection is real.
		return doctorCheck{Level: doctorFail, Section: doctorSectionGitLab, Name: name, Message: fmt.Sprintf("%s token rejected by GitLab (HTTP %d)", source, resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return doctorCheck{Level: doctorWarn, Section: doctorSectionGitLab, Name: name, Message: fmt.Sprintf("%s token probe returned HTTP %d", source, resp.StatusCode)}
	}

	var user struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return doctorCheck{Level: doctorFail, Section: doctorSectionGitLab, Name: name, Message: fmt.Sprintf("%s token probe decode failed: %v", source, err)}
	}
	login := user.Username
	if login == "" {
		login = "unknown user"
	}
	return doctorCheck{Level: doctorPass, Section: doctorSectionGitLab, Name: name, Message: fmt.Sprintf("%s token valid for %s", source, login)}
}

// gitlabToken resolves the default credential for one probe's instance,
// mirroring the chain the daemon wires for it (HostTokenSource and
// DotComTokenSource in adapters/scm/gitlab/auth.go):
//
//   - gitlab.com keeps the documented env-vars-first precedence, then glab.
//   - a self-managed host prefers the credential glab holds for that instance,
//     because the env vars are a global default whose value belongs to
//     gitlab.com; sending it to an internal server would disclose it there and
//     fail with 401 even though the host had its own credential.
//
// Every glab lookup is host-attributed: `--hostname` scopes the query, and the
// unscoped fallback (which keeps a glab too old for that flag working) is only
// trusted for the host its output names. Doctor therefore never validates one
// instance's token against another's API.
func (c *commandContext) gitlabToken(ctx context.Context, probe gitlabProbe, glabRuns *glabStatusCache) gitlabCredential {
	env := gitlabEnvCredential()
	if probe.host == "" && env.token != "" {
		return env
	}
	token, err := c.glabHostToken(ctx, probe.tokenHost, glabRuns)
	if token != "" {
		return gitlabCredential{token: token, source: "glab"}
	}
	// The env vars are the last resort for a self-managed host, and the daemon
	// does use them there. The credential stays marked shared: nothing
	// attributes it to this instance, so doctor reports it rather than sending
	// it (checkGitLabToken).
	if env.token != "" {
		return env
	}
	return gitlabCredential{err: err}
}

// gitlabEnvCredential returns the global default token, or the zero value when
// neither env var is set.
func gitlabEnvCredential() gitlabCredential {
	for _, name := range []string{"AO_GITLAB_TOKEN", "GITLAB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return gitlabCredential{token: v, source: name, shared: true}
		}
	}
	return gitlabCredential{}
}

// glabHostToken asks glab for the token it holds for host, or explains why it
// has none. The error is what `ao doctor` prints, so it names the remedy.
func (c *commandContext) glabHostToken(ctx context.Context, host string, glabRuns *glabStatusCache) (string, error) {
	host = scmgitlab.NormalizeHost(host)
	path, lookErr := c.deps.LookPath("glab")
	if lookErr != nil || path == "" || host == "" {
		return "", errors.New("no GitLab token found (set AO_GITLAB_TOKEN/GITLAB_TOKEN or run `glab auth login`)")
	}
	// The exit code does not gate the output: `glab auth status` exits non-zero
	// when *any* configured instance is unauthenticated, while still printing a
	// usable block for the ones that are. What guards against reading a
	// diagnostic as a credential is the parsing itself — only a "Token:" /
	// "Token found:" line attributed to this host is accepted.
	scoped := c.glabStatus(ctx, path, host, glabRuns)
	if token := scmgitlab.GLabScopedToken(scoped.output, host); token != "" {
		return token, nil
	}
	// A glab too old for `--hostname` (or one that knows the instance under
	// another name) still prints a status block naming every host it is
	// authenticated against; take this host's token from it, and nothing else.
	unscoped := c.glabStatus(ctx, path, "", glabRuns)
	if token := scmgitlab.GLabTokenForHost(unscoped.output, host); token != "" {
		return token, nil
	}
	// Surface why glab failed — "no token" and "glab could not run at all" are
	// very different problems for whoever is reading `ao doctor`.
	if cmdErr := firstErr(unscoped.err, scoped.err); cmdErr != nil {
		return "", fmt.Errorf("glab is installed but has no token for %s (`glab auth status --show-token` failed: %w); run `glab auth login --hostname %s`", host, cmdErr, host)
	}
	return "", fmt.Errorf("glab is installed but has no token for %s; run `glab auth login --hostname %s` or set AO_GITLAB_HOST_TOKENS for it", host, host)
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// glabResult is one memoized `glab auth status --show-token` invocation: the
// raw output (glab prints status to stderr, so this is the combined output the
// CommandOutput dep returns) and the command failure, if any.
type glabResult struct {
	output string
	err    error
}

// glabStatusCache memoizes `glab auth status` output per hostname for one
// doctor run. Hosts are resolved concurrently, so the map needs a lock; the
// lock is only ever held around map access, never across the subprocess, which
// leaves two goroutines free to race on the same key. The loser's duplicate run
// is harmless — the command only reads local credential state.
type glabStatusCache struct {
	mu   sync.Mutex
	runs map[string]glabResult
}

func newGLabStatusCache() *glabStatusCache {
	return &glabStatusCache{runs: map[string]glabResult{}}
}

func (g *glabStatusCache) get(host string) (glabResult, bool) {
	if g == nil {
		return glabResult{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	run, ok := g.runs[host]
	return run, ok
}

func (g *glabStatusCache) put(host string, run glabResult) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.runs[host] = run
}

// glabStatus runs `glab auth status --show-token`, optionally scoped to one
// host, memoizing the result per hostname for the caller's doctor run.
func (c *commandContext) glabStatus(ctx context.Context, path, host string, glabRuns *glabStatusCache) glabResult {
	if run, ok := glabRuns.get(host); ok {
		return run
	}
	args := []string{"auth", "status", "--show-token"}
	if host != "" {
		args = append(args, "--hostname", host)
	}
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, cmdErr := c.deps.CommandOutput(reqCtx, path, args...)
	run := glabResult{output: string(out), err: cmdErr}
	glabRuns.put(host, run)
	return run
}

var (
	ansiRE       = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	gitVersionRE = regexp.MustCompile(`(?i)\bgit version\s+(\d+(?:\.\d+){1,3})`)
)

func parseGitVersion(out string) (string, error) {
	clean := ansiRE.ReplaceAllString(out, "")
	m := gitVersionRE.FindStringSubmatch(clean)
	if len(m) < 2 {
		return "", fmt.Errorf("parse git version from %q", strings.TrimSpace(clean))
	}
	return m[1], nil
}

func firstOutputLine(out []byte) string {
	clean := strings.TrimSpace(ansiRE.ReplaceAllString(string(out), ""))
	if clean == "" {
		return ""
	}
	line := strings.SplitN(clean, "\n", 2)[0]
	return strings.TrimSpace(line)
}

func compareDottedVersion(a, b string) (int, error) {
	ap, err := dottedVersionParts(a)
	if err != nil {
		return 0, err
	}
	bp, err := dottedVersionParts(b)
	if err != nil {
		return 0, err
	}
	maxLen := len(ap)
	if len(bp) > maxLen {
		maxLen = len(bp)
	}
	for i := 0; i < maxLen; i++ {
		var av, bv int
		if i < len(ap) {
			av = ap[i]
		}
		if i < len(bp) {
			bv = bp[i]
		}
		switch {
		case av < bv:
			return -1, nil
		case av > bv:
			return 1, nil
		}
	}
	return 0, nil
}

func dottedVersionParts(s string) ([]int, error) {
	raw := strings.Split(s, ".")
	parts := make([]int, 0, len(raw))
	for _, part := range raw {
		if part == "" {
			return nil, fmt.Errorf("empty version segment in %q", s)
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("parse version segment %q in %q: %w", part, s, err)
		}
		parts = append(parts, n)
	}
	return parts, nil
}
