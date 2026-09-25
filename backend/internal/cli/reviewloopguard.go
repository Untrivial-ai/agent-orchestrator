package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Issue #5701: reviewer agents degenerate into dozens of consecutive no-op
// tool calls — a stream of no-op Bash probes (true, :, bare echo/printf whose
// commands differ every call: "echo marker-1", "echo marker-2", ...) or the
// exact same Read repeated 30+ times. Every call succeeds, so nothing in the
// harness stops the drift, and wall-clock and tokens burn until a human types
// into the pane. The guard counts both shapes per reviewer session and denies
// the call that crosses the limit: a denial surfaces as feedback the model
// routes around, which reliably breaks the pattern (denying bare true/: alone
// does not — the observed loops emit a fresh echo marker on every call, so a
// stream rule is needed alongside the identical-repeat rule).
//
// Each PreToolUse hook is a separate process, so the counters live in a
// per-reviewer state file under AO_DATA_DIR and reset at turn boundaries
// (stop / user-prompt-submit), keeping legitimately repeated work like
// re-running a test after edits unharmed.

const (
	reviewLoopProbeLimit  = 5 // consecutive no-op probe calls
	reviewLoopRepeatLimit = 5 // consecutive identical tool calls
	maxReviewLoopKeyLen   = 512
)

// reviewLoopState is one reviewer session's guard counters. Key is the last
// call's identity; Probes counts the current consecutive no-op probe streak;
// Repeats counts the current consecutive identical-call streak.
type reviewLoopState struct {
	Key     string `json:"key"`
	Probes  int    `json:"probes"`
	Repeats int    `json:"repeats"`
}

// claudePreToolUseHookOutput is Claude Code's PreToolUse decision output: the
// hook answers whether the tool call may run before it starts.
type claudePreToolUseHookOutput struct {
	HookSpecificOutput struct {
		HookEventName             string `json:"hookEventName"`
		PermissionDecision        string `json:"permissionDecision"`
		PermissionDecisionMessage string `json:"permissionDecisionMessage,omitempty"`
	} `json:"hookSpecificOutput"`
}

var noopProbePattern = regexp.MustCompile(`^(echo|printf)( |$)`)

// noopProbeCommand reports whether a Bash command is a pure no-op probe: an
// explicit success stub (true, :) or an echo/printf with no operator, pipe,
// or redirect — a marker emission ("echo ok"), never real work. Anything
// chaining, piping, or redirecting (the review submit shapes included) is
// real work and never counts as a probe.
func noopProbeCommand(command string) bool {
	c := strings.Join(strings.Fields(command), " ")
	if c == "true" || c == ":" {
		return true
	}
	if !noopProbePattern.MatchString(c) {
		return false
	}
	return !strings.ContainsAny(c, "|&<>;")
}

// reviewLoopToolName lifts the tool name from a PreToolUse payload.
func reviewLoopToolName(payload []byte) string {
	var p struct {
		ToolName string `json:"tool_name"`
	}
	_ = json.Unmarshal(payload, &p)
	return p.ToolName
}

// reviewLoopToolKey builds the stable identity of a tool call: the tool name
// plus its canonical input. Bash compares on the whitespace-collapsed command,
// so the same script re-wrapped on newlines still counts as the same call;
// every other tool compares on its compacted input JSON (Read repeats with
// identical offset/limit are the observed pathology, offset paging is not).
func reviewLoopToolKey(payload []byte) (string, bool) {
	var p struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	_ = json.Unmarshal(payload, &p)
	if p.ToolName == "" {
		return "", false
	}
	input := ""
	if len(p.ToolInput) > 0 {
		if p.ToolName == "Bash" {
			var cmd struct {
				Command string `json:"command"`
			}
			_ = json.Unmarshal(p.ToolInput, &cmd)
			input = strings.Join(strings.Fields(cmd.Command), " ")
		} else {
			var compact bytes.Buffer
			if json.Compact(&compact, p.ToolInput) == nil {
				input = compact.String()
			} else {
				input = string(p.ToolInput)
			}
		}
	}
	key := p.ToolName + "\x1f" + input
	if len(key) > maxReviewLoopKeyLen {
		sum := sha256.Sum256([]byte(key))
		key = key[:maxReviewLoopKeyLen] + "\x1f" + hex.EncodeToString(sum[:8])
	}
	return key, true
}

// reviewLoopStatePath is where one reviewer session's counters live.
func reviewLoopStatePath(dataDir, reviewSessionID string) string {
	return filepath.Join(dataDir, "review-loops", reviewSessionID+".json")
}

func loadReviewLoopState(path string) reviewLoopState {
	var state reviewLoopState
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	return state
}

func saveReviewLoopState(path string, state reviewLoopState) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return
	}
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o600) //nolint:gosec // path is rooted in AO's own data dir
}

// reviewLoopGuardReset clears a reviewer session's counters. Called at turn
// boundaries so a stale streak never leaks into the next turn.
func reviewLoopGuardReset(dataDir, reviewSessionID string) {
	if dataDir == "" || reviewSessionID == "" {
		return
	}
	_ = os.Remove(reviewLoopStatePath(dataDir, reviewSessionID))
}

// reviewLoopGuardDecision updates the reviewer session's counters with this
// PreToolUse call and, when a streak crosses a limit, returns the deny output
// that breaks the loop. A denial still counts as handled: the hook writes the
// decision and reports no activity — the post-tool-use event restores it.
func reviewLoopGuardDecision(payload []byte, dataDir, reviewSessionID string) (claudePreToolUseHookOutput, bool) {
	key, ok := reviewLoopToolKey(payload)
	if !ok || dataDir == "" || reviewSessionID == "" {
		return claudePreToolUseHookOutput{}, false
	}
	state := loadReviewLoopState(reviewLoopStatePath(dataDir, reviewSessionID))

	probe := reviewLoopToolName(payload) == "Bash"
	if probe {
		var p struct {
			ToolInput struct {
				Command string `json:"command"`
			} `json:"tool_input"`
		}
		_ = json.Unmarshal(payload, &p)
		probe = noopProbeCommand(p.ToolInput.Command)
	}
	if probe {
		state.Probes++
	} else {
		state.Probes = 0
	}
	if key == state.Key {
		state.Repeats++
	} else {
		state.Repeats = 1
	}
	state.Key = key

	var out claudePreToolUseHookOutput
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	denied := false
	switch {
	case state.Probes >= reviewLoopProbeLimit:
		out.HookSpecificOutput.PermissionDecision = "deny"
		out.HookSpecificOutput.PermissionDecisionMessage = fmt.Sprintf(
			"AO review loop guard: %d consecutive no-op probe calls (last: %.120q). "+
				"Emit the real tool call you already planned, or state in text what is blocking it.",
			state.Probes, state.Key)
		state.Probes = 0
		state.Repeats = 0
		denied = true
	case state.Repeats >= reviewLoopRepeatLimit:
		out.HookSpecificOutput.PermissionDecision = "deny"
		out.HookSpecificOutput.PermissionDecisionMessage = fmt.Sprintf(
			"AO review loop guard: the same tool call has now been issued %d times in a row (%.120q). "+
				"Emit the real tool call you already planned, or state in text what is blocking it.",
			state.Repeats, state.Key)
		state.Probes = 0
		state.Repeats = 0
		denied = true
	}
	saveReviewLoopState(reviewLoopStatePath(dataDir, reviewSessionID), state)
	return out, denied
}
