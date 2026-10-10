package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type testingLegStartedDTO struct {
	testingAttemptStartedDTO
	Leg           string                     `json:"leg"`
	CommitSHA     string                     `json:"commitSha"`
	EvidenceDir   string                     `json:"evidenceDir"`
	TargetContext ports.TestingWorkerContext `json:"targetContext"`
}

func newTestingLegCommand(ctx *commandContext) *cobra.Command {
	var jsonOutput bool
	leg := &cobra.Command{Use: "leg", Short: "Switch revisions in the current testing worker"}
	start := &cobra.Command{Use: "start base|head", Short: "Clean up the previous target and start a pinned revision", Args: usageArgs(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] != "base" && args[0] != "head" {
			return usageError{errors.New("leg must be base or head")}
		}
		session := strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
		if session == "" {
			return usageError{errors.New("AO_SESSION_ID is required to select the testing worker")}
		}
		var result testingLegStartedDTO
		if err := ctx.doJSONPathWithHeadersAndTimeout(cmd.Context(), http.MethodPost, "/api/v1/testing/sessions/"+url.PathEscape(session)+"/legs/"+args[0]+"/start", struct{}{}, &result, nil, 2*time.Hour); err != nil {
			return err
		}
		if result.WorkerSessionID != session || result.Leg != args[0] || result.RunID == "" || result.AttemptID == "" || result.CommitSHA == "" || result.EvidenceDir == "" {
			return errors.New("daemon returned incomplete leg identifiers")
		}
		if jsonOutput {
			return writeJSON(cmd.OutOrStdout(), result)
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "leg: %s\nrun ID: %s\nattempt ID: %s\nworker session ID: %s\ncommit SHA: %s\nevidence directory: %s\ncheckout: %s\ntarget CLI: %s\nrun file: %s\ndata directory: %s\n", result.Leg, result.RunID, result.AttemptID, result.WorkerSessionID, result.CommitSHA, result.EvidenceDir, result.TargetContext.CheckoutPath, result.TargetContext.CLIPath, result.TargetContext.RunFilePath, result.TargetContext.DataDir)
		return err
	}}
	start.Flags().BoolVar(&jsonOutput, "json", false, "Print JSON including the target's checked launch facts")
	leg.AddCommand(start)
	return leg
}
