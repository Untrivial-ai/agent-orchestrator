//go:build !race

package accountsmanager

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// CLIProxyAPI currently reports a race inside its own server start/stop
// lifecycle when linked into a race-enabled test binary. Keep this boundary
// smoke test in the normal suite; route selection and state are race-tested in
// the package's remaining tests.
func TestServiceStartsLoopbackProxyAndFailsClosedWithoutAccounts(t *testing.T) {
	service, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := service.Start(ctx); err != nil {
		_ = service.Close(context.Background())
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = service.Close(context.Background()) }()
	_, err = service.RouteForSession(ctx, "session-without-account")
	if !errors.Is(err, ports.ErrCodexProxyNoAccounts) {
		t.Fatalf("RouteForSession error = %v, want ErrCodexProxyNoAccounts", err)
	}
}
