package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type reportOptions struct {
	note                                string
	noteSet                             bool
	artifacts, prsCreated, prsReviewed  []string
	checkpoint, needsInput, stuck, done bool
}

type reportAPIOutput struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
}

type reportAPIRequest struct {
	SessionID string            `json:"sessionId"`
	State     string            `json:"state,omitempty"`
	Note      string            `json:"note,omitempty"`
	Message   string            `json:"message,omitempty"`
	Outputs   []reportAPIOutput `json:"outputs,omitempty"`
}

type reportAPIResponse struct {
	ID string `json:"id"`
}

func newReportCommand(ctx *commandContext) *cobra.Command {
	var o reportOptions
	cmd := &cobra.Command{Use: "report <free-form text>", Short: "Report worker progress to the orchestrator", Args: cobra.ArbitraryArgs, RunE: func(cmd *cobra.Command, args []string) error {
		o.noteSet = cmd.Flags().Changed("note")
		return ctx.report(cmd.Context(), args, o)
	}}
	f := cmd.Flags()
	f.StringVar(&o.note, "note", "", "Structured state note")
	f.StringArrayVar(&o.artifacts, "artifact", nil, "Attach an opaque artifact reference (repeatable)")
	f.StringArrayVar(&o.prsCreated, "pr-created", nil, "Attach a created GitHub pull request (repeatable)")
	f.StringArrayVar(&o.prsReviewed, "pr-reviewed", nil, "Attach a reviewed GitHub pull request (repeatable)")
	f.BoolVar(&o.checkpoint, "checkpoint", false, "Report a checkpoint")
	f.BoolVar(&o.needsInput, "needs-input", false, "Report that input is needed")
	f.BoolVar(&o.stuck, "stuck", false, "Report that work is stuck")
	f.BoolVar(&o.done, "done", false, "Report that work is done")
	return cmd
}

func (c *commandContext) report(ctx context.Context, args []string, o reportOptions) error {
	states := []struct {
		on    bool
		state domain.ReportState
	}{{o.checkpoint, domain.ReportCheckpoint}, {o.needsInput, domain.ReportNeedsInput}, {o.stuck, domain.ReportStuck}, {o.done, domain.ReportDone}}
	selected := domain.ReportState("")
	count := 0
	for _, candidate := range states {
		if candidate.on {
			selected = candidate.state
			count++
		}
	}
	if count > 1 {
		return usageError{errors.New("usage: state flags are mutually exclusive")}
	}

	outputs := make([]domain.ReportOutput, 0, len(o.artifacts)+len(o.prsCreated)+len(o.prsReviewed))
	for _, reference := range o.artifacts {
		outputs = append(outputs, domain.ReportOutput{Kind: domain.ReportOutputArtifact, Reference: reference})
	}
	for _, reference := range o.prsCreated {
		outputs = append(outputs, domain.ReportOutput{Kind: domain.ReportOutputPRCreated, Reference: reference})
	}
	for _, reference := range o.prsReviewed {
		outputs = append(outputs, domain.ReportOutput{Kind: domain.ReportOutputPRReviewed, Reference: reference})
	}

	if len(args) == 1 && legacyReportStatus(args[0]) {
		return usageError{errors.New("usage: positional report status is unsupported; use a state flag and --note")}
	}
	message := strings.Join(args, " ")
	if len(args) > 0 && (selected != "" || o.noteSet || len(outputs) > 0) {
		return usageError{errors.New("usage: free-form text cannot be combined with --note, a state flag, or output options")}
	}
	if selected != "" && strings.TrimSpace(o.note) == "" {
		return usageError{errors.New("usage: --note is required for structured state reports")}
	}
	if selected == "" && o.noteSet {
		return usageError{errors.New("usage: --note requires a structured state flag")}
	}
	if strings.TrimSpace(message) == "" && selected == "" && len(outputs) == 0 {
		return usageError{errors.New("usage: free-form text, a state flag, or at least one output option is required")}
	}
	if utf8.RuneCountInString(message) > domain.MaxReportTextCharacters || utf8.RuneCountInString(o.note) > domain.MaxReportTextCharacters {
		return usageError{errors.New("usage: report text must be at most 1000 characters")}
	}
	for _, output := range outputs {
		if strings.TrimSpace(output.Reference) == "" {
			return usageError{errors.New("usage: output references must be non-empty")}
		}
		if (output.Kind == domain.ReportOutputPRCreated || output.Kind == domain.ReportOutputPRReviewed) && !domain.IsGitHubPullRequestURL(output.Reference) {
			return usageError{errors.New("usage: --pr-created and --pr-reviewed require HTTP(S) GitHub pull-request URLs")}
		}
	}

	sessionID := strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
	if sessionID == "" {
		return usageError{errors.New("usage: AO_SESSION_ID is required")}
	}
	apiOutputs := make([]reportAPIOutput, len(outputs))
	for i, output := range outputs {
		apiOutputs[i] = reportAPIOutput{Kind: string(output.Kind), Reference: output.Reference}
	}
	var response reportAPIResponse
	return c.postJSON(ctx, "reports", reportAPIRequest{SessionID: sessionID, State: string(selected), Note: o.note, Message: message, Outputs: apiOutputs}, &response)
}

func legacyReportStatus(value string) bool {
	switch strings.ToLower(strings.ReplaceAll(value, "-", "_")) {
	case "working", "checkpoint", "needs_input", "stuck", "done", "pr_created", "pr_reviewed", "artifact":
		return true
	default:
		return false
	}
}
