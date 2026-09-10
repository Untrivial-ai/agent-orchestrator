// Package agentauth owns the fixed, daemon-trusted authentication plans for
// supported harnesses. Clients select only an agent ID; they never provide
// commands or credentials.
package agentauth

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

// ExecutableFinder resolves an executable on the host PATH.
type ExecutableFinder interface {
	LookPath(string) (string, error)
}

// AgentBinaryResolver resolves an agent through the same adapter-aware search
// used by normal session startup, including managed install locations outside
// the daemon PATH.
type AgentBinaryResolver interface {
	ResolveAgentBinary(context.Context, string) (string, error)
}

type executableFinderFunc func(string) (string, error)

func (f executableFinderFunc) LookPath(name string) (string, error) { return f(name) }

// Action describes the native authentication action a plan offers.
type Action string

const (
	// ActionLogin opens an agent's native login flow.
	ActionLogin Action = "login"
	// ActionSetup opens an agent's native provider/setup flow.
	ActionSetup Action = "setup"
	// ActionInstructions points the user to agent-owned setup documentation.
	ActionInstructions Action = "instructions"
)

// LaunchMode describes how the user enters an agent-owned authentication or
// provider setup flow.
type LaunchMode string

const (
	// LaunchTerminal opens a daemon-owned terminal running a reviewed command.
	LaunchTerminal LaunchMode = "terminal"
	// LaunchDocumentation opens the agent's official setup documentation.
	LaunchDocumentation LaunchMode = "documentation"
)

// Plan is the display-safe authentication plan for one harness. Trusted
// command and terminal details remain private to this package.
type Plan struct {
	AgentID          string     `json:"agentId"`
	Action           Action     `json:"action"`
	LaunchMode       LaunchMode `json:"launchMode" enum:"terminal,documentation"`
	Available        bool       `json:"available"`
	DisplayCommand   string     `json:"displayCommand,omitempty"`
	Guidance         string     `json:"guidance,omitempty"`
	DocumentationURL string     `json:"documentationUrl"`
	Reason           string     `json:"reason,omitempty"`
	command          []string
	title            string
	terminalInput    string
}

// TerminalOpener opens the daemon-trusted terminal used for a native
// authentication command.
type TerminalOpener interface {
	OpenCommandTerminal(context.Context, shellterm.OpenCommandTerminalInput) (shellterm.ShellTerminal, error)
}

// StartResult is the display-safe result of starting a native authentication
// flow. Command arguments remain private to the resolved plan.
type StartResult struct {
	AgentID       string                  `json:"agentId"`
	Action        Action                  `json:"action"`
	Guidance      string                  `json:"guidance,omitempty"`
	TerminalInput string                  `json:"terminalInput,omitempty"`
	Terminal      shellterm.ShellTerminal `json:"terminal"`
}

// Service resolves the fixed authentication registry through AO's registered
// harness adapters, with direct PATH lookup only for callers without one.
type Service struct {
	executables ExecutableFinder
	agents      AgentBinaryResolver
	terminals   TerminalOpener
}

// New creates an authentication-plan service.
func New(executables ExecutableFinder, terminals TerminalOpener) *Service {
	return NewWithAgentResolver(executables, nil, terminals)
}

// NewWithAgentResolver creates a service that uses AO's adapter-aware binary
// resolver as the authoritative validation and discovery boundary.
func NewWithAgentResolver(executables ExecutableFinder, agents AgentBinaryResolver, terminals TerminalOpener) *Service {
	return &Service{executables: executables, agents: agents, terminals: terminals}
}

// Plans returns every known harness plan in stable Harness settings order.
func (s *Service) Plans(ctx context.Context) []Plan {
	out := make([]Plan, 0, len(plans))
	for _, plan := range plans {
		out = append(out, s.resolve(ctx, plan))
	}
	return out
}

// Plan returns the resolved plan for agentID.
func (s *Service) Plan(ctx context.Context, agentID string) (Plan, error) {
	plan, ok := planByAgentID[agentID]
	if !ok {
		return Plan{}, apierr.Invalid("AGENT_AUTH_TARGET_UNKNOWN", fmt.Sprintf("unknown agent authentication target %q", agentID), nil)
	}
	return s.resolve(ctx, plan), nil
}

// Start opens the reviewed native authentication flow for agentID. Callers
// choose only the registry key; command arguments come exclusively from the
// resolved private plan fields. Interactive slash commands are returned as a
// fixed, reviewed action that the user explicitly triggers after the TUI starts.
func (s *Service) Start(ctx context.Context, agentID string) (StartResult, error) {
	plan, ok := planByAgentID[agentID]
	if !ok {
		return StartResult{}, apierr.Invalid("AGENT_AUTH_TARGET_UNKNOWN", fmt.Sprintf("unknown agent authentication target %q", agentID), nil)
	}
	plan = s.resolve(ctx, plan)
	if !plan.Available {
		return StartResult{}, apierr.Invalid("AGENT_AUTH_UNAVAILABLE", plan.Reason, nil)
	}
	if plan.Action == ActionInstructions {
		return StartResult{}, apierr.Invalid("AGENT_AUTH_INSTRUCTIONS_ONLY", "This authentication target provides instructions only.", nil)
	}
	if plan.LaunchMode == LaunchDocumentation {
		return StartResult{}, apierr.Invalid("AGENT_AUTH_DOCUMENTATION_ONLY", "This setup target opens the agent's documentation instead of a terminal.", nil)
	}
	if s.terminals == nil {
		return StartResult{}, apierr.Internal("AGENT_AUTH_TERMINAL_UNAVAILABLE", "Authentication terminal service is unavailable.")
	}
	terminal, err := s.terminals.OpenCommandTerminal(ctx, shellterm.OpenCommandTerminalInput{
		Argv:  plan.command,
		Title: plan.title,
	})
	if err != nil {
		return StartResult{}, err
	}
	return StartResult{
		AgentID:       plan.AgentID,
		Action:        plan.Action,
		Guidance:      plan.Guidance,
		TerminalInput: plan.terminalInput,
		Terminal:      terminal,
	}, nil
}

func (s *Service) resolve(ctx context.Context, plan Plan) Plan {
	plan.command = append([]string(nil), plan.command...)
	if len(plan.command) == 0 {
		plan.Available = true
		return plan
	}
	var path string
	var err error
	if s.agents != nil {
		path, err = s.agents.ResolveAgentBinary(ctx, plan.AgentID)
	} else if s.executables != nil {
		path, err = s.executables.LookPath(plan.command[0])
	}
	if err != nil || path == "" {
		plan.Reason = fmt.Sprintf("%s was not found on PATH.", plan.command[0])
		return plan
	}
	plan.command[0] = path
	plan.Available = true
	return plan
}
