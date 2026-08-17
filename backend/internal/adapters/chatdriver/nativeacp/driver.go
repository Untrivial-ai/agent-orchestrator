// Package nativeacp binds a user-installed AO agent plugin to the reusable ACP
// transport. It contains the invariant shared by native ACP harnesses: AO never
// downloads, packages, or substitutes the provider CLI.
package nativeacp

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	acpdriver "github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/acp"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Plugin is the existing agent-plugin surface native ACP bindings reuse for
// installation discovery and authentication.
type Plugin interface {
	ResolveBinary(context.Context) (string, error)
	AuthStatus(context.Context) (ports.AgentAuthStatus, error)
}

// Configure adds provider-native arguments and environment overlays to one ACP
// launch. The returned environment is merged over the session environment.
type Configure func(acpdriver.LaunchConfig) (args []string, env map[string]string, err error)

// VersionProbe checks the resolved binary's version. If set, the probe runs
// after binary resolution and before auth status; a returned error fails the
// probe with ErrChatDriverIncompatible. The string argument is the resolved
// binary path.
type VersionProbe func(ctx context.Context, bin string) error

// Config describes the small provider-specific portion of a native ACP binding.
type Config struct {
	Harness        domain.AgentHarness
	Capabilities   ports.ChatCapabilities
	Configure      Configure
	SessionMode    func(ports.PermissionMode) string
	SessionOptions func(ports.ChatTurnSettings) []acpdriver.SessionOption
	// VersionProbe optionally gates admission on a minimum binary version.
	VersionProbe VersionProbe
}

// New creates a Chat driver that launches the exact executable resolved by the
// supplied agent plugin. There is deliberately no command override or fallback
// download path in this package.
func New(plugin Plugin, cfg Config, log *slog.Logger) ports.ChatDriver {
	return acpdriver.New(buildConfig(plugin, cfg, log), log)
}

func buildConfig(plugin Plugin, cfg Config, log *slog.Logger) acpdriver.Config {
	capabilities := ports.ChatCapabilities{
		ports.ChatCapabilityStreaming: true,
		ports.ChatCapabilityTools:     true,
		ports.ChatCapabilityApprovals: true,
		ports.ChatCapabilityInterrupt: true,
		ports.ChatCapabilityResume:    true,
	}
	for capability, enabled := range cfg.Capabilities {
		capabilities[capability] = enabled
	}

	return acpdriver.Config{
		Harness:      cfg.Harness,
		Capabilities: capabilities,
		Probe: func(ctx context.Context) error {
			if cfg.Configure == nil {
				return fmt.Errorf("%w: incomplete native ACP binding for %s",
					ports.ErrChatDriverUnavailable, cfg.Harness)
			}
			bin, err := plugin.ResolveBinary(ctx)
			if err != nil {
				return fmt.Errorf("%w: %w", ports.ErrChatDriverUnavailable, err)
			}
			if cfg.VersionProbe != nil {
				versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				versionErr := cfg.VersionProbe(versionCtx, bin)
				cancel()
				if versionErr != nil {
					return fmt.Errorf("%w: %w", ports.ErrChatDriverIncompatible, versionErr)
				}
			}
			status, err := plugin.AuthStatus(ctx)
			if err == nil && status == ports.AgentAuthStatusUnauthorized {
				return ports.ErrChatAuthRequired
			}
			if err != nil && log != nil {
				// An inconclusive local probe is not evidence that a user-installed
				// agent cannot run. The ACP handshake remains authoritative.
				log.Debug("native ACP auth probe inconclusive; continuing",
					"harness", cfg.Harness, "error", err)
			}
			return nil
		},
		Launch: func(ctx context.Context, launchCfg acpdriver.LaunchConfig) (acpdriver.Launch, error) {
			if cfg.Configure == nil {
				return acpdriver.Launch{}, fmt.Errorf("%w: incomplete native ACP binding for %s",
					ports.ErrChatDriverUnavailable, cfg.Harness)
			}
			binary, err := plugin.ResolveBinary(ctx)
			if err != nil {
				return acpdriver.Launch{}, fmt.Errorf("%w: %w", ports.ErrChatDriverUnavailable, err)
			}
			args, overrides, err := cfg.Configure(launchCfg)
			if err != nil {
				return acpdriver.Launch{}, err
			}
			env := make(map[string]string, len(launchCfg.Env)+len(overrides))
			for key, value := range launchCfg.Env {
				env[key] = value
			}
			for key, value := range overrides {
				env[key] = value
			}
			return acpdriver.Launch{
				Command: binary,
				Args:    append([]string(nil), args...),
				Env:     env,
			}, nil
		},
		SessionMode:    cfg.SessionMode,
		SessionOptions: cfg.SessionOptions,
	}
}
