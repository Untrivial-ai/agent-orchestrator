package daemon

import (
	"context"
	"log/slog"

	trackerintake "github.com/aoagents/agent-orchestrator/backend/internal/observe/trackerintake"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// startTrackerIntake wires the opt-in issue-intake loop. The observer always
// runs — Poll re-reads each project's config on every tick and skips projects
// with intake disabled, so a project enabling intake after daemon boot is
// picked up on the next tick without a restart. The multi-tracker (supporting
// both GitHub and GitLab) is built once in Run and shared between the session
// service and the intake observer.
func startTrackerIntake(ctx context.Context, store *sqlite.Store, sessions *sessionsvc.Service, tracker ports.Tracker, logger *slog.Logger) <-chan struct{} {
	// SingleTrackerResolver with an empty Provider matches any provider,
	// letting the multi-tracker dispatch based on each project's configured
	// provider. A nil tracker (no credentials) causes Resolve to return an
	// error for every project, which triggers backoff — correct behavior.
	resolver := trackerintake.SingleTrackerResolver{
		Adapter: tracker,
	}
	observer := trackerintake.New(resolver, store, sessions, trackerintake.Config{Logger: logger})
	return observer.Start(ctx)
}
