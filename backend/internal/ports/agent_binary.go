package ports

import (
	"context"
	"errors"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// BinaryResolvePurpose distinguishes process-free presence from launch validation.
type BinaryResolvePurpose string

const (
	// BinaryResolvePresence must not start an agent process.
	BinaryResolvePresence BinaryResolvePurpose = "presence"
	// BinaryResolveLaunch includes adapter identity validation.
	BinaryResolveLaunch BinaryResolvePurpose = "launch"
)

// ErrAgentBinaryChecking means discovery has not established presence or absence.
var ErrAgentBinaryChecking = errors.New("agent: binary discovery is checking")

// AgentBinaryResolution records the selected executable and observation time.
type AgentBinaryResolution struct {
	Executable string
	Source     string
	CheckedAt  time.Time
}

// AgentBinaryDiscovery is shared by every adapter owned by a daemon.
type AgentBinaryDiscovery interface {
	Resolve(context.Context, domain.AgentHarness, BinaryResolvePurpose) (AgentBinaryResolution, error)
	Invalidate(domain.AgentHarness)
}

// AgentBinarySpec keeps raw lookups separate from injected resolution to avoid
// recursion. Presence and candidate normalization must not start an agent when
// purpose is presence. An empty Names list explicitly disables shell discovery
// on the current platform.
type AgentBinarySpec struct {
	Names     []string
	Lookup    func(context.Context) (string, error)
	Presence  func(context.Context) (string, error)
	Normalize func(context.Context, string, BinaryResolvePurpose) (string, error)
}

// AgentBinaryDiscoveryProvider exposes raw metadata and shared resolver injection.
type AgentBinaryDiscoveryProvider interface {
	BinaryDiscoverySpec() AgentBinarySpec
	SetBinaryDiscovery(AgentBinaryDiscovery)
}

// AgentBinaryRuntimeEnvironment applies discovery metadata only to one child.
type AgentBinaryRuntimeEnvironment interface {
	AugmentBinaryRuntimeEnv(context.Context, map[string]string, []string, string)
}

// AgentBinaryInvalidator discards selection after a proven pre-start failure.
type AgentBinaryInvalidator interface{ InvalidateBinary(domain.AgentHarness) }

// ErrAgentProcessNotStarted may only wrap failures before process creation.
// Protocol errors and provider exits must never use it or replay a prompt.
var ErrAgentProcessNotStarted = errors.New("agent: process did not start")
