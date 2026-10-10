package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// These DTOs mirror the daemon's testing API without importing controllers.
type testingRunCreateDTO struct {
	PRURL         string `json:"prUrl,omitempty"`
	ProjectID     string `json:"projectId"`
	IssueURL      string `json:"issueUrl"`
	IssueSnapshot string `json:"issueSnapshot,omitempty"`
	CommitSHA     string `json:"commitSha,omitempty"`
	RecipeID      string `json:"recipeId"`
	Requester     string `json:"requester"`
}

type testingRunDTO struct {
	RunID     string    `json:"runId"`
	HeadRunID string    `json:"headRunId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type testingAttemptStartDTO struct {
	Harness        string `json:"harness,omitempty"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	WorkerPrompt   string `json:"workerPrompt"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

type testingAttemptStartedDTO struct {
	RunID           string `json:"runId"`
	AttemptID       string `json:"attemptId"`
	WorkerSessionID string `json:"workerSessionId"`
}

type testingAttemptDTO struct {
	AttemptID    string `json:"attemptId"`
	Phase        string `json:"phase"`
	Outcome      string `json:"outcome"`
	CleanupState string `json:"cleanupState"`
	RecordingGap string `json:"recordingGap"`
}

type testingEvidenceDTO struct {
	Evidence []domain.TestEvidenceReceipt `json:"evidence"`
}

type testingToolRequestDTO struct {
	SessionID string          `json:"sessionId"`
	RequestID string          `json:"requestId"`
	Input     json.RawMessage `json:"input"`
}

type testingScreenshotDTO struct {
	Screenshot        *domain.TestScreenshot       `json:"screenshot"`
	Action            *domain.TestActionResult     `json:"action,omitempty"`
	ObservationStatus string                       `json:"observationStatus,omitempty"`
	ObservationError  string                       `json:"observationError,omitempty"`
	Evidence          []domain.TestEvidenceReceipt `json:"evidence"`
}

func newTestingCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{Use: "testing", Short: "Manage agentic testing attempts"}
	cmd.AddCommand(newTestingStartCommand(ctx), newTestingStopCommand(ctx), newTestingEvidenceCommand(ctx), newTestingMCPCommand(ctx))
	return cmd
}

func newTestingStartCommand(ctx *commandContext) *cobra.Command {
	var prURL, project, issueFile, issueURL, commit, recipe, promptFile, agent, model, effort string
	var timeout int
	var jsonOutput bool
	cmd := &cobra.Command{
		Use: "start", Short: "Create a test run and start its investigator attempt", Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			required := []struct{ name, value string }{
				{"project", project}, {"issue-file", issueFile}, {"commit", commit}, {"recipe", recipe},
			}
			if prURL != "" {
				if issueFile != "" || issueURL != "" || commit != "" {
					return usageError{errors.New("--pr cannot be combined with --issue-file, --issue-url or --commit")}
				}
				required = required[3:]
			}
			for _, flag := range required {
				if strings.TrimSpace(flag.value) == "" {
					return usageError{fmt.Errorf("--%s is required", flag.name)}
				}
			}
			if timeout < 1 || timeout > 7200 {
				return usageError{errors.New("--timeout must be between 1 and 7200 seconds")}
			}
			requester := strings.TrimSpace(os.Getenv("USER"))
			if requester == "" {
				return usageError{errors.New("USER is required to identify the requester")}
			}
			var issue string
			var err error
			if prURL != "" {
				resolved, e := ctx.resolveSpawnProject(cmd.Context(), project)
				if e != nil {
					return e
				}
				project = resolved.ID
			} else {
				issue, err = readTestingTextFile(issueFile, 256<<10)
				if err != nil {
					return usageError{fmt.Errorf("issue snapshot: %w", err)}
				}
			}
			prompt := "Investigate the supplied issue or pull request."
			if promptFile != "" {
				prompt, err = readTestingTextFile(promptFile, 64<<10)
				if err != nil {
					return usageError{fmt.Errorf("worker prompt: %w", err)}
				}
			}
			var run testingRunDTO
			if err := ctx.postJSON(cmd.Context(), "testing/runs", testingRunCreateDTO{
				PRURL: prURL, ProjectID: project, IssueURL: issueURL, IssueSnapshot: issue, CommitSHA: commit, RecipeID: recipe, Requester: requester,
			}, &run); err != nil {
				return err
			}
			if run.RunID == "" {
				return errors.New("daemon returned a testing run without its ID")
			}
			var attempt testingAttemptStartedDTO
			start := testingAttemptStartDTO{Harness: agent, Model: model, Effort: effort, WorkerPrompt: prompt, TimeoutSeconds: timeout}
			endpoint := "testing/runs/" + url.PathEscape(run.RunID) + "/attempts"
			var body any = start
			if prURL != "" {
				if run.HeadRunID == "" || run.HeadRunID == run.RunID {
					return errors.New("daemon returned a PR comparison without distinct base and head run IDs")
				}
				endpoint = "testing/comparisons"
				body = struct {
					testingAttemptStartDTO
					RunID     string `json:"runId"`
					HeadRunID string `json:"headRunId"`
				}{start, run.RunID, run.HeadRunID}
			}
			if err := ctx.postJSON(cmd.Context(), endpoint, body, &attempt); err != nil {
				return fmt.Errorf("run %s created; start attempt: %w", run.RunID, err)
			}
			if attempt.RunID != run.RunID || attempt.AttemptID == "" || attempt.WorkerSessionID == "" {
				return fmt.Errorf("run %s created; daemon returned incomplete attempt identifiers", run.RunID)
			}
			if jsonOutput {
				return writeJSON(cmd.OutOrStdout(), attempt)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "run ID: %s\nattempt ID: %s\nworker session ID: %s\n", attempt.RunID, attempt.AttemptID, attempt.WorkerSessionID)
			return err
		},
	}
	cmd.Flags().StringVar(&prURL, "pr", "", "GitHub pull request URL; snapshot base and head and start one comparison worker")
	cmd.Flags().StringVar(&project, "project", "", "Project ID; inferred inside AO for --pr")
	cmd.Flags().StringVar(&issueFile, "issue-file", "", "File containing the issue text snapshot, up to 256 KiB")
	cmd.Flags().StringVar(&issueURL, "issue-url", "", "Issue URL")
	cmd.Flags().StringVar(&commit, "commit", "", "Commit SHA under test")
	cmd.Flags().StringVar(&recipe, "recipe", "local-ao", "Configured testing recipe ID")
	cmd.Flags().StringVar(&promptFile, "prompt-file", "", "Optional investigator instructions, up to 64 KiB; the testing skill is always loaded")
	cmd.Flags().StringVar(&agent, "agent", "", "Investigator agent harness, defaults to project configuration")
	cmd.Flags().StringVar(&model, "model", "", "Investigator model, defaults to project configuration")
	cmd.Flags().StringVar(&effort, "effort", "", "Investigator effort, validated by the selected provider")
	cmd.Flags().IntVar(&timeout, "timeout", 1800, "Attempt timeout in seconds, from 1 to 7200")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print JSON")
	return cmd
}

func newTestingStopCommand(ctx *commandContext) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use: "stop <attemptId>", Short: "Cancel an attempt and stop its target", Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return usageError{errors.New("attempt ID is required")}
			}
			var attempt testingAttemptDTO
			if err := ctx.postJSON(cmd.Context(), "testing/attempts/"+url.PathEscape(args[0])+"/cancel", struct{}{}, &attempt); err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd.OutOrStdout(), attempt)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s phase=%s outcome=%s cleanup=%s\n", attempt.AttemptID, attempt.Phase, attempt.Outcome, attempt.CleanupState)
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print JSON")
	return cmd
}

func newTestingEvidenceCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use: "evidence <attemptId>", Short: "Print retained evidence receipts as JSON", Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return usageError{errors.New("attempt ID is required")}
			}
			var evidence testingEvidenceDTO
			if err := ctx.getJSON(cmd.Context(), "testing/attempts/"+url.PathEscape(args[0])+"/evidence", &evidence); err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), evidence)
		},
	}
}

func readTestingTextFile(path string, maxBytes int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("must be a regular text file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxBytes {
		return "", fmt.Errorf("exceeds %d bytes", maxBytes)
	}
	if !utf8.Valid(data) || strings.TrimSpace(string(data)) == "" {
		return "", errors.New("must contain nonempty UTF-8 text")
	}
	return string(data), nil
}
