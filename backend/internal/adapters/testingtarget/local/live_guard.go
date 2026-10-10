package local

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

type liveDaemonGuard struct {
	path    string
	info    *runfile.Info
	started time.Time
}

func (a *Adapter) observeLiveDaemon() (*liveDaemonGuard, error) {
	home, err := a.ops.home()
	if err != nil {
		return nil, err
	}
	g := &liveDaemonGuard{path: filepath.Join(home, ".ao", "running.json")}
	g.info, err = runfile.Read(g.path)
	if err != nil {
		return nil, fmt.Errorf("live AO daemon guard: %w", err)
	}
	if g.info != nil {
		if g.info.PID <= 0 || g.info.StartedAt.IsZero() {
			return nil, errors.New("live AO daemon guard: invalid recorded identity")
		}
		g.started, err = a.ops.startTime(g.info.PID)
		if err != nil {
			return nil, fmt.Errorf("live AO daemon guard: %w", err)
		}
	}
	return g, nil
}

func (a *Adapter) checkLiveDaemon(g *liveDaemonGuard) error {
	if g == nil {
		return nil
	}
	info, err := runfile.Read(g.path)
	if err != nil {
		return fmt.Errorf("live AO daemon guard: %w", err)
	}
	if g.info == nil && info == nil {
		return nil
	}
	if g.info == nil || info == nil || info.PID != g.info.PID || info.Port != g.info.Port || !info.StartedAt.Equal(g.info.StartedAt) {
		return errors.New("live AO daemon guard: recorded identity changed; stopping target")
	}
	started, err := a.ops.startTime(info.PID)
	if err != nil || !started.Equal(g.started) {
		return errors.Join(errors.New("live AO daemon guard: kernel process identity changed; stopping target"), err)
	}
	return nil
}
