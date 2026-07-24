package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/spf13/cobra"
)

// setUserConfigRequest mirrors userconfigsvc.SetUserConfigInput for
// PUT /api/v1/user-config.
type setUserConfigRequest struct {
	AgentConfig agentConfig `json:"agentConfig"`
}

// userConfigResponse mirrors controllers.UserConfigResponse for
// GET/PUT /api/v1/user-config.
type userConfigResponse struct {
	AgentConfig agentConfig `json:"agentConfig"`
}

type userConfigSetOptions struct {
	model      string
	permission string
	configJSON string
	clear      bool
	json       bool
}

func newUserConfigCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user-config",
		Short: "Manage the user-scoped agent config (the default profile every project inherits)",
		Long: "Get or set the user-scope agent config — the lowest-precedence scope, inherited " +
			"by every project unless a project overrides a field. Fields not set here fall through " +
			"to the agent's built-in defaults. Set fields via flags, pass the whole object with " +
			"--config-json, or --clear to remove the config entirely.\n\n" +
			"This is a foundation layer; it does not affect worker resolution until a follow-up " +
			"wires the merge.",
	}
	cmd.AddCommand(newUserConfigGetCommand(ctx))
	cmd.AddCommand(newUserConfigSetCommand(ctx))
	return cmd
}

func newUserConfigGetCommand(ctx *commandContext) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show the user-scoped agent config",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var res userConfigResponse
			if err := ctx.getJSON(cmd.Context(), "user-config", &res); err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			if reflect.DeepEqual(res.AgentConfig, agentConfig{}) {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "(unset)")
				return err
			}
			return writeUserConfig(cmd.OutOrStdout(), res.AgentConfig)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Output the config as JSON")
	return cmd
}

func newUserConfigSetCommand(ctx *commandContext) *cobra.Command {
	var opts userConfigSetOptions
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Replace the user-scoped agent config",
		Long: "Replace the user-scope agent config wholesale. Set fields via flags, pass the " +
			"whole agentConfig object with --config-json, or --clear to remove the config.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := buildUserConfig(opts)
			if err != nil {
				return err
			}
			req := setUserConfigRequest{AgentConfig: cfg}
			var res userConfigResponse
			if err := ctx.putJSON(cmd.Context(), "user-config", req, &res); err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), res)
			}
			if reflect.DeepEqual(res.AgentConfig, agentConfig{}) {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "cleared user-scoped config")
				return err
			}
			return writeUserConfig(cmd.OutOrStdout(), res.AgentConfig)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.model, "model", "", "Agent model override (e.g. claude-opus-4-8)")
	f.StringVar(&opts.permission, "permission", "", "Permission mode: default, accept-edits, auto, bypass-permissions")
	f.StringVar(&opts.configJSON, "config-json", "", "Full agentConfig as a JSON object (overrides field flags)")
	f.BoolVar(&opts.clear, "clear", false, "Clear the user-scoped config")
	f.BoolVar(&opts.json, "json", false, "Output the updated config as JSON")
	return cmd
}

// buildUserConfig turns the set flags into the typed agentConfig sent to the
// daemon. --clear empties the config; --config-json supplies the whole object;
// otherwise the field flags form it. The daemon validates the values.
func buildUserConfig(opts userConfigSetOptions) (agentConfig, error) {
	if opts.clear {
		return agentConfig{}, nil
	}
	if opts.configJSON != "" {
		var cfg agentConfig
		if err := json.Unmarshal([]byte(opts.configJSON), &cfg); err != nil {
			return agentConfig{}, usageError{fmt.Errorf("--config-json is not a valid JSON object: %w", err)}
		}
		return cfg, nil
	}
	cfg := agentConfig{Model: strings.TrimSpace(opts.model), Permissions: strings.TrimSpace(opts.permission)}
	if reflect.DeepEqual(cfg, agentConfig{}) {
		return agentConfig{}, usageError{errors.New("usage: provide at least one flag (--model, --permission), --config-json, or --clear")}
	}
	return cfg, nil
}

// writeUserConfig prints the agent config as labeled lines.
func writeUserConfig(w interface{ Write(p []byte) (int, error) }, cfg agentConfig) error {
	var b strings.Builder
	if cfg.Model != "" {
		fmt.Fprintf(&b, "model\t%s\n", cfg.Model)
	}
	if cfg.Permissions != "" {
		fmt.Fprintf(&b, "permissions\t%s\n", cfg.Permissions)
	}
	_, err := w.Write([]byte(b.String()))
	return err
}
