// Package ompacp binds the user's own OMP installation to AO's reusable ACP
// Chat transport.
package ompacp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/omp"
	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `omp acp` from the exact binary resolved by the existing OMP
// agent plugin. OMP keeps filesystem and terminal execution in its own process;
// AO advertises neither ACP client capability and receives only typed session
// updates and permission requests.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return newDriver(plugin, versionProbe, log)
}

func newDriver(plugin nativeacp.Plugin, probe nativeacp.VersionProbe, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness: domain.HarnessOMP,
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityUsage: true,
			ports.ChatCapabilityDiffs: true,
			ports.ChatCapabilityPlans: true,
		},
		Configure:            configure,
		SessionOptions:       sessionOptions,
		ValidateTurnSettings: validateTurnSettings,
		VersionProbe:         probe,
	}, log)
}

func configure(_ context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	args := []string{"acp"}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		args = append(args, "--model", model)
	}
	if prompt := strings.TrimSpace(cfg.SystemPrompt); prompt != "" {
		args = append(args, "--append-system-prompt", prompt)
	}
	omp.AppendApprovalModeFlag(&args, cfg.Permissions)
	return args, nil, nil
}

func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	if model := strings.TrimSpace(settings.Model); model != "" {
		return []acpdriver.SessionOption{{ID: "model", Value: model}}
	}
	return nil
}

func validateTurnSettings(initial ports.PermissionMode, settings ports.ChatTurnSettings) error {
	if settings.Approval == "" || omp.ApprovalMode(settings.Approval) == omp.ApprovalMode(initial) {
		return nil
	}
	return fmt.Errorf("%w: OMP ACP approval mode is fixed at process launch (%s); restart Chat to change it to %s",
		acpdriver.ErrACPSetterUnsupported, omp.ApprovalMode(initial), omp.ApprovalMode(settings.Approval))
}
