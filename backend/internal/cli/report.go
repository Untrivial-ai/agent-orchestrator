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
	note                                                     string
	noteSet                                                  bool
	prCreated, artifact, checkpoint, needsInput, stuck, done bool
}

type reportAPIRequest struct {
	SessionID string `json:"sessionId"`
	Type      string `json:"type"`
	Note      string `json:"note"`
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
	f.StringVar(&o.note, "note", "", "Structured report note")
	f.BoolVar(&o.prCreated, "pr-created", false, "Report a created pull request")
	f.BoolVar(&o.artifact, "artifact", false, "Report an artifact reference")
	f.BoolVar(&o.checkpoint, "checkpoint", false, "Report a checkpoint")
	f.BoolVar(&o.needsInput, "needs-input", false, "Report that input is needed")
	f.BoolVar(&o.stuck, "stuck", false, "Report that work is stuck")
	f.BoolVar(&o.done, "done", false, "Report that work is done")
	return cmd
}

func (c *commandContext) report(ctx context.Context, args []string, o reportOptions) error {
	types := []struct {
		on  bool
		typ domain.ReportType
	}{{o.prCreated, domain.ReportPRCreated}, {o.artifact, domain.ReportArtifact}, {o.checkpoint, domain.ReportCheckpoint}, {o.needsInput, domain.ReportNeedsInput}, {o.stuck, domain.ReportStuck}, {o.done, domain.ReportDone}}
	selected := domain.ReportType("")
	count := 0
	for _, v := range types {
		if v.on {
			selected = v.typ
			count++
		}
	}
	freeForm := strings.Join(args, " ")
	if count > 1 {
		return usageError{errors.New("usage: structured report flags are mutually exclusive")}
	}
	if count == 0 {
		if strings.TrimSpace(freeForm) == "" {
			return usageError{errors.New("usage: free-form text or one structured report flag is required")}
		}
		if o.noteSet {
			return usageError{errors.New("usage: --note requires a structured report flag")}
		}
		selected = domain.ReportFreeForm
		o.note = freeForm
	} else {
		if len(args) > 0 {
			return usageError{errors.New("usage: free-form text cannot be combined with a structured report flag")}
		}
		if strings.TrimSpace(o.note) == "" {
			return usageError{errors.New("usage: --note is required for structured reports")}
		}
	}
	if utf8.RuneCountInString(o.note) > domain.MaxReportNoteCharacters {
		return usageError{errors.New("usage: report text must be at most 1000 characters")}
	}
	if selected == domain.ReportPRCreated && !domain.IsGitHubPullRequestURL(o.note) {
		return usageError{errors.New("usage: --pr-created --note must be an HTTP(S) GitHub pull-request URL")}
	}
	sessionID := strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
	if sessionID == "" {
		return usageError{errors.New("usage: AO_SESSION_ID is required")}
	}
	var response reportAPIResponse
	return c.postJSON(ctx, "reports", reportAPIRequest{SessionID: sessionID, Type: string(selected), Note: o.note}, &response)
}
