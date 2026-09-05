package runtimeselect

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// directHandlePrefix is persisted as part of the opaque runtime handle.
// Handles written before the direct-host rollout have no prefix and therefore
// remain permanently routed to tmux. The version leaves room for a future host
// protocol migration without guessing from session metadata.
const directHandlePrefix = "ptyhost-v1:"

// routedBackend captures the capabilities the daemon conditionally consumes.
// Both tmux and the detached PTY host provide them; keeping them on the router
// avoids silently disabling styled-output safety checks or process supervision.
type routedBackend interface {
	Runtime
	ports.StyledTerminalOutputReader
	ports.SupervisedProcessInspector
	ports.ExactSupervisedProcessInspector
}

type hybridRuntime struct {
	legacy   routedBackend
	direct   routedBackend
	log      *slog.Logger
	platform string
}

var _ Runtime = (*hybridRuntime)(nil)
var _ ports.RuntimeRestarter = (*hybridRuntime)(nil)
var _ ports.StyledTerminalOutputReader = (*hybridRuntime)(nil)
var _ ports.SupervisedProcessInspector = (*hybridRuntime)(nil)
var _ ports.ExactSupervisedProcessInspector = (*hybridRuntime)(nil)
var _ ports.RuntimeHandleResolver = (*hybridRuntime)(nil)
var _ ports.ExactRuntimeHandleResolver = (*hybridRuntime)(nil)
var _ ports.RuntimeIdentityInspector = (*hybridRuntime)(nil)

func newHybridRuntime(legacy, direct routedBackend, log *slog.Logger, platform string) *hybridRuntime {
	if log == nil {
		log = slog.Default()
	}
	if platform == "" {
		platform = "native"
	}
	return &hybridRuntime{legacy: legacy, direct: direct, log: log, platform: platform}
}

// Create opts only new sessions into the native PTY host. If host startup
// fails before a handle is returned, tmux remains a compatibility fallback so
// a host-specific problem does not prevent an agent session from starting.
func (r *hybridRuntime) Create(ctx context.Context, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	handle, err := r.direct.Create(ctx, cfg)
	if err == nil {
		handle.ID = directHandlePrefix + handle.ID
		return handle, nil
	}
	// An inconclusive direct-runtime error means a native host may already own
	// this session or its recovery registry could not be made durable. Starting
	// tmux behind the same session id would turn uncertainty into a duplicate.
	if errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		return ports.RuntimeHandle{}, err
	}
	r.log.Warn(r.platform+" direct PTY host unavailable; falling back to tmux",
		"session_id", cfg.SessionID,
		"err", err,
	)
	fallback, fallbackErr := r.legacy.Create(ctx, cfg)
	if fallbackErr != nil {
		return ports.RuntimeHandle{}, errors.Join(
			fmt.Errorf("%s direct PTY host: %w", r.platform, err),
			fmt.Errorf("%s tmux fallback: %w", r.platform, fallbackErr),
		)
	}
	return fallback, nil
}

func (r *hybridRuntime) Destroy(ctx context.Context, handle ports.RuntimeHandle) error {
	backend, raw := r.route(handle)
	return backend.Destroy(ctx, raw)
}

func (r *hybridRuntime) IsAlive(ctx context.Context, handle ports.RuntimeHandle) (bool, error) {
	backend, raw := r.route(handle)
	return backend.IsAlive(ctx, raw)
}

func (r *hybridRuntime) ProbeFencedRuntime(ctx context.Context, ref ports.FencedRuntimeRef) ports.FencedProbeResult {
	backend, raw := r.route(ref.Handle)
	ref.Handle = raw
	return backend.ProbeFencedRuntime(ctx, ref)
}

func (r *hybridRuntime) Attach(ctx context.Context, handle ports.RuntimeHandle, rows, cols uint16) (ports.Stream, error) {
	backend, raw := r.route(handle)
	return backend.Attach(ctx, raw, rows, cols)
}

func (r *hybridRuntime) Interrupt(ctx context.Context, handle ports.RuntimeHandle) error {
	backend, raw := r.route(handle)
	return backend.Interrupt(ctx, raw)
}

func (r *hybridRuntime) SendInput(ctx context.Context, handle ports.RuntimeHandle, input string) error {
	backend, raw := r.route(handle)
	return backend.SendInput(ctx, raw, input)
}

func (r *hybridRuntime) SendMessage(ctx context.Context, handle ports.RuntimeHandle, message string) error {
	backend, raw := r.route(handle)
	return backend.SendMessage(ctx, raw, message)
}

func (r *hybridRuntime) GetOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error) {
	backend, raw := r.route(handle)
	return backend.GetOutput(ctx, raw, lines)
}

func (r *hybridRuntime) GetStyledOutput(ctx context.Context, handle ports.RuntimeHandle, lines int) (string, error) {
	backend, raw := r.route(handle)
	return backend.GetStyledOutput(ctx, raw, lines)
}

func (r *hybridRuntime) IsSupervisedProcessAlive(ctx context.Context, handle ports.RuntimeHandle, ref ports.SupervisedProcessRef) (bool, error) {
	backend, raw := r.route(handle)
	return backend.IsSupervisedProcessAlive(ctx, raw, ref)
}

func (r *hybridRuntime) IsExactSupervisedProcessAlive(ctx context.Context, handle ports.RuntimeHandle, ref ports.SupervisedProcessRef) (bool, error) {
	backend, raw := r.route(handle)
	return backend.IsExactSupervisedProcessAlive(ctx, raw, ref)
}

// ResolveRuntimeHandle routes legacy tmux migration to tmux and asks the native
// backend to prove a ptyhost handle's exact registry and launch ownership. The
// outer prefix makes the backend route durable; it is not itself ownership
// evidence.
func (r *hybridRuntime) ResolveRuntimeHandle(ctx context.Context, handle ports.RuntimeHandle, owner ports.SupervisedProcessRef) (ports.RuntimeHandle, bool, error) {
	if strings.HasPrefix(handle.ID, directHandlePrefix) {
		resolver, ok := r.direct.(ports.RuntimeHandleResolver)
		if !ok {
			return ports.RuntimeHandle{}, false, fmt.Errorf("%s direct runtime does not support handle resolution", r.platform)
		}
		raw := handle
		raw.ID = strings.TrimPrefix(raw.ID, directHandlePrefix)
		resolved, found, err := resolver.ResolveRuntimeHandle(ctx, raw, owner)
		if err != nil || !found {
			return ports.RuntimeHandle{}, found, err
		}
		if strings.TrimSpace(resolved.ID) == "" {
			return ports.RuntimeHandle{}, false, fmt.Errorf("%s direct runtime returned an empty handle", r.platform)
		}
		resolved.ID = directHandlePrefix + resolved.ID
		return resolved, true, nil
	}
	resolver, ok := r.legacy.(ports.RuntimeHandleResolver)
	if !ok {
		return ports.RuntimeHandle{}, false, fmt.Errorf("%s legacy runtime does not support handle resolution", r.platform)
	}
	return resolver.ResolveRuntimeHandle(ctx, handle, owner)
}

// ResolveExactRuntimeHandle keeps destructive recovery on the handle's native
// backend and requires that backend to prove the exact persisted launch owner.
// Versioned ptyhost handles are stripped only for the direct adapter call and
// restored before returning to Session Manager.
func (r *hybridRuntime) ResolveExactRuntimeHandle(ctx context.Context, handle ports.RuntimeHandle, owner ports.SupervisedProcessRef) (ports.RuntimeHandle, bool, error) {
	backend, raw := r.route(handle)
	resolver, ok := backend.(ports.ExactRuntimeHandleResolver)
	if !ok {
		return ports.RuntimeHandle{}, false, fmt.Errorf("%s runtime does not support exact handle resolution", r.platform)
	}
	resolved, found, err := resolver.ResolveExactRuntimeHandle(ctx, raw, owner)
	if err != nil || !found {
		return ports.RuntimeHandle{}, found, err
	}
	if strings.TrimSpace(resolved.ID) == "" {
		return ports.RuntimeHandle{}, false, fmt.Errorf("%s runtime returned an empty exact handle", r.platform)
	}
	if backend == r.direct {
		resolved.ID = directHandlePrefix + resolved.ID
	}
	return resolved, true, nil
}

func (r *hybridRuntime) InspectRuntimeIdentity(ctx context.Context, handle ports.RuntimeHandle, expectedSessionID domain.SessionID) (ports.RuntimeIdentity, error) {
	backend, raw := r.route(handle)
	inspector, ok := backend.(ports.RuntimeIdentityInspector)
	if !ok {
		return ports.RuntimeIdentity{}, fmt.Errorf("%s runtime does not support identity inspection", r.platform)
	}
	return inspector.InspectRuntimeIdentity(ctx, raw, expectedSessionID)
}

// Restart preserves tmux's in-place restart behavior for every legacy handle.
// Direct hosts currently restart by replacing only their own host and return a
// new handle with the same versioned identity.
func (r *hybridRuntime) Restart(ctx context.Context, handle ports.RuntimeHandle, cfg ports.RuntimeConfig) (ports.RuntimeHandle, error) {
	backend, raw := r.route(handle)
	if backend == r.legacy {
		restarter, ok := r.legacy.(ports.RuntimeRestarter)
		if !ok {
			return ports.RuntimeHandle{}, fmt.Errorf("%s legacy runtime does not support restart", r.platform)
		}
		return restarter.Restart(ctx, raw, cfg)
	}
	if err := r.direct.Destroy(ctx, raw); err != nil {
		return ports.RuntimeHandle{}, err
	}
	// Re-enter the normal creation policy so an unavailable replacement host
	// can still recover the session on tmux and return its unprefixed handle.
	return r.Create(ctx, cfg)
}

func (r *hybridRuntime) route(handle ports.RuntimeHandle) (routedBackend, ports.RuntimeHandle) {
	if strings.HasPrefix(handle.ID, directHandlePrefix) {
		handle.ID = strings.TrimPrefix(handle.ID, directHandlePrefix)
		return r.direct, handle
	}
	return r.legacy, handle
}
