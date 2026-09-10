package systeminstall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ErrInstallationChanged fences a displayed advisory from a new install target.
var ErrInstallationChanged = errors.New("selected Codex installation changed; refresh and try again")

// CodexUpdateAdvisory is independent of installation/authentication readiness.
// The token identifies daemon-derived ownership evidence, never executable argv.
type CodexUpdateAdvisory struct {
	scope            string
	Path             string    `json:"path"`
	RealPath         string    `json:"realPath"`
	Version          string    `json:"version"`
	Source           string    `json:"source"`
	VersionSource    string    `json:"versionSource"`
	AvailableVersion string    `json:"availableVersion,omitempty"`
	UpdateAvailable  bool      `json:"updateAvailable"`
	CanUpdate        bool      `json:"canUpdate"`
	Token            string    `json:"token,omitempty"`
	Stale            bool      `json:"stale"`
	Warning          string    `json:"warning,omitempty"`
	CheckedAt        time.Time `json:"checkedAt"`
	RunningSessions  int       `json:"runningSessions"`
}

func newer(a, b string) bool {
	return semver.IsValid("v"+a) && semver.IsValid("v"+b) && semver.Compare("v"+a, "v"+b) > 0
}

// CodexUpdate checks at most hourly (five minutes after failures), coalesces
// concurrent readers, and bounds all host/network reads. Refresh is an explicit
// advisory refresh only; neither branch can execute an update.
func (s *Service) CodexUpdate(ctx context.Context, refresh bool) (CodexUpdateAdvisory, error) {
	if s.codexMaintenance == nil {
		return CodexUpdateAdvisory{}, fmt.Errorf("maintenance for Codex is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 16*time.Second)
	defer cancel()
	s.advisoryMu.Lock()
	if call := s.advisoryCall; call != nil {
		s.advisoryMu.Unlock()
		select {
		case <-ctx.Done():
			return CodexUpdateAdvisory{}, ctx.Err()
		case <-call:
		}
		// A forced post-update check must not reuse a read started before it.
		return s.CodexUpdate(ctx, refresh)
	}
	if !refresh && time.Now().Before(s.advisoryUntil) {
		a := s.advisory
		s.advisoryMu.Unlock()
		return s.withCodexSessions(ctx, a)
	}
	call := make(chan struct{})
	s.advisoryCall = call
	previous := s.advisory
	s.advisoryMu.Unlock()
	installation, resolveErr := s.codexMaintenance.Resolve(ctx)
	var latest string
	var versionErr error
	if resolveErr == nil {
		latest, versionErr = s.codexMaintenance.Latest(ctx, installation)
	}
	a := advisoryFor(installation, latest)
	ttl := time.Hour
	if err := errors.Join(resolveErr, versionErr); err != nil {
		if previous.Path == installation.Path && previous.RealPath == installation.RealPath {
			if a.Version == "" {
				a.Version = previous.Version
			}
			if a.AvailableVersion == "" {
				a.AvailableVersion = previous.AvailableVersion
			}
		}
		a.Stale, a.CanUpdate = true, false
		a.Warning = strings.TrimSpace(a.Warning + " Version check failed: " + err.Error() + ". You can continue using a compatible Codex; refresh to retry.")
		ttl = 5 * time.Minute
	}
	s.advisoryMu.Lock()
	s.advisory, s.advisoryUntil = a, time.Now().Add(ttl)
	s.advisoryCall = nil
	close(call)
	s.advisoryMu.Unlock()
	return s.withCodexSessions(ctx, a)
}

func advisoryFor(i ports.CodexInstallation, latest string) CodexUpdateAdvisory {
	outdated := newer(latest, i.Version)
	return CodexUpdateAdvisory{scope: i.Scope, Path: i.Path, RealPath: i.RealPath, Version: i.Version,
		Source: i.Source, VersionSource: i.VersionSource, AvailableVersion: latest,
		UpdateAvailable: outdated, CanUpdate: outdated && len(i.Command.Argv) > 0,
		Token: i.Fingerprint, Warning: i.Warning, CheckedAt: time.Now().UTC()}
}

func (s *Service) withCodexSessions(ctx context.Context, a CodexUpdateAdvisory) (CodexUpdateAdvisory, error) {
	if s.sessions == nil {
		a.CanUpdate = false
		return a, nil
	}
	sessions, err := s.sessions.ListAllSessions(ctx)
	if err != nil {
		return a, err
	}
	for _, session := range sessions {
		if session.Harness == domain.HarnessCodex && !session.IsTerminated {
			a.RunningSessions++
		}
	}
	return a, nil
}

// StartCodexUpdate accepts only a displayed ownership token. It uses the same
// durable jobs as first installs and never terminates existing provider hosts.
func (s *Service) StartCodexUpdate(ctx context.Context, token string) (Job, error) {
	a, err := s.CodexUpdate(ctx, false)
	if err != nil {
		return Job{}, err
	}
	if token == "" || token != a.Token {
		return Job{}, ErrInstallationChanged
	}
	if !a.CanUpdate {
		return Job{}, fmt.Errorf("%w: refresh Codex update information or update the selected installation manually", ErrInstallMethod)
	}
	if a.RunningSessions > 0 {
		return Job{}, fmt.Errorf("%w: stop AO Codex sessions explicitly before updating the shared installation; restarting AO does not replace surviving provider processes", ErrHarnessActive)
	}
	s.mu.Lock()
	if job := s.jobs[TargetCodex]; job != nil && activeStatus(job.Status) {
		s.mu.Unlock()
		return Job{}, ErrInstallActive
	}
	now := time.Now().UTC()
	job := &Job{Target: TargetCodex, Status: StatusQueued, Method: "update:" + a.Source, ExpectedDestination: a.Path, StartedAt: &now, UpdatedAt: &now}
	previous := s.jobs[TargetCodex]
	s.jobs[TargetCodex] = job
	initial := *job
	s.mu.Unlock()
	if err := s.persistJob(ctx, initial); err != nil {
		s.mu.Lock()
		if previous == nil {
			delete(s.jobs, TargetCodex)
		} else {
			s.jobs[TargetCodex] = previous
		}
		s.mu.Unlock()
		return Job{}, err
	}
	if !s.beginWorker() {
		s.finishAgentJob(job, StatusInterrupted, "", "daemon shutdown interrupted the update", "")
		return initial, nil
	}
	go func() { //nolint:gosec // bounded daemon-owned job intentionally outlives the HTTP request.
		defer s.workers.Done()
		s.runCodexUpdate(job, a)
	}()
	return initial, nil
}

func (s *Service) acquireInstaller(ctx context.Context, job *Job) error {
	if s.installerGate == nil {
		return nil
	} // legacy resolver-only services
	select {
	case s.installerGate <- struct{}{}:
		return nil
	default:
		if IsAgentTarget(job.Target) {
			if err := s.transitionAgentJob(job, StatusQueued, "Waiting for another installer job.", "", ""); err != nil {
				return err
			}
		}
	}
	select {
	case s.installerGate <- struct{}{}:
		if IsAgentTarget(job.Target) {
			if err := s.transitionAgentJob(job, StatusInstalling, "", "", ""); err != nil {
				s.releaseInstaller()
				return err
			}
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("installer queue interrupted: %w", ctx.Err())
	}
}
func (s *Service) releaseInstaller() {
	if s.installerGate != nil {
		<-s.installerGate
	}
}

func (s *Service) runCodexUpdate(job *Job, before CodexUpdateAdvisory) {
	ctx, cancel := context.WithTimeout(s.backgroundContext, s.installTimeout)
	defer cancel()
	if err := s.acquireInstaller(ctx, job); err != nil {
		s.finishAgentJob(job, StatusInterrupted, "", err.Error(), "")
		return
	}
	defer s.releaseInstaller()
	if !s.codexGate.TryLock() {
		s.finishAgentJob(job, StatusFailed, "", "A Codex session is starting; wait, then refresh and try again.", "")
		return
	}
	defer s.codexGate.Unlock()
	a, err := s.withCodexSessions(ctx, CodexUpdateAdvisory{})
	if err != nil || s.sessions == nil || a.RunningSessions > 0 {
		s.finishAgentJob(job, StatusFailed, "", "Stop AO Codex sessions explicitly before updating; AO could not establish a safe stopped state.", "")
		return
	}
	installation, err := s.codexMaintenance.Resolve(ctx)
	if err != nil || installation.Fingerprint != before.Token || len(installation.Command.Argv) == 0 {
		s.finishAgentJob(job, StatusFailed, "", ErrInstallationChanged.Error(), "")
		return
	}
	s.mu.Lock()
	job.Command = strings.Join(installation.Command.Argv, " ")
	s.mu.Unlock()
	if err := s.transitionAgentJob(job, StatusInstalling, "Updating the shared user installation. Existing provider processes are not upgraded by restarting AO.", "", ""); err != nil {
		s.finishAgentJob(job, StatusFailed, "", err.Error(), "")
		return
	}
	out := &capturedOutput{max: maxOutputBytes}
	var runErr error
	if s.installCommands == nil {
		runErr = fmt.Errorf("installer runner is unavailable")
	} else {
		command := installation.Command
		command.Env = append(append([]string{}, command.Env...), "CI=1", "NONINTERACTIVE=1", "NPM_CONFIG_AUDIT=false", "NPM_CONFIG_FUND=false")
		writer := &codexJobOutput{service: s, job: job, output: out}
		runErr = s.installCommands.RunInstall(ctx, command, writer, writer)
	}
	if ctx.Err() != nil {
		runErr = fmt.Errorf("update of Codex interrupted or timed out: %w", ctx.Err())
	}
	// Even unsuccessful installers may have replaced files. Refresh independently
	// of the expired command context and preserve stale catalogs on probe errors.
	commandError := ""
	if runErr != nil {
		commandError = runErr.Error()
	}
	transitionErr := s.transitionAgentJob(job, StatusVerifying, "", commandError, installation.Path)
	verifyCtx, verifyCancel := context.WithTimeout(s.backgroundContext, 90*time.Second)
	defer verifyCancel()
	after, probeErr := s.CodexUpdate(verifyCtx, true)
	var refreshErr error
	if s.refreshCodex != nil {
		refreshErr = s.refreshCodex(verifyCtx)
	} else {
		refreshErr = fmt.Errorf("provider readiness/model refresh is unavailable")
	}
	if s.backgroundContext.Err() != nil {
		s.finishAgentJob(job, StatusInterrupted, "", "Daemon shutdown interrupted Codex update verification. Refresh before retrying.", "")
		return
	}
	if runErr != nil {
		s.finishAgentJob(job, StatusFailed, "", "Codex update command failed: "+runErr.Error()+". Check installer output and installation permissions; AO will not request elevation or switch installers.", "")
		return
	}
	if transitionErr != nil {
		s.finishAgentJob(job, StatusFailed, "", "Could not persist update verification state: "+transitionErr.Error(), "")
		return
	}
	if probeErr != nil || after.Stale || after.Version == "" {
		s.finishAgentJob(job, StatusFailed, "", "Update command completed, but the effective installation or available version could not be verified. Refresh and try again. "+after.Warning, after.Path)
		return
	}
	if after.Path != before.Path || after.scope != before.scope {
		s.finishAgentJob(job, StatusFailed, "", "Update completed but the selected installation changed. Refresh and verify the effective Codex manually.", after.Path)
		return
	}
	if !newer(after.Version, before.Version) || after.UpdateAvailable || newer(before.AvailableVersion, after.Version) {
		s.finishAgentJob(job, StatusFailed, "", fmt.Sprintf("Update command completed, but effective Codex is unchanged or still outdated (%s). Refresh or update this installation manually.", after.Version), after.Path)
		return
	}
	if refreshErr != nil {
		s.finishAgentJob(job, StatusFailed, "", "Codex "+after.Version+" is installed, but provider readiness/model refresh failed: "+refreshErr.Error(), after.Path)
		return
	}
	verified, finalErr := s.codexMaintenance.Resolve(verifyCtx)
	if finalErr != nil || verified.Fingerprint != after.Token {
		s.finishAgentJob(job, StatusFailed, "", "The effective Codex installation changed during provider verification. Refresh and try again.", after.Path)
		return
	}
	s.finishAgentJob(job, StatusSucceeded, "Verified fresh Codex "+after.Version+" and refreshed provider models. Model access still depends on the selected account. Surviving external processes retain their old runtime.", "", after.Path)
}

type codexJobOutput struct {
	service *Service
	job     *Job
	output  *capturedOutput
}

func (w *codexJobOutput) Write(p []byte) (int, error) {
	w.service.mu.Lock()
	defer w.service.mu.Unlock()
	n, err := w.output.Write(p)
	w.job.Output = w.output.String()
	return n, err
}
