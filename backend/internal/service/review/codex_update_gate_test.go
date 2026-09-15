package review

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/codexops"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	reviewcore "github.com/aoagents/agent-orchestrator/backend/internal/review"
)

type codexReviewWorkerStore struct {
	Store
	worker domain.SessionRecord
}

func (s codexReviewWorkerStore) GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error) {
	return s.worker, true, nil
}

func TestCodexReviewerLaunchAdmissionOnNonCodexWorker(t *testing.T) {
	for _, preference := range []domain.ReviewerHarness{domain.ReviewerCodex, ""} {
		t.Run(string(preference), func(t *testing.T) {
			ctx := context.Background()
			gate := codexops.NewGate()
			lease, err := gate.AcquireExclusive(ctx) // installer owns replacement
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			s := New(nil, codexReviewWorkerStore{worker: domain.SessionRecord{ID: "claude-worker", Harness: domain.HarnessClaudeCode, ReviewerHarness: preference}}, WithCodexAccountOperationGate(gate))
			calls := 0
			s.engineTrigger = func(context.Context, domain.SessionID, domain.ReviewerHarness, domain.AgentConfig, domain.ReviewTriggerSource) (reviewcore.TriggerResult, error) {
				calls++
				return reviewcore.TriggerResult{}, nil
			}
			for _, harness := range []domain.ReviewerHarness{domain.ReviewerCodex, ""} {
				if _, err := s.Trigger(ctx, "claude-worker", harness, domain.AgentConfig{}); !errors.Is(err, ports.ErrCodexAccountSwitchInProgress) {
					t.Fatalf("manual reviewer launch admitted: %v", err)
				}
				if _, err := s.TriggerAuto(ctx, "claude-worker", harness); !errors.Is(err, ports.ErrCodexAccountSwitchInProgress) {
					t.Fatalf("automatic reviewer launch admitted: %v", err)
				}
			}
			if err := s.RestoreReviewer(ctx, "claude-worker"); !errors.Is(err, ports.ErrCodexAccountSwitchInProgress) {
				t.Fatalf("reviewer restore admitted: %v", err)
			}
			if _, err := s.SwitchReviewer(ctx, "claude-worker", domain.ReviewerCodex, domain.AgentConfig{}); !errors.Is(err, ports.ErrCodexAccountSwitchInProgress) {
				t.Fatalf("reviewer switch admitted: %v", err)
			}
			if calls != 0 {
				t.Fatalf("review engine called %d times during replacement", calls)
			}
			lease.Release()
			if _, err := s.Trigger(ctx, "claude-worker", "", domain.AgentConfig{}); err != nil || calls != 1 {
				t.Fatalf("fresh reviewer launch after replacement: calls=%d, error=%v", calls, err)
			}
		})
	}
}
