package unrealagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/processenv"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type plugin interface {
	AuthStatus(context.Context) (ports.AgentAuthStatus, error)
}

type environmentAuthPlugin interface {
	AuthStatusWithEnv(context.Context, map[string]string) (ports.AgentAuthStatus, error)
}

type connectHostFunc func(context.Context, persistenthost.Config) (*persistenthost.Transport, error)

// Driver embeds Unreal Agent as a persistent AO Chat provider.
type Driver struct {
	plugin       plugin
	log          *slog.Logger
	executable   func() (string, error)
	connectHost  connectHostFunc
	shutdownHost func(context.Context, string, string) error
}

// New creates the built-in Unreal Agent Chat adapter.
func New(plugin plugin, log *slog.Logger) *Driver {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Driver{
		plugin: plugin, log: log, executable: os.Executable,
		connectHost: persistenthost.ConnectOrStart, shutdownHost: persistenthost.Shutdown,
	}
}

var _ ports.ChatDriver = (*Driver)(nil)

// Harness returns the domain harness owned by this driver.
func (*Driver) Harness() domain.AgentHarness { return domain.HarnessUnreal }

func capabilities() ports.ChatCapabilities {
	return ports.ChatCapabilities{
		ports.ChatCapabilityStreaming: true,
		ports.ChatCapabilityTools:     true,
		ports.ChatCapabilityInterrupt: true,
		ports.ChatCapabilityResume:    true,
		ports.ChatCapabilityUsage:     true,
	}
}

// Probe verifies platform support, the embedded executable, and provider auth.
func (d *Driver) Probe(ctx context.Context) (ports.ChatCapabilities, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil, fmt.Errorf("%w: Unreal Agent supports macOS and Linux", ports.ErrChatDriverUnavailable)
	}
	if d.plugin == nil {
		return nil, fmt.Errorf("%w: Unreal Agent plugin is unavailable", ports.ErrChatDriverUnavailable)
	}
	if _, err := d.executable(); err != nil {
		return nil, fmt.Errorf("%w: locate AO executable: %w", ports.ErrChatDriverUnavailable, err)
	}
	status, err := d.plugin.AuthStatus(ctx)
	if err != nil {
		d.log.Debug("Unreal Agent auth probe inconclusive; continuing", "error", err)
	} else if status == ports.AgentAuthStatusUnauthorized {
		// Probe has no project context. A project-scoped provider key may still
		// make the launch valid, so defer the authoritative check to Start/Resume.
		d.log.Debug("Unreal Agent auth unavailable in daemon environment; deferring to launch environment")
	}
	return capabilities(), nil
}

// Start opens a fresh persistent Unreal conversation.
func (d *Driver) Start(ctx context.Context, cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
	if err := validateStart(cfg.WorkspacePath, cfg.Permissions, cfg.ReadOnly, cfg.AdditionalDirectories, cfg.MCPServers); err != nil {
		return nil, err
	}
	if err := d.validateLaunchAuth(ctx, cfg.Env); err != nil {
		return nil, err
	}
	providerID := uuid.NewString()
	conv, err := d.connect(ctx, providerConfig{
		AOSessionID: string(cfg.SessionID), ProviderConversationID: providerID,
		DataDir: cfg.DataDir, WorkspacePath: cfg.WorkspacePath,
		Model: strings.TrimSpace(cfg.Model), Effort: normalizeEffort(cfg.Effort),
		SystemPrompt: cfg.SystemPrompt,
	}, cfg.Env, cfg.PrepareEnv, cfg.ProviderScopeID)
	if err != nil {
		return nil, err
	}
	if conv.reconnected {
		_ = conv.Close()
		return nil, fmt.Errorf("%w: fresh Unreal Agent start found a live provider", ports.ErrChatRecoveryInconclusive)
	}
	return conv, nil
}

// Resume reconnects to a live host or resumes Unreal's durable native session.
func (d *Driver) Resume(ctx context.Context, cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
	if strings.TrimSpace(cfg.ProviderConversationID) == "" {
		return nil, fmt.Errorf("%w: Unreal Agent conversation id is empty", ports.ErrChatResumeFailed)
	}
	if err := validateStart(cfg.WorkspacePath, cfg.Permissions, cfg.ReadOnly, cfg.AdditionalDirectories, cfg.MCPServers); err != nil {
		return nil, err
	}
	if err := d.validateLaunchAuth(ctx, cfg.Env); err != nil {
		return nil, err
	}
	conv, err := d.connect(ctx, providerConfig{
		AOSessionID: string(cfg.SessionID), ProviderConversationID: cfg.ProviderConversationID,
		DataDir: cfg.DataDir, WorkspacePath: cfg.WorkspacePath,
		Model: strings.TrimSpace(cfg.Model), Effort: normalizeEffort(cfg.Effort),
		SystemPrompt: cfg.SystemPrompt,
	}, cfg.Env, cfg.PrepareEnv, cfg.ProviderScopeID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ports.ErrChatResumeFailed, err)
	}
	return conv, nil
}

func validateStart(workspace string, permissions ports.PermissionMode, readOnly bool, additional []string, mcp []ports.ChatMCPServerConfig) error {
	if !filepath.IsAbs(workspace) {
		return fmt.Errorf("workspace path must be absolute, got %q", workspace)
	}
	if ports.NormalizePermissionMode(permissions) != ports.PermissionModeBypassPermissions {
		return errors.New("unreal agent currently requires bypass-permissions because its harness has no interactive approval channel")
	}
	if readOnly {
		return errors.New("unreal agent does not provide a read-only sandbox")
	}
	if len(additional) != 0 {
		return errors.New("unreal agent does not support additional workspace directories")
	}
	if len(mcp) != 0 {
		return errors.New("unreal agent does not support AO-supplied MCP servers")
	}
	return nil
}

func normalizeEffort(value string) string {
	switch strings.TrimSpace(value) {
	case "low", "medium", "high", "xhigh", "max":
		return strings.TrimSpace(value)
	default:
		return "high"
	}
}

func (d *Driver) validateLaunchAuth(ctx context.Context, env map[string]string) error {
	checker, ok := d.plugin.(environmentAuthPlugin)
	if !ok {
		return nil
	}
	status, err := checker.AuthStatusWithEnv(ctx, env)
	if err != nil {
		d.log.Debug("Unreal Agent launch auth probe inconclusive; continuing", "error", err)
		return nil
	}
	if status == ports.AgentAuthStatusUnauthorized {
		return ports.ErrChatAuthRequired
	}
	return nil
}

func (d *Driver) connect(
	ctx context.Context,
	cfg providerConfig,
	env map[string]string,
	prepareEnv func(context.Context) (map[string]string, error),
	providerScopeID string,
) (*conversation, error) {
	owner, _ := json.Marshal([]string{filepath.Clean(cfg.WorkspacePath), string(domain.HarnessUnreal), providerScopeID})
	fingerprint := sha256.Sum256(owner)
	hostConfig := persistenthost.Config{
		SessionID: cfg.AOSessionID, DataDir: cfg.DataDir, Workdir: cfg.WorkspacePath,
		Protocol: persistenthost.ProtocolUnreal, OwnershipFingerprint: hex.EncodeToString(fingerprint[:]),
		Prepare: func(prepareCtx context.Context) (persistenthost.PreparedProvider, error) {
			launchEnv := env
			if prepareEnv != nil {
				var err error
				launchEnv, err = prepareEnv(prepareCtx)
				if err != nil {
					return persistenthost.PreparedProvider{}, err
				}
			}
			executable, err := d.executable()
			if err != nil {
				return persistenthost.PreparedProvider{}, fmt.Errorf("locate AO executable: %w", err)
			}
			configPath, err := writeProviderConfig(cfg)
			if err != nil {
				return persistenthost.PreparedProvider{}, err
			}
			return persistenthost.PreparedProvider{
				Env:  processenv.Merge(launchEnv),
				Argv: []string{executable, "unreal-provider", configPath},
			}, nil
		},
	}
	transport, err := d.connectHost(ctx, hostConfig)
	if err != nil {
		if errors.Is(err, persistenthost.ErrOwnershipInconclusive) ||
			errors.Is(err, persistenthost.ErrAttached) ||
			errors.Is(err, persistenthost.ErrIncompatible) ||
			errors.Is(err, persistenthost.ErrUnauthorized) {
			return nil, fmt.Errorf("%w: persistent Unreal Agent host: %w", ports.ErrChatRecoveryInconclusive, err)
		}
		return nil, fmt.Errorf("%w: persistent Unreal Agent host: %w", ports.ErrChatDriverUnavailable, err)
	}
	conv := newConversation(cfg, transport, d.log, d.shutdownHost)
	if !transport.Reconnected {
		if err := conv.waitReady(ctx); err != nil {
			_ = conv.Terminate()
			return nil, err
		}
	}
	replayCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := conv.request(replayCtx, command{Type: "replay"}); err != nil {
		if conv.reconnected {
			_ = conv.Close()
		} else {
			_ = conv.Terminate()
		}
		return nil, fmt.Errorf("replay Unreal Agent events: %w", err)
	}
	return conv, nil
}
