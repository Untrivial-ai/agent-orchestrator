package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/activitydispatch"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/cursor"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/pricing"
)

// sessionIDPattern bounds the AO_SESSION_ID we will place in a request path to
// the id alphabet the daemon issues. Validating the externally-set env value
// before it reaches the loopback URL keeps it from steering the request.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

const (
	// hooksLogName is the file under AO_DATA_DIR where hook delivery failures
	// are appended. Agent hook runners swallow stderr, so without a durable
	// sink a dead activity feed (e.g. an unreachable daemon) stays invisible.
	hooksLogName = "hooks.log"
	// maxHooksLogBytes caps hooks.log: an append against a file already past
	// the cap truncates it first, so a persistently failing hook cannot grow
	// the file without bound.
	maxHooksLogBytes = 1 << 20
)

// setActivityAPIRequest mirrors the daemon's SetActivityRequest body for
// POST /api/v1/sessions/{id}/activity. The CLI keeps its own copy so it need
// not import httpd. Event carries the AO hook sub-command that produced the
// state; ToolName/ToolUseID are the tool-use correlation facts lifted from the
// native payload when present. AgentSessionID is a native resumable-session id
// lifted from the payload when present. All four are optional: an old daemon
// decodes the body leniently and simply ignores them.
type setActivityAPIRequest struct {
	State                 string             `json:"state,omitempty"`
	Event                 string             `json:"event,omitempty"`
	ToolName              string             `json:"toolName,omitempty"`
	ToolUseID             string             `json:"toolUseId,omitempty"`
	AgentSessionID        string             `json:"agentSessionId,omitempty"`
	LatestUserPrompt      string             `json:"latestUserPrompt,omitempty"`
	LatestAssistantUpdate string             `json:"latestAssistantUpdate,omitempty"`
	TranscriptPath        string             `json:"transcriptPath,omitempty"`
	LaunchID              string             `json:"launchId,omitempty"`
	Usage                 *usageHookMetadata `json:"usage,omitempty"`
}

type usageHookMetadata struct {
	Harness                string `json:"harness"`
	ProviderID             string `json:"providerId,omitempty"`
	TranscriptPath         string `json:"transcriptPath,omitempty"`
	ModelID                string `json:"modelId,omitempty"`
	SubagentID             string `json:"subagentId,omitempty"`
	SubagentTranscriptPath string `json:"subagentTranscriptPath,omitempty"`
}

// setReviewActivityAPIRequest mirrors POST /api/v1/reviews/{id}/activity.
// Reviewer hooks only persist reviewer-owned restore metadata for now; they do
// not feed worker lifecycle/tool-flight state.
type setReviewActivityAPIRequest struct {
	State          string `json:"state,omitempty"`
	Event          string `json:"event,omitempty"`
	AgentSessionID string `json:"agentSessionId,omitempty"`
	LaunchID       string `json:"launchId,omitempty"`
}

// maxActivityMetaLen caps the correlation fields lifted from a native hook
// payload before they go on the wire — they are ids/names, anything longer is
// garbage and gets dropped rather than truncated (a truncated id would never
// match its pre/post counterpart).
const maxActivityMetaLen = 256

const (
	maxHookInteractionLen = 16 << 10
	maxHookTranscriptPath = 4096
)

// activityMeta extracts the tool-use correlation facts from a native hook
// payload. The field names are shared vocabulary across agent CLIs that emit
// them (claude-code's PreToolUse/PostToolUse/PostToolUseFailure and
// PermissionRequest payloads); adapters whose payloads lack them yield empty
// strings and the signal degrades to today's state-only form.
func activityMeta(payload []byte) (toolName, toolUseID string) {
	var p struct {
		ToolName  string `json:"tool_name"`
		ToolUseID string `json:"tool_use_id"`
	}
	_ = json.Unmarshal(payload, &p)
	if len(p.ToolName) > maxActivityMetaLen {
		p.ToolName = ""
	}
	if len(p.ToolUseID) > maxActivityMetaLen {
		p.ToolUseID = ""
	}
	return p.ToolName, p.ToolUseID
}

// hookAgentSessionID extracts the native resume handle shared by Agy, Copilot,
// Codex, Claude Code, and other hook payloads. It is independent of activity
// derivation because SessionStart is intentionally metadata-only for harnesses
// where process startup is not proof that a turn is active.
func hookAgentSessionID(payload []byte) string {
	var p struct {
		SessionID           string `json:"session_id"`
		SessionIDCamel      string `json:"sessionId"`
		ConversationID      string `json:"conversation_id"`
		ConversationIDCamel string `json:"conversationId"`
	}
	_ = json.Unmarshal(payload, &p)
	id := strings.TrimSpace(p.SessionID)
	if id == "" {
		id = strings.TrimSpace(p.SessionIDCamel)
	}
	if id == "" {
		id = strings.TrimSpace(p.ConversationID)
	}
	if id == "" {
		id = strings.TrimSpace(p.ConversationIDCamel)
	}
	if len(id) > maxActivityMetaLen {
		return ""
	}
	return id
}

// hookLaunchID extracts the runtime launch id a plugin embeds in its payload.
// It is a fallback for AO_RUNTIME_LAUNCH_ID when child-process env inheritance
// is trimmed by the agent runtime.
func hookLaunchID(payload []byte) string {
	var p struct {
		LaunchID      string `json:"launch_id"`
		LaunchIDCamel string `json:"launchId"`
	}
	_ = json.Unmarshal(payload, &p)
	id := strings.TrimSpace(p.LaunchID)
	if id == "" {
		id = strings.TrimSpace(p.LaunchIDCamel)
	}
	if len(id) > maxActivityMetaLen {
		return ""
	}
	return id
}

// hookUsageMetadata extracts provider-native usage metadata. It deliberately
// decodes separately from conversation facts because hook producers may emit
// a malformed field in one projection while the other remains useful.
func hookUsageMetadata(agent string, payload []byte) *usageHookMetadata {
	harness := domain.AgentHarness(agent)
	if harness != domain.HarnessClaudeCode && harness != domain.HarnessCodex {
		return nil
	}
	var native struct {
		TranscriptPath         string `json:"transcript_path"`
		Model                  string `json:"model"`
		SubagentID             string `json:"agent_id"`
		SubagentTranscriptPath string `json:"agent_transcript_path"`
	}
	if json.Unmarshal(payload, &native) != nil {
		return nil
	}
	meta := &usageHookMetadata{
		Harness:                agent,
		TranscriptPath:         strings.TrimSpace(native.TranscriptPath),
		ModelID:                strings.TrimSpace(native.Model),
		SubagentID:             strings.TrimSpace(native.SubagentID),
		SubagentTranscriptPath: strings.TrimSpace(native.SubagentTranscriptPath),
	}
	if meta.TranscriptPath == "" && meta.SubagentTranscriptPath == "" && meta.ModelID == "" {
		return nil
	}
	meta.ProviderID = claudeHookProviderHint(harness)
	return meta
}

func claudeHookProviderHint(harness domain.AgentHarness) string {
	if harness != domain.HarnessClaudeCode {
		return ""
	}
	bedrock := hookRouteFlagEnabled(os.Getenv("CLAUDE_CODE_USE_BEDROCK"))
	vertex := hookRouteFlagEnabled(os.Getenv("CLAUDE_CODE_USE_VERTEX"))
	if bedrock != vertex {
		if bedrock {
			return "bedrock"
		}
		return "vertex_ai"
	}
	if bedrock {
		// Both flags set: the route is certainly not plain Anthropic, so this
		// still has to rule out inferring one from the model.
		return pricing.UnidentifiedBillingRoute
	}
	baseURL := strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL"))
	if baseURL == "" {
		return "anthropic"
	}
	// A base URL AO cannot name still rules out inferring one from the model:
	// the session is routed somewhere, and reporting that is the difference
	// between "no hook has run" and "a hook ran and the route is not ours".
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return pricing.UnidentifiedBillingRoute
	}
	if parsed.Hostname() == "" && !strings.Contains(baseURL, "://") {
		parsed, err = url.Parse("https://" + baseURL)
	}
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return pricing.UnidentifiedBillingRoute
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "api.anthropic.com":
		return "anthropic"
	case "api.z.ai":
		return "zai"
	default:
		return pricing.UnidentifiedBillingRoute
	}
}

func hookRouteFlagEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

type hookConversationSnapshot struct {
	LatestUserPrompt      string
	LatestAssistantUpdate string
	TranscriptPath        string
}

func hookConversationFacts(payload []byte) hookConversationSnapshot {
	var p struct {
		Prompt                    string `json:"prompt"`
		UserPrompt                string `json:"user_prompt"`
		UserPromptCamel           string `json:"userPrompt"`
		LastAssistantMessage      string `json:"last_assistant_message"`
		LastAssistantMessageCamel string `json:"lastAssistantMessage"`
		AssistantMessage          string `json:"assistant_message"`
		AssistantMessageCamel     string `json:"assistantMessage"`
		TranscriptPath            string `json:"transcript_path"`
		TranscriptPathCamel       string `json:"transcriptPath"`
	}
	_ = json.Unmarshal(payload, &p)
	userPrompt := firstHookValue(p.Prompt, p.UserPrompt, p.UserPromptCamel)
	assistant := firstHookValue(p.LastAssistantMessage, p.LastAssistantMessageCamel, p.AssistantMessage, p.AssistantMessageCamel)
	// AO's own handoff request and continuation kickoff are coordination turns,
	// not the latest real user instruction. They remain in provider history but
	// must not overwrite deterministic user intent.
	if strings.HasPrefix(strings.TrimSpace(userPrompt), "<ao-handoff-request") {
		assistant = ""
	}
	if isAOCoordinationMessage(userPrompt) {
		userPrompt = ""
	}
	return hookConversationSnapshot{
		LatestUserPrompt:      capHookText(userPrompt, maxHookInteractionLen),
		LatestAssistantUpdate: capHookText(assistant, maxHookInteractionLen),
		TranscriptPath:        capHookText(firstHookValue(p.TranscriptPath, p.TranscriptPathCamel), maxHookTranscriptPath),
	}
}

func firstHookValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func isAOCoordinationMessage(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "<ao-handoff-request") ||
		strings.HasPrefix(value, "AO transferred the previous agent's context in hidden system instructions.")
}

func capHookText(value string, limit int) string {
	value = domain.SanitizeControlChars(strings.TrimSpace(value))
	if limit <= 0 || len(value) <= limit {
		return value
	}
	const marker = "\n[... truncated by AO ...]\n"
	budget := limit - len(marker)
	if budget <= 0 {
		return ""
	}
	head := budget / 2
	tail := budget - head
	return strings.ToValidUTF8(string([]byte(value)[:head])+marker+string([]byte(value)[len(value)-tail:]), "?")
}

type sessionStartHookOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

type cursorPermissionHookOutput struct {
	Permission string `json:"permission"`
}

// newHooksCommand builds the hidden `ao hooks <agent> <event>` command that
// agent CLIs invoke from their workspace-local hook config. It reads the native
// hook payload from stdin and the AO session id from AO_SESSION_ID, derives an
// activity state for the event, and reports it to the daemon.
//
// It is best-effort by design: a hook must never break the user's agent, so a
// non-AO session (no AO_SESSION_ID), an event that carries no activity signal,
// or an unreachable daemon all exit 0 rather than erroring.
func newHooksCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use:    "hooks <agent> <event>",
		Short:  "Receive an agent hook callback (internal)",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.runHook(cmd.Context(), args[0], args[1])
		},
	}
}

func (c *commandContext) runHook(ctx context.Context, agent, event string) error {
	if isAgyModernHookEvent(agent, event) {
		// AGY requires every modern hook handler to return a JSON object, even
		// when the command is running outside an AO-managed session.
		_, _ = fmt.Fprintln(c.deps.Out, "{}")
	}
	reviewSessionID := strings.TrimSpace(os.Getenv("AO_REVIEW_SESSION_ID"))
	if reviewSessionID != "" {
		if !sessionIDPattern.MatchString(reviewSessionID) {
			return nil
		}
		return c.runReviewHook(ctx, agent, event, reviewSessionID)
	}
	sessionID := strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
	if !sessionIDPattern.MatchString(sessionID) {
		// Not an AO-managed session (unset/empty), or an id we won't put in a
		// request path. Return before reading stdin so a manual invocation
		// without a piped payload can't block on EOF.
		return nil
	}
	var payload []byte
	if hookReadsStdin(agent, event) {
		var err error
		payload, err = io.ReadAll(c.deps.In)
		if err != nil {
			// Surface read errors for parity with the daemon-error path, but keep
			// the empty payload and exit 0: a failed hook must not break the
			// agent. The deriver tolerates an empty payload.
			c.reportHookFailure(agent, event, sessionID, fmt.Errorf("read stdin: %w", err))
		}
	}
	if shouldEmitSessionStartContext(agent, event) {
		c.emitSessionStartContext(agent, event, sessionID)
	}
	if isCursorPermissionHook(agent, event) {
		return c.runCursorPermissionHook(ctx, agent, event, sessionID, payload)
	}

	state, hasActivity := activitydispatch.Derive(agent, event, payload)
	agentSessionID := ""
	if activitydispatch.SupportsHarness(domain.AgentHarness(agent)) {
		agentSessionID = hookAgentSessionID(payload)
	}
	if adapterSessionID, ok := activitydispatch.AgentSessionID(agent, payload); ok {
		agentSessionID = adapterSessionID
	}
	usage := hookUsageMetadata(agent, payload)
	if !hasActivity && agentSessionID == "" && usage == nil {
		// Unknown agent, or an event carrying neither activity nor resumable
		// session metadata: report nothing.
		return nil
	}

	launchID := validLaunchID(os.Getenv("AO_RUNTIME_LAUNCH_ID"))
	if launchID == "" {
		launchID = validLaunchID(hookLaunchID(payload))
	}

	toolName, toolUseID := activityMeta(payload)
	if domain.AgentHarness(agent) == domain.HarnessCursor && event == "post-tool-use-failure" {
		if failureEvent, failureTool, ok := cursor.TerminalFailureCorrelation(payload); ok {
			event = failureEvent
			toolName = failureTool
		}
	}
	conversation := hookConversationSnapshot{}
	switch domain.AgentHarness(agent) {
	case domain.HarnessClaudeCode, domain.HarnessCodex, domain.HarnessContinue:
		conversation = hookConversationFacts(payload)
	}
	path := "sessions/" + url.PathEscape(sessionID) + "/activity"
	req := setActivityAPIRequest{
		Event:                 event,
		ToolName:              toolName,
		ToolUseID:             toolUseID,
		AgentSessionID:        agentSessionID,
		LatestUserPrompt:      conversation.LatestUserPrompt,
		LatestAssistantUpdate: conversation.LatestAssistantUpdate,
		TranscriptPath:        conversation.TranscriptPath,
		LaunchID:              launchID,
		Usage:                 usage,
	}
	if hasActivity {
		req.State = string(state)
	}
	if err := c.postJSON(ctx, path, req, nil); err != nil {
		// Surface the failure for diagnosis, but exit 0: a failed activity
		// report must not disrupt the agent.
		c.reportHookFailure(agent, event, sessionID, err)
	}
	return nil
}

func isCursorPermissionHook(agent, event string) bool {
	if domain.AgentHarness(agent) != domain.HarnessCursor {
		return false
	}
	switch event {
	case "before-shell-execution", "before-mcp-execution":
		return true
	default:
		return false
	}
}

func (c *commandContext) runCursorPermissionHook(ctx context.Context, agent, event, sessionID string, payload []byte) error {
	mode := ports.PermissionMode(strings.TrimSpace(os.Getenv(cursor.EnvPermissionMode)))
	decision := cursor.EvaluatePermission(mode, event, payload)

	launchID := validLaunchID(os.Getenv("AO_RUNTIME_LAUNCH_ID"))
	if launchID == "" {
		launchID = validLaunchID(hookLaunchID(payload))
	}

	path := "sessions/" + url.PathEscape(sessionID) + "/activity"
	req := setActivityAPIRequest{
		State:          string(decision.State),
		Event:          event,
		ToolName:       cursor.HookToolName(event, payload),
		AgentSessionID: hookAgentSessionID(payload),
		LaunchID:       launchID,
	}
	if err := c.postJSON(ctx, path, req, nil); err != nil {
		c.reportHookFailure(agent, event, sessionID, err)
		if decision.Permission == "ask" {
			return fmt.Errorf("persist blocked Cursor activity: %w", err)
		}
	}

	out := cursorPermissionHookOutput{Permission: decision.Permission}
	if err := json.NewEncoder(c.deps.Out).Encode(out); err != nil {
		c.reportHookFailure(agent, event, sessionID, fmt.Errorf("write permission response: %w", err))
	}
	return nil
}

func isAgyModernHookEvent(agent, event string) bool {
	if domain.AgentHarness(agent) != domain.HarnessAgy {
		return false
	}
	switch event {
	case "pre-invocation", "post-tool-use", "stop":
		return true
	default:
		return false
	}
}

func (c *commandContext) runReviewHook(ctx context.Context, agent, event, reviewSessionID string) error {
	var payload []byte
	if hookReadsStdin(agent, event) {
		var err error
		payload, err = io.ReadAll(c.deps.In)
		if err != nil {
			c.reportHookFailure(agent, event, reviewSessionID, fmt.Errorf("read stdin: %w", err))
		}
	}
	state, hasActivity := activitydispatch.Derive(agent, event, payload)
	agentSessionID := ""
	if activitydispatch.SupportsHarness(domain.AgentHarness(agent)) {
		agentSessionID = hookAgentSessionID(payload)
	}
	if !hasActivity && agentSessionID == "" {
		return nil
	}
	launchID := validLaunchID(os.Getenv("AO_RUNTIME_LAUNCH_ID"))
	if launchID == "" {
		launchID = validLaunchID(hookLaunchID(payload))
	}
	path := "reviews/" + url.PathEscape(reviewSessionID) + "/activity"
	req := setReviewActivityAPIRequest{
		Event:          event,
		AgentSessionID: agentSessionID,
		LaunchID:       launchID,
	}
	if hasActivity {
		req.State = string(state)
	}
	if err := c.postJSON(ctx, path, req, nil); err != nil {
		c.reportHookFailure(agent, event, reviewSessionID, err)
	}
	return nil
}

// Aider's notification callback is synchronous and inherits the interactive
// PTY stdin, but its activity transition carries no payload. Reading stdin
// here would wait for the next user prompt and stall Aider's redraw.
func hookReadsStdin(agent, event string) bool {
	return agent != "aider" || event != "notification"
}

func validLaunchID(value string) string {
	value = strings.TrimSpace(value)
	if !sessionIDPattern.MatchString(value) {
		return ""
	}
	return value
}

func shouldEmitSessionStartContext(agent, event string) bool {
	if event != "session-start" {
		return false
	}
	switch agent {
	case "agy", "devin":
		return true
	default:
		return false
	}
}

func (c *commandContext) emitSessionStartContext(agent, event, sessionID string) {
	dataDir := strings.TrimSpace(os.Getenv("AO_DATA_DIR"))
	if dataDir == "" {
		return
	}
	path := filepath.Join(dataDir, "prompts", sessionID, "system.md")
	data, err := os.ReadFile(path) //nolint:gosec // sessionID is bounded by sessionIDPattern.
	if err != nil {
		c.reportHookFailure(agent, event, sessionID, fmt.Errorf("read system prompt: %w", err))
		return
	}
	prompt := strings.TrimSpace(string(data))
	if prompt == "" {
		return
	}
	var out sessionStartHookOutput
	out.HookSpecificOutput.HookEventName = "SessionStart"
	out.HookSpecificOutput.AdditionalContext = prompt
	if err := json.NewEncoder(c.deps.Out).Encode(out); err != nil {
		c.reportHookFailure(agent, event, sessionID, fmt.Errorf("write session-start context: %w", err))
	}
}

// reportHookFailure surfaces a hook delivery failure without breaking the
// agent: stderr for the agent's hook runner, plus a best-effort append to
// $AO_DATA_DIR/hooks.log so the failure can be diagnosed after the fact.
func (c *commandContext) reportHookFailure(agent, event, sessionID string, cause error) {
	msg := fmt.Sprintf("ao hooks %s %s: %v", agent, event, cause)
	_, _ = fmt.Fprintln(c.deps.Err, msg)
	dataDir := strings.TrimSpace(os.Getenv("AO_DATA_DIR"))
	if dataDir == "" {
		return
	}
	line := fmt.Sprintf("%s session=%s %s\n", time.Now().UTC().Format(time.RFC3339), sessionID, msg)
	appendHooksLog(dataDir, line)
}

// appendHooksLog appends one line to the hooks log, truncating first when the
// file has outgrown maxHooksLogBytes. Errors are dropped: this sink is itself
// best-effort and has nowhere better to report.
func appendHooksLog(dataDir, line string) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return
	}
	path := filepath.Join(dataDir, hooksLogName)
	flags := os.O_APPEND | os.O_CREATE | os.O_WRONLY
	if info, err := os.Stat(path); err == nil && info.Size() > maxHooksLogBytes {
		flags = os.O_TRUNC | os.O_CREATE | os.O_WRONLY
	}
	f, err := os.OpenFile(path, flags, 0o600) //nolint:gosec // path is rooted in AO's own data dir
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(line)
}
