// Package clineacp binds the user's own Cline CLI installation to AO's reusable
// ACP Chat transport.
//
// Cline exposes ACP natively via `cline --acp`; AO launches the exact binary
// resolved by the existing Cline agent plugin, so sign-in, models, providers,
// organization billing, and settings remain the user's own.
package clineacp

import (
	"context"
	"log/slog"
	"strings"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// New launches `cline --acp` from the exact binary resolved by the existing
// Cline agent plugin. Model and provider catalogs, Plan/Act modes, and the
// per-session auto-approve toggle come from the live ACP session
// advertisements. Cline owns tools and credentials; AO resolves permissions
// through ACP.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:              domain.HarnessCline,
		Configure:            configure,
		PermissionPolicy:     acpdriver.StandardPermissionPolicy(),
		SessionOptions:       acpdriver.ModelOption,
		ValidateTurnSettings: acpdriver.ApprovalFixedAtLaunch("Cline ACP auto-approval", nil),
	}, log)
}

// configure builds the `cline --acp` argv. Cline's ACP entrypoint reads its
// launch defaults from CLINE_MODEL/CLINE_PROVIDER rather than the `--model`
// flag, which is ignored once `--acp` is set, so the model is delivered as an
// environment override. Only an explicit auto/bypass choice enables
// `--auto-approve true`; Cline deliberately refuses to auto-approve ACP
// sessions otherwise.
//
// Standing instructions are not injectable in ACP mode: Cline ignores `--system`
// there and reads its rules from the workspace, so AO's role prompt is not
// forwarded. This is documented in docs/harnesses/acp-bindings.md.
func configure(_ context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	args := []string{"--acp"}
	switch ports.NormalizePermissionMode(cfg.Permissions) {
	case ports.PermissionModeAuto, ports.PermissionModeBypassPermissions:
		args = append(args, "--auto-approve", "true")
	}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		return args, map[string]string{"CLINE_MODEL": model}, nil
	}
	return args, nil, nil
}
