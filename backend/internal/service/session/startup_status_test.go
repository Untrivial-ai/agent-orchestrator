package session

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

func TestStartupRecoveryDerivesBlockedWithoutPersistingActivity(t *testing.T) {
	for _, mode := range []domain.SessionMode{domain.SessionModeTUI, domain.SessionModeChat} {
		t.Run(string(mode), func(t *testing.T) {
			rec := statusRec(domain.ActivityIdle, false)
			rec.Mode = mode
			rec.Metadata.Startup = &domain.SessionStartup{ID: "attempt-1", Stage: "cleanup_pending", LastError: "runtime exit not confirmed"}
			if got := deriveStatus(rec, nil, statusNow, true); got != domain.StatusNeedsInput {
				t.Fatalf("startup failure status = %s", got)
			}
			presentation := deriveKanbanPresentation(rec, nil, nil, statusNow, true)
			if presentation.DisplayStatus != contract.DisplayBlocked {
				t.Fatalf("startup failure presentation = %+v", presentation)
			}
			if rec.Activity.State != domain.ActivityIdle {
				t.Fatal("stored activity changed")
			}
			rec.IsTerminated = true
			if got := deriveStatus(rec, nil, statusNow, true); got != domain.StatusTerminated {
				t.Fatalf("confirmed shutdown status = %s", got)
			}
		})
	}
}

func TestInFlightStartupIsNotPresentedAsFailure(t *testing.T) {
	rec := statusRec(domain.ActivityIdle, false)
	rec.Metadata.Startup = &domain.SessionStartup{ID: "attempt-1", Stage: "provisioning", StartedAt: statusNow}
	if got := deriveStatus(rec, nil, statusNow, true); got != domain.StatusIdle {
		t.Fatalf("in-flight startup status = %s", got)
	}
}
