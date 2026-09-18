package daemon

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type persistentChatSessionStore interface {
	ListAllSessions(context.Context) ([]domain.SessionRecord, error)
}

// reconcilePersistentChatHosts removes hosts only when durable state proves
// there is no live Chat session to adopt. An unreadable session set is not
// evidence that any host is orphaned.
func reconcilePersistentChatHosts(ctx context.Context, dataDir string, store persistentChatSessionStore) error {
	records, err := store.ListAllSessions(ctx)
	if err != nil {
		return fmt.Errorf("list sessions for persistent chat hosts: %w", err)
	}
	return persistenthost.Reconcile(ctx, dataDir, persistentChatHostKeepSet(records))
}

func persistentChatHostKeepSet(records []domain.SessionRecord) map[string]struct{} {
	keep := make(map[string]struct{})
	for _, rec := range records {
		if rec.IsTerminated || domain.NormalizeSessionMode(rec.Mode) != domain.SessionModeChat {
			continue
		}
		keep[string(rec.ID)] = struct{}{}
	}
	return keep
}
