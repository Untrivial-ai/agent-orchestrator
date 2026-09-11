package workerexec

import (
	"context"
	"errors"
	"log/slog"
	"os"
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
	Control         ControlPlane
	Builder         CommandBuilder
	Runner          Runner
	Workspace       string
	PollInterval    time.Duration
	CancelInterval  time.Duration
	CompletionRetry time.Duration
	Logger          *slog.Logger

	// busy is read by the transport supervisor while an interface handoff is
	// draining Chat work. It belongs to the long-lived controller instance, not
	// an individual turn, so a TUI handoff never mistakes a running headless
	// provider process for an idle controller.
	busy atomic.Bool
}

// Idle reports whether the Chat controller has no currently executing turn.
func (s *Supervisor) Idle() bool {
	return !s.busy.Load()
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

	for {
		turn, err := s.Control.ClaimTurn(ctx)
		if err != nil {
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
			if !wait(ctx, s.PollInterval) {
				return nil
			}
			continue
		}
		s.busy.Store(true)
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
	if turn.CancelRequested {
		return s.retryComplete(ctx, turn.ID, turn.Attempt, true)
	}
	credential, err := s.Control.Credential(ctx)
	if err != nil {
		return s.retryFailure(ctx, turn.ID, turn.Attempt, "coding-agent credential unavailable")
	}
	command, err := s.Builder.Build(ctx, turn, credential, s.Workspace)
	credential.Secret = ""
	if err != nil {
		return s.retryFailure(ctx, turn.ID, turn.Attempt, err.Error())
	}
	if command.Cleanup != nil {
		defer command.Cleanup()
	}
	executionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	projector := newChatOutputProjector(turn.Harness)
	publish := func(output Output) error {
		for _, projected := range projector.Project(output) {
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

	runErr := s.Runner.Run(executionCtx, command, publish)
	var flushed []Output
	if runErr == nil {
		// Codex normally terminates JSONL records with a newline, but flush the
		// final partial record before reading the identity so a clean process
		// exit cannot strand a thread.started event in the projector buffer.
		flushed = projector.Flush()
	}
	if identity := projector.NativeConversationID(); identity != "" {
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

	if cancellation.Load() {
		return s.retryComplete(ctx, turn.ID, turn.Attempt, true)
	}
	if ctx.Err() != nil {
		return ctx.Err()
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
