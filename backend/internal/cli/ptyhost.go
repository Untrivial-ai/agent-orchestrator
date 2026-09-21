package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/runtime/conpty"
)

// newPtyHostCommand registers the "ao pty-host" hidden subcommand that the
// detached runtime spawns on Windows and macOS to host a PTY over loopback TCP.
// DisableFlagParsing ensures agent shell args with leading dashes are not
// consumed by cobra before being passed to RunHost.
func newPtyHostCommand() *cobra.Command {
	return &cobra.Command{
		Use:                "pty-host",
		Short:              "Run a detached pty-host process (internal)",
		Hidden:             true,
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			code := conpty.RunHost(args, os.Stdout)
			if code != 0 {
				os.Exit(code)
			}
			return nil
		},
	}
}
