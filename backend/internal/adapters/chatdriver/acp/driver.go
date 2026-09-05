// Package acp implements AO's provider-neutral Chat driver over the Agent
// Client Protocol. Provider packages supply only discovery, launch, metadata,
// and capability policy; this package owns the ACP lifecycle and translates ACP
// updates into AO's durable conversation vocabulary.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/persistenthost"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/processenv"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const handshakeTimeout = 60 * time.Second

// Launch describes one ACP agent process. Command may be either AO's packaged
// protocol bridge or the exact user-installed provider executable resolved by
// the existing agent plugin.
type Launch struct {
	Command string
	Args    []string
	Env     map[string]string
}

// LaunchConfig is the resolved AO session context a provider binding may use to
// construct its process. It intentionally contains no install mechanism: binary
// ownership stays with the existing agent plugin.
type LaunchConfig struct {
	SessionID       domain.SessionID
	DataDir         string
	WorkspacePath   string
	Env             map[string]string
	Model           string
	Permissions     ports.PermissionMode
	SystemPrompt    string
	ProviderScopeID string
}

// Config binds one harness to an ACP agent implementation.
type Config struct {
	Harness      domain.AgentHarness
	Capabilities ports.ChatCapabilities
	Probe        func(context.Context) error
	Launch       func(context.Context, LaunchConfig) (Launch, error)
	// ValidateInitialize optionally admits only the tested ACP distribution and
	// version after the protocol handshake identifies it. This is preferable to
	// invoking an adapter-specific version flag, which many stdio agents do not
	// implement and which can emit protocol bytes instead of a version string.
	ValidateInitialize func(acpsdk.InitializeResponse) error
	// SessionMeta carries adapter-defined ACP extensions whenever AO creates the
	// provider-side session object: session/new, session/load, or
	// session/resume. Standing context such as a system prompt is process input,
	// not transcript history, so a resumed native conversation must receive it
	// again even though the provider recovers the messages itself.
	SessionMeta func(LaunchConfig) map[string]any
	// SessionMode maps AO's approval vocabulary onto this ACP agent's mode ids.
	// Empty means "leave the provider/user default unchanged".
	SessionMode func(ports.PermissionMode) string
	// SessionOptions maps AO's per-turn choices onto ACP config option ids.
	SessionOptions func(ports.ChatTurnSettings) []SessionOption
	// PermissionPolicy lets a provider binding resolve permission requests that
	// have an exact AO policy mapping before the generic client parks them for a
	// human. Returning handled=false preserves the ordinary approval flow.
	PermissionPolicy PermissionPolicy
	// ClientExtension handles provider-defined agent-to-client JSON-RPC methods.
	ClientExtension ClientExtensionHandler
	// ClientExtensionAliases maps explicitly supported legacy wire methods onto
	// ACP-compliant underscore extension names handled by the pinned SDK.
	ClientExtensionAliases map[string]string
	// ValidateTurnSettings rejects provider settings that cannot be applied to a
	// live process. The initial permission mode is the launch-time value.
	ValidateTurnSettings TurnSettingsValidator
	// Persistent keeps the provider's initialized ACP connection outside the
	// daemon. Bindings enable it only after their real provider restart gate passes.
	Persistent bool
}

// TurnSettingsValidator validates live turn settings against launch-time state.
type TurnSettingsValidator func(ports.PermissionMode, ports.ChatTurnSettings) error

// PermissionPolicy maps the active AO permission mode and the provider's exact
// offered choices to an automatic response when the mapping is unambiguous.
type PermissionPolicy func(
	ports.PermissionMode,
	acpsdk.RequestPermissionRequest,
) (acpsdk.PermissionOptionId, bool)

// SessionOption is one ACP session configuration selection.
type SessionOption struct {
	ID    string
	Value string
}

// Driver opens ACP conversations for a single harness.
type Driver struct {
	cfg         Config
	log         *slog.Logger
	spawn       spawnFunc
	connectHost func(context.Context, persistenthost.Config) (*persistenthost.Transport, error)
}

var _ ports.ChatDriver = (*Driver)(nil)

// New returns an ACP Chat driver from a provider binding.
func New(cfg Config, log *slog.Logger) *Driver {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Driver{cfg: cfg, log: log, spawn: spawnAgent, connectHost: persistenthost.ConnectOrStart}
}

// DiscoverConfigOptions opens a short-lived ACP session and returns the
// provider-owned configuration catalog advertised by session/new. It sends no
// prompt and closes the process immediately after the handshake.
func DiscoverConfigOptions(
	ctx context.Context,
	launch Launch,
	workingDir string,
	log *slog.Logger,
) ([]ports.ChatConfigOption, error) {
	driver := New(Config{
		Launch: func(context.Context, LaunchConfig) (Launch, error) { return launch, nil },
	}, log)
	return driver.discoverConfigOptions(ctx, workingDir)
}

func (d *Driver) discoverConfigOptions(ctx context.Context, workingDir string) ([]ports.ChatConfigOption, error) {
	if !filepath.IsAbs(workingDir) {
		return nil, fmt.Errorf("workspace path must be absolute, got %q", workingDir)
	}
	conv, _, err := d.connect(ctx, LaunchConfig{WorkspacePath: workingDir})
	if err != nil {
		return nil, err
	}
	defer func() { _ = conv.Close() }()

	openCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	resp, err := conv.conn.NewSession(openCtx, acpsdk.NewSessionRequest{
		Cwd:        workingDir,
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		return nil, normalizeACPError("ACP session/new", err)
	}
	return normalizeConfigOptions(resp.ConfigOptions), nil
}

// Harness identifies the AO harness this ACP transport adapts.
func (d *Driver) Harness() domain.AgentHarness { return d.cfg.Harness }

// Probe checks the provider binding without creating an ACP session or worktree.
func (d *Driver) Probe(ctx context.Context) (ports.ChatCapabilities, error) {
	if d.cfg.Probe == nil || d.cfg.Launch == nil {
		return nil, fmt.Errorf("%w: incomplete ACP binding", ports.ErrChatDriverUnavailable)
	}
	if err := d.cfg.Probe(ctx); err != nil {
		return nil, err
	}
	return cloneCapabilities(d.cfg.Capabilities), nil
}

// Start creates a new ACP session in the AO worktree.
func (d *Driver) Start(ctx context.Context, cfg ports.ChatStartConfig) (ports.ChatConversation, error) {
	if !filepath.IsAbs(cfg.WorkspacePath) {
		return nil, fmt.Errorf("workspace path must be absolute, got %q", cfg.WorkspacePath)
	}
	if d.cfg.ValidateTurnSettings != nil {
		if err := d.cfg.ValidateTurnSettings(cfg.Permissions, ports.ChatTurnSettings{
			Model: cfg.Model, Approval: cfg.Permissions,
		}); err != nil {
			return nil, fmt.Errorf("validate ACP session settings: %w", err)
		}
	}
	launchCfg := LaunchConfig{
		SessionID: cfg.SessionID, DataDir: cfg.DataDir, WorkspacePath: cfg.WorkspacePath,
		Env:   cfg.Env,
		Model: cfg.Model, Permissions: cfg.Permissions, SystemPrompt: cfg.SystemPrompt,
		ProviderScopeID: cfg.ProviderScopeID,
	}
	conv, init, live, err := d.connect(ctx, launchCfg, cfg.PrepareEnv)
	if err != nil {
		return nil, err
	}
	if live != nil {
		// A fresh-start request colliding with a surviving host is a durable-state
		// mismatch. Detach without destroying a provider that may still be working.
		_ = conv.Close()
		return nil, fmt.Errorf("%w: fresh ACP start found an existing initialized session", ports.ErrChatRecoveryInconclusive)
	}
	// An agent that does not advertise session/resume can still start new
	// sessions; it just cannot be resumed after a disconnect. The resume
	// capability is downgraded in conversationCapabilities, and Resume()
	// returns ErrChatResumeFailed if called.
	additional, err := normalizeAdditionalDirectories(cfg.WorkspacePath, cfg.AdditionalDirectories,
		init.AgentCapabilities.SessionCapabilities.AdditionalDirectories != nil)
	if err != nil {
		_ = conv.Close()
		return nil, err
	}
	mcpServers, err := normalizeMCPServers(cfg.MCPServers, init.AgentCapabilities.McpCapabilities)
	if err != nil {
		_ = conv.Close()
		return nil, err
	}

	meta := map[string]any(nil)
	if d.cfg.SessionMeta != nil {
		meta = d.cfg.SessionMeta(launchCfg)
	}
	openCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	resp, err := conv.conn.NewSession(openCtx, acpsdk.NewSessionRequest{
		Meta:                  meta,
		Cwd:                   cfg.WorkspacePath,
		AdditionalDirectories: additional,
		McpServers:            mcpServers,
	})
	if err != nil {
		_ = conv.Close()
		return nil, normalizeACPError("ACP session/new", err)
	}
	if resp.SessionId == "" {
		_ = conv.Close()
		return nil, errors.New("ACP session/new returned no session id")
	}
	conv.start(
		string(resp.SessionId), conversationCapabilities(d.cfg.Capabilities, init),
		d.cfg.SessionMode, d.cfg.SessionOptions, d.cfg.PermissionPolicy,
		cfg.Permissions, d.cfg.ValidateTurnSettings, resp.ConfigOptions,
		conv.legacyWire.modelState(), resp.Modes,
	)
	if err := conv.applyTurnSettings(ctx, ports.ChatTurnSettings{Model: cfg.Model, Approval: cfg.Permissions}); err != nil {
		// Initial model and permission mode may have been applied via launch-time
		// flags (e.g. kimchiacp passes --model, --auto, --yolo). An agent that
		// does not implement the runtime ACP setters returns -32601; tolerate it
		// at session start so those bindings can still open a session.
		if !errors.Is(err, ErrACPSetterUnsupported) {
			_ = conv.Close()
			return nil, fmt.Errorf("configure ACP session: %w", err)
		}
	}
	return conv, nil
}

// Resume reconnects to the stored ACP session. When the agent advertises
// session/load, AO uses it to recover both provider context and the normalized
// transcript; resume-only agents recover context but explicitly report that no
// typed history replay is available.
func (d *Driver) Resume(ctx context.Context, cfg ports.ChatResumeConfig) (ports.ChatConversation, error) {
	if cfg.ProviderConversationID == "" {
		return nil, fmt.Errorf("%w: no stored ACP session id", ports.ErrChatResumeFailed)
	}
	if !filepath.IsAbs(cfg.WorkspacePath) {
		return nil, fmt.Errorf("workspace path must be absolute, got %q", cfg.WorkspacePath)
	}
	if d.cfg.ValidateTurnSettings != nil {
		if err := d.cfg.ValidateTurnSettings(cfg.Permissions, ports.ChatTurnSettings{
			Model: cfg.Model, Effort: cfg.Effort, Approval: cfg.Permissions,
		}); err != nil {
			return nil, fmt.Errorf("%w: validate ACP session settings: %w", ports.ErrChatResumeFailed, err)
		}
	}
	launchCfg := LaunchConfig{
		SessionID: cfg.SessionID, DataDir: cfg.DataDir, WorkspacePath: cfg.WorkspacePath,
		Env:   cfg.Env,
		Model: cfg.Model, Permissions: cfg.Permissions, SystemPrompt: cfg.SystemPrompt,
		ProviderScopeID: cfg.ProviderScopeID,
	}
	conv, init, live, err := d.connect(ctx, launchCfg, cfg.PrepareEnv)
	if err != nil {
		return nil, err
	}
	if live != nil {
		if live.SessionID != cfg.ProviderConversationID {
			_ = conv.Close()
			return nil, fmt.Errorf("%w: persistent ACP session %q does not match %q",
				ports.ErrChatRecoveryInconclusive, live.SessionID, cfg.ProviderConversationID)
		}
		var setup struct {
			ConfigOptions []acpsdk.SessionConfigOption `json:"configOptions,omitempty"`
			Modes         *acpsdk.SessionModeState     `json:"modes,omitempty"`
			Models        *legacySessionModelState     `json:"models,omitempty"`
		}
		if len(live.SessionResult) == 0 {
			_ = conv.Close()
			return nil, fmt.Errorf("%w: persistent ACP session has no setup response", ports.ErrChatRecoveryInconclusive)
		}
		if decodeErr := json.Unmarshal(live.SessionResult, &setup); decodeErr != nil {
			_ = conv.Close()
			return nil, fmt.Errorf("%w: decode persistent ACP session: %w", ports.ErrChatRecoveryInconclusive, decodeErr)
		}
		conv.liveState = live
		conv.start(
			cfg.ProviderConversationID, conversationCapabilities(d.cfg.Capabilities, init),
			d.cfg.SessionMode, d.cfg.SessionOptions, d.cfg.PermissionPolicy,
			cfg.Permissions, d.cfg.ValidateTurnSettings, setup.ConfigOptions,
			setup.Models, setup.Modes,
		)
		return conv, nil
	}
	if !supportsSessionRestore(init) {
		_ = conv.Close()
		return nil, fmt.Errorf("%w: ACP agent supports neither session/load nor session/resume", ports.ErrChatResumeFailed)
	}
	additional, err := normalizeAdditionalDirectories(cfg.WorkspacePath, cfg.AdditionalDirectories,
		init.AgentCapabilities.SessionCapabilities.AdditionalDirectories != nil)
	if err != nil {
		_ = conv.Close()
		if isACPAuthRequired(err) {
			return nil, normalizeACPError("ACP session/resume", err)
		}
		return nil, fmt.Errorf("%w: %w", ports.ErrChatResumeFailed, err)
	}
	mcpServers, err := normalizeMCPServers(cfg.MCPServers, init.AgentCapabilities.McpCapabilities)
	if err != nil {
		_ = conv.Close()
		return nil, fmt.Errorf("%w: %w", ports.ErrChatResumeFailed, err)
	}

	resumeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	meta := map[string]any(nil)
	if d.cfg.SessionMeta != nil {
		meta = d.cfg.SessionMeta(launchCfg)
	}
	var configOptions []acpsdk.SessionConfigOption
	var modes *acpsdk.SessionModeState
	var historyConversation *refreshableConversation
	if init.AgentCapabilities.LoadSession {
		historyConversation = newRefreshableConversation(conv, acpsdk.LoadSessionRequest{
			Meta:                  meta,
			SessionId:             acpsdk.SessionId(cfg.ProviderConversationID),
			Cwd:                   cfg.WorkspacePath,
			AdditionalDirectories: additional,
			McpServers:            mcpServers,
		})
		resp, err := historyConversation.loadHistory(resumeCtx)
		if err != nil {
			_ = conv.Close()
			return nil, fmt.Errorf("%w: %w", ports.ErrChatResumeFailed, normalizeACPError("ACP session/load", err))
		}
		configOptions = resp.ConfigOptions
		modes = resp.Modes
	} else {
		resp, err := conv.conn.ResumeSession(resumeCtx, acpsdk.ResumeSessionRequest{
			Meta:                  meta,
			SessionId:             acpsdk.SessionId(cfg.ProviderConversationID),
			Cwd:                   cfg.WorkspacePath,
			AdditionalDirectories: additional,
			McpServers:            mcpServers,
		})
		if err != nil {
			_ = conv.Close()
			return nil, fmt.Errorf("%w: %w", ports.ErrChatResumeFailed, err)
		}
		configOptions = resp.ConfigOptions
		modes = resp.Modes
	}
	conv.start(
		cfg.ProviderConversationID, conversationCapabilities(d.cfg.Capabilities, init),
		d.cfg.SessionMode, d.cfg.SessionOptions, d.cfg.PermissionPolicy,
		cfg.Permissions, d.cfg.ValidateTurnSettings, configOptions,
		conv.legacyWire.modelState(), modes,
	)
	if err := conv.applyTurnSettings(ctx, ports.ChatTurnSettings{Model: cfg.Model, Effort: cfg.Effort, Approval: cfg.Permissions}); err != nil {
		if !errors.Is(err, ErrACPSetterUnsupported) {
			_ = conv.Close()
			return nil, fmt.Errorf("%w: configure ACP session: %w", ports.ErrChatResumeFailed, err)
		}
	}
	if historyConversation != nil {
		return historyConversation, nil
	}
	return conv, nil
}

func (d *Driver) connect(
	ctx context.Context,
	cfg LaunchConfig,
	prepareEnv func(context.Context) (map[string]string, error),
) (*conversation, acpsdk.InitializeResponse, *persistenthost.ACPState, error) {
	// Non-persistent providers are always launched, so prepare their one-shot
	// environment before resolving launch configuration. Persistent providers
	// defer it until the host proves that it cannot adopt a live process.
	if !d.cfg.Persistent && prepareEnv != nil {
		preparedEnv, err := prepareEnv(ctx)
		if err != nil {
			return nil, acpsdk.InitializeResponse{}, nil, err
		}
		cfg.Env = preparedEnv
	}
	launch, err := d.cfg.Launch(ctx, cfg)
	if err != nil {
		return nil, acpsdk.InitializeResponse{}, nil, err
	}
	if launch.Command == "" {
		return nil, acpsdk.InitializeResponse{}, nil, fmt.Errorf("%w: ACP launch command is empty", ports.ErrChatDriverUnavailable)
	}
	proc, err := d.connectProcess(ctx, cfg, launch, prepareEnv)
	if err != nil {
		return nil, acpsdk.InitializeResponse{}, nil, err
	}
	conv := newConversation(
		proc, d.log, cfg.ProviderScopeID, d.cfg.ClientExtension, d.cfg.ClientExtensionAliases,
	)
	if proc.reconnected {
		state := proc.acpState
		if state == nil || len(state.InitializeResult) == 0 || len(state.SessionResult) == 0 || state.SessionID == "" {
			_ = conv.Close()
			return nil, acpsdk.InitializeResponse{}, nil, fmt.Errorf(
				"%w: persistent ACP host has incomplete reconnect state", ports.ErrChatRecoveryInconclusive)
		}
		var init acpsdk.InitializeResponse
		if err := json.Unmarshal(state.InitializeResult, &init); err != nil {
			_ = conv.Close()
			return nil, acpsdk.InitializeResponse{}, nil, fmt.Errorf(
				"%w: decode persistent ACP initialize: %w", ports.ErrChatRecoveryInconclusive, err)
		}
		if d.cfg.ValidateInitialize != nil {
			if err := d.cfg.ValidateInitialize(init); err != nil {
				_ = conv.Close()
				return nil, acpsdk.InitializeResponse{}, nil, fmt.Errorf("%w: %w", ports.ErrChatDriverIncompatible, err)
			}
		}
		return conv, init, state, nil
	}

	initCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	init, err := conv.conn.Initialize(initCtx, acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientInfo: &acpsdk.Implementation{
			Name:    "agent-orchestrator",
			Title:   pointer("Agent Orchestrator"),
			Version: "0.1.0",
		},
		ClientCapabilities: acpsdk.ClientCapabilities{
			// These two Claude bridge extensions enrich the transcript. They do
			// not grant the agent access to AO's terminal or filesystem APIs.
			Meta: map[string]any{
				"subagent-transcript": true,
				"terminal_output":     true,
				// claude-agent-acp publishes retryable API/transport failures only
				// when the client opts into this namespaced metadata extension. It is
				// observational: AO receives status, but grants no new capability.
				"jetbrains": map[string]any{
					"air": map[string]any{
						"version":      1,
						"capabilities": []string{"sessionFailure"},
					},
				},
			},
			Elicitation: &acpsdk.ElicitationCapabilities{
				Form: &acpsdk.ElicitationFormCapabilities{},
				Url:  &acpsdk.ElicitationUrlCapabilities{},
			},
			PlanCapabilities: &acpsdk.PlanCapabilities{},
		},
	})
	if err != nil {
		_ = conv.Close()
		if isACPAuthRequired(err) {
			return nil, acpsdk.InitializeResponse{}, nil, normalizeACPError("ACP initialize", err)
		}
		return nil, acpsdk.InitializeResponse{}, nil, fmt.Errorf("%w: ACP initialize: %w", ports.ErrChatDriverIncompatible, err)
	}
	if d.cfg.ValidateInitialize != nil {
		if err := d.cfg.ValidateInitialize(init); err != nil {
			_ = conv.Close()
			return nil, acpsdk.InitializeResponse{}, nil, fmt.Errorf("%w: %w", ports.ErrChatDriverIncompatible, err)
		}
	}
	return conv, init, nil, nil
}

func (d *Driver) connectProcess(
	ctx context.Context,
	cfg LaunchConfig,
	launch Launch,
	prepareEnv func(context.Context) (map[string]string, error),
) (*process, error) {
	if !d.cfg.Persistent {
		proc, err := d.spawn(launch, cfg.WorkspacePath)
		if err != nil {
			return nil, fmt.Errorf("%w: launch ACP agent: %w", ports.ErrChatDriverUnavailable, err)
		}
		return proc, nil
	}
	if !filepath.IsAbs(cfg.DataDir) {
		return nil, fmt.Errorf("%w: persistent ACP host requires an absolute data directory", ports.ErrChatDriverUnavailable)
	}
	provider := preparedACPProvider(cfg, launch)
	hostConfig := persistenthost.Config{
		SessionID: string(cfg.SessionID), DataDir: cfg.DataDir, Workdir: cfg.WorkspacePath,
		Env: provider.Env, Argv: provider.Argv, Protocol: persistenthost.ProtocolACP,
		LaunchFingerprint: provider.LaunchFingerprint,
	}
	if prepareEnv != nil {
		hostConfig.Prepare = func(prepareCtx context.Context) (persistenthost.PreparedProvider, error) {
			preparedEnv, prepareErr := prepareEnv(prepareCtx)
			if prepareErr != nil {
				return persistenthost.PreparedProvider{}, prepareErr
			}
			preparedCfg := cfg
			preparedCfg.Env = preparedEnv
			preparedLaunch, prepareErr := d.cfg.Launch(prepareCtx, preparedCfg)
			if prepareErr != nil {
				return persistenthost.PreparedProvider{}, prepareErr
			}
			return preparedACPProvider(preparedCfg, preparedLaunch), nil
		}
	}
	transport, err := d.connectHost(ctx, hostConfig)
	if err != nil {
		if errors.Is(err, persistenthost.ErrOwnershipInconclusive) ||
			errors.Is(err, persistenthost.ErrAttached) ||
			errors.Is(err, persistenthost.ErrIncompatible) ||
			errors.Is(err, persistenthost.ErrUnauthorized) {
			return nil, fmt.Errorf("%w: persistent ACP host: %w", ports.ErrChatRecoveryInconclusive, err)
		}
		return nil, fmt.Errorf("%w: persistent ACP host: %w", ports.ErrChatDriverUnavailable, err)
	}
	gate := newGatedReader(transport.Stdout, !transport.Reconnected)
	proc := &process{
		stdin: transport.Stdin, stdout: gate, gate: gate,
		reconnected: transport.Reconnected, acpState: transport.ACPState,
	}
	proc.stop = persistentStop(gate, transport.Stdin)
	proc.terminate = persistentTerminate(gate, transport.Stdin, func(shutdownCtx context.Context) error {
		return persistenthost.Shutdown(shutdownCtx, cfg.DataDir, string(cfg.SessionID))
	})
	return proc, nil
}

func preparedACPProvider(cfg LaunchConfig, launch Launch) persistenthost.PreparedProvider {
	argv := append([]string{launch.Command}, launch.Args...)
	fingerprintEnv := append(processenv.FingerprintEntries(launch.Env),
		"_AO_MODEL="+cfg.Model,
		"_AO_PERMISSIONS="+string(ports.NormalizePermissionMode(cfg.Permissions)),
		"_AO_SYSTEM_PROMPT="+cfg.SystemPrompt,
	)
	return persistenthost.PreparedProvider{
		Env:  processenv.Merge(launch.Env),
		Argv: argv,
		LaunchFingerprint: persistenthost.ComputeLaunchFingerprint(
			cfg.WorkspacePath, fingerprintEnv, argv, persistenthost.ProtocolACP,
		),
	}
}

func cloneCapabilities(in ports.ChatCapabilities) ports.ChatCapabilities {
	out := make(ports.ChatCapabilities, len(in))
	for capability, enabled := range in {
		out[capability] = enabled
	}
	return out
}

func conversationCapabilities(
	configured ports.ChatCapabilities,
	init acpsdk.InitializeResponse,
) ports.ChatCapabilities {
	caps := cloneCapabilities(configured)
	if !supportsSessionRestore(init) {
		caps[ports.ChatCapabilityResume] = false
	}
	caps[ports.ChatCapabilityHistory] = init.AgentCapabilities.LoadSession
	if extensionSupported(init.Meta, "steering") {
		caps[ports.ChatCapabilitySteer] = true
	}
	caps[ports.ChatCapabilityImages] = init.AgentCapabilities.PromptCapabilities.Image
	caps[ports.ChatCapabilityEmbeddedContext] = init.AgentCapabilities.PromptCapabilities.EmbeddedContext
	// ResourceLink is a baseline ACP content block rather than an optional prompt
	// capability. Every ACP conversation preserves it natively.
	caps[ports.ChatCapabilityResourceLinks] = true
	// These are facilities AO itself negotiated as the ACP client. An agent that
	// never uses them simply produces no matching events.
	caps[ports.ChatCapabilityElicitation] = true
	caps[ports.ChatCapabilityNestedAgents] = true
	caps[ports.ChatCapabilityTerminalOutput] = true
	return caps
}

func supportsSessionRestore(init acpsdk.InitializeResponse) bool {
	return init.AgentCapabilities.LoadSession ||
		init.AgentCapabilities.SessionCapabilities.Resume != nil
}

func extensionSupported(meta map[string]any, name string) bool {
	extension, ok := meta[name].(map[string]any)
	if !ok {
		return false
	}
	supported, _ := extension["supported"].(bool)
	return supported
}

func pointer[T any](value T) *T { return &value }

func isACPAuthRequired(err error) bool {
	var requestErr *acpsdk.RequestError
	return errors.As(err, &requestErr) && requestErr.Code == -32000
}

// isACPMethodNotFound reports whether err is a JSON-RPC -32601 "Method not
// found" from the ACP agent. Agents that implement a subset of the protocol
// (e.g. no session/set_mode or session/set_config_option) return this code;
// the driver treats those calls as optional so a minimal agent can still run.
func isACPMethodNotFound(err error) bool {
	var requestErr *acpsdk.RequestError
	return errors.As(err, &requestErr) && requestErr.Code == -32601
}

func normalizeACPError(operation string, err error) error {
	if isACPAuthRequired(err) {
		return fmt.Errorf("%w: %s: %w", ports.ErrChatAuthRequired, operation, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}
