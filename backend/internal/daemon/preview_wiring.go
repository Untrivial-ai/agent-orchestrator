package daemon

import (
	"context"
	"log/slog"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
)

// previewExitSessions is the slice of the session service the managed preview
// exit callback needs: read the current preview URL and clear it.
type previewExitSessions interface {
	Get(ctx context.Context, id domain.SessionID) (domain.Session, error)
	SetPreview(ctx context.Context, id domain.SessionID, previewURL string) (domain.Session, error)
}

// wireManagedPreviewExit registers the OnExit callback that clears a session's
// preview URL when its managed preview server crashes after launch.
func wireManagedPreviewExit(managedPreview *previewserver.Manager, sessions previewExitSessions, log *slog.Logger) {
	managedPreview.SetOnExit(managedPreviewExitFunc(sessions, log))
}

// managedPreviewExitFunc returns the OnExit callback. It fires from the
// preview manager's wait goroutine when a managed server exits without a
// user-initiated stop. It clears the session's preview URL only when the
// current preview URL still points at the failed server, so an unrelated
// static-file preview or an already-restarted server is not clobbered. The
// session update fans out to the renderer via the existing CDC
// session_updated event (issue #4500).
func managedPreviewExitFunc(sessions previewExitSessions, log *slog.Logger) previewserver.OnExitFunc {
	return func(ctx context.Context, id domain.SessionID, status previewserver.Status) {
		sess, err := sessions.Get(ctx, id)
		if err != nil {
			log.Warn("fetch session to clear preview URL after managed preview crash", "session", id, "err", err)
			return
		}
		if status.URL == "" || sess.Metadata.PreviewURL != status.URL {
			return
		}
		if _, err := sessions.SetPreview(ctx, id, ""); err != nil {
			log.Warn("clear preview URL after managed preview crash", "session", id, "err", err)
		}
	}
}
