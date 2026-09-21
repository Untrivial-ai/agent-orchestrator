package cursor

import (
	"encoding/json"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// EnvPermissionMode carries the AO permission mode for hook-time evaluation.
// session_manager pins it into the runtime env at spawn/restore.
const EnvPermissionMode = "AO_PERMISSION_MODE"

// PermissionDecision is the outcome of evaluating a Cursor before-execution hook
// against AO's permission policy.
type PermissionDecision struct {
	Permission     string
	State          domain.ActivityState
	ReportActivity bool
}

// DeriveActivityState maps a Cursor hook sub-command onto an AO activity state.
// Before-execution hooks carry no fixed mapping — the CLI evaluates them via
// EvaluatePermission and posts the resulting state separately.
func DeriveActivityState(event string, _ []byte) (domain.ActivityState, bool) {
	switch event {
	case "session-start", "user-prompt-submit":
		return domain.ActivityActive, true
	case "stop":
		return domain.ActivityIdle, true
	case "after-shell-execution", "after-mcp-execution", "post-tool-use", "post-tool-use-failure":
		return domain.ActivityActive, true
	case "before-shell-execution", "before-mcp-execution":
		return "", false
	default:
		return "", false
	}
}

// EvaluatePermission decides whether a Cursor shell/MCP attempt needs user
// approval under AO's permission mode. A native permission dialog is blocked:
// ordinary input must not be delivered while Cursor is waiting for the user's
// decision.
func EvaluatePermission(mode ports.PermissionMode, _ string, _ []byte) PermissionDecision {
	if permissionRequired(mode) {
		return PermissionDecision{
			Permission:     "ask",
			State:          domain.ActivityBlocked,
			ReportActivity: true,
		}
	}
	return PermissionDecision{
		Permission:     "allow",
		State:          domain.ActivityActive,
		ReportActivity: true,
	}
}

func permissionRequired(mode ports.PermissionMode) bool {
	switch ports.NormalizePermissionMode(mode) {
	case ports.PermissionModeBypassPermissions, ports.PermissionModeAuto:
		return false
	default:
		return true
	}
}

// HookToolName extracts a display/correlation name from a Cursor hook payload.
func HookToolName(event string, payload []byte) string {
	var p struct {
		Command  string `json:"command"`
		ToolName string `json:"tool_name"`
	}
	_ = json.Unmarshal(payload, &p)
	switch event {
	case "before-shell-execution", "after-shell-execution":
		return capHookField(p.Command)
	default:
		return capHookField(p.ToolName)
	}
}

// TerminalFailureCorrelation converts Cursor's generic postToolUseFailure
// payload into the same execution family/name pair captured by the specialized
// before-execution hook. Missing correlation stays unresolved so lifecycle
// tracking fails closed.
func TerminalFailureCorrelation(payload []byte) (event, toolName string, ok bool) {
	var p struct {
		ToolName    string `json:"tool_name"`
		FailureType string `json:"failure_type"`
		IsInterrupt bool   `json:"is_interrupt"`
		ToolInput   struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal(payload, &p); err != nil || !terminalFailure(p.FailureType, p.IsInterrupt) {
		return "", "", false
	}
	switch {
	case p.ToolName == "Shell":
		command := capHookField(p.ToolInput.Command)
		return "cursor-shell-terminal-failure", command, command != ""
	case strings.HasPrefix(p.ToolName, "MCP:"):
		name := capHookField(strings.TrimPrefix(p.ToolName, "MCP:"))
		return "cursor-mcp-terminal-failure", name, name != ""
	default:
		return "", "", false
	}
}

func terminalFailure(failureType string, isInterrupt bool) bool {
	if isInterrupt {
		return true
	}
	switch failureType {
	case "permission_denied", "error", "timeout":
		return true
	default:
		return false
	}
}

func capHookField(value string) string {
	value = strings.TrimSpace(value)
	const maxLen = 256
	if len(value) <= maxLen {
		return value
	}
	return value[:maxLen]
}
