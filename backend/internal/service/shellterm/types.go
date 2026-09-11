// Package shellterm owns standalone shell terminals: PTYs the user opens by
// hand from the desktop app, deliberately NOT bound to any agent session.
//
// Why this is its own package rather than a mode of the session service: a
// shell terminal has no agent, no worktree, no lifecycle state machine, and no
// place on the board. It shares exactly one mechanism with sessions — the
// runtime adapter that knows how to spawn and attach a PTY — and nothing else.
// Keeping it separate is what stops "open a terminal" from having to satisfy
// the session lifecycle's invariants.
//
// It needs no changes to internal/terminal: that package already treats the
// terminal id it is handed as an opaque runtime handle and never resolves it
// against a session, so a shell terminal's handle streams over the existing mux
// unmodified.
package shellterm

import (
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// ShellTerminal is one standalone shell pane. HandleID is the runtime handle
// the terminal mux attaches to — the same opaque id an agent session's pane
// uses, drawn from a separate namespace (see newShellTerminalHandleID).
type ShellTerminal struct {
	HandleID   string           `json:"handleId"`
	ProjectID  domain.ProjectID `json:"projectId,omitempty"`
	SessionID  domain.SessionID `json:"sessionId,omitempty"`
	WorkingDir string           `json:"workingDir"`
	Title      string           `json:"title"`
	CreatedAt  time.Time        `json:"createdAt"`
}

// OpenShellTerminalInput is the request to open a new shell pane. An empty
// ProjectID opens the shell in the daemon's data dir instead of a project root,
// which is what the topbar action does when no project is selected. SessionID
// scopes the shell to an agent session so it appears only in that session's tab
// strip; an empty SessionID makes it a standalone shell on the /terminals screen.
type OpenShellTerminalInput struct {
	ProjectID domain.ProjectID `json:"projectId,omitempty"`
	SessionID domain.SessionID `json:"sessionId,omitempty"`
	Shell     string           `json:"shell,omitempty"`
}

// InitialInputReadyState describes a terminal state that is ready to receive
// the command's initial input.
type InitialInputReadyState struct {
	Text      string
	RawPrefix string
}

// OpenCommandTerminalInput is a daemon-trusted command terminal request. It
// is intentionally separate from OpenShellTerminalInput: public callers may
// open only the user's login shell, while backend callers provide a reviewed
// command. InitialInput and InitialInputReadyStates are private backend-only
// values from the reviewed auth registry; the input is sent only after the
// harness renders one of its known editor-ready states.
type OpenCommandTerminalInput struct {
	Argv                    []string
	Env                     map[string]string
	WorkingDir              string
	Title                   string
	InitialInput            string
	InitialInputReadyStates []InitialInputReadyState
}
