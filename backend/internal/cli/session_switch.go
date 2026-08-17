package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

type sessionSwitchAgentOptions struct {
	idempotencyKey string
	json           bool
}

type sessionAgentSwitchListOptions struct {
	json bool
}

type sessionHandoffSubmitOptions struct {
	session            string
	switchID           string
	sourceGenerationID string
	file               string
	json               bool
}

var (
	switchAgentOverallWait  = 10 * time.Minute
	switchAgentPollInterval = 500 * time.Millisecond
)

// switchAgentRequest mirrors the daemon's request body for
// POST /api/v1/sessions/{id}/switch-agent. Keeping the wire type here avoids
// coupling the thin CLI client to the HTTP controller package.
type switchAgentRequest struct {
	TargetHarness  string `json:"targetHarness"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

// submitAgentHandoffRequest mirrors the daemon's request body for
// POST /api/v1/sessions/{id}/agent-switches/{switchId}/handoff.
type submitAgentHandoffRequest struct {
	SourceGenerationID string          `json:"sourceGenerationId"`
	Handoff            json.RawMessage `json:"handoff"`
}

// agentSwitchDTO mirrors the daemon's public agent-switch view. Provider-native
// identifiers, handoff payloads, artifacts, and generation fencing remain
// private to the daemon.
type agentSwitchDTO struct {
	ID                      string    `json:"id"`
	SessionID               string    `json:"sessionId"`
	FromHarness             string    `json:"fromHarness"`
	TargetHarness           string    `json:"targetHarness"`
	TargetStartMode         string    `json:"targetStartMode,omitempty"`
	State                   string    `json:"state"`
	AgentHandoffStatus      string    `json:"agentHandoffStatus"`
	SemanticHandoffIncluded bool      `json:"semanticHandoffIncluded"`
	SourceTranscriptStatus  string    `json:"sourceTranscriptStatus,omitempty"`
	ErrorCode               string    `json:"errorCode,omitempty"`
	RequestedAt             time.Time `json:"requestedAt"`
	UpdatedAt               time.Time `json:"updatedAt"`
}

type agentSwitchResponse struct {
	Switch agentSwitchDTO `json:"switch"`
}

type agentSwitchListResponse struct {
	Switches []agentSwitchDTO `json:"switches"`
}

func newSessionSwitchAgentCommand(ctx *commandContext) *cobra.Command {
	var opts sessionSwitchAgentOptions
	cmd := &cobra.Command{
		Use:   "switch-agent <id> <target-harness>",
		Short: "Switch a running session to another agent",
		Args:  usageArgs(cobra.ExactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID, err := normalizeSessionID(args[0])
			if err != nil {
				return err
			}
			targetHarness := strings.TrimSpace(args[1])
			if targetHarness == "" {
				return usageError{errors.New("target harness is required")}
			}
			return ctx.switchSessionAgent(cmd.Context(), cmd, sessionID, targetHarness, opts)
		},
	}
	cmd.Flags().StringVar(&opts.idempotencyKey, "idempotency-key", "", "Reuse a prior identical switch request safely")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output the agent switch as JSON")
	return cmd
}

func newSessionAgentSwitchCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "agent-switch",
		Aliases: []string{"agent-switches"},
		Short:   "Inspect agent switches for a session",
	}
	cmd.AddCommand(newSessionAgentSwitchListCommand(ctx))
	return cmd
}

func newSessionAgentSwitchListCommand(ctx *commandContext) *cobra.Command {
	var opts sessionAgentSwitchListOptions
	cmd := &cobra.Command{
		Use:     "ls <session-id>",
		Aliases: []string{"list"},
		Short:   "List agent switches for a session",
		Args:    oneSessionIDArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionID, err := normalizeSessionID(args[0])
			if err != nil {
				return err
			}
			return ctx.listSessionAgentSwitches(cmd.Context(), cmd, sessionID, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output agent switches as JSON")
	return cmd
}

func newSessionHandoffCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "handoff",
		Short:  "Manage agent-switch handoffs",
		Hidden: true,
	}
	cmd.AddCommand(newSessionHandoffSubmitCommand(ctx))
	return cmd
}

func newSessionHandoffSubmitCommand(ctx *commandContext) *cobra.Command {
	var opts sessionHandoffSubmitOptions
	cmd := &cobra.Command{
		Use:   "submit",
		Short: "Submit a source agent's handoff document",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.submitSessionAgentHandoff(cmd.Context(), cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.session, "session", "", "AO session id (default: AO_SESSION_ID)")
	cmd.Flags().StringVar(&opts.switchID, "switch", "", "Agent switch id (required)")
	cmd.Flags().StringVar(&opts.sourceGenerationID, "source-generation", "", "Source agent generation id (required)")
	cmd.Flags().StringVar(&opts.file, "file", "", "Path to the JSON handoff document (required)")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output the updated agent switch as JSON")
	return cmd
}

func (c *commandContext) switchSessionAgent(
	ctx context.Context,
	cmd *cobra.Command,
	sessionID, targetHarness string,
	opts sessionSwitchAgentOptions,
) error {
	waitCtx, cancel := context.WithTimeout(ctx, switchAgentOverallWait)
	defer cancel()

	req := switchAgentRequest{
		TargetHarness:  targetHarness,
		IdempotencyKey: strings.TrimSpace(opts.idempotencyKey),
	}
	var res agentSwitchResponse
	path := "sessions/" + url.PathEscape(sessionID) + "/switch-agent"
	if err := c.postJSON(waitCtx, path, req, &res); err != nil {
		return err
	}
	if agentSwitchTerminal(res.Switch.State) {
		return writeFinalAgentSwitch(cmd, res, opts.json)
	}

	historyPath := "sessions/" + url.PathEscape(sessionID) + "/agent-switches"
	for {
		timer := time.NewTimer(switchAgentPollInterval)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			return switchAgentWaitError(ctx, waitCtx, res.Switch.ID, sessionID)
		case <-timer.C:
		}

		var history agentSwitchListResponse
		if err := c.getJSON(waitCtx, historyPath, &history); err != nil {
			if waitCtx.Err() != nil {
				return switchAgentWaitError(ctx, waitCtx, res.Switch.ID, sessionID)
			}
			return err
		}
		for _, candidate := range history.Switches {
			if candidate.ID != res.Switch.ID || !agentSwitchTerminal(candidate.State) {
				continue
			}
			res.Switch = candidate
			return writeFinalAgentSwitch(cmd, res, opts.json)
		}
	}
}

func agentSwitchTerminal(state string) bool {
	return state == "completed" || state == "failed"
}

func writeFinalAgentSwitch(cmd *cobra.Command, res agentSwitchResponse, jsonOutput bool) error {
	var writeErr error
	if jsonOutput {
		writeErr = writeJSON(cmd.OutOrStdout(), res)
	} else {
		writeErr = writeAgentSwitchDetails(cmd, res.Switch)
	}
	if writeErr != nil {
		return writeErr
	}
	if res.Switch.State != "failed" {
		return nil
	}
	if res.Switch.ErrorCode != "" {
		return fmt.Errorf("agent switch %s failed (%s)", res.Switch.ID, res.Switch.ErrorCode)
	}
	return fmt.Errorf("agent switch %s failed", res.Switch.ID)
}

func switchAgentWaitError(parent, waitCtx context.Context, switchID, sessionID string) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf(
			"agent switch %s did not finish before the overall timeout; inspect it with `ao session agent-switch ls %s`",
			switchID,
			sessionID,
		)
	}
	return waitCtx.Err()
}

func (c *commandContext) listSessionAgentSwitches(
	ctx context.Context,
	cmd *cobra.Command,
	sessionID string,
	opts sessionAgentSwitchListOptions,
) error {
	var res agentSwitchListResponse
	path := "sessions/" + url.PathEscape(sessionID) + "/agent-switches"
	if err := c.getJSON(ctx, path, &res); err != nil {
		return err
	}
	if opts.json {
		return writeJSON(cmd.OutOrStdout(), res)
	}
	return writeAgentSwitchList(cmd, res.Switches)
}

func (c *commandContext) submitSessionAgentHandoff(
	ctx context.Context,
	cmd *cobra.Command,
	opts sessionHandoffSubmitOptions,
) error {
	sessionID := strings.TrimSpace(opts.session)
	if sessionID == "" {
		sessionID = strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
	}
	var err error
	sessionID, err = normalizeSessionID(sessionID)
	if err != nil {
		return usageError{errors.New("session id is required (pass --session or set AO_SESSION_ID)")}
	}
	switchID := strings.TrimSpace(opts.switchID)
	if switchID == "" {
		return usageError{errors.New("--switch is required")}
	}
	sourceGenerationID := strings.TrimSpace(opts.sourceGenerationID)
	if sourceGenerationID == "" {
		return usageError{errors.New("--source-generation is required")}
	}
	file := strings.TrimSpace(opts.file)
	if file == "" {
		return usageError{errors.New("--file is required")}
	}
	info, err := os.Stat(file)
	if err != nil {
		return usageError{fmt.Errorf("stat handoff file: %w", err)}
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return usageError{errors.New("handoff file must be a regular JSON file no larger than 64 KiB")}
	}
	raw, err := os.ReadFile(file) //nolint:gosec // --file is an explicit user-provided input path.
	if err != nil {
		return usageError{fmt.Errorf("read handoff file: %w", err)}
	}
	if !json.Valid(raw) {
		return usageError{errors.New("handoff file must contain valid JSON")}
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed[0] != '{' {
		return usageError{errors.New("handoff file must contain a JSON object")}
	}

	var res agentSwitchResponse
	path := "sessions/" + url.PathEscape(sessionID) + "/agent-switches/" + url.PathEscape(switchID) + "/handoff"
	req := submitAgentHandoffRequest{
		SourceGenerationID: sourceGenerationID,
		Handoff:            json.RawMessage(raw),
	}
	if err := c.postJSON(ctx, path, req, &res); err != nil {
		return err
	}
	if opts.json {
		return writeJSON(cmd.OutOrStdout(), res)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "handoff submitted for switch %s (state: %s)\n", res.Switch.ID, res.Switch.State)
	return err
}

func writeAgentSwitchList(cmd *cobra.Command, switches []agentSwitchDTO) error {
	out := cmd.OutOrStdout()
	if len(switches) == 0 {
		_, err := fmt.Fprintln(out, "(no agent switches)")
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tFROM\tTARGET\tSTATE\tHANDOFF\tUPDATED"); err != nil {
		return err
	}
	for _, agentSwitch := range switches {
		updated := ""
		if !agentSwitch.UpdatedAt.IsZero() {
			updated = agentSwitch.UpdatedAt.Format(time.RFC3339)
		}
		if _, err := fmt.Fprintf(
			tw,
			"%s\t%s\t%s\t%s\t%s\t%s\n",
			agentSwitch.ID,
			agentSwitch.FromHarness,
			agentSwitch.TargetHarness,
			agentSwitch.State,
			agentSwitch.AgentHandoffStatus,
			updated,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func writeAgentSwitchDetails(cmd *cobra.Command, agentSwitch agentSwitchDTO) error {
	out := cmd.OutOrStdout()
	fields := [][2]string{
		{"id", agentSwitch.ID},
		{"session", agentSwitch.SessionID},
		{"from", agentSwitch.FromHarness},
		{"target", agentSwitch.TargetHarness},
		{"state", agentSwitch.State},
		{"handoff", agentSwitch.AgentHandoffStatus},
		{"semantic handoff included", strconv.FormatBool(agentSwitch.SemanticHandoffIncluded)},
		{"source transcript", agentSwitch.SourceTranscriptStatus},
		{"target start", agentSwitch.TargetStartMode},
		{"error code", agentSwitch.ErrorCode},
	}
	for _, field := range fields {
		if field[1] == "" {
			continue
		}
		if _, err := fmt.Fprintf(out, "%s: %s\n", field[0], field[1]); err != nil {
			return err
		}
	}
	if !agentSwitch.RequestedAt.IsZero() {
		if _, err := fmt.Fprintf(out, "requested: %s\n", agentSwitch.RequestedAt.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if !agentSwitch.UpdatedAt.IsZero() {
		if _, err := fmt.Fprintf(out, "updated: %s\n", agentSwitch.UpdatedAt.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return nil
}
