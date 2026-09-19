package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/activitydispatch"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
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
// native payload when present. All four are optional: an old daemon decodes
// the body leniently and simply ignores them.
type setActivityAPIRequest struct {
	ObservedAt                   time.Time                           `json:"observedAt,omitempty"`
	State                        string                              `json:"state,omitempty"`
	Event                        string                              `json:"event,omitempty"`
	ToolName                     string                              `json:"toolName,omitempty"`
	ToolUseID                    string                              `json:"toolUseId,omitempty"`
	AgentSessionID               string                              `json:"agentSessionId,omitempty"`
	LatestUserPrompt             string                              `json:"latestUserPrompt,omitempty"`
	LatestAssistantUpdate        string                              `json:"latestAssistantUpdate,omitempty"`
	ConversationCheckpointOrigin domain.ConversationCheckpointOrigin `json:"conversationCheckpointOrigin,omitempty"`
	ProviderTurnID               string                              `json:"providerTurnId,omitempty"`
	SubmissionID                 string                              `json:"submissionId,omitempty"`
	TranscriptPath               string                              `json:"transcriptPath,omitempty"`
	LaunchID                     string                              `json:"launchId,omitempty"`
	Usage                        *usageHookMetadata                  `json:"usage,omitempty"`
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

// activityMeta extracts the tool-use correlation facts from a native hook
// payload. The field names are shared vocabulary across agent CLIs that emit
// them (opencode's PreToolUse/PostToolUse/PostToolUseFailure payloads);
// adapters whose payloads lack them yield empty strings and the signal degrades
// to today's state-only form.
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

// hookAgentSessionID extracts the native resume handle shared by hook payloads.
// It is independent of activity derivation because SessionStart is
// intentionally metadata-only for harnesses where process startup is not proof
// that a turn is active.
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
	observedAt := c.deps.Now()
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

	state, hasActivity := activitydispatch.Derive(agent, event, payload)
	agentSessionID := ""
	if activitydispatch.SupportsHarness(domain.AgentHarness(agent)) {
		agentSessionID = hookAgentSessionID(payload)
	}
	if !hasActivity && agentSessionID == "" {
		// Unknown agent, or an event carrying neither activity nor resumable
		// session metadata: report nothing.
		return nil
	}

	launchID := validLaunchID(os.Getenv("AO_RUNTIME_LAUNCH_ID"))
	if launchID == "" {
		launchID = validLaunchID(hookLaunchID(payload))
	}

	toolName, toolUseID := activityMeta(payload)
	path := "sessions/" + url.PathEscape(sessionID) + "/activity"
	req := setActivityAPIRequest{
		ObservedAt:                   observedAt,
		Event:                        event,
		ToolName:                     toolName,
		ToolUseID:                    toolUseID,
		AgentSessionID:               agentSessionID,
		LatestUserPrompt:             "",
		LatestAssistantUpdate:        "",
		ConversationCheckpointOrigin: domain.ConversationCheckpointOriginUnknown,
		ProviderTurnID:               "",
		TranscriptPath:               "",
		LaunchID:                     launchID,
		Usage:                        nil,
	}
	if hasActivity {
		req.State = string(state)
	}
	if err := c.postActivityHook(ctx, path, req); err != nil {
		// Surface the failure for diagnosis, but exit 0: a failed activity
		// report must not disrupt the agent.
		c.reportHookFailure(agent, event, sessionID, err)
	}
	return nil
}

func (c *commandContext) postActivityHook(ctx context.Context, path string, req setActivityAPIRequest) error {
	for attempt := 0; ; attempt++ {
		err := c.postJSON(ctx, path, req, nil)
		var response apiResponseError
		// Only this response guarantees the signal did not commit. Retrying
		// transport errors or generic 503s could duplicate an applied Stop.
		if attempt >= 3 || !errors.As(err, &response) || response.StatusCode != http.StatusServiceUnavailable ||
			response.ErrorBody.Code != "ACTIVITY_PROJECTION_BUSY" {
			return err
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
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

// hookReadsStdin reports whether a hook event carries a stdin payload. Every
// managed hook feeds a JSON payload; keep the seam so a future payload-free
// event can skip the read without blocking on EOF.
func hookReadsStdin(agent, event string) bool {
	return true
}

func validLaunchID(value string) string {
	value = strings.TrimSpace(value)
	if !sessionIDPattern.MatchString(value) {
		return ""
	}
	return value
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
