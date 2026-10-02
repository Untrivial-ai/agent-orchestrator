// Package commandcodeacp binds the user's own Command Code installation to AO's
// reusable ACP Chat transport.
//
// Command Code ships a first-party ACP agent (`cmd acp`, "Run as an Agent Client
// Protocol (ACP) agent over stdio"), verified against v1.74.0 on 2026-10-02:
//
//	protocolVersion 1
//	loadSession      true            -> ChatCapabilityResume
//	promptCapabilities{image, embeddedContext}
//	mcpCapabilities{http}
//	sessionCapabilities{list, resume, close}
//
// Its session/new advertises five modes and two config options, which is more
// than AO needs to map: every AO permission mode has an exact Command Code
// equivalent, and `model` plus `effort` arrive as live provider-owned config
// options rather than launch flags.
//
// Like every native ACP binding, AO launches the exact binary the existing
// Command Code agent plugin resolves. AO never downloads, packages, or
// substitutes the CLI, and registration here adds no second answer to "is
// Command Code installed and logged in".
package commandcodeacp

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	acpsdk "github.com/coder/acp-go-sdk"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/nativeacp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Command Code advertises these mode ids from session/new. They were read off a
// live v1.74.0 handshake, not taken from documentation, so an unexpected id
// means the provider changed its vocabulary rather than that AO guessed wrong.
const (
	modeDefault   = "default"
	modeAutoAccpt = "auto-accept"
	modePlan      = "plan"
	modeDontAsk   = "dont-ask"
	modeBypass    = "bypass"
)

// New launches `cmd acp` from the exact binary resolved by the existing
// Command Code agent plugin.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness: domain.HarnessCommandCode,
		Capabilities: ports.ChatCapabilities{
			ports.ChatCapabilityUsage:           true,
			ports.ChatCapabilityDiffs:           true,
			ports.ChatCapabilityPlans:           true,
			ports.ChatCapabilityModels:          true,
			ports.ChatCapabilityConfigOptions:   true,
			ports.ChatCapabilityCompaction:      true,
			ports.ChatCapabilityRename:          true,
			ports.ChatCapabilitySkills:          true,
			ports.ChatCapabilityImages:          true,
			ports.ChatCapabilityEmbeddedContext: true,
			ports.ChatCapabilityRateLimits:      true,
		},
		Configure:            configure,
		SessionMode:          sessionMode,
		SessionOptions:       sessionOptions,
		PermissionPolicy:     permissionPolicy,
		ValidateTurnSettings: validateTurnSettings,
	}, log)
}

// configure builds `cmd acp [flags]`.
//
// The model and permission mode are ALSO passed as launch flags so the initial
// posture survives an agent that rejects the runtime setters with -32601; see
// the same reasoning in kimchiacp. `cmd acp` accepts the same global flags as
// the TUI, so this is one vocabulary, not two.
func configure(_ context.Context, cfg acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	args := []string{
		"acp",
		"--skip-onboarding",
		"--no-auto-update",
		"--trust",
	}
	if model := strings.TrimSpace(cfg.Model); model != "" {
		args = append(args, "--model", model)
	}
	if mode := permissionMode(cfg.Permissions); mode != "" {
		args = append(args, "--permission-mode", mode)
	}
	return args, nil, nil
}

// sessionMode maps AO's approval vocabulary onto Command Code's advertised mode
// ids. Every AO mode has an exact equivalent, so this never returns "" for a
// mode Command Code actually understands.
func sessionMode(mode ports.PermissionMode) string { return permissionMode(mode) }

func permissionMode(mode ports.PermissionMode) string {
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeAcceptEdits, ports.PermissionModeAuto:
		return modeAutoAccpt
	case ports.PermissionModeBypassPermissions:
		return modeBypass
	default:
		return modeDefault
	}
}

// sessionOptions maps AO's per-turn settings onto Command Code's live config
// option ids. Command Code publishes `model` and `effort` (category
// thought_level) from session/new, so both apply mid-conversation without a
// restart.
func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	var options []acpdriver.SessionOption
	if model := strings.TrimSpace(settings.Model); model != "" {
		options = append(options, acpdriver.SessionOption{ID: "model", Value: model})
	}
	if effort := strings.TrimSpace(settings.Effort); effort != "" {
		options = append(options, acpdriver.SessionOption{ID: "effort", Value: effort})
	}
	return options
}

// permissionPolicy leaves every request to the generic human-approval flow.
// Command Code advertises distinct modes rather than resolving approvals per
// request, so auto-approving here would contradict the mode the user selected.
func permissionPolicy(ports.PermissionMode, acpsdk.RequestPermissionRequest) (acpsdk.PermissionOptionId, bool) {
	return "", false
}

// validateTurnSettings rejects an approval change the provider cannot apply.
// Command Code does implement session/set_mode, so this should stay silent; it
// exists so a future regression surfaces as "restart Chat" instead of a session
// silently running at the wrong permission posture.
func validateTurnSettings(initial ports.PermissionMode, settings ports.ChatTurnSettings) error {
	if settings.Approval == "" {
		return nil
	}
	if permissionMode(initial) == permissionMode(settings.Approval) {
		return nil
	}
	return fmt.Errorf("%w: Command Code applies approval changes through session/set_mode; "+
		"restart Chat if this change does not take effect",
		acpdriver.ErrACPSetterUnsupported)
}
