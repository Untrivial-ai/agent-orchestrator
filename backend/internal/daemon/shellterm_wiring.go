package daemon

import (
	"context"
	"log/slog"
	"os"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
	shelltermsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// startShellTerminals restores durable user shells and cleans up expired command
// terminals before accepting clients. A different app launch alone never ends
// a user shell's lifetime.
func startShellTerminals(
	ctx context.Context,
	cfg config.Config,
	runtime shelltermsvc.ShellRuntime,
	store *sqlite.Store,
	projects projectsvc.Manager,
	sessions *sessionsvc.Service,
	log *slog.Logger,
) *shelltermsvc.Service {
	svc := shelltermsvc.NewService(
		runtime,
		store,
		&projectRootLocator{projects: projects},
		&sessionWorkspaceLocator{sessions: sessions},
		cfg.DataDir,
		cfg.AppRunID,
		log,
	)
	// Best-effort: a failed sweep must never block boot. The rows survive and
	// the next boot retries.
	if _, err := svc.ReapShellTerminalsFromPreviousAppRuns(ctx); err != nil {
		log.Warn("reaping shell terminals from previous app runs failed", "err", err)
	}
	return svc
}

// projectRootLocator adapts the project service to the narrow lookup the shell
// terminal service needs: an id in, a directory out.
type projectRootLocator struct {
	projects projectsvc.Manager
}

// ProjectRoot returns the project's path, or "" when no such project exists so
// the caller can answer 404. A degraded project (its config failed to load)
// still has a usable path on disk, and a shell in it is exactly the tool a user
// would want to fix it with — so degraded is resolved, not rejected.
func (l *projectRootLocator) ProjectRoot(ctx context.Context, id domain.ProjectID) (string, error) {
	if l.projects == nil {
		return "", nil
	}
	res, err := l.projects.Get(ctx, id)
	if err != nil {
		return "", err
	}
	switch {
	case res.Project != nil:
		return res.Project.Path, nil
	case res.Degraded != nil:
		return res.Degraded.Path, nil
	default:
		return "", nil
	}
}

// sessionGetter is the narrow slice of the session service the workspace
// locator needs. *sessionsvc.Service satisfies it; tests substitute a fake so
// this adapter's validation logic doesn't need a real session stack.
type sessionGetter interface {
	Get(ctx context.Context, id domain.SessionID) (domain.Session, error)
}

// sessionWorkspaceLocator adapts the session service to the narrow lookup the
// shell terminal service needs: a session id in, its live workspace path and
// owning project id out.
type sessionWorkspaceLocator struct {
	sessions sessionGetter
}

// SessionWorkspace returns the session's current workspace path (its
// worktree) and project id. An unknown session propagates the session
// service's own NotFound apierr unchanged, so the shell terminal open request
// answers the same 404 shape an unknown project does.
//
// Kill and Cleanup can remove a session's worktree without clearing its
// durable Metadata.WorkspacePath — that field doubles as "what to recreate on
// restore", so it deliberately survives a clean teardown and only a dirty,
// preserved worktree keeps a directory that still exists. A recorded path
// that no longer exists on disk is therefore treated as no workspace, so the
// caller falls back to the project root instead of trying to chdir into a
// directory that is gone.
func (l *sessionWorkspaceLocator) SessionWorkspace(ctx context.Context, id domain.SessionID) (string, domain.ProjectID, error) {
	if l.sessions == nil {
		return "", "", nil
	}
	sess, err := l.sessions.Get(ctx, id)
	if err != nil {
		return "", "", err
	}
	path := sess.Metadata.WorkspacePath
	if path != "" {
		if _, statErr := os.Stat(path); statErr != nil {
			path = ""
		}
	}
	return path, sess.ProjectID, nil
}
