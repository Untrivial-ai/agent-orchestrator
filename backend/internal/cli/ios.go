package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

func newIOSCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{Use: "ios", Short: "Inspect the iOS Simulator toolchain (macOS only)", Args: noArgs}
	cmd.AddCommand(&cobra.Command{Use: "toolchain status", Short: "Show Xcode and Simulator runtime status", Args: noArgs, RunE: func(cmd *cobra.Command, _ []string) error { return iosToolchainStatus(ctx, cmd) }})
	return cmd
}

func iosToolchainStatus(ctx *commandContext, cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, fmt.Sprintf("http://%s:%d/api/v1/ios-device/toolchain/status", config.LoopbackHost, cfg.Port), http.NoBody)
	if err != nil {
		return err
	}
	resp, err := ctx.deps.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("daemon request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("daemon returned HTTP %d: %s", resp.StatusCode, body)
	}
	var status struct {
		XcodeDetected           bool `json:"xcodeDetected"`
		CLTOnly                 bool `json:"cltOnly"`
		SimctlAvailable         bool `json:"simctlAvailable"`
		DefaultRuntimeAvailable bool `json:"defaultRuntimeAvailable"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	state := "Xcode not installed"
	if status.XcodeDetected {
		state = "Xcode installed"
	} else if status.CLTOnly {
		state = "Command Line Tools only"
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "AO iOS toolchain: %s (simctl=%t, runtime=%t)\n", state, status.SimctlAvailable, status.DefaultRuntimeAvailable)
	return err
}
