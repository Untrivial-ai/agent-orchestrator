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

// newIOSCommand creates the `ao ios` command group, which owns the iOS
// Simulator toolchain surface (Track B). The subcommands are thin clients over
// the daemon's /api/v1/ios-device routes, mirroring the Android SDK commands.
func newIOSCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ios",
		Short: "Inspect and manage the iOS Simulator toolchain (macOS only)",
		Args:  noArgs,
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "toolchain status",
		Short: "Show Xcode / simctl install status",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return iosToolchainStatus(ctx, cmd)
		},
	})

	return cmd
}

// iosToolchainStatus queries GET /api/v1/ios-device/toolchain/status and prints
// a concise human-readable report.
func iosToolchainStatus(ctx *commandContext, cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	reqCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet,
		fmt.Sprintf("http://%s:%d/api/v1/ios-device/toolchain/status",
			config.LoopbackHost, cfg.Port), http.NoBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	resp, err := ctx.deps.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("daemon request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("daemon returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var body struct {
		XcodeDetected           bool   `json:"xcodeDetected"`
		CLTOnly                 bool   `json:"cltOnly"`
		SimctlAvailable         bool   `json:"simctlAvailable"`
		DefaultRuntimeAvailable bool   `json:"defaultRuntimeAvailable"`
		GuidanceAppStoreURL     string `json:"guidanceAppStoreURL,omitempty"`
		GuidanceDeveloperURL    string `json:"guidanceDeveloperURL,omitempty"`
		GuidanceWhyMissing      string `json:"guidanceWhyMissing,omitempty"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	// Emit a compact one-line report.
	report := "AO iOS toolchain status: "
	switch {
	case body.XcodeDetected:
		report += "Xcode installed"
	case body.CLTOnly:
		report += "Command Line Tools only"
	default:
		report += "Xcode not installed"
	}
	if !body.XcodeDetected && body.GuidanceAppStoreURL != "" {
		report += fmt.Sprintf(" — Install: %s", body.GuidanceAppStoreURL)
	}

	_, err = fmt.Fprintln(cmd.OutOrStdout(), report)
	return err
}
