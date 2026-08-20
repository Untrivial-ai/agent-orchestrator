// Package cursoracp binds the user's own Cursor Agent installation to AO's
// reusable ACP Chat transport.
package cursoracp

import (
	"log/slog"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `cursor-agent acp` from the exact binary resolved by the
// existing Cursor adapter. Models and slash commands are populated only from
// the live ACP session advertisements; Cursor owns tools, project rules, auth,
// and permission requests inside its native agent process.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:        domain.HarnessCursor,
		Configure:      configure,
		SessionOptions: sessionOptions,
		VersionProbe:   versionProbe,
	}, log)
}

func configure(acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	return []string{"acp"}, nil, nil
}

func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	if settings.Model == "" {
		return nil
	}
	return []acpdriver.SessionOption{{ID: "model", Value: settings.Model}}
}
