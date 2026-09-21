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

type Supervisor struct {
	Control         ControlPlane
	Builder         CommandBuilder
	Runner          Runner
	Workspace       string
	PollInterval    time.Duration
	CancelInterval  time.Duration
	CompletionRetry time.Duration
	Logger          *slog.Logger
}

func (s Supervisor) Run(ctx context.Context) error {
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
		if err := s.execute(ctx, *turn); err != nil {
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

func (s Supervisor) execute(ctx context.Context, turn worker.Turn) error {
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

	runErr := s.Runner.Run(executionCtx, command, func(output Output) error {
		return s.Control.PublishOutput(executionCtx, worker.OutputEvent{
			TurnID:  turn.ID,
			Attempt: turn.Attempt,
			Stream:  output.Stream,
			Text:    output.Text,
		})
	})
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

func (s Supervisor) retryComplete(
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

func (s Supervisor) retryFailure(
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
