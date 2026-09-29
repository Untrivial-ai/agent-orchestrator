package cli

import (
	"errors"

	"github.com/spf13/cobra"

	unrealchat "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/unrealagent"
)

func newUnrealProviderCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "unreal-provider <config-file>",
		Short:  "Run the embedded Unreal Agent provider (internal)",
		Hidden: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return usageError{errors.New("unreal-provider requires one config file")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return unrealchat.RunProvider(cmd.Context(), args[0], cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}
