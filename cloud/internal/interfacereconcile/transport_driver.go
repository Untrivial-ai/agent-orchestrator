package interfacereconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/postgres"
)

// RequestStore is the control-plane surface the transport driver uses to
// dispatch a worker command and read its result.
type RequestStore interface {
	CreateCoordinatedInterfaceRequest(ctx context.Context, orgID, sessionID, kind string, payload json.RawMessage) (domain.WorkerRequest, error)
	GetCoordinatedInterfaceRequestResult(ctx context.Context, orgID, sessionID, requestID string) (domain.WorkerRequest, error)
}

// TransportDriver dispatches interface commands to a live worker through AO's
// durable request channel and waits for the worker's completion. The worker owns
// the agent process, so the control plane never touches a PTY directly.
type TransportDriver struct {
	store RequestStore
	owner string
	step  time.Duration
	log   *slog.Logger
}

// NewTransportDriver builds a worker-backed interface driver.
func NewTransportDriver(store RequestStore, owner string, step time.Duration, log *slog.Logger) *TransportDriver {
	if step <= 0 {
		step = defaultStepTimeout
	}
	if log == nil {
		log = slog.Default()
	}
	return &TransportDriver{store: store, owner: owner, step: step, log: log}
}

func (d *TransportDriver) PreflightTarget(
	ctx context.Context,
	transition postgres.CoordinatedInterfaceTransition,
) error {
	if !strings.EqualFold(transition.Harness, "claude-code") &&
		!strings.EqualFold(transition.Harness, "codex") &&
		!strings.EqualFold(transition.Harness, "cursor") {
		return fmt.Errorf("%s does not support interface handoff", transition.Harness)
	}
	return nil
}

func (d *TransportDriver) InspectSource(
	ctx context.Context,
	transition postgres.CoordinatedInterfaceTransition,
) (SourceInspection, error) {
	var result SourceInspection
	payload, _ := json.Marshal(map[string]any{
		"sourceInterface": transition.SourceInterface,
		"sessionId":       transition.SessionID,
	})
	err := d.dispatch(ctx, transition, "interface.inspect", payload, &result)
	if err != nil {
		return SourceInspection{}, err
	}
	return result, nil
}

func (d *TransportDriver) InterruptSource(
	ctx context.Context,
	transition postgres.CoordinatedInterfaceTransition,
) error {
	payload, _ := json.Marshal(map[string]any{"sourceInterface": transition.SourceInterface})
	return d.dispatchAck(ctx, transition, "interface.interrupt", payload)
}

func (d *TransportDriver) StopSource(
	ctx context.Context,
	transition postgres.CoordinatedInterfaceTransition,
) error {
	payload, _ := json.Marshal(map[string]any{"sourceInterface": transition.SourceInterface})
	return d.dispatch(ctx, transition, "interface.stop", payload, nil)
}

func (d *TransportDriver) ResolveNativeConversationID(
	ctx context.Context,
	transition postgres.CoordinatedInterfaceTransition,
) (string, error) {
	var result struct {
		NativeConversationID string `json:"nativeConversationId"`
	}
	payload, _ := json.Marshal(map[string]any{"sourceInterface": transition.SourceInterface})
	err := d.dispatch(ctx, transition, "interface.native-id", payload, &result)
	if err != nil {
		return "", err
	}
	return result.NativeConversationID, nil
}

func (d *TransportDriver) StartTarget(
	ctx context.Context,
	transition postgres.CoordinatedInterfaceTransition,
	nativeConversationID string,
) error {
	payload, _ := json.Marshal(map[string]any{
		"targetInterface":      transition.TargetInterface,
		"nativeConversationId": nativeConversationID,
	})
	return d.dispatch(ctx, transition, "interface.start", payload, nil)
}

// dispatch enqueues a worker command and awaits its result within the step
// budget. A no-result interaction (interrupt) is acknowledged on enqueue.
func (d *TransportDriver) dispatch(
	ctx context.Context,
	transition postgres.CoordinatedInterfaceTransition,
	kind string,
	payload json.RawMessage,
	out any,
) error {
	request, err := d.store.CreateCoordinatedInterfaceRequest(
		ctx, transition.OrgID, transition.SessionID, kind, payload,
	)
	if err != nil {
		return fmt.Errorf("dispatch %s: %w", kind, err)
	}
	return d.awaitResult(ctx, transition, request, out)
}

func (d *TransportDriver) dispatchAck(
	ctx context.Context,
	transition postgres.CoordinatedInterfaceTransition,
	kind string,
	payload json.RawMessage,
) error {
	_, err := d.store.CreateCoordinatedInterfaceRequest(
		ctx, transition.OrgID, transition.SessionID, kind, payload,
	)
	if err != nil {
		return fmt.Errorf("dispatch %s: %w", kind, err)
	}
	return nil
}

func (d *TransportDriver) awaitResult(
	ctx context.Context,
	transition postgres.CoordinatedInterfaceTransition,
	request domain.WorkerRequest,
	out any,
) error {
	// The worker completes requests asynchronously. Poll the durable row for this
	// step instead of immediately returning pending; otherwise even a healthy
	// worker cannot complete a handoff before the coordinator retries the phase.
	deadline := time.NewTimer(d.step)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	check := func() error {
		if request.Status == "succeeded" {
			if out == nil || len(request.Response) == 0 {
				return nil
			}
			return json.Unmarshal(request.Response, out)
		}
		if request.Status == "failed" {
			return fmt.Errorf("worker interface command %s failed: %s",
				request.Kind, firstNonEmpty(request.ErrorCode, request.ErrorMessage))
		}
		return nil
	}
	if err := check(); err != nil || request.Status == "succeeded" {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return errPendingWorkerCommand
		case <-deadline.C:
			return errPendingWorkerCommand
		case <-ticker.C:
			updated, err := d.store.GetCoordinatedInterfaceRequestResult(
				ctx, transition.OrgID, transition.SessionID, request.ID,
			)
			if err != nil {
				return fmt.Errorf("get worker interface command %s: %w", request.Kind, err)
			}
			request = updated
			if err := check(); err != nil || request.Status == "succeeded" {
				return err
			}
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "unknown error"
}
