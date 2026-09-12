package sessionmanager

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/codexops"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type codexExclusiveOperationContextKey struct{}

func codexExclusiveOperationContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, codexExclusiveOperationContextKey{}, true)
}

func (m *Manager) acquireCodexControllerAdmission(ctx context.Context, harness domain.AgentHarness) (func(), error) {
	if harness != domain.HarnessCodex || m.codexOperationGate == nil || ctx.Value(codexExclusiveOperationContextKey{}) == true {
		return func() {}, nil
	}
	// Account bootstrap reconciles the device-global credential while holding
	// this gate exclusively. Ordinary controller launches must wait for that
	// one-time setup to finish before attempting shared admission; otherwise a
	// startup restore can mistake bootstrap for an active account switch and
	// leave an otherwise resumable Codex session exited until manual recovery.
	if credentials, ok := m.agentReadiness.(ports.CodexAccountCredentialManager); ok {
		if err := credentials.WaitCodexAccountBootstrap(ctx); err != nil {
			return nil, err
		}
	}
	return m.codexOperationGate.AcquireShared(ctx)
}

func defaultCodexOperationGate(gate ports.CodexOperationGate) ports.CodexOperationGate {
	if gate != nil {
		return gate
	}
	return codexops.NewGate()
}
