package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// persistentChatHostReconcileInterval bounds how long a persistent chat host
// (a detached, re-exec'd `ao chat-host` process kept alive so a session
// survives daemon restarts) can outlive its session before being reaped.
// Reconciliation used to run only once, at daemon boot: a host whose session
// was deleted or archived after boot had nothing to reap it until the next
// full daemon restart, so a long-running daemon leaked one orphaned `ao`
// process per session churned through in between. Fifteen minutes bounds
// that leak without adding meaningful reconcile overhead.
const persistentChatHostReconcileInterval = 15 * time.Minute

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

// persistentChatHostReconcileLoop re-runs reconcilePersistentChatHosts every
// interval for as long as ctx stays alive, so orphaned hosts created after
// boot are still reaped without a daemon restart. Errors are logged and
// never stop the loop: a transient failure to list sessions must not
// abandon reconciliation for the rest of the daemon's uptime. interval is a
// parameter (production wires persistentChatHostReconcileInterval) so tests
// can drive the loop without a real 15-minute wait.
func persistentChatHostReconcileLoop(
	ctx context.Context,
	dataDir string,
	store persistentChatSessionStore,
	log *slog.Logger,
	interval time.Duration,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := reconcilePersistentChatHosts(ctx, dataDir, store); err != nil {
				log.Error("periodic persistent chat host reconciliation failed", "err", err)
			}
		}
	}
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
