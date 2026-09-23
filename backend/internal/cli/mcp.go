package cli

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	aomcp "github.com/aoagents/agent-orchestrator/backend/internal/mcp"
)

type mcpOptions struct {
	httpAddr string
}

func newMCPCommand(ctx *commandContext) *cobra.Command {
	var opts mcpOptions
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run the AO MCP server for Claude Code, Cursor, or Codex",
		Long: "Expose the local AO daemon as an MCP server so external agents can list projects,\n" +
			"list sessions, spawn workers, send messages, read recent output, inspect PR/CI status,\n" +
			"and kill sessions.\n\n" +
			"Default transport is stdio (for client config snippets). Pass --http to serve\n" +
			"streamable HTTP on a loopback address instead.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runMCP(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.httpAddr, "http", "", "Serve streamable HTTP on a loopback address (e.g. 127.0.0.1:8787) instead of stdio")
	return cmd
}

type mcpDaemonAPI struct {
	c *commandContext
}

func (a mcpDaemonAPI) GetJSON(ctx context.Context, path string, out any) error {
	return a.c.getJSON(ctx, path, out)
}

func (a mcpDaemonAPI) PostJSON(ctx context.Context, path string, body, out any) error {
	return a.c.postJSON(ctx, path, body, out)
}

func (c *commandContext) runMCP(ctx context.Context, opts mcpOptions) error {
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := log.New(os.Stderr, "ao mcp: ", log.LstdFlags)
	err := aomcp.Run(runCtx, mcpDaemonAPI{c: c}, aomcp.Options{
		Version:    Version,
		ListenAddr: opts.httpAddr,
		Logger:     logger,
	})
	if err != nil && runCtx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("mcp server: %w", err)
	}
	return nil
}
