package shellterm

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ShellTerminalRecord is one persisted shell terminal row. It carries only what
// is needed to re-attach after daemon/desktop restarts. Only transient command
// terminals expire when their originating app launch ends.
type ShellTerminalRecord struct {
	HandleID   string
	ProjectID  domain.ProjectID
	SessionID  domain.SessionID
	WorkingDir string
	Title      string
	AppRunID   string
	CreatedAt  time.Time
	Transient  bool // Trusted command terminal, owned by its app launch.
}

// Store is the shell terminal service's persistence surface. The SQLite store
// satisfies it; the interface lives next to its only consumer so this service
// does not depend on storage internals.
type Store interface {
	SelectRestorableShellTerminals(ctx context.Context, appRunID string) ([]ShellTerminalRecord, error)
	InsertShellTerminal(ctx context.Context, rec ShellTerminalRecord) error
	UpdateShellTerminalTitle(ctx context.Context, handleID, title string) (ShellTerminalRecord, bool, error)
	SelectShellTerminalByHandleID(ctx context.Context, handleID string) (ShellTerminalRecord, bool, error)
	SelectShellTerminalsByAppRunID(ctx context.Context, appRunID string) ([]ShellTerminalRecord, error)
	SelectShellTerminalsBySessionID(ctx context.Context, sessionID domain.SessionID) ([]ShellTerminalRecord, error)
	SelectShellTerminalsFromPreviousAppRuns(ctx context.Context, appRunID string) ([]ShellTerminalRecord, error)
	DeleteShellTerminalByHandleID(ctx context.Context, handleID string) (bool, error)
	DeleteShellTerminalsFromPreviousAppRuns(ctx context.Context, appRunID string) (int64, error)
}
