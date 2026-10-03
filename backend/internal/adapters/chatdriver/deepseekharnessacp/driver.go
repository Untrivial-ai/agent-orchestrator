// Package deepseekacp binds the user's own DeepSeek Harness installation to
// AO's reusable ACP Chat transport.
package deepseekharnessacp

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

// acpProfile is the shipped DeepSeek Harness profile that serves the
// automation-only ACP v1 stdio surface.
const acpProfile = "acp"

// New launches `dsh --profile acp` from the exact binary resolved by the
// DeepSeek Harness agent plugin. The profile supplies the model route and
// credentials; AO adds no environment overlay of its own.
func New(plugin nativeacp.Plugin, log *slog.Logger) ports.ChatDriver {
	return nativeacp.New(plugin, nativeacp.Config{
		Harness:              domain.HarnessDeepSeek,
		Configure:            configure,
		SessionOptions:       sessionOptions,
		PermissionPolicy:     permissionPolicy,
		ValidateTurnSettings: validateTurnSettings,
	}, log)
}

func configure(_ context.Context, _ acpdriver.LaunchConfig) ([]string, map[string]string, error) {
	return []string{"--profile", acpProfile}, nil, nil
}

// deepseekEfforts maps AO's provider-neutral reasoning-effort vocabulary onto
// the levels DeepSeek Harness advertises (off, low, high, max). Harness has no
// middle level, so AO's "medium" takes Harness's own default balance. Values not
// listed here pass through unchanged: a model that grows a level keeps working,
// and an unrecognized value is rejected by the agent rather than silently
// replaced.
var deepseekEfforts = map[string]string{
	"none":    "off",
	"minimal": "off",
	"off":     "off",
	"low":     "low",
	"medium":  "high",
	"high":    "high",
	"xhigh":   "max",
	"max":     "max",
}

// sessionOptions maps AO's per-turn choices onto DeepSeek Harness's ACP config
// options. Model values are opaque: Harness encodes each choice as a JSON array
// string of the form ["deepseek-official","deepseek-v4-flash"], and the value AO
// received from the session catalog is the value it sends back.
func sessionOptions(settings ports.ChatTurnSettings) []acpdriver.SessionOption {
	options := make([]acpdriver.SessionOption, 0, 2)
	if model := strings.TrimSpace(settings.Model); model != "" {
		options = append(options, acpdriver.SessionOption{ID: "model", Value: model})
	}
	if effort := strings.TrimSpace(settings.Effort); effort != "" {
		options = append(options, acpdriver.SessionOption{ID: "reasoning_effort", Value: deepseekEffort(effort)})
	}
	if len(options) == 0 {
		return nil
	}
	return options
}

// deepseekEffort resolves one AO effort value to a Harness level, passing an
// already-native or unknown value through untouched.
func deepseekEffort(effort string) string {
	if mapped, ok := deepseekEfforts[strings.ToLower(effort)]; ok {
		return mapped
	}
	return effort
}

// ValidateTurnSettings rejects a turn setting that cannot reach the session, so
// the failure surfaces before DAO creates durable state rather than part-way
// through the first turn. Harness model values are the opaque strings its own
// session catalog advertises, so the model is only checked for presence; the
// effort must resolve to one of the four advertised levels.
func validateTurnSettings(_ ports.PermissionMode, settings ports.ChatTurnSettings) error {
	effort := strings.TrimSpace(settings.Effort)
	if effort == "" {
		return nil
	}
	switch deepseekEffort(effort) {
	case "off", "low", "high", "max":
		return nil
	default:
		return fmt.Errorf("%w: DeepSeek Harness reasoning effort %q is not one of off, low, high, max", ports.ErrChatConfigOptionInvalid, settings.Effort)
	}
}

// permissionPolicy is AO's side of accept-edits and auto. DeepSeek Harness asks
// through session/request_permission with one-shot allow/reject choices and
// expects the client to answer, so AO answers per request rather than granting
// anything up front. Bypass needs no entry: Harness only asks about actions its
// own configuration has not already decided.
func permissionPolicy(
	mode ports.PermissionMode,
	params acpsdk.RequestPermissionRequest,
) (acpsdk.PermissionOptionId, bool) {
	mode = ports.NormalizePermissionMode(mode)
	kind := params.ToolCall.Kind
	if mode != ports.PermissionModeAuto && (mode != ports.PermissionModeAcceptEdits || kind == nil ||
		(*kind != acpsdk.ToolKindEdit && *kind != acpsdk.ToolKindDelete && *kind != acpsdk.ToolKindMove)) {
		return "", false
	}
	for _, option := range params.Options {
		if option.Kind == acpsdk.PermissionOptionKindAllowOnce {
			return option.OptionId, true
		}
	}
	return "", false
}
