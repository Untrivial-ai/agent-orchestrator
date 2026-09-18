package ports

import "context"

// AgentShellProbe captures executable locations visible to the user's login shell.
type AgentShellProbe interface {
	ProbeAgentShell(context.Context, []string) (AgentShellSnapshot, error)
}

// AgentShellSnapshot is one bounded observation of a login shell environment.
type AgentShellSnapshot struct {
	Paths map[string]string
	Path  string
}
