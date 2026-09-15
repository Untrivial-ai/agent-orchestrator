package session

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// StageAttachments writes files into a session's worktree and returns their
// worktree-relative paths, for a caller that will name those paths in the message
// it is about to send.
//
// It is a separate step from sending rather than a field on the message because
// the transport is a path reference, not the bytes: the conversation port carries
// text, and a file reaches the agent by existing in the worktree the agent can
// read. Staging first also means a failed write is reported before a message
// claiming to have attachments is recorded in the timeline.
func (s *Service) StageAttachments(
	ctx context.Context,
	id domain.SessionID,
	attachments []ports.SpawnAttachment,
) ([]string, error) {
	return s.manager.StageAttachments(ctx, id, attachments)
}

// ReleaseAttachments discards staged draft attachments (worktree-relative refs,
// as returned by StageAttachments) that were never sent — an explicit chip
// removal, or a draft being thrown away outright.
//
// A ref whose message was already accepted is left alone: only an uncommitted
// lease is deletable, so this can never remove an attachment that made it into
// conversation history, even if it races the send that committed it.
func (s *Service) ReleaseAttachments(
	ctx context.Context,
	id domain.SessionID,
	refs []string,
) error {
	return s.manager.ReleaseAttachments(ctx, id, refs)
}
