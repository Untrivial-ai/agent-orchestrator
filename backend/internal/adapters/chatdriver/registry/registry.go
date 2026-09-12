// Package registry resolves the Chat driver for a harness.
//
// Registration is the whole capability gate: a harness with no driver here cannot
// run in chat mode, and asking for one fails with ErrChatUnsupported rather than
// quietly producing a TUI session. Adding a harness to chat means adding it here,
// which keeps "which agents support chat" answerable by reading one file instead
// of inferring it from behavior.
package registry

import (
	"log/slog"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/claudecode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/codex"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/cursor"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/droid"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/kimchi"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/kimi"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/omp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/opencode"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/pi"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/claudeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/cursoracp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/droidacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/kimchiacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/kimiacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/ompacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/opencodeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/piacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Registry maps a harness to its Chat driver.
type Registry struct {
	drivers map[domain.AgentHarness]ports.ChatDriver
}

var _ ports.ChatDriverRegistry = (*Registry)(nil)

// New builds a registry from an explicit driver list. Tests use it to register a
// fake without touching the shipped set.
func New(drivers ...ports.ChatDriver) *Registry {
	byHarness := make(map[domain.AgentHarness]ports.ChatDriver, len(drivers))
	for _, driver := range drivers {
		if driver == nil {
			continue
		}
		byHarness[driver.Harness()] = driver
	}
	return &Registry{drivers: byHarness}
}

// Build returns the drivers the daemon ships.
//
// Codex uses its native app-server protocol. Claude Code uses AO's reusable ACP
// transport plus claude-agent-acp, pointed at the user's own Claude executable.
// Cursor, OpenCode, Droid, Kimi, Kimchi, Pi, and OMP expose ACP themselves, so AO
// launches the exact executable resolved by each existing agent plugin. No path
// scrapes terminal output or packages a second provider CLI.
//
// Every other harness stays TUI-only until the same is true of it. The driver
// reuses the harness's existing agent plugin for binary resolution and auth, so
// registration adds no second answer to "is this agent installed and logged in".
func Build(log *slog.Logger) *Registry {
	return New(
		codexappserver.New(codex.New(), log),
		claudeacp.New(claudecode.New(), log),
		opencodeacp.New(opencode.New(), log),
		droidacp.New(droid.New(), log),
		kimiacp.New(kimi.New(), log),
		kimchiacp.New(kimchi.New(), log),
		piacp.New(pi.New(), log),
		cursoracp.New(cursor.New(), log),
		ompacp.New(omp.New(), log),
	)
}

// Driver returns the driver for a harness.
func (r *Registry) Driver(harness domain.AgentHarness) (ports.ChatDriver, error) {
	driver, ok := r.drivers[harness]
	if !ok {
		return nil, ports.ErrChatUnsupported
	}
	return driver, nil
}

// SupportsChat reports whether a harness has a driver at all, without probing the
// local install. Use it to decide whether Chat is even offerable; use Driver plus
// Probe to decide whether it will work right now.
func (r *Registry) SupportsChat(harness domain.AgentHarness) bool {
	_, ok := r.drivers[harness]
	return ok
}

// Harnesses lists the harnesses with a registered driver, for diagnostics and for
// telling a user which agents can run in chat mode.
func (r *Registry) Harnesses() []domain.AgentHarness {
	out := make([]domain.AgentHarness, 0, len(r.drivers))
	for harness := range r.drivers {
		out = append(out, harness)
	}
	return out
}
