package review

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

const cancelInterruptDelay = 150 * time.Millisecond

const defaultReviewerInitialDelay = 500 * time.Millisecond

const reviewerTaskMessagePrefix = "Read and follow the AO review task in `"

// EnvRunFile points reviewer-local AO CLI calls at the live daemon run-file.
const EnvRunFile = "AO_RUN_FILE"

// EnvAOCommandWarning records why AO could not guarantee a reviewer-local `ao`
// command. Review launch remains best-effort so provider reviews can still be
// posted and later reconciled, but this makes the degraded path diagnosable.
const EnvAOCommandWarning = "AO_REVIEW_AO_COMMAND_WARNING"

// Launcher spawns, re-notifies, and probes a reviewer over a worker's worktree.
// It is the side of the engine that talks to the reviewer registry and runtime;
// the engine owns the orchestration and persistence.
type Launcher interface {
	// Preflight checks whether the reviewer for the given harness is available
	// to run (binary on PATH, etc.) without starting a runtime pane. It runs
	// only when a reviewer launch is actually required, after ReviewRun rows
	// have been created. On failure the engine's Trigger() calls failRuns() to
	// mark those rows as failed, matching the existing Spawn failure semantics.
	Preflight(ctx context.Context, harness domain.ReviewerHarness, workspacePath string) error
	// live pane (stable per worker, reused across passes) plus any native agent
	// session id known at launch time.
	Spawn(ctx context.Context, spec LaunchSpec) (LaunchResult, error)
	// RestoreTerminal launches an idle reviewer pane for a worker that already
	// has review history, without creating or starting a new review run.
	RestoreTerminal(ctx context.Context, spec LaunchSpec) (LaunchResult, error)
	// Notify asks an already-running reviewer pane to review a new commit.
	Notify(ctx context.Context, handleID string, spec LaunchSpec) error
	// Alive reports whether a reviewer pane is still running.
	Alive(ctx context.Context, handleID string) (bool, error)
	// Reusable reports whether the harness accepts another review task in its
	// existing TUI. Reviewers with launch-fixed context return false.
	Reusable(harness domain.ReviewerHarness) bool
	// Cancel interrupts a running reviewer pane while keeping the terminal alive.
	Cancel(ctx context.Context, handleID string, harness domain.ReviewerHarness) error
	// Destroy tears down a reviewer pane entirely. This is used when the owning
	// worker session itself is torn down, not for user-facing review cancellation.
	Destroy(ctx context.Context, handleID string) error
}

// LaunchSpec is the engine's request to (re)launch a reviewer for one pass.
type LaunchSpec struct {
	RunID           string
	BatchID         string
	ReviewSessionID string
	WorkerID        domain.SessionID
	ProjectID       domain.ProjectID
	Harness         domain.ReviewerHarness
	WorkspacePath   string
	AgentSessionID  string
	PreviousRuns    []domain.ReviewRun
	PRURL           string
	TargetSHA       string
	ReviewQueue     []ports.ReviewTask
	ReviewIndex     int
}

// LaunchResult is the terminal/runtime state created by a reviewer launch.
type LaunchResult struct {
	HandleID       string
	AgentSessionID string
}

// reviewerRuntime is the runtime surface the launcher needs: create a pane,
// inject a message into a running pane, and probe liveness. The tmux runtime
// satisfies it.
type reviewerRuntime interface {
	Create(ctx context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error)
	Destroy(ctx context.Context, handle ports.RuntimeHandle) error
	Interrupt(ctx context.Context, handle ports.RuntimeHandle) error
	SendInput(ctx context.Context, handle ports.RuntimeHandle, input string) error
	IsAlive(ctx context.Context, handle ports.RuntimeHandle) (bool, error)
	SendMessage(ctx context.Context, handle ports.RuntimeHandle, message string) error
	GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error)
}

// agentLauncher resolves a reviewer adapter from the registry and drives the
// runtime. The reviewer reuses the worker's worktree (a fresh session worktree
// would branch off the default branch and so would not contain the PR changes).
type agentLauncher struct {
	reviewers  ports.ReviewerResolver
	runtime    reviewerRuntime
	dataDir    string
	runFile    string
	auth       agentAuthResolver
	executable func() (string, error)
}

type preLaunchReviewer interface {
	PreLaunch(ctx context.Context, inv ports.ReviewInvocation) error
}

type preflightReviewer interface {
	ReviewPreflight(ctx context.Context, workspacePath string) error
}

type agentAuthResolver interface {
	AuthStatus(ctx context.Context, harness domain.ReviewerHarness) (ports.AgentAuthStatus, bool, error)
}

// LauncherOption configures reviewer launcher behavior.
type LauncherOption func(*agentLauncher)

// WithAgentAuth lets reviewer preflight reuse the agent auth catalog for the
// same harness. Reviewer-specific auth probes must not be stricter than the
// normal agent's local auth status.
func WithAgentAuth(auth agentAuthResolver) LauncherOption {
	return func(l *agentLauncher) {
		l.auth = auth
	}
}

// WithExecutable overrides os.Executable for reviewer PATH pinning. Production
// leaves this unset; tests use it to model the daemon binary that should make a
// bare `ao review submit` available inside reviewer panes.
func WithExecutable(executable func() (string, error)) LauncherOption {
	return func(l *agentLauncher) {
		l.executable = executable
	}
}

// WithRunFilePath pins reviewer-local AO CLI calls to this daemon's run-file.
// This is intentionally separate from AO_DATA_DIR: the CLI discovers the live
// daemon from AO_RUN_FILE, not from the durable data directory.
func WithRunFilePath(path string) LauncherOption {
	return func(l *agentLauncher) {
		l.runFile = path
	}
}

// NewLauncher builds the production reviewer launcher.
func NewLauncher(reviewers ports.ReviewerResolver, rt reviewerRuntime, dataDir string, opts ...LauncherOption) Launcher {
	l := &agentLauncher{reviewers: reviewers, runtime: rt, dataDir: dataDir, executable: os.Executable}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// Preflight checks whether the reviewer for the given harness can be launched
// without starting a runtime pane. It uses the same source of truth as Spawn:
// resolve the adapter, build the real ReviewCommand, and validate the
// executable. The only difference from Spawn is that Preflight stops before
// runtime.Create().
func (l *agentLauncher) Preflight(ctx context.Context, harness domain.ReviewerHarness, workspacePath string) error {
	reviewer, ok := l.reviewers.Reviewer(harness)
	if !ok {
		return fmt.Errorf("no reviewer adapter for harness %q", harness)
	}
	cmd, err := reviewer.ReviewCommand(ctx, ports.ReviewInvocation{WorkspacePath: workspacePath})
	if err != nil {
		return fmt.Errorf("reviewer command: %w", err)
	}
	if len(cmd.Argv) == 0 {
		return fmt.Errorf("reviewer produced empty command")
	}
	// Unwrap any leading env KEY=value ... prefix so the real binary is
	// validated. Mirrors launchBinary in the session manager, which already
	// skips the same prefix to validate the worker agent binary.
	bin := cmd.Argv[0]
	if filepath.Base(bin) == "env" {
		for _, arg := range cmd.Argv[1:] {
			if !strings.Contains(arg, "=") {
				bin = arg
				break
			}
		}
	}
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("reviewer binary %q not found: %w", bin, err)
	}
	authStatus, authKnown, err := l.agentAuthStatus(ctx, harness)
	if err != nil {
		return err
	}
	if authKnown && authStatus == ports.AgentAuthStatusUnauthorized {
		return fmt.Errorf("agent auth catalog reports reviewer harness %q is unauthorized", harness)
	}
	if pf, ok := reviewer.(preflightReviewer); ok {
		if err := pf.ReviewPreflight(ctx, workspacePath); err != nil {
			if authKnown && authStatus == ports.AgentAuthStatusAuthorized && looksLikeAuthFailure(err) {
				return nil
			}
			return err
		}
	}
	return nil
}

func (l *agentLauncher) agentAuthStatus(ctx context.Context, harness domain.ReviewerHarness) (ports.AgentAuthStatus, bool, error) {
	if l.auth == nil {
		return "", false, nil
	}
	status, ok, err := l.auth.AuthStatus(ctx, harness)
	if err != nil {
		return "", false, fmt.Errorf("agent auth catalog for reviewer harness %q: %w", harness, err)
	}
	return status, ok, nil
}

func looksLikeAuthFailure(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, token := range []string{"auth", "login", "logged in", "credential", "token", "api key", "unauthor"} {
		if strings.Contains(msg, token) {
			return true
		}
	}
	return false
}

// reviewerHandleID is the stable runtime handle for a worker's reviewer pane, so
// one live reviewer is reused across passes.
func reviewerHandleID(workerID domain.SessionID) string {
	return "review-" + string(workerID)
}

func (l *agentLauncher) invocation(spec LaunchSpec) ports.ReviewInvocation {
	prompt, systemPrompt := reviewTexts(spec)
	return ports.ReviewInvocation{
		ReviewerID:      reviewerHandleID(spec.WorkerID),
		RunID:           spec.RunID,
		WorkerSessionID: spec.WorkerID,
		AgentSessionID:  spec.AgentSessionID,
		PRURL:           spec.PRURL,
		TargetSHA:       spec.TargetSHA,
		ReviewQueue:     spec.ReviewQueue,
		ReviewIndex:     spec.ReviewIndex,
		WorkspacePath:   spec.WorkspacePath,
		DataDir:         l.dataDir,
		RunFilePath:     l.runFile,
		Prompt:          prompt,
		SystemPrompt:    systemPrompt,
	}
}

// prepareInvocation stores the full reviewer instructions outside the
// worktree, then replaces the terminal-visible prompt with a short file
// reference.
// Reviewer panes are shared by desktop, mobile, and direct runtime attaches,
// so keeping the full text out of the PTY is the only device-independent way
// to hide it.
func (l *agentLauncher) prepareInvocation(ctx context.Context, spec LaunchSpec) (ports.ReviewInvocation, error) {
	if err := ctx.Err(); err != nil {
		return ports.ReviewInvocation{}, err
	}
	inv := l.invocation(spec)
	if strings.TrimSpace(l.dataDir) == "" {
		return ports.ReviewInvocation{}, fmt.Errorf("reviewer prompt data directory is required")
	}
	if strings.TrimSpace(spec.BatchID) == "" || strings.TrimSpace(spec.RunID) == "" {
		return ports.ReviewInvocation{}, fmt.Errorf("reviewer prompt batch and run ids are required")
	}
	promptRoot := filepath.Join(l.dataDir, "prompts", string(spec.WorkerID), "reviewer")
	requestDir := filepath.Join(promptRoot, "requests", spec.BatchID, spec.RunID)
	if err := os.MkdirAll(requestDir, 0o700); err != nil {
		return ports.ReviewInvocation{}, fmt.Errorf("create reviewer prompt directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return ports.ReviewInvocation{}, err
	}
	taskPath := filepath.Join(requestDir, "task.md")
	if err := os.WriteFile(taskPath, []byte(strings.TrimRight(inv.Prompt, "\n")+"\n"), 0o600); err != nil {
		return ports.ReviewInvocation{}, fmt.Errorf("write reviewer task prompt: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return ports.ReviewInvocation{}, err
	}
	systemPath := filepath.Join(promptRoot, "system.md")
	systemPrompt := strings.TrimRight(inv.SystemPrompt, "\n") + "\n\n" +
		"AO stores each review task in an immutable file. Whenever AO asks you to start a review task, " +
		"read the exact file path in that request first and follow it completely.\n"
	if err := os.WriteFile(systemPath, []byte(systemPrompt), 0o600); err != nil {
		return ports.ReviewInvocation{}, fmt.Errorf("write reviewer system prompt: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return ports.ReviewInvocation{}, err
	}
	inv.Prompt = reviewerTaskMessagePrefix + filepath.ToSlash(taskPath) + "`."
	inv.SystemPrompt = ""
	inv.SystemPromptFile = systemPath
	inv.TaskPromptFile = taskPath
	inv.TaskPromptRoot = promptRoot
	return inv, nil
}

func (l *agentLauncher) prepareIdleInvocation(spec LaunchSpec) (ports.ReviewInvocation, error) {
	promptRoot := filepath.Join(l.dataDir, "prompts", string(spec.WorkerID), "reviewer")
	systemPath := filepath.Join(promptRoot, "system.md")
	systemPrompt := reviewSystemPrompt() + "\n\n" +
		"AO may restore your terminal before a new review task exists. In that state, wait for AO to send a review task file path before reviewing or submitting results.\n"
	if err := os.MkdirAll(promptRoot, 0o700); err != nil {
		return ports.ReviewInvocation{}, fmt.Errorf("create reviewer prompt directory: %w", err)
	}
	if err := os.WriteFile(systemPath, []byte(systemPrompt), 0o600); err != nil {
		return ports.ReviewInvocation{}, fmt.Errorf("write reviewer system prompt: %w", err)
	}
	prompt := fmt.Sprintf("Reviewer terminal restored for worker session %s.\n\n%s\n\nWait for AO to send the next review task file path before submitting a new review.", spec.WorkerID, previousReviewHistoryText(spec.PreviousRuns))
	return ports.ReviewInvocation{
		ReviewerID:       reviewerHandleID(spec.WorkerID),
		WorkerSessionID:  spec.WorkerID,
		AgentSessionID:   spec.AgentSessionID,
		WorkspacePath:    spec.WorkspacePath,
		DataDir:          l.dataDir,
		RunFilePath:      l.runFile,
		Prompt:           prompt,
		SystemPrompt:     "",
		SystemPromptFile: systemPath,
		TaskPromptRoot:   promptRoot,
	}, nil
}

func previousReviewHistoryText(runs []domain.ReviewRun) string {
	if len(runs) == 0 {
		return "No previous review runs are recorded for this worker session."
	}
	var b strings.Builder
	b.WriteString("Previous review runs for this worker session, newest first:")
	const maxRuns = 20
	for i, run := range runs {
		if i >= maxRuns {
			fmt.Fprintf(&b, "\n* ... %d older review run(s) omitted.", len(runs)-maxRuns)
			break
		}
		fmt.Fprintf(&b, "\n* %s", run.PRURL)
		if run.TargetSHA != "" {
			fmt.Fprintf(&b, " at %s", run.TargetSHA)
		}
		fmt.Fprintf(&b, " - status: %s", run.Status)
		if run.Verdict != "" {
			fmt.Fprintf(&b, ", verdict: %s", run.Verdict)
		}
		if run.GithubReviewID != "" {
			fmt.Fprintf(&b, ", GitHub review: %s", run.GithubReviewID)
		}
		body := strings.TrimSpace(run.Body)
		if body != "" {
			fmt.Fprintf(&b, "\n  Body: %s", truncateReviewHistoryBody(body))
		}
	}
	return b.String()
}

func truncateReviewHistoryBody(body string) string {
	const maxBody = 1200
	body = strings.Join(strings.Fields(body), " ")
	if len(body) <= maxBody {
		return body
	}
	return strings.TrimSpace(body[:maxBody]) + "..."
}

func (l *agentLauncher) Spawn(ctx context.Context, spec LaunchSpec) (LaunchResult, error) {
	inv, err := l.prepareInvocation(ctx, spec)
	if err != nil {
		return LaunchResult{}, err
	}
	return l.launchReviewerTerminal(ctx, spec, inv)
}

func (l *agentLauncher) RestoreTerminal(ctx context.Context, spec LaunchSpec) (LaunchResult, error) {
	inv, err := l.prepareIdleInvocation(spec)
	if err != nil {
		return LaunchResult{}, err
	}
	return l.launchReviewerTerminalWithMode(ctx, spec, inv, true)
}

func (l *agentLauncher) launchReviewerTerminal(ctx context.Context, spec LaunchSpec, inv ports.ReviewInvocation) (LaunchResult, error) {
	return l.launchReviewerTerminalWithMode(ctx, spec, inv, false)
}

func (l *agentLauncher) launchReviewerTerminalWithMode(ctx context.Context, spec LaunchSpec, inv ports.ReviewInvocation, restoring bool) (LaunchResult, error) {
	reviewer, ok := l.reviewers.Reviewer(spec.Harness)
	if !ok {
		return LaunchResult{}, fmt.Errorf("no reviewer adapter for harness %q", spec.Harness)
	}
	if pl, ok := reviewer.(preLaunchReviewer); ok {
		if err := pl.PreLaunch(ctx, inv); err != nil {
			return LaunchResult{}, fmt.Errorf("reviewer pre-launch: %w", err)
		}
	}
	var cmd ports.ReviewCommandSpec
	if restoring {
		if restorer, ok := reviewer.(ports.ReviewerRestorer); ok {
			restoreCmd, restoreOK, err := restorer.ReviewRestoreCommand(ctx, inv)
			if err != nil {
				return LaunchResult{}, fmt.Errorf("reviewer restore command: %w", err)
			}
			if restoreOK {
				cmd = restoreCmd
			}
		}
	}
	if len(cmd.Argv) == 0 {
		var err error
		cmd, err = reviewer.ReviewCommand(ctx, inv)
		if err != nil {
			return LaunchResult{}, fmt.Errorf("reviewer command: %w", err)
		}
	}
	handleID := reviewerHandleID(spec.WorkerID)
	// The reviewer handle is stable per worker, so a still-live pane from a
	// previous pass would otherwise block `tmux new-session` (duplicate name) or,
	// worse, keep serving under its old harness. Destroy any stale pane on this
	// handle first so the reviewer always (re)launches under spec.Harness's
	// sandbox/permissions/env — which are applied only here at Create, never by
	// Notify. Destroy is idempotent when no pane exists (first spawn / dead pane).
	if err := l.runtime.Destroy(ctx, ports.RuntimeHandle{ID: handleID}); err != nil {
		return LaunchResult{}, fmt.Errorf("reviewer replace stale pane: %w", err)
	}
	workingDirectory := cmd.WorkingDirectory
	if workingDirectory == "" {
		workingDirectory = spec.WorkspacePath
	}
	handle, err := l.runtime.Create(ctx, ports.RuntimeConfig{
		SessionID:     domain.SessionID(handleID),
		WorkspacePath: workingDirectory,
		Argv:          cmd.Argv,
		Env:           l.runtimeEnv(ctx, spec, cmd.Argv, cmd.Env),
	})
	if err != nil {
		return LaunchResult{}, fmt.Errorf("reviewer runtime: %w", err)
	}
	if cmd.InitialMessage != "" {
		if err := l.waitForPromptReadiness(ctx, reviewer, handle); err != nil {
			return LaunchResult{}, fmt.Errorf("reviewer prompt readiness: %w", err)
		}
		if err := l.runtime.SendMessage(ctx, handle, cmd.InitialMessage); err != nil {
			return LaunchResult{}, fmt.Errorf("reviewer initial message: %w", err)
		}
	}
	agentSessionID := strings.TrimSpace(cmd.AgentSessionID)
	if agentSessionID == "" {
		agentSessionID = strings.TrimSpace(spec.AgentSessionID)
	}
	return LaunchResult{HandleID: handle.ID, AgentSessionID: agentSessionID}, nil
}

func (l *agentLauncher) waitForPromptReadiness(ctx context.Context, reviewer ports.Reviewer, handle ports.RuntimeHandle) error {
	hints := ports.PromptReadinessHints{InitialDelay: defaultReviewerInitialDelay}
	if provider, ok := reviewer.(ports.ReviewerPromptReadinessProvider); ok {
		provided, err := provider.ReviewPromptReadinessHints(ctx)
		if err != nil {
			return err
		}
		hints = provided
	}
	if hints.InitialDelay > 0 {
		timer := time.NewTimer(hints.InitialDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if len(hints.Patterns) == 0 || hints.Timeout <= 0 {
		return nil
	}
	poll := hints.PollInterval
	if poll <= 0 {
		poll = 200 * time.Millisecond
	}
	lines := hints.Lines
	if lines <= 0 {
		lines = 80
	}
	deadline := time.NewTimer(hints.Timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		output, err := l.runtime.GetOutput(ctx, handle, lines)
		if err == nil && outputContainsAny(output, hints.Patterns) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			// Readiness is best-effort: never block a review forever when a CLI
			// changes its prompt marker. The startup delay still prevents an
			// immediate write into process initialization.
			return nil
		case <-ticker.C:
		}
	}
}

func outputContainsAny(output string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern != "" && strings.Contains(output, pattern) {
			return true
		}
	}
	return false
}

func (l *agentLauncher) runtimeEnv(ctx context.Context, spec LaunchSpec, argv []string, base map[string]string) map[string]string {
	env := make(map[string]string, len(base)+3)
	for k, v := range base {
		env[k] = v
	}
	delete(env, sessionmanager.EnvSessionID)
	env["AO_REVIEW_SESSION_ID"] = spec.ReviewSessionID
	env["AO_REVIEW_WORKER_SESSION_ID"] = string(spec.WorkerID)
	env["AO_REVIEW_HARNESS"] = string(spec.Harness)
	env[sessionmanager.EnvProjectID] = string(spec.ProjectID)
	env[sessionmanager.EnvDataDir] = l.dataDir
	if strings.TrimSpace(l.runFile) != "" {
		env[EnvRunFile] = l.runFile
	}
	path, err := sessionmanager.HookPATH(l.executable, os.Getenv, env)
	if err == nil {
		env["PATH"] = path
	} else if shimDir, shimErr := l.ensureAOShimDir(); shimErr == nil {
		env["PATH"] = prependPathDir(shimDir, env["PATH"])
	} else {
		env[EnvAOCommandWarning] = fmt.Sprintf("PATH pin failed: %v; AO shim fallback failed: %v", err, shimErr)
	}
	sessionmanager.AugmentRuntimePATHForLaunchBinary(ctx, env, argv, exec.LookPath)
	return env
}

func (l *agentLauncher) ensureAOShimDir() (string, error) {
	if strings.TrimSpace(l.dataDir) == "" {
		return "", fmt.Errorf("reviewer AO shim data directory is required")
	}
	exe, err := l.executable()
	if err != nil {
		return "", fmt.Errorf("resolve AO executable: %w", err)
	}
	if !filepath.IsAbs(exe) {
		exe, err = filepath.Abs(exe)
		if err != nil {
			return "", fmt.Errorf("make AO executable absolute: %w", err)
		}
	}
	shimDir := filepath.Join(l.dataDir, "reviewer-runtime", "bin")
	if err := os.MkdirAll(shimDir, 0o700); err != nil {
		return "", fmt.Errorf("create reviewer AO shim directory: %w", err)
	}
	shimPath := filepath.Join(shimDir, "ao")
	if runtime.GOOS == "windows" {
		shimPath += ".cmd"
	}
	if err := os.WriteFile(shimPath, []byte(aoShimScript(exe)), 0o600); err != nil {
		return "", fmt.Errorf("write reviewer AO shim: %w", err)
	}
	if err := os.Chmod(shimPath, 0o700); err != nil { // #nosec G302 -- the reviewer shim must be executable by the AO user.
		return "", fmt.Errorf("mark reviewer AO shim executable: %w", err)
	}
	return shimDir, nil
}

func aoShimScript(executable string) string {
	if runtime.GOOS == "windows" {
		return "@echo off\r\n\"" + executable + "\" %*\r\n"
	}
	return "#!/bin/sh\nexec " + shellQuote(executable) + ` "$@"` + "\n"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func prependPathDir(dir, path string) string {
	if strings.TrimSpace(path) == "" {
		return dir
	}
	return dir + string(os.PathListSeparator) + path
}

func (l *agentLauncher) Notify(ctx context.Context, handleID string, spec LaunchSpec) error {
	reviewer, ok := l.reviewers.Reviewer(spec.Harness)
	if !ok {
		return fmt.Errorf("no reviewer adapter for harness %q", spec.Harness)
	}
	inv, err := l.prepareInvocation(ctx, spec)
	if err != nil {
		return err
	}
	msg, err := reviewer.ReviewMessage(ctx, inv)
	if err != nil {
		return fmt.Errorf("reviewer message: %w", err)
	}
	if err := l.runtime.SendMessage(ctx, ports.RuntimeHandle{ID: handleID}, msg); err != nil {
		return fmt.Errorf("notify reviewer: %w", err)
	}
	return nil
}

func (l *agentLauncher) Alive(ctx context.Context, handleID string) (bool, error) {
	if handleID == "" {
		return false, nil
	}
	return l.runtime.IsAlive(ctx, ports.RuntimeHandle{ID: handleID})
}

func (l *agentLauncher) Reusable(harness domain.ReviewerHarness) bool {
	reviewer, ok := l.reviewers.Reviewer(harness)
	if !ok {
		return false
	}
	policy, ok := reviewer.(ports.ReviewerReusePolicy)
	return !ok || policy.ReviewProcessReusable()
}

func (l *agentLauncher) Cancel(ctx context.Context, handleID string, harness domain.ReviewerHarness) error {
	if handleID == "" {
		return nil
	}
	reviewer, ok := l.reviewers.Reviewer(harness)
	if !ok {
		return fmt.Errorf("no reviewer adapter for harness %q", harness)
	}
	canceller, ok := reviewer.(ports.ReviewerCanceller)
	if !ok {
		return fmt.Errorf("reviewer adapter %q does not support cancellation", harness)
	}
	spec, err := canceller.ReviewCancel(ctx)
	if err != nil {
		return fmt.Errorf("reviewer cancel: %w", err)
	}
	switch spec.Mode {
	case ports.ReviewCancelMessage:
		message := strings.TrimSpace(spec.Message)
		if message == "" {
			return fmt.Errorf("reviewer adapter %q returned empty cancel message", harness)
		}
		return l.runtime.SendMessage(ctx, ports.RuntimeHandle{ID: handleID}, message)
	case ports.ReviewCancelInput:
		inputs := spec.Inputs
		if len(inputs) == 0 && spec.Input != "" {
			inputs = []string{spec.Input}
		}
		if len(inputs) == 0 {
			return fmt.Errorf("reviewer adapter %q returned empty cancel input", harness)
		}
		for i, input := range inputs {
			if input == "" {
				return fmt.Errorf("reviewer adapter %q returned empty cancel input", harness)
			}
			if err := l.runtime.SendInput(ctx, ports.RuntimeHandle{ID: handleID}, input); err != nil {
				return err
			}
			if i < len(inputs)-1 && spec.InputDelay > 0 {
				timer := time.NewTimer(spec.InputDelay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
		return nil
	case ports.ReviewCancelInterrupt:
		interrupts := spec.Interrupts
		if interrupts <= 0 {
			interrupts = 1
		}
		for i := 0; i < interrupts; i++ {
			if err := l.runtime.Interrupt(ctx, ports.RuntimeHandle{ID: handleID}); err != nil {
				return err
			}
			if i < interrupts-1 {
				timer := time.NewTimer(cancelInterruptDelay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("reviewer adapter %q returned unsupported cancel mode %q", harness, spec.Mode)
	}
}

func (l *agentLauncher) Destroy(ctx context.Context, handleID string) error {
	if handleID == "" {
		return nil
	}
	return l.runtime.Destroy(ctx, ports.RuntimeHandle{ID: handleID})
}
