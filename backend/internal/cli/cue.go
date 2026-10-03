package cli

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// These DTOs mirror the daemon's Cue API without importing controller types.
type cueDTO struct {
	ID          string `json:"id"`
	ProjectID   string `json:"projectId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Command     string `json:"command"`
	Prompt      string `json:"prompt"`
}

type cueEnvelopeDTO struct {
	Cue cueDTO `json:"cue"`
}

type cueListDTO struct {
	Cues []cueDTO `json:"cues"`
}

type cueCreateDTO struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"`
	Command     string `json:"command,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
}

func newCueCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "cue",
		Aliases: []string{"cues"},
		Short:   "Create and list project Cues",
		Long:    "Create reusable project Cues or list existing Cues. Editing and deleting Cues is available in the desktop app.",
	}
	cmd.AddCommand(newCueCreateCommand(ctx), newCueListCommand(ctx))
	return cmd
}

func newCueCreateCommand(ctx *commandContext) *cobra.Command {
	var project, name, description, command, prompt string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a command or agent Cue",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(name) == "" {
				return usageError{errors.New("--name is required")}
			}
			if (strings.TrimSpace(command) == "") == (strings.TrimSpace(prompt) == "") {
				return usageError{errors.New("provide exactly one of --command or --prompt")}
			}
			resolved, err := ctx.resolveSpawnProject(cmd.Context(), project)
			if err != nil {
				return err
			}
			body := cueCreateDTO{Name: name, Description: description}
			if strings.TrimSpace(command) != "" {
				body.Type, body.Command = "command", command
			} else {
				body.Type, body.Prompt = "agent", prompt
			}
			var response cueEnvelopeDTO
			if err := ctx.postJSON(cmd.Context(), "projects/"+url.PathEscape(resolved.ID)+"/cues", body, &response); err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd.OutOrStdout(), response)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "created %s cue %s (%s) in project %s\n", response.Cue.Type, response.Cue.ID, response.Cue.Name, response.Cue.ProjectID)
			return err
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project id (default: AO_PROJECT_ID, current session, or current registered repo)")
	cmd.Flags().StringVar(&name, "name", "", "Cue name")
	cmd.Flags().StringVar(&description, "description", "", "Optional Cue description")
	cmd.Flags().StringVar(&command, "command", "", "Exact shell command for a command Cue")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Reusable agent instruction for an agent Cue")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print JSON")
	return cmd
}

func newCueListCommand(ctx *commandContext) *cobra.Command {
	var project string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List a project's Cues",
		Args:    usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := ctx.resolveSpawnProject(cmd.Context(), project)
			if err != nil {
				return err
			}
			var response cueListDTO
			if err := ctx.getJSON(cmd.Context(), "projects/"+url.PathEscape(resolved.ID)+"/cues", &response); err != nil {
				return err
			}
			if jsonOutput {
				return writeJSON(cmd.OutOrStdout(), response)
			}
			writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			_, _ = fmt.Fprintln(writer, "ID\tNAME\tTYPE\tDESCRIPTION")
			for _, cue := range response.Cues {
				_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", cue.ID, cue.Name, cue.Type, cue.Description)
			}
			return writer.Flush()
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project id (default: AO_PROJECT_ID, current session, or current registered repo)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print JSON, including Cue definitions")
	return cmd
}
