// Package acp implements AO's provider-neutral Chat driver over the Agent
// Client Protocol. Provider packages supply only discovery, launch, metadata,
// and capability policy; this package owns the ACP lifecycle and translates ACP
// updates into AO's durable conversation vocabulary.
package acp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

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
	SessionID     domain.SessionID
	DataDir       string
	WorkspacePath string
	Env           map[string]string
	Model         string
	Permissions   ports.PermissionMode
	SystemPrompt  string
}

// Config binds one harness to an ACP agent implementation.
type Config struct {
	Harness      domain.AgentHarness
	Capabilities ports.ChatCapabilities
	Probe        func(context.Context) error
	Launch       func(context.Context, LaunchConfig) (Launch, error)
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
}

// SessionOption is one ACP session configuration selection.
type SessionOption struct {
	ID    string
	Value string
}

// Driver opens ACP conversations for a single harness.
type Driver struct {
	cfg   Config
	log   *slog.Logger
	spawn spawnFunc
}

var _ ports.ChatDriver = (*Driver)(nil)

// New returns an ACP Chat driver from a provider binding.
func New(cfg Config, log *slog.Logger) *Driver {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Driver{cfg: cfg, log: log, spawn: spawnAgent}
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
	launchCfg := LaunchConfig{
		SessionID: cfg.SessionID, DataDir: cfg.DataDir, WorkspacePath: cfg.WorkspacePath,
		Env: cfg.Env, Model: cfg.Model, Permissions: cfg.Permissions, SystemPrompt: cfg.SystemPrompt,
	}
	conv, init, err := d.connect(ctx, launchCfg)
	if err != nil {
		return nil, err
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
		d.cfg.SessionMode, d.cfg.SessionOptions, resp.ConfigOptions,
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
	launchCfg := LaunchConfig{
		SessionID: cfg.SessionID, DataDir: cfg.DataDir, WorkspacePath: cfg.WorkspacePath,
		Env: cfg.Env, Permissions: cfg.Permissions, SystemPrompt: cfg.SystemPrompt,
	}
	conv, init, err := d.connect(ctx, launchCfg)
	if err != nil {
		return nil, err
	}
	if !init.AgentCapabilities.LoadSession && init.AgentCapabilities.SessionCapabilities.Resume == nil {
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
	if init.AgentCapabilities.LoadSession {
		conv.beginHistoryReplay(cfg.ProviderConversationID)
		resp, err := conv.conn.LoadSession(resumeCtx, acpsdk.LoadSessionRequest{
			Meta:                  meta,
			SessionId:             acpsdk.SessionId(cfg.ProviderConversationID),
			Cwd:                   cfg.WorkspacePath,
			AdditionalDirectories: additional,
			McpServers:            mcpServers,
		})
		if err != nil {
			conv.abortHistoryReplay()
			_ = conv.Close()
			return nil, fmt.Errorf("%w: %w", ports.ErrChatResumeFailed, normalizeACPError("ACP session/load", err))
		}
		conv.finishHistoryReplay()
		configOptions = resp.ConfigOptions
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
	}
	conv.start(
		cfg.ProviderConversationID, conversationCapabilities(d.cfg.Capabilities, init),
		d.cfg.SessionMode, d.cfg.SessionOptions, configOptions,
	)
	if err := conv.applyTurnSettings(ctx, ports.ChatTurnSettings{Approval: cfg.Permissions}); err != nil {
		if !errors.Is(err, ErrACPSetterUnsupported) {
			_ = conv.Close()
			return nil, fmt.Errorf("%w: configure ACP session: %w", ports.ErrChatResumeFailed, err)
		}
	}
	return conv, nil
}

func (d *Driver) connect(
	ctx context.Context,
	cfg LaunchConfig,
) (*conversation, acpsdk.InitializeResponse, error) {
	launch, err := d.cfg.Launch(ctx, cfg)
	if err != nil {
		return nil, acpsdk.InitializeResponse{}, err
	}
	if launch.Command == "" {
		return nil, acpsdk.InitializeResponse{}, fmt.Errorf("%w: ACP launch command is empty", ports.ErrChatDriverUnavailable)
	}
	proc, err := d.spawn(launch, cfg.WorkspacePath)
	if err != nil {
		return nil, acpsdk.InitializeResponse{}, fmt.Errorf("%w: launch ACP agent: %w", ports.ErrChatDriverUnavailable, err)
	}
	conv := newConversation(proc, d.log)

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
			return nil, acpsdk.InitializeResponse{}, normalizeACPError("ACP initialize", err)
		}
		return nil, acpsdk.InitializeResponse{}, fmt.Errorf("%w: ACP initialize: %w", ports.ErrChatDriverIncompatible, err)
	}
	return conv, init, nil
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
	if init.AgentCapabilities.SessionCapabilities.Resume == nil {
		caps[ports.ChatCapabilityResume] = false
	}
	caps[ports.ChatCapabilityHistory] = init.AgentCapabilities.LoadSession
	if extensionSupported(init.Meta, "steering") {
		caps[ports.ChatCapabilitySteer] = true
	}
	caps[ports.ChatCapabilityImages] = init.AgentCapabilities.PromptCapabilities.Image
	caps[ports.ChatCapabilityEmbeddedContext] = init.AgentCapabilities.PromptCapabilities.EmbeddedContext
	// These are facilities AO itself negotiated as the ACP client. An agent that
	// never uses them simply produces no matching events.
	caps[ports.ChatCapabilityElicitation] = true
	caps[ports.ChatCapabilityNestedAgents] = true
	caps[ports.ChatCapabilityTerminalOutput] = true
	return caps
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
