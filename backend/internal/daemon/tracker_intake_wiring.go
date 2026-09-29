package daemon

import (
	"context"
	"log/slog"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	trackerintake "github.com/aoagents/agent-orchestrator/backend/internal/observe/trackerintake"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

// startTrackerIntake starts the intake observer when cfg.TrackerIntake is on.
// The observer then runs unconditionally: Poll re-reads each project's config
// every tick, so a project enabling intake after boot is picked up without a
// restart.
func startTrackerIntake(ctx context.Context, cfg config.Config, store *sqlite.Store, sessions *sessionsvc.Service, tracker ports.Tracker, logger *slog.Logger) <-chan struct{} {
	if !cfg.TrackerIntake {
		logGatedOffIntake(ctx, store, logger)
		return nil
	}
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

func logGatedOffIntake(ctx context.Context, store *sqlite.Store, logger *slog.Logger) {
	const hint = "tracker intake: gated off, set AO_TRACKER_INTAKE=on to enable"
	projects, err := store.ListProjects(ctx)
	if err != nil {
		if ctx.Err() != nil {
			// Boot was interrupted; the scan failed because the daemon is
			// shutting down, not because projects are unreadable.
			return
		}
		logger.Warn(hint, "projectScanErr", err)
		return
	}
	stranded := 0
	for _, project := range projects {
		if project.Config.TrackerIntake.Enabled {
			stranded++
		}
	}
	if stranded == 0 {
		logger.Info(hint)
		return
	}
	logger.Warn(hint, "projectsWithIntakeEnabled", stranded)
}
