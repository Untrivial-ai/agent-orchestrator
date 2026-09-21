// Package agentauth owns the fixed, daemon-trusted authentication plans for
// supported harnesses. Clients select only an agent ID; they never provide
// commands or credentials.
package agentauth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	// initialInput and initialInputReadyStates, when set, make the daemon inject
	// the reviewed input automatically once the terminal renders a known ready
	// state, instead of waiting for the user to trigger terminalInput.
	initialInput            string
	initialInputReadyStates []shellterm.InitialInputReadyState
	// prepareWorkspace, when set, runs reviewed harness-specific setup against
	// the plan's stable auth workspace before the terminal launches (for
	// example pre-recording workspace trust so a first-run dialog cannot
	// swallow the login flow).
	prepareWorkspace func(context.Context, string) error
	launcher         string
	launcherArgs     []string
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
// dataDir roots the stable per-harness auth workspaces used by plans with a
// prepareWorkspace hook.
type Service struct {
	executables    ExecutableFinder
	agents         AgentBinaryResolver
	terminals      TerminalOpener
	dataDir        string
	selfExecutable func() (string, error)
}

// New creates an authentication-plan service.
func New(executables ExecutableFinder, terminals TerminalOpener) *Service {
	return NewWithAgentResolver(executables, nil, terminals, "")
}

// NewWithAgentResolver creates a service that uses AO's adapter-aware binary
// resolver as the authoritative validation and discovery boundary.
func NewWithAgentResolver(executables ExecutableFinder, agents AgentBinaryResolver, terminals TerminalOpener, dataDir string) *Service {
	return &Service{executables: executables, agents: agents, terminals: terminals, dataDir: dataDir, selfExecutable: os.Executable}
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
// resolved private plan fields. Interactive slash commands are either returned
// as a fixed, reviewed action that the user explicitly triggers after the TUI
// starts, or — for plans with initialInput — injected by the daemon
// automatically once the terminal renders a reviewed ready state.
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
	argv := plan.command
	if plan.launcher != "" {
		self, err := s.selfExecutable()
		if err != nil || strings.TrimSpace(self) == "" {
			return StartResult{}, apierr.Internal("AGENT_AUTH_TERMINAL_UNAVAILABLE", "Authentication login menu is unavailable.")
		}
		argv = append([]string{self, plan.launcher, "--executable", plan.command[0]}, plan.launcherArgs...)
	}
	input := shellterm.OpenCommandTerminalInput{
		Argv:                    argv,
		Title:                   plan.title,
		InitialInput:            plan.initialInput,
		InitialInputReadyStates: plan.initialInputReadyStates,
	}
	if plan.prepareWorkspace != nil {
		workingDir, err := s.prepareAuthWorkspace(ctx, plan)
		if err != nil {
			return StartResult{}, err
		}
		input.WorkingDir = workingDir
	}
	terminal, err := s.terminals.OpenCommandTerminal(ctx, input)
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

// authWorkspaceRootName mirrors shellterm's auth-workspace root so all
// daemon-owned authentication terminals live under one directory; plans with a
// prepareWorkspace hook get a stable per-harness subdirectory instead of a
// throwaway per-handle one, so harness state the hook seeds (for example
// Kimi's workspace-trust record) persists across login attempts.
const authWorkspaceRootName = "auth-workspace"

func (s *Service) prepareAuthWorkspace(ctx context.Context, plan Plan) (string, error) {
	if strings.TrimSpace(s.dataDir) == "" {
		return "", apierr.Internal("AGENT_AUTH_WORKSPACE_UNAVAILABLE", "Authentication workspace is unavailable.")
	}
	dir := filepath.Join(s.dataDir, authWorkspaceRootName, plan.AgentID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create authentication workspace: %w", err)
	}
	if err := plan.prepareWorkspace(ctx, dir); err != nil {
		return "", fmt.Errorf("prepare %s authentication workspace: %w", plan.AgentID, err)
	}
	return dir, nil
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
