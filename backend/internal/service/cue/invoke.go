package cue

import (
	"context"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

// Sessions is the session side of a cue invocation: resolving an active session
// to message, or spawning a worker when no session was requested.
type Sessions interface {
	Get(ctx context.Context, id domain.SessionID) (domain.Session, error)
	Send(ctx context.Context, id domain.SessionID, message string, attachment *ports.SpawnAttachment) error
	Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error)
}

// CommandTerminals dispatches trusted command cues to shells without an agent.
type CommandTerminals interface {
	RunCueCommand(context.Context, shellterm.RunCueCommandInput) (shellterm.ShellTerminal, error)
}

// Invoke runs one cue. Agent cues are delivered to the selected session, or
// spawn a worker when invoked from the project board. Command cues open a
// normal terminal in the selected session worktree or project root and
// never create or message an agent.
func (s *Service) Invoke(ctx context.Context, cueID domain.CueID, input InvokeInput) (InvokeResult, error) {
	if err := ctx.Err(); err != nil {
		return InvokeResult{}, err
	}
	if s == nil || s.store == nil {
		return InvokeResult{}, fmt.Errorf("cue: store is required")
	}
	if strings.TrimSpace(string(cueID)) == "" {
		return InvokeResult{}, apierr.Invalid("INVALID_CUE_ID", "Cue id is required", nil)
	}
	cue, ok, err := s.store.SelectCueByID(ctx, cueID)
	if err != nil {
		return InvokeResult{}, err
	}
	if !ok {
		return InvokeResult{}, apierr.NotFound("CUE_NOT_FOUND", "Unknown cue")
	}
	if cue.Type == domain.CueTypeCommand {
		if !input.AllowDirectCommand {
			return InvokeResult{}, apierr.Forbidden("CUE_COMMAND_LOOPBACK_REQUIRED", "Command Cues can only run through the local daemon")
		}
		if s.terminals == nil {
			return InvokeResult{}, fmt.Errorf("cue: command terminals are required")
		}
		terminal, err := s.terminals.RunCueCommand(ctx, shellterm.RunCueCommandInput{
			ProjectID: cue.ProjectID, SessionID: input.SessionID, Shell: input.Shell,
			Command: cue.Command, PreferredHandleID: input.PreferredTerminalHandleID,
		})
		if err != nil {
			return InvokeResult{}, err
		}
		return InvokeResult{Kind: domain.CueTypeCommand, Terminal: &terminal}, nil
	}
	if s.sessions == nil {
		return InvokeResult{}, fmt.Errorf("cue: sessions are required")
	}
	message := cue.Prompt

	if input.SessionID != "" {
		if strings.TrimSpace(string(input.SessionID)) == "" {
			return InvokeResult{}, apierr.Invalid("INVALID_SESSION_ID", "Session id must not be blank", nil)
		}
		sess, err := s.sessions.Get(ctx, input.SessionID)
		if err != nil {
			return InvokeResult{}, err
		}
		if !sessionMessageable(sess, cue.ProjectID) {
			return InvokeResult{}, apierr.Conflict("CUE_TARGET_UNAVAILABLE", "This session cannot accept the cue. Select an available session in the cue's project.", nil)
		}
		if err := ctx.Err(); err != nil {
			return InvokeResult{}, err
		}
		if err := s.sessions.Send(ctx, input.SessionID, message, nil); err != nil {
			return InvokeResult{}, err
		}
		return InvokeResult{Kind: domain.CueTypeAgent, SessionID: input.SessionID}, nil
	}

	if err := ctx.Err(); err != nil {
		return InvokeResult{}, err
	}
	spawned, _, _, err := s.sessions.Spawn(ctx, ports.SpawnConfig{
		ProjectID: cue.ProjectID,
		Kind:      domain.KindWorker,
		Prompt:    message,
	})
	if err != nil {
		return InvokeResult{}, err
	}
	return InvokeResult{Kind: domain.CueTypeAgent, SessionID: spawned.ID}, nil
}

// sessionMessageable reports whether a cue may be injected into a session:
// it must exist in the cue's project, not be terminated or exited, and must
// not be blocked on a pending decision (stray input could answer a permission
// dialog on the user's behalf). Delivery performs its own final state checks.
func sessionMessageable(sess domain.Session, projectID domain.ProjectID) bool {
	if sess.IsTerminated {
		return false
	}
	if sess.ProjectID != projectID {
		return false
	}
	switch sess.Activity.State {
	case domain.ActivityExited, domain.ActivityBlocked:
		return false
	}
	return true
}
