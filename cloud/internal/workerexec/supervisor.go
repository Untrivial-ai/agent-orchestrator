package workerexec

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
)

type ControlPlane interface {
	ClaimTurn(context.Context) (*worker.Turn, error)
	Credential(context.Context) (worker.CredentialResponse, error)
	PublishOutput(context.Context, worker.OutputEvent) error
	CancellationRequested(context.Context, string, int) (bool, error)
	CompleteTurn(context.Context, string, int, bool) error
	FailTurn(context.Context, string, int, string) error
}

// conversationIdentityPublisher is optional so alternate worker controls can
// keep the existing runner contract. The Cloud client implements it to make a
// Chat-first session restorable in the native TUI after Codex announces its
// thread id on stdout.
type conversationIdentityPublisher interface {
	PublishActivity(context.Context, worker.ActivityEvent) error
}

type Supervisor struct {
	Control             ControlPlane
	Builder             CommandBuilder
	Runner              Runner
	UseProviderProtocol bool
	Workspace           string
	PollInterval        time.Duration
	CancelInterval      time.Duration
	CompletionRetry     time.Duration
	Logger              *slog.Logger
	PullRequestClaimer  func(context.Context, string) error
	GitRefReporter      func(context.Context) error

	// busy covers both a claim in flight and its turn until completion. A
	// handoff must not cancel Run between a durable claim and turn completion.
	busy atomic.Bool
	// stopping fences turns claimed while an interface interrupt is in flight.
	// A turn can be busy before execute publishes its cancel function.
	stopping atomic.Bool
	// drainFences stop new claims without interrupting a turn already in flight.
	// A timed-out handoff releases its own fence so the source can keep running.
	drainFences           atomic.Int32
	workspaceStartupError atomic.Pointer[string]

	activeMu    sync.Mutex
	active      *activeExecution
	activeACP   *acpSession
	activeCodex *codexSession
	prObserver  *pullRequestObserver
}

// SetWorkspaceStartupError keeps Chat from running a prompt in an empty
// checkout. The controller still claims queued turns so each receives a
// durable failure instead of waiting forever for a workspace that cannot load.
func (s *Supervisor) SetWorkspaceStartupError(message string) {
	if message == "" {
		s.workspaceStartupError.Store(nil)
		return
	}
	s.workspaceStartupError.Store(&message)
}

type activeExecution struct {
	cancel      context.CancelFunc
	interrupted atomic.Bool
}

// Idle reports whether the Chat controller has no currently executing turn.
func (s *Supervisor) Idle() bool {
	return !s.busy.Load()
}

// ResetInterrupt prepares this controller for a new Chat activation after a
// completed interface handoff or rollback. The previous Run must have exited.
func (s *Supervisor) ResetInterrupt() {
	s.stopping.Store(false)
}

// FenceClaims lets a drain finish the current turn without starting another.
// It shares activeMu with the claim boundary, so an in-flight claim keeps
// Idle false until that turn has been settled.
func (s *Supervisor) FenceClaims() func() {
	s.activeMu.Lock()
	s.drainFences.Add(1)
	s.activeMu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { s.drainFences.Add(-1) }) }
}

// Interrupt stops only the running Chat turn. It returns false when the
// controller is between turns, which is still a successful stop-now boundary.
func (s *Supervisor) Interrupt() bool {
	s.stopping.Store(true)
	s.activeMu.Lock()
	active := s.active
	if active != nil {
		active.interrupted.Store(true)
	}
	s.activeMu.Unlock()
	if active != nil {
		active.cancel()
	}
	return active != nil
}

// Steer delivers guidance to the exact in-flight provider turn. The transport
// request is acknowledged only after the provider reports an injected steer.
func (s *Supervisor) Steer(ctx context.Context, turnID, text string) error {
	s.activeMu.Lock()
	active := s.activeACP
	codex := s.activeCodex
	s.activeMu.Unlock()
	if active != nil {
		return active.Steer(ctx, turnID, text)
	}
	if codex != nil {
		return codex.Steer(ctx, turnID, text)
	}
	return errors.New("there is no steerable provider turn")
}

func (s *Supervisor) Run(ctx context.Context) error {
	if s.Control == nil || s.Builder == nil || s.Runner == nil {
		return errors.New("worker supervisor dependencies are incomplete")
	}
	if s.Logger == nil {
		s.Logger = slog.Default()
	}
	if s.PollInterval <= 0 {
		s.PollInterval = time.Second
	}
	if s.CancelInterval <= 0 {
		s.CancelInterval = 500 * time.Millisecond
	}
	if s.CompletionRetry <= 0 {
		s.CompletionRetry = time.Second
	}
	if s.Workspace == "" {
		return errors.New("AO_WORKSPACE_DIR is required")
	}
	if err := os.MkdirAll(s.Workspace, 0o700); err != nil {
		return err
	}
	if s.PullRequestClaimer != nil {
		s.prObserver = &pullRequestObserver{claim: s.PullRequestClaimer}
	}

	for {
		if s.stopping.Load() || s.drainFences.Load() > 0 {
			if !wait(ctx, s.PollInterval) {
				return nil
			}
			continue
		}
		// Interrupt and the start of a claim share activeMu. Either stopping
		// wins before the claim, or Idle stays false until its turn is settled.
		s.activeMu.Lock()
		if s.stopping.Load() || s.drainFences.Load() > 0 {
			s.activeMu.Unlock()
			continue
		}
		s.busy.Store(true)
		s.activeMu.Unlock()
		turn, err := s.Control.ClaimTurn(ctx)
		if err != nil {
			s.busy.Store(false)
			if ctx.Err() != nil {
				return nil
			}
			s.Logger.Warn("claim worker turn failed", "error", err)
			if !wait(ctx, s.PollInterval) {
				return nil
			}
			continue
		}
		if turn == nil {
			s.busy.Store(false)
			if !wait(ctx, s.PollInterval) {
				return nil
			}
			continue
		}
		err = s.execute(ctx, *turn)
		s.busy.Store(false)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.Logger.Warn(
				"worker turn execution failed",
				"turn_id", turn.ID,
				"attempt", turn.Attempt,
				"error", err,
			)
		}
	}
}

func (s *Supervisor) execute(ctx context.Context, turn worker.Turn) error {
	if turn.CancelRequested || s.stopping.Load() {
		return s.retryComplete(ctx, turn.ID, turn.Attempt, true)
	}
	if failure := s.workspaceStartupError.Load(); failure != nil {
		return s.retryFailure(ctx, turn.ID, turn.Attempt, *failure)
	}
	executionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	active := &activeExecution{cancel: cancel}
	s.activeMu.Lock()
	s.active = active
	interrupted := s.stopping.Load()
	if interrupted {
		active.interrupted.Store(true)
	}
	s.activeMu.Unlock()
	if interrupted {
		cancel()
	}
	defer func() {
		s.activeMu.Lock()
		if s.active == active {
			s.active = nil
		}
		s.activeMu.Unlock()
	}()
	if active.interrupted.Load() {
		return s.retryComplete(ctx, turn.ID, turn.Attempt, true)
	}
	credential, err := s.Control.Credential(executionCtx)
	if active.interrupted.Load() {
		return s.retryComplete(ctx, turn.ID, turn.Attempt, true)
	}
	if err != nil {
		return s.retryFailure(ctx, turn.ID, turn.Attempt, "coding-agent credential unavailable")
	}
	command, err := s.Builder.Build(executionCtx, turn, credential, s.Workspace)
	credential.Secret = ""
	if active.interrupted.Load() {
		if command.Cleanup != nil {
			command.Cleanup()
		}
		return s.retryComplete(ctx, turn.ID, turn.Attempt, true)
	}
	if err != nil {
		return s.retryFailure(ctx, turn.ID, turn.Attempt, err.Error())
	}
	if command.Cleanup != nil {
		defer command.Cleanup()
	}
	if active.interrupted.Load() {
		return s.retryComplete(ctx, turn.ID, turn.Attempt, true)
	}
	projector := newChatOutputProjector(turn.Harness)
	publish := func(output Output) error {
		for _, projected := range projector.Project(output) {
			if s.prObserver != nil {
				s.prObserver.observe(projected)
			}
			if err := s.Control.PublishOutput(executionCtx, worker.OutputEvent{
				TurnID:  turn.ID,
				Attempt: turn.Attempt,
				Stream:  projected.Stream,
				Text:    projected.Text,
			}); err != nil {
				return err
			}
		}
		return nil
	}
	done := make(chan struct{})
	var cancellation atomic.Bool
	go func() {
		ticker := time.NewTicker(s.CancelInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-executionCtx.Done():
				return
			case <-ticker.C:
				requested, pollErr := s.Control.CancellationRequested(
					executionCtx, turn.ID, turn.Attempt,
				)
				if pollErr != nil {
					continue
				}
				if requested {
					cancellation.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	acpTurn := s.UseProviderProtocol && (turn.Harness == "claude-code" || turn.Harness == "cursor")
	codexTurn := s.UseProviderProtocol && turn.Harness == "codex"
	var runErr error
	if acpTurn {
		runErr = s.runACP(executionCtx, turn, command, func(output Output) error {
			if s.prObserver != nil {
				s.prObserver.observe(output)
			}
			return s.Control.PublishOutput(executionCtx, worker.OutputEvent{
				TurnID: turn.ID, Attempt: turn.Attempt, Stream: output.Stream, Text: output.Text, ItemID: output.ItemID, Activity: output.Activity,
			})
		}, func(identity string) error {
			if publisher, ok := s.Control.(conversationIdentityPublisher); ok {
				return publisher.PublishActivity(executionCtx, worker.ActivityEvent{
					Harness: turn.Harness, Event: "session-start", AgentSessionID: identity,
				})
			}
			return nil
		})
	} else if codexTurn {
		runErr = s.runCodex(executionCtx, turn, command, func(output Output) error {
			if s.prObserver != nil {
				s.prObserver.observe(output)
			}
			return s.Control.PublishOutput(executionCtx, worker.OutputEvent{
				TurnID: turn.ID, Attempt: turn.Attempt, Stream: output.Stream, Text: output.Text, ItemID: output.ItemID, Activity: output.Activity,
			})
		}, func(identity string) error {
			if publisher, ok := s.Control.(conversationIdentityPublisher); ok {
				return publisher.PublishActivity(executionCtx, worker.ActivityEvent{
					Harness: turn.Harness, Event: "session-start", AgentSessionID: identity,
				})
			}
			return nil
		})
	} else {
		runErr = s.Runner.Run(executionCtx, command, publish)
	}
	var flushed []Output
	if runErr == nil && !acpTurn && !codexTurn {
		// Codex normally terminates JSONL records with a newline, but flush the
		// final partial record before reading the identity so a clean process
		// exit cannot strand a thread.started event in the projector buffer.
		flushed = projector.Flush()
	}
	if identity := projector.NativeConversationID(); identity != "" && !acpTurn && !codexTurn {
		if publisher, ok := s.Control.(conversationIdentityPublisher); ok {
			if err := publisher.PublishActivity(executionCtx, worker.ActivityEvent{
				Harness:        turn.Harness,
				Event:          "session-start",
				AgentSessionID: identity,
			}); err != nil && executionCtx.Err() == nil {
				s.Logger.Warn("publish headless conversation identity", "error", err)
			}
		}
	}
	if runErr == nil {
		for _, output := range flushed {
			if err := s.Control.PublishOutput(executionCtx, worker.OutputEvent{
				TurnID:  turn.ID,
				Attempt: turn.Attempt,
				Stream:  output.Stream,
				Text:    output.Text,
			}); err != nil {
				runErr = err
				break
			}
		}
	}
	close(done)

	if cancellation.Load() || active.interrupted.Load() {
		return s.retryComplete(ctx, turn.ID, turn.Attempt, true)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.prObserver != nil {
		claimCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if err := s.prObserver.claimObserved(claimCtx, s.Workspace); err != nil {
			s.Logger.Warn("automatic pull request claim failed", "error", err)
		}
		cancel()
	}
	if s.GitRefReporter != nil {
		reportCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		if err := s.GitRefReporter(reportCtx); err != nil {
			s.Logger.Warn("report worker branch heads", "error", err)
		}
		cancel()
	}
	if runErr != nil {
		return s.retryFailure(ctx, turn.ID, turn.Attempt, boundedError(runErr.Error()))
	}
	return s.retryComplete(ctx, turn.ID, turn.Attempt, false)
}

func (s *Supervisor) retryComplete(
	ctx context.Context,
	turnID string,
	attempt int,
	cancelled bool,
) error {
	for {
		err := s.Control.CompleteTurn(ctx, turnID, attempt, cancelled)
		if err == nil {
			return nil
		}
		if !wait(ctx, s.CompletionRetry) {
			return ctx.Err()
		}
	}
}

func (s *Supervisor) retryFailure(
	ctx context.Context,
	turnID string,
	attempt int,
	message string,
) error {
	message = boundedError(message)
	for {
		err := s.Control.FailTurn(ctx, turnID, attempt, message)
		if err == nil {
			return nil
		}
		if !wait(ctx, s.CompletionRetry) {
			return ctx.Err()
		}
	}
}

func boundedError(message string) string {
	const limit = 4 << 10
	if len(message) <= limit {
		return message
	}
	return message[:limit]
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
