package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestStartSessionInterfaceTransitionRejectsInvalidModesBeforeDatabaseAccess(t *testing.T) {
	store := &Store{}
	for _, tc := range []struct {
		name   string
		source domain.SessionInterface
		target domain.SessionInterface
	}{
		{name: "unknown source", source: "unknown", target: domain.SessionInterfaceChat},
		{name: "unknown target", source: domain.SessionInterfaceTUI, target: "unknown"},
		{name: "same mode", source: domain.SessionInterfaceTUI, target: domain.SessionInterfaceTUI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.StartSessionInterfaceTransition(
				context.Background(), domain.Principal{}, "org", "session",
				tc.source, tc.target, domain.SessionInterfaceTransitionDrain, "",
			)
			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("start error = %v, want ErrInvalidTransition", err)
			}
		})
	}
}

func TestActiveTransitionConstraintReportsTransitionInProgress(t *testing.T) {
	err := normalizeConstraintError(&pgconn.PgError{
		Code:           "23505",
		ConstraintName: "ao_interface_transitions_one_active",
	})
	if !errors.Is(err, ErrTransitionInProgress) {
		t.Fatalf("duplicate active transition error = %v, want ErrTransitionInProgress", err)
	}
}

func TestLatestRelevantTransitionDoesNotResurfaceFailureAfterSuccess(t *testing.T) {
	// The store selects the newest attempt before applying this rule. A
	// completed attempt supersedes an earlier failed notice.
	for _, tc := range []struct {
		phase domain.SessionInterfaceTransitionPhase
		want  bool
	}{
		{domain.SessionInterfaceTransitionRequested, true},
		{domain.SessionInterfaceTransitionFailed, true},
		{domain.SessionInterfaceTransitionRecovery, true},
		{domain.SessionInterfaceTransitionCompleted, false},
		{domain.SessionInterfaceTransitionCancelled, false},
	} {
		if got := latestTransitionIsRelevant(tc.phase); got != tc.want {
			t.Errorf("phase %q relevant = %v, want %v", tc.phase, got, tc.want)
		}
	}
}
