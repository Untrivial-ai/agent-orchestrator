package cue

import (
	"context"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// commandCueInstructionFmt shapes a command cue into an agent instruction. A
// command cue is executed by the agent, not by AO: the agent runs the command
// in its sandbox and reports the output back, so the message is an instruction
// to do exactly that.
const commandCueInstructionFmt = "Run `%s` and report the output."

// Sessions is the session side of a cue invocation: resolving an active session
// to message, or spawning a worker when no session could be messaged.
type Sessions interface {
	Get(ctx context.Context, id domain.SessionID) (domain.Session, error)
	Send(ctx context.Context, id domain.SessionID, message string, attachment *ports.SpawnAttachment) error
	Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error)
}

// Invoke runs one cue. Inside a session, the cue's content is sent to that
// session as a message (command cues travel as "run this command and report the
// output" instructions because the agent owns execution). Without a session, or
// when the given session is stale or unusable (unknown id, other project,
// terminated, exited, or blocked on a pending decision), AO spawns a worker
// session for the cue's project seeded with the same message. It returns the id
// of the session that received the message.
func (s *Service) Invoke(ctx context.Context, cueID domain.CueID, sessionID domain.SessionID) (domain.SessionID, error) {
	if s == nil || s.store == nil {
		return "", fmt.Errorf("cue: store is required")
	}
	if s.sessions == nil {
		return "", fmt.Errorf("cue: sessions are required")
	}
	if strings.TrimSpace(string(cueID)) == "" {
		return "", apierr.Invalid("INVALID_CUE_ID", "Cue id is required", nil)
	}
	cue, ok, err := s.store.SelectCueByID(ctx, cueID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", apierr.NotFound("CUE_NOT_FOUND", "Unknown cue")
	}
	message := invokeMessage(cue)

	if strings.TrimSpace(string(sessionID)) != "" {
		if sess, err := s.sessions.Get(ctx, sessionID); err == nil && sessionMessageable(sess, cue.ProjectID) {
			if err := s.sessions.Send(ctx, sessionID, message, nil); err != nil {
				return "", err
			}
			return sessionID, nil
		}
	}

	spawned, _, _, err := s.sessions.Spawn(ctx, ports.SpawnConfig{
		ProjectID: cue.ProjectID,
		Kind:      domain.KindWorker,
		Prompt:    message,
	})
	if err != nil {
		return "", err
	}
	return spawned.ID, nil
}

// invokeMessage is the agent-facing payload a cue becomes: agent cues are the
// authored prompt verbatim; command cues are instructions to run the command.
func invokeMessage(cue domain.Cue) string {
	if cue.Type == domain.CueTypeCommand {
		return fmt.Sprintf(commandCueInstructionFmt, cue.Command)
	}
	return cue.Prompt
}

// sessionMessageable reports whether a cue may be injected into a session:
// it must exist in the cue's project, not be terminated or exited, and must
// not be blocked on a pending decision (stray input could answer a permission
// dialog on the user's behalf). Everything else — unknown, stale, other
// project — is treated as unusable and falls back to spawning a worker.
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
