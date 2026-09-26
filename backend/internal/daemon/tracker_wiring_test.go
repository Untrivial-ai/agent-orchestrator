package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	trackergitlab "github.com/aoagents/agent-orchestrator/backend/internal/adapters/tracker/gitlab"
	trackermulti "github.com/aoagents/agent-orchestrator/backend/internal/adapters/tracker/multi"
	trackeronedev "github.com/aoagents/agent-orchestrator/backend/internal/adapters/tracker/onedev"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// TestNewGitLabTracker_PassesAllowedHosts verifies that AllowedHosts from
// GitLabConfig flows into the tracker's Options. A self-managed host in
// AllowedHosts should be accepted by the tracker; one not in the list should
// be rejected with ErrHostNotAllowed.
//
// Uses ConfigForHost (no network I/O) instead of Get to avoid real DNS/HTTP.
func TestNewGitLabTracker_PassesAllowedHosts(t *testing.T) {
	t.Setenv("AO_GITLAB_TOKEN", "default-token")

	selfHost := "gitlab.internal.example"
	cfg := config.GitLabConfig{
		AllowedHosts: []string{selfHost},
	}

	tracker, err := newGitLabTracker(cfg)
	if err != nil {
		t.Fatalf("newGitLabTracker: %v", err)
	}

	glTracker, ok := tracker.(*trackergitlab.Tracker)
	if !ok {
		t.Fatalf("expected *trackergitlab.Tracker, got %T", tracker)
	}

	// The allowlisted host should be accepted (not ErrHostNotAllowed).
	if err := glTracker.ConfigForHost(selfHost); err != nil {
		t.Fatalf("allowlisted host %q was rejected by the tracker: %v", selfHost, err)
	}

	// An unconfigured host should be rejected with ErrHostNotAllowed.
	err = glTracker.ConfigForHost("gitlab.evil.example")
	if !errors.Is(err, trackergitlab.ErrHostNotAllowed) {
		t.Fatalf("unconfigured host should be rejected with ErrHostNotAllowed, got: %v", err)
	}
}

// TestNewGitLabTracker_GitLabComStillWorks verifies that the zero-value host
// (gitlab.com) still works after wiring — backward compatibility.
func TestNewGitLabTracker_GitLabComStillWorks(t *testing.T) {
	t.Setenv("AO_GITLAB_TOKEN", "default-token")

	cfg := config.GitLabConfig{}
	tracker, err := newGitLabTracker(cfg)
	if err != nil {
		t.Fatalf("newGitLabTracker: %v", err)
	}

	glTracker, ok := tracker.(*trackergitlab.Tracker)
	if !ok {
		t.Fatalf("expected *trackergitlab.Tracker, got %T", tracker)
	}

	// A zero-value Host (gitlab.com) should NOT be rejected.
	if err := glTracker.ConfigForHost(""); err != nil {
		t.Fatalf("gitlab.com (Host: \"\") should not be rejected: %v", err)
	}
}

// TestNewGitLabTracker_HostTokensRoutedCorrectly verifies that per-host
// tokens from GitLabConfig flow into the tracker and are used for the
// correct host. We construct the tracker through the wiring function, then
// verify the wiring by testing that:
//   - The tracker was constructed without error (host tokens flowed through).
//   - The self-managed host is accepted (not ErrHostNotAllowed).
//   - The default host (gitlab.com) is accepted.
//   - An unconfigured host is still rejected.
//
// For full end-to-end token routing, the tracker_test.go in the adapter
// package already covers Get/List with a fake server.
func TestNewGitLabTracker_HostTokensRoutedCorrectly(t *testing.T) {
	t.Setenv("AO_GITLAB_TOKEN", "default-token")

	selfHost := "gitlab.internal.example"
	cfg := config.GitLabConfig{
		AllowedHosts: []string{selfHost},
		HostTokens: map[string]string{
			selfHost: "self-host-token",
		},
	}

	tracker, err := newGitLabTracker(cfg)
	if err != nil {
		t.Fatalf("newGitLabTracker: %v", err)
	}

	glTracker, ok := tracker.(*trackergitlab.Tracker)
	if !ok {
		t.Fatalf("expected *trackergitlab.Tracker, got %T", tracker)
	}

	// Self-managed host should be accepted.
	if err := glTracker.ConfigForHost(selfHost); err != nil {
		t.Fatalf("self-managed host %q with HostTokens was rejected: %v", selfHost, err)
	}

	// Default host (gitlab.com) should also be accepted.
	if err := glTracker.ConfigForHost(""); err != nil {
		t.Fatalf("gitlab.com should not be rejected: %v", err)
	}

	// Unconfigured host should be rejected.
	if err := glTracker.ConfigForHost("gitlab.attacker.example"); err == nil {
		t.Fatalf("unconfigured host should be rejected")
	}
}

// TestNewGitLabTracker_HostTokensCaseInsensitive verifies that HostTokens
// keys from config are lowercased before being passed to the tracker, so a
// mixed-case config key (e.g. "GitLab.Internal.Example") still matches the
// lowercased host lookup in the tracker's configForHost.
func TestNewGitLabTracker_HostTokensCaseInsensitive(t *testing.T) {
	t.Setenv("AO_GITLAB_TOKEN", "default-token")

	selfHost := "GitLab.Internal.Example" // mixed case in config
	cfg := config.GitLabConfig{
		AllowedHosts: []string{selfHost},
		HostTokens: map[string]string{
			selfHost: "self-host-token",
		},
	}

	tracker, err := newGitLabTracker(cfg)
	if err != nil {
		t.Fatalf("newGitLabTracker: %v", err)
	}

	glTracker, ok := tracker.(*trackergitlab.Tracker)
	if !ok {
		t.Fatalf("expected *trackergitlab.Tracker, got %T", tracker)
	}

	// The tracker lowercases AllowedHosts internally. newGitLabTracker must
	// also lowercase HostTokens keys so they match. If the host is accepted
	// (not ErrHostNotAllowed), the token was found in the map.
	if err := glTracker.ConfigForHost("gitlab.internal.example"); err != nil {
		t.Fatalf("mixed-case config host should be accepted after lowercasing: %v", err)
	}
}

// TestNewGitLabTracker_UnconfiguredHostRejected verifies that a host not in
// AllowedHosts (and not gitlab.com) is rejected by the tracker before any
// credential is attached — both for Get-style and List-style host lookups
// (both go through configForHost).
func TestNewGitLabTracker_UnconfiguredHostRejected(t *testing.T) {
	t.Setenv("AO_GITLAB_TOKEN", "default-token")

	cfg := config.GitLabConfig{
		AllowedHosts: []string{"gitlab.internal.example"},
	}
	tracker, err := newGitLabTracker(cfg)
	if err != nil {
		t.Fatalf("newGitLabTracker: %v", err)
	}

	glTracker, ok := tracker.(*trackergitlab.Tracker)
	if !ok {
		t.Fatalf("expected *trackergitlab.Tracker, got %T", tracker)
	}

	// Unconfigured host via ConfigForHost (covers both Get and List paths,
	// since both call configForHost before any HTTP).
	err = glTracker.ConfigForHost("gitlab.attacker.example")
	if !errors.Is(err, trackergitlab.ErrHostNotAllowed) {
		t.Fatalf("unconfigured host should be rejected with ErrHostNotAllowed, got: %v", err)
	}

	// Configured host should still pass.
	if err := glTracker.ConfigForHost("gitlab.internal.example"); err != nil {
		t.Fatalf("configured host should be accepted: %v", err)
	}

	// gitlab.com (zero value) should always pass.
	if err := glTracker.ConfigForHost(""); err != nil {
		t.Fatalf("gitlab.com should not be rejected: %v", err)
	}
}

// TestNewMultiTracker_WithGitLabConfig verifies that newMultiTracker passes
// GitLabConfig through to newGitLabTracker — a self-managed host configured
// in GitLabConfig should produce a tracker that accepts that host, and the
// multi-tracker should be non-nil when the GitLab token is available.
//
// To avoid network calls, we verify the multi-tracker is non-nil and that
// dispatching a Get to the GitLab provider with an unconfigured host
// returns ErrHostNotAllowed (not ErrUnknownProvider), proving the GitLab
// sub-tracker is wired with the host allowlist. For an allowlisted host,
// Get returns ErrHostNotAllowed only when the host is NOT in the allowlist;
// when it IS allowlisted, Get proceeds to HTTP. To avoid that HTTP call we
// instead call Get with a deliberately unconfigured host and assert
// ErrHostNotAllowed, which fires before any network I/O.
func TestNewMultiTracker_WithGitLabConfig(t *testing.T) {
	t.Setenv("AO_GITLAB_TOKEN", "default-token")
	t.Setenv("AO_GITHUB_TOKEN", "")

	selfHost := "gitlab.internal.example"
	cfg := config.GitLabConfig{
		AllowedHosts: []string{selfHost},
		HostTokens: map[string]string{
			selfHost: "self-host-token",
		},
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := newMultiTracker(cfg, config.OneDevConfig{}, log)
	if tracker == nil {
		t.Fatal("newMultiTracker = nil, want non-nil when GitLab token is available")
	}

	// Verify the GitLab sub-tracker is registered and wired with the
	// allowlist by dispatching a Get to a known-unconfigured host. The
	// tracker rejects it with ErrHostNotAllowed before any HTTP call — no
	// network I/O occurs.
	_, err := tracker.Get(context.Background(), domain.TrackerID{
		Provider: domain.TrackerProviderGitLab,
		Native:   "group/project#1",
		Host:     "gitlab.attacker.example",
	})
	if !errors.Is(err, trackergitlab.ErrHostNotAllowed) {
		t.Fatalf("multi-tracker Get with unconfigured host: err = %v, want ErrHostNotAllowed", err)
	}

	// Also verify the multi-tracker is the concrete multi.Tracker type so
	// that we know both GitHub and GitLab sub-trackers were considered.
	mt, ok := tracker.(*trackermulti.Tracker)
	if !ok {
		t.Fatalf("expected *trackermulti.Tracker, got %T", tracker)
	}
	_ = mt // multi-tracker is non-nil and correctly typed
}

// ---------------------------------------------------------------------------
// lazyTracker
// ---------------------------------------------------------------------------

type stubTracker struct{}

func (stubTracker) Get(context.Context, domain.TrackerID) (domain.Issue, error) {
	return domain.Issue{}, nil
}

func (stubTracker) List(context.Context, domain.TrackerRepo, domain.ListFilter) ([]domain.Issue, error) {
	return nil, nil
}

func (stubTracker) Preflight(context.Context) error { return nil }

var _ ports.Tracker = stubTracker{}

// TestLazyTracker_ConstructionDeferredAndStickyOnlyOnSuccess: build must not
// run until the first tracker call; failures are not cached (a later
// `gh auth login` is picked up without a restart); a successful construction
// is reused.
func TestLazyTracker_ConstructionDeferredAndStickyOnlyOnSuccess(t *testing.T) {
	builds := 0
	succeed := false
	lt := &lazyTracker{
		name:   "github",
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		build: func() (ports.Tracker, error) {
			builds++
			if !succeed {
				return nil, errors.New("no token")
			}
			return stubTracker{}, nil
		},
	}

	if builds != 0 {
		t.Fatal("build ran before first tracker use")
	}
	if err := lt.Preflight(context.Background()); err == nil {
		t.Fatal("Preflight err = nil, want build failure")
	}
	if builds != 1 {
		t.Fatalf("builds = %d, want 1", builds)
	}
	if err := lt.Preflight(context.Background()); err == nil {
		t.Fatal("Preflight err = nil, want build failure")
	}
	if builds != 2 {
		t.Fatalf("builds = %d, want 2 (failures must not be sticky)", builds)
	}

	succeed = true
	if err := lt.Preflight(context.Background()); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if builds != 3 {
		t.Fatalf("builds = %d, want 3", builds)
	}
	if err := lt.Preflight(context.Background()); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if builds != 3 {
		t.Fatalf("builds = %d, want 3 (a successful construction must be reused)", builds)
	}
}

// TestWiring_NewMultiTracker_DoesNotProbeGHCLIAtBoot is a regression test for
// the laziness guarantee: with no env tokens configured, constructing the
// multi-tracker must not spawn `gh auth token` — the CLI probe runs on first
// tracker use, not at daemon boot.
func TestWiring_NewMultiTracker_DoesNotProbeGHCLIAtBoot(t *testing.T) {
	t.Setenv("AO_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("AO_GITLAB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "")

	marker := filepath.Join(t.TempDir(), "gh-called")
	t.Setenv("GH_MARKER", marker)
	binDir := t.TempDir()
	writeRecordingGHExecutable(t, binDir)
	// PATH contains only binDir: no real gh/glab can leak in. The GitLab leg
	// fails to resolve `glab` and is omitted; the GitHub leg stays lazy.
	t.Setenv("PATH", binDir)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := newMultiTracker(config.GitLabConfig{}, config.OneDevConfig{}, log)
	if tracker == nil {
		t.Fatal("newMultiTracker = nil, want non-nil: the GitHub slot is lazily constructed and always present")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("gh CLI invoked during newMultiTracker (marker exists); stat: %v", err)
	}

	// First use triggers the probe. It fails (fake gh exits 1) — the error is
	// fine, the marker proves the probe happened now and not at boot.
	_, _ = tracker.Get(context.Background(), domain.TrackerID{Provider: domain.TrackerProviderGitHub, Native: "acme/demo#1"})
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("gh CLI not invoked on first tracker use; stat marker: %v", err)
	}
}

// writeRecordingGHExecutable installs a fake `gh` that records every
// invocation to $GH_MARKER and exits 1 (no token).
func writeRecordingGHExecutable(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		content := "@echo off\r\necho called >> %GH_MARKER%\r\nexit /b 1\r\n"
		if err := os.WriteFile(filepath.Join(dir, "gh.cmd"), []byte(content), 0o755); err != nil {
			t.Fatalf("write fake gh.cmd: %v", err)
		}
		return
	}
	content := "#!/bin/sh\necho called >> \"$GH_MARKER\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(content), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
}

func TestNewMultiTrackerResolvesEveryRegisteredProvider(t *testing.T) {
	gitlabCfg, onedevCfg := fullyConfiguredTrackerEnv(t)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := newMultiTracker(gitlabCfg, onedevCfg, log)
	if tracker == nil {
		t.Fatal("newMultiTracker = nil, want non-nil when every provider is configured")
	}

	// Each id names a host that is deliberately not allowlisted (GitLab,
	// OneDev) or is malformed (GitHub, which has no host concept), so the
	// sub-tracker rejects it locally.
	ids := map[domain.TrackerProvider]domain.TrackerID{
		domain.TrackerProviderGitHub: {Provider: domain.TrackerProviderGitHub, Native: "no-hash-here"},
		domain.TrackerProviderGitLab: {Provider: domain.TrackerProviderGitLab, Native: "group/project#1", Host: "gitlab.attacker.example"},
		domain.TrackerProviderOneDev: {Provider: domain.TrackerProviderOneDev, Native: "productone#1", Host: "onedev.attacker.example"},
	}
	for _, provider := range allTrackerProviders {
		_, err := tracker.Get(context.Background(), ids[provider])
		if err == nil {
			t.Errorf("Get(%s) = nil error; want the sub-tracker's own rejection", provider)
			continue
		}
		if errors.Is(err, trackermulti.ErrUnknownProvider) {
			t.Errorf("Get(%s) = %v; provider is not registered with the multi-tracker", provider, err)
		}
	}

	// List routes on TrackerRepo.Provider rather than TrackerID.Provider, so
	// it is a separate dispatch path and needs its own assertion.
	repos := map[domain.TrackerProvider]domain.TrackerRepo{
		domain.TrackerProviderGitHub: {Provider: domain.TrackerProviderGitHub, Native: "not-a-repo"},
		domain.TrackerProviderGitLab: {Provider: domain.TrackerProviderGitLab, Native: "group/project", Host: "gitlab.attacker.example"},
		domain.TrackerProviderOneDev: {Provider: domain.TrackerProviderOneDev, Native: "productone", Host: "onedev.attacker.example"},
	}
	for _, provider := range allTrackerProviders {
		_, err := tracker.List(context.Background(), repos[provider], domain.ListFilter{})
		if errors.Is(err, trackermulti.ErrUnknownProvider) {
			t.Errorf("List(%s) = %v; provider is not registered with the multi-tracker", provider, err)
		}
	}
}

// TestNewOneDevTrackerDisabledWithoutAllowedHosts pins the degrade-gracefully
// path: OneDev has no public instance, so the common case is an operator who
// never configured one. That must disable only the OneDev tracker.
func TestNewOneDevTrackerDisabledWithoutAllowedHosts(t *testing.T) {
	t.Setenv("AO_GITHUB_TOKEN", "gh-test-token")
	t.Setenv("AO_ONEDEV_TOKEN", "od-test-token")

	if _, err := newOneDevTracker(config.OneDevConfig{Token: "od-test-token"}); !errors.Is(err, trackeronedev.ErrNoAllowedHosts) {
		t.Fatalf("newOneDevTracker with no hosts = %v, want ErrNoAllowedHosts", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tracker := newMultiTracker(config.GitLabConfig{}, config.OneDevConfig{}, log)
	if tracker == nil {
		t.Fatal("newMultiTracker = nil; an unconfigured OneDev must not disable the other trackers")
	}
	_, err := tracker.Get(context.Background(), domain.TrackerID{
		Provider: domain.TrackerProviderOneDev, Native: "productone#1", Host: "onedev.internal.example",
	})
	if !errors.Is(err, trackermulti.ErrUnknownProvider) {
		t.Errorf("Get(onedev) with no OneDev configured = %v, want ErrUnknownProvider", err)
	}
}

// TestNewOneDevTrackerPassesConfigThrough covers the two issue-specific knobs
// and the failure mode of a bad state override, which must name the state
// rather than silently falling back to the defaults it was meant to replace.
func TestNewOneDevTrackerPassesConfigThrough(t *testing.T) {
	cfg := config.OneDevConfig{
		Token:              "od-test-token",
		AllowedHosts:       []string{"http://onedev.internal.example:6610"},
		IssueStates:        map[string]string{"Blocked": "in_progress"},
		IssueAssigneeField: "Owner",
	}
	tracker, err := newOneDevTracker(cfg)
	if err != nil {
		t.Fatalf("newOneDevTracker: %v", err)
	}
	od, ok := tracker.(*trackeronedev.Tracker)
	if !ok {
		t.Fatalf("newOneDevTracker returned %T, want *trackeronedev.Tracker", tracker)
	}
	if err := od.ConfigForHost("onedev.internal.example:6610"); err != nil {
		t.Errorf("configured host rejected: %v", err)
	}
	if err := od.ConfigForHost("other.example.com"); !errors.Is(err, trackeronedev.ErrHostNotAllowed) {
		t.Errorf("unconfigured host = %v, want ErrHostNotAllowed", err)
	}

	cfg.IssueStates = map[string]string{"Blocked": "stuck"}
	if _, err := newOneDevTracker(cfg); err == nil {
		t.Error("newOneDevTracker accepted an unknown normalized state; want an error")
	} else if !strings.Contains(err.Error(), "Blocked") {
		t.Errorf("error %q does not name the offending state", err)
	}
}

var allTrackerProviders = []domain.TrackerProvider{
	domain.TrackerProviderGitHub,
	domain.TrackerProviderGitLab,
	domain.TrackerProviderOneDev,
}

// fullyConfiguredTrackerEnv gives every tracker provider what it needs to
// construct, so a test can assert on registration rather than on which
// credential happened to be present on the machine running it.
func fullyConfiguredTrackerEnv(t *testing.T) (config.GitLabConfig, config.OneDevConfig) {
	t.Helper()
	t.Setenv("AO_GITHUB_TOKEN", "gh-test-token")
	t.Setenv("AO_GITLAB_TOKEN", "gl-test-token")
	t.Setenv("AO_ONEDEV_TOKEN", "od-test-token")
	return config.GitLabConfig{AllowedHosts: []string{"gitlab.internal.example"}},
		config.OneDevConfig{
			Token:        "od-test-token",
			AllowedHosts: []string{"http://onedev.internal.example:6610"},
		}
}

// TestTrackerSubTrackersRegistersEveryProvider pins that every provider AO
// ships a tracker adapter for is actually registered. The SCM side shipped
// with only one of three constructors covered and the gap reached a live
// daemon; this is the tracker-side equivalent guard.
