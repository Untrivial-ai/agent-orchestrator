package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type researchRun struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Result   string `json:"result"`
	Error    string `json:"error"`
	Approval *struct {
		RequestID string `json:"requestId"`
		Summary   string `json:"summary"`
		Options   []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"options"`
	} `json:"approval,omitempty"`
	Agent       string `json:"agent"`
	AgentConfig struct {
		Model  string `json:"model"`
		Effort string `json:"effort"`
	} `json:"agentConfig"`
}

type researchRunResponse struct {
	Research researchRun `json:"research"`
}

type listResearchRunsResponse struct {
	Research []researchRun `json:"research"`
}

func researchParent(explicit string) (string, error) {
	id := strings.TrimSpace(explicit)
	if id == "" {
		id = strings.TrimSpace(os.Getenv("AO_SESSION_ID"))
	}
	if id == "" {
		return "", usageError{errors.New("orchestrator session id is required (pass --session or set AO_SESSION_ID)")}
	}
	return id, nil
}

func researchPath(parent, id string) string {
	path := "sessions/" + url.PathEscape(parent) + "/research"
	if id != "" {
		path += "/" + url.PathEscape(id)
	}
	return path
}

func newResearchCommand(client *commandContext) *cobra.Command {
	var session string
	var detach, jsonOutput bool
	cmd := &cobra.Command{
		Use:   "research <question>",
		Short: "Ask the project's researcher to investigate the repository",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			parent, err := researchParent(session)
			if err != nil {
				return err
			}
			question := strings.TrimSpace(args[0])
			if question == "" {
				return usageError{errors.New("research question must not be blank")}
			}
			var started researchRunResponse
			if err := client.postJSON(cmd.Context(), researchPath(parent, ""), map[string]string{"prompt": question}, &started); err != nil {
				return err
			}
			if started.Research.ID == "" {
				return errors.New("daemon returned no research id")
			}
			if detach {
				if jsonOutput {
					return writeJSON(cmd.OutOrStdout(), started)
				}
				_, err := fmt.Fprintln(cmd.OutOrStdout(), started.Research.ID)
				return err
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Research run: %s\n", started.Research.ID)
			finished, err := client.waitResearch(cmd.Context(), parent, started.Research.ID)
			if err != nil {
				if cmd.Context().Err() != nil {
					cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), 2*time.Second)
					defer cancel()
					var ignored researchRunResponse
					if cancelErr := client.deleteJSON(cancelCtx, researchPath(parent, started.Research.ID), &ignored); cancelErr != nil {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Could not cancel research %s: %v\n", started.Research.ID, cancelErr)
					}
				}
				return err
			}
			return printResearch(cmd, parent, finished, jsonOutput)
		},
	}
	cmd.Flags().StringVar(&session, "session", "", "Orchestrator session id (default: AO_SESSION_ID)")
	cmd.Flags().BoolVar(&detach, "detach", false, "Start research and print its id without waiting")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print JSON")
	cmd.AddCommand(newResearchGetCommand(client))
	cmd.AddCommand(newResearchListCommand(client))
	cmd.AddCommand(newResearchCancelCommand(client))
	cmd.AddCommand(newResearchApproveCommand(client))
	return cmd
}

func (c *commandContext) waitResearch(ctx context.Context, parent, id string) (researchRun, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var response researchRunResponse
		if err := c.getJSON(ctx, researchPath(parent, id), &response); err != nil {
			return researchRun{}, err
		}
		switch response.Research.Status {
		case "completed", "failed", "cancelled", "interrupted":
			return response.Research, nil
		case "queued", "running":
		default:
			return researchRun{}, fmt.Errorf("research %s returned unknown status %q", id, response.Research.Status)
		}
		if response.Research.Approval != nil {
			return response.Research, nil
		}
		select {
		case <-ctx.Done():
			return researchRun{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func printResearch(cmd *cobra.Command, parent string, run researchRun, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), researchRunResponse{Research: run})
	}
	if run.Approval != nil {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Research %s requests approval: %s\n", run.ID, run.Approval.Summary); err != nil {
			return err
		}
		for _, option := range run.Approval.Options {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "  %s\t%s\n", option.ID, option.Label); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Reply with: ao research approve %s %s <option-id> --session %s\nThen resume with: ao research get %s --wait --session %s\n", run.ID, run.Approval.RequestID, parent, run.ID, parent)
		return err
	}
	if run.Status == "queued" || run.Status == "running" {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", run.ID, run.Status)
		return err
	}
	if run.Status != "completed" {
		return fmt.Errorf("research %s %s: %s", run.ID, run.Status, run.Error)
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), run.Result)
	return err
}

func newResearchGetCommand(client *commandContext) *cobra.Command {
	var session string
	var wait, jsonOutput bool
	cmd := &cobra.Command{
		Use: "get <research-id>", Short: "Get a research run and its report",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			parent, err := researchParent(session)
			if err != nil {
				return err
			}
			if wait {
				run, err := client.waitResearch(cmd.Context(), parent, args[0])
				if err != nil {
					return err
				}
				return printResearch(cmd, parent, run, jsonOutput)
			}
			var response researchRunResponse
			if err := client.getJSON(cmd.Context(), researchPath(parent, args[0]), &response); err != nil {
				return err
			}
			return printResearch(cmd, parent, response.Research, jsonOutput)
		},
	}
	cmd.Flags().StringVar(&session, "session", "", "Orchestrator session id (default: AO_SESSION_ID)")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait for the report")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print JSON")
	return cmd
}

func newResearchListCommand(client *commandContext) *cobra.Command {
	var session string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use: "ls", Short: "List an orchestrator's research runs",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			parent, err := researchParent(session)
			if err != nil {
				return err
			}
			var response listResearchRunsResponse
			if err := client.getJSON(cmd.Context(), researchPath(parent, ""), &response); err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd.OutOrStdout(), response)
			}
			for _, run := range response.Research {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", run.ID, run.Status, run.Agent); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&session, "session", "", "Orchestrator session id (default: AO_SESSION_ID)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print JSON")
	return cmd
}

func newResearchCancelCommand(client *commandContext) *cobra.Command {
	var session string
	cmd := &cobra.Command{
		Use: "cancel <research-id>", Short: "Cancel a research run",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			parent, err := researchParent(session)
			if err != nil {
				return err
			}
			var response researchRunResponse
			if err := client.deleteJSON(cmd.Context(), researchPath(parent, args[0]), &response); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Cancellation requested for research %s\n", response.Research.ID)
			return err
		},
	}
	cmd.Flags().StringVar(&session, "session", "", "Orchestrator session id (default: AO_SESSION_ID)")
	return cmd
}

func newResearchApproveCommand(client *commandContext) *cobra.Command {
	var session string
	cmd := &cobra.Command{
		Use: "approve <research-id> <request-id> <option-id>", Short: "Answer a researcher's approval request",
		Args: usageArgs(cobra.ExactArgs(3)),
		RunE: func(cmd *cobra.Command, args []string) error {
			parent, err := researchParent(session)
			if err != nil {
				return err
			}
			var response researchRunResponse
			if err := client.postJSON(cmd.Context(), researchPath(parent, args[0])+"/approval", map[string]string{
				"requestId": args[1], "optionId": args[2],
			}, &response); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Approval answered for research %s\n", response.Research.ID)
			return err
		},
	}
	cmd.Flags().StringVar(&session, "session", "", "Orchestrator session id (default: AO_SESSION_ID)")
	return cmd
}
