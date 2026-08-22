package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type agentListOptions struct {
	refresh bool
	json    bool
}

type agentProbeOptions struct {
	json bool
}

// agentInfo mirrors the daemon's agent Info body for the CLI client.
type agentInfo struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	AuthStatus string `json:"authStatus,omitempty"`
}

// agentInventory mirrors GET /api/v1/agents and POST /api/v1/agents/refresh.
type agentInventory struct {
	Supported  []agentInfo `json:"supported"`
	Installed  []agentInfo `json:"installed"`
	Authorized []agentInfo `json:"authorized"`
}

func newAgentCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Inspect agent catalog readiness",
	}
	cmd.AddCommand(newAgentListCommand(ctx))
	cmd.AddCommand(newAgentProbeCommand(ctx))
	return cmd
}

func newAgentListCommand(ctx *commandContext) *cobra.Command {
	var opts agentListOptions
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List supported agents and local auth readiness",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			inv, err := ctx.fetchAgentInventory(cmd.Context(), opts.refresh)
			if err != nil {
				return err
			}
			if opts.json {
				return writeJSON(cmd.OutOrStdout(), inv)
			}
			return writeAgentList(cmd, inv)
		},
	}
	cmd.Flags().BoolVar(&opts.refresh, "refresh", false, "Refresh local install/auth probes before listing")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output raw agent catalog JSON")
	return cmd
}

func newAgentProbeCommand(ctx *commandContext) *cobra.Command {
	var opts agentProbeOptions
	cmd := &cobra.Command{
		Use:   "probe <agent>",
		Short: "Run a fresh readiness probe for one agent",
		Long:  "Probe one supported agent for local install and auth readiness. Results are advisory; spawn remains the authoritative validation point.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return usageError{err}
			}
			if strings.TrimSpace(args[0]) == "" {
				return usageError{errors.New("agent id is required")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return ctx.probeAgent(cmd.Context(), cmd, strings.TrimSpace(args[0]), opts)
		},
	}
	cmd.Flags().BoolVar(&opts.json, "json", false, "Output raw probe JSON")
	return cmd
}

func (c *commandContext) probeAgent(ctx context.Context, cmd *cobra.Command, agentID string, opts agentProbeOptions) error {
	result, err := c.probeSpawnAgent(ctx, agentID)
	if err != nil {
		return err
	}
	if opts.json {
		return writeJSON(cmd.OutOrStdout(), result)
	}
	return writeAgentProbe(cmd, result)
}

func writeAgentProbe(cmd *cobra.Command, result agentProbeResult) error {
	out := cmd.OutOrStdout()
	id := result.Agent.ID
	if id == "" {
		id = "(unknown)"
	}
	label := result.Agent.Label
	if label == "" {
		label = id
	}
	install := "not installed"
	if result.Installed {
		install = "installed"
	}
	supported := "unsupported"
	if result.Supported {
		supported = "supported"
	}
	auth := result.Agent.AuthStatus
	if auth == "" {
		auth = "unknown"
	}
	lines := []string{
		fmt.Sprintf("agent: %s", id),
		fmt.Sprintf("label: %s", label),
		fmt.Sprintf("supported: %s", supported),
		fmt.Sprintf("install: %s", install),
		fmt.Sprintf("auth: %s", auth),
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return err
		}
	}
	return nil
}

func writeAgentList(cmd *cobra.Command, inv agentInventory) error {
	out := cmd.OutOrStdout()
	if len(inv.Supported) == 0 {
		_, err := fmt.Fprintln(out, "No agents supported by this daemon.")
		return err
	}

	sort.Slice(inv.Supported, func(i, j int) bool {
		return inv.Supported[i].ID < inv.Supported[j].ID
	})
	installed := agentInfoByID(inv.Installed)
	authorized := agentInfoByID(inv.Authorized)

	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tLABEL\tINSTALL\tAUTH"); err != nil {
		return err
	}
	for _, info := range inv.Supported {
		installLabel := "needs install"
		authLabel := "auth unknown"
		if installedInfo, ok := installed[info.ID]; ok {
			installLabel = "installed"
			switch installedInfo.AuthStatus {
			case "authorized":
				authLabel = "authorized"
			case "unauthorized":
				authLabel = "needs auth"
			default:
				authLabel = "auth unknown"
			}
		}
		if _, ok := authorized[info.ID]; ok {
			installLabel = "installed"
			authLabel = "authorized"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", info.ID, info.Label, installLabel, authLabel); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func agentInfoByID(infos []agentInfo) map[string]agentInfo {
	out := make(map[string]agentInfo, len(infos))
	for _, info := range infos {
		out[info.ID] = info
	}
	return out
}
