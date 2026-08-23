package cli

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

// connectStatusDTO mirrors controllers.MobileStatusResponse — the loopback
// Connect Mobile bridge's status/enable/disable/regenerate response shape.
// ao connect is a thin client over those routes (they are loopback-only by
// design, and this deployment has no Electron settings UI to drive them from).
type connectStatusDTO struct {
	Enabled       bool   `json:"enabled"`
	Host          string `json:"host"`
	TailscaleHost string `json:"tailscaleHost"`
	Port          int    `json:"port"`
	Password      string `json:"password"`
	Warning       string `json:"warning"`
}

func newConnectCommand(ctx *commandContext) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Manage the daemon's LAN/tailnet bridge (Connect Mobile)",
		Long: "Enable, disable, or inspect the daemon's network-facing bridge. This is a thin\n" +
			"client over the loopback-only /api/v1/mobile/* routes: they exist for the\n" +
			"desktop app's settings UI, and this command is the equivalent for a headless\n" +
			"install with no Electron UI to drive them from.",
		Args: noArgs,
	}
	cmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "print the structured response as JSON")

	cmd.AddCommand(&cobra.Command{
		Use:   "enable",
		Short: "Turn on the bridge and print the connection password",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out connectStatusDTO
			if err := ctx.doJSONPath(cmd.Context(), http.MethodPost, "/api/v1/mobile/enable", nil, &out); err != nil {
				return err
			}
			return writeConnectStatus(cmd, out, jsonOutput)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "disable",
		Short: "Turn off the bridge",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out connectStatusDTO
			if err := ctx.doJSONPath(cmd.Context(), http.MethodPost, "/api/v1/mobile/disable", nil, &out); err != nil {
				return err
			}
			return writeConnectStatus(cmd, out, jsonOutput)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show whether the bridge is enabled and the reachable URL",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out connectStatusDTO
			if err := ctx.doJSONPath(cmd.Context(), http.MethodGet, "/api/v1/mobile/status", nil, &out); err != nil {
				return err
			}
			return writeConnectStatus(cmd, out, jsonOutput)
		},
	})

	passwordCmd := &cobra.Command{
		Use:   "password",
		Short: "Show or regenerate the connection password",
		Args:  noArgs,
	}
	var regenerate bool
	passwordCmd.Flags().BoolVar(&regenerate, "regenerate", false, "rotate the password (revokes every existing web session)")
	passwordCmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if !regenerate {
			var out connectStatusDTO
			if err := ctx.doJSONPath(cmd.Context(), http.MethodGet, "/api/v1/mobile/status", nil, &out); err != nil {
				return err
			}
			if !out.Enabled {
				return usageError{fmt.Errorf("bridge is disabled; run 'ao connect enable' first")}
			}
			// The status route never returns the password for an already-enabled
			// bridge (it's transient, on enable/regenerate only) — regenerate is
			// the only way to see it again without re-enabling.
			return usageError{fmt.Errorf("the password is only shown on enable or --regenerate; use one of those")}
		}
		var out connectStatusDTO
		if err := ctx.doJSONPath(cmd.Context(), http.MethodPost, "/api/v1/mobile/regenerate", nil, &out); err != nil {
			return err
		}
		return writeConnectStatus(cmd, out, jsonOutput)
	}
	cmd.AddCommand(passwordCmd)

	return cmd
}

func writeConnectStatus(cmd *cobra.Command, out connectStatusDTO, jsonOutput bool) error {
	if jsonOutput {
		return writeJSON(cmd.OutOrStdout(), out)
	}
	state := "disabled"
	if out.Enabled {
		state = "enabled"
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Bridge: %s\n", state); err != nil {
		return err
	}
	if !out.Enabled {
		return nil
	}
	host := out.Host
	if out.TailscaleHost != "" {
		host = out.TailscaleHost
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "URL: http://%s:%d\n", host, out.Port); err != nil {
		return err
	}
	if out.Password != "" {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Password: %s\n", out.Password); err != nil {
			return err
		}
	}
	if out.Warning != "" {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Warning: %s\n", out.Warning)
		return err
	}
	return nil
}
