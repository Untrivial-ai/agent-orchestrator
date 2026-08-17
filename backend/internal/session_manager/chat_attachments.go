package sessionmanager

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/attachmentstore"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// StageAttachments durably stores files, projects them into a live session's
// worktree, and returns their worktree-relative paths.
//
// This is the spawn attachment path applied to a conversation that is already
// running: files land in the worktree and the caller names the paths in the
// message it sends, so the agent reads them off disk. It exists as its own step
// because a chat session attaches files repeatedly over its life, while spawn
// does it once for the opening brief.
//
// Names are randomized rather than sequential. Spawn can use attachment-1 /
// attachment-2 because it writes exactly once; a chat session writing the same
// name on its tenth message would overwrite the file it sent on its first,
// silently changing what an earlier message in the visible timeline points at.
func (m *Manager) StageAttachments(
	ctx context.Context,
	id domain.SessionID,
	attachments []ports.SpawnAttachment,
) ([]string, error) {
	if len(attachments) == 0 {
		return nil, nil
	}

	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", id, err)
	}
	if !ok {
		return nil, ports.ErrSessionNotFound
	}
	if rec.Metadata.WorkspacePath == "" {
		// Nothing to write into. Refusing beats writing somewhere the agent cannot
		// reach and then telling the user their image was attached.
		return nil, fmt.Errorf("session %s has no workspace", id)
	}

	refs := make([]string, 0, len(attachments))
	for i, a := range attachments {
		ext := a.Ext
		if ext == "" {
			ext = ".bin"
		}
		var name string
		for attempt := 0; attempt < 8; attempt++ {
			suffix, err := m.attachmentSuffix()
			if err != nil {
				return nil, fmt.Errorf("name attachment %d: %w", i+1, err)
			}
			name = "attachment-" + suffix + ext
			err = m.attachments.Put(ctx, id, rec.Metadata.WorkspacePath, name, a.Data)
			if err == nil {
				break
			}
			if !errors.Is(err, attachmentstore.ErrExists) {
				return nil, fmt.Errorf("write attachment %d: %w", i+1, err)
			}
			name = ""
		}
		if name == "" {
			return nil, fmt.Errorf("write attachment %d: could not allocate a unique name", i+1)
		}
		refs = append(refs, attachmentsDir+"/"+name)
	}

	// Keep the directory out of git status. Best-effort for the same reason spawn
	// treats it that way: the files are already written and usable, and a session
	// the user cannot attach to is worse than a worktree that reads as dirty.
	if err := m.workspace.AddExclude(ctx, workspaceInfo(rec), "/"+attachmentsDir+"/"); err != nil {
		m.logger.Warn("stage attachments: exclude attachments dir", "sessionID", id, "error", err)
	}
	return refs, nil
}

// randomSuffix is a collision-resistant name part used in user-visible paths.
func randomSuffix() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
