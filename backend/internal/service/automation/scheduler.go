package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	catchupLimit      = 3
	dueScanLimit      = 100
	dispatchScanLimit = 100
	spawnLease        = 2 * time.Minute
	maxRunErrorBytes  = 512
)

// SessionSpawner is the existing user-facing session launch boundary.
type SessionSpawner interface {
	Spawn(context.Context, ports.SpawnConfig) (domain.Session, int, int, error)
	Kill(context.Context, domain.SessionID) (bool, error)
}

// schedulerStore is deliberately separate from CRUD Store so focused CRUD
// tests and alternate read adapters need not implement daemon-only operations.
type schedulerStore interface {
	ListDueAutomations(context.Context, time.Time, int64) ([]domain.Automation, error)
	MaterializeAutomationRuns(context.Context, domain.AutomationID, time.Time, []domain.AutomationRun, time.Time, time.Time, time.Time) (bool, error)
	ClaimNextAutomationRun(context.Context, time.Time, time.Time) (domain.AutomationRun, bool, error)
	ListActiveAutomationRuns(context.Context) ([]domain.AutomationRun, error)
	ListExpiredSpawningAutomationRuns(context.Context, time.Time) ([]domain.AutomationRun, error)
	GetSessionByAutomationRunID(context.Context, domain.AutomationRunID) (domain.SessionRecord, bool, error)
	GetSession(context.Context, domain.SessionID) (domain.SessionRecord, bool, error)
	MarkAutomationRunRunning(context.Context, domain.AutomationRunID, domain.SessionID, time.Time) (bool, error)
	CompleteAutomationRun(context.Context, domain.AutomationRunID, time.Time) (bool, error)
	FailAutomationRun(context.Context, domain.AutomationRunID, string, time.Time) (bool, error)
	ReleaseAutomationRun(context.Context, domain.AutomationRunID, string, time.Time) (bool, error)
}

func (s *Service) schedulerStore() (schedulerStore, error) {
	store, ok := s.store.(schedulerStore)
	if !ok {
		return nil, fmt.Errorf("automation scheduler storage is unavailable")
	}
	return store, nil
}

// Tick performs one bounded materialization, lifecycle projection, claim, and
// dispatch pass. One bad definition is joined into the returned diagnostic but
// does not prevent other definitions from progressing.
func (s *Service) Tick(ctx context.Context, at time.Time) error {
	store, err := s.schedulerStore()
	if err != nil {
		return err
	}
	now := at.UTC()
	var problems []error
	if err := s.completeAndAdoptActive(ctx, store, now); err != nil {
		problems = append(problems, err)
	}
	due, err := store.ListDueAutomations(ctx, now, dueScanLimit)
	if err != nil {
		problems = append(problems, err)
	} else {
		for _, definition := range due {
			if err := s.materializeDefinition(ctx, store, definition, now); err != nil {
				problems = append(problems, fmt.Errorf("automation %s: %w", definition.ID, err))
			}
		}
	}

	// Claim the batch before launching anything. Each spawning claim blocks its
	// siblings, which guarantees at most one dispatch per definition this poll.
	claims := make([]domain.AutomationRun, 0)
	for len(claims) < dispatchScanLimit && ctx.Err() == nil {
		run, ok, claimErr := store.ClaimNextAutomationRun(ctx, now, now.Add(spawnLease))
		if claimErr != nil {
			problems = append(problems, claimErr)
			break
		}
		if !ok {
			break
		}
		claims = append(claims, run)
	}
	for _, run := range claims {
		if err := s.dispatch(ctx, store, run, now); err != nil {
			problems = append(problems, fmt.Errorf("automation run %s: %w", run.ID, err))
		}
	}
	if ctx.Err() != nil {
		problems = append(problems, ctx.Err())
	}
	return errors.Join(problems...)
}

func (s *Service) materializeDefinition(ctx context.Context, store schedulerStore, definition domain.Automation, now time.Time) error {
	if !definition.Enabled || definition.NextRunAt.After(now) {
		return nil
	}
	occurrence := definition.NextRunAt.UTC()
	runs := make([]domain.AutomationRun, 0, catchupLimit)
	var last time.Time
	for !occurrence.After(now) && len(runs) < catchupLimit {
		runs = append(runs, domain.AutomationRun{
			ID: domain.AutomationRunID("automation-run-" + s.newID()), AutomationID: definition.ID,
			ScheduledFor: occurrence, Status: domain.AutomationRunPending,
			CreatedAt: now, UpdatedAt: now,
		})
		last = occurrence
		next, err := NextOccurrence(definition.RRuleText, definition.Timezone, occurrence)
		if err != nil {
			return err
		}
		occurrence = next
	}
	if !occurrence.After(now) {
		var err error
		occurrence, err = NextOccurrence(definition.RRuleText, definition.Timezone, now)
		if err != nil {
			return err
		}
	}
	if len(runs) == 0 {
		return nil
	}
	_, err := store.MaterializeAutomationRuns(ctx, definition.ID, definition.NextRunAt, runs, last, occurrence, now)
	return err
}

func (s *Service) dispatch(ctx context.Context, store schedulerStore, run domain.AutomationRun, now time.Time) error {
	definition, err := s.Get(ctx, run.AutomationID)
	if err != nil {
		message := runError(err)
		_, markErr := store.FailAutomationRun(context.WithoutCancel(ctx), run.ID, message, now)
		return errors.Join(err, markErr)
	}
	if s.spawner == nil {
		err := fmt.Errorf("session spawner is unavailable")
		_, releaseErr := store.ReleaseAutomationRun(context.WithoutCancel(ctx), run.ID, runError(err), now)
		return errors.Join(err, releaseErr)
	}
	session, _, _, spawnErr := s.spawner.Spawn(ctx, ports.SpawnConfig{
		ProjectID: definition.ProjectID, Kind: definition.Kind, Harness: definition.Harness,
		Prompt: definition.Prompt, DisplayName: definition.DisplayName, AutomationRunID: &run.ID,
	})
	if spawnErr != nil {
		var apiError *apierr.Error
		if session, ok, lookupErr := store.GetSessionByAutomationRunID(context.WithoutCancel(ctx), run.ID); lookupErr != nil {
			return errors.Join(spawnErr, lookupErr)
		} else if ok {
			if session.AutomationLaunchCompleted {
				_, markErr := store.MarkAutomationRunRunning(context.WithoutCancel(ctx), run.ID, session.ID, now)
				return errors.Join(spawnErr, markErr)
			}
			if !session.IsTerminated {
				_, killErr := s.spawner.Kill(context.WithoutCancel(ctx), session.ID)
				if killErr != nil {
					return errors.Join(spawnErr, killErr)
				}
			}
			_, markErr := store.FailAutomationRun(context.WithoutCancel(ctx), run.ID, runError(spawnErr), now)
			return errors.Join(spawnErr, markErr)
		}
		permanent := errors.As(spawnErr, &apiError) && (apiError.Kind == apierr.KindInvalid || apiError.Kind == apierr.KindNotFound)
		if permanent {
			_, markErr := store.FailAutomationRun(context.WithoutCancel(ctx), run.ID, runError(spawnErr), now)
			return errors.Join(spawnErr, markErr)
		}
		_, releaseErr := store.ReleaseAutomationRun(context.WithoutCancel(ctx), run.ID, runError(spawnErr), now)
		return errors.Join(spawnErr, releaseErr)
	}
	_, err = store.MarkAutomationRunRunning(context.WithoutCancel(ctx), run.ID, session.ID, now)
	return err
}

// Reconcile repairs crash-interrupted claims, projects durable completion, and
// materializes a bounded startup catch-up. Dispatch begins in the observer's
// immediate Tick after reconciliation.
func (s *Service) Reconcile(ctx context.Context) error {
	store, err := s.schedulerStore()
	if err != nil {
		return err
	}
	now := s.clock().UTC()
	var problems []error
	expired, err := store.ListExpiredSpawningAutomationRuns(ctx, now)
	if err != nil {
		problems = append(problems, err)
	} else {
		for _, run := range expired {
			session, ok, lookupErr := store.GetSessionByAutomationRunID(ctx, run.ID)
			if lookupErr != nil {
				problems = append(problems, lookupErr)
				continue
			}
			if ok && session.AutomationLaunchCompleted {
				_, lookupErr = store.MarkAutomationRunRunning(ctx, run.ID, session.ID, now)
			} else if ok {
				lookupErr = s.rollbackIncompleteLaunch(ctx, store, run, session, now)
			} else {
				_, lookupErr = store.ReleaseAutomationRun(ctx, run.ID, "Recovered expired spawn claim", now)
			}
			if lookupErr != nil {
				problems = append(problems, lookupErr)
			}
		}
	}
	if err := s.completeAndAdoptActive(ctx, store, now); err != nil {
		problems = append(problems, err)
	}
	due, err := store.ListDueAutomations(ctx, now, dueScanLimit)
	if err != nil {
		problems = append(problems, err)
	} else {
		for _, definition := range due {
			if err := s.materializeDefinition(ctx, store, definition, now); err != nil {
				problems = append(problems, err)
			}
		}
	}
	return errors.Join(problems...)
}

func (s *Service) completeAndAdoptActive(ctx context.Context, store schedulerStore, now time.Time) error {
	runs, err := store.ListActiveAutomationRuns(ctx)
	if err != nil {
		return err
	}
	var problems []error
	for _, run := range runs {
		if run.Status == domain.AutomationRunSpawning {
			session, ok, lookupErr := store.GetSessionByAutomationRunID(ctx, run.ID)
			if lookupErr != nil {
				problems = append(problems, lookupErr)
			} else if ok && session.AutomationLaunchCompleted {
				_, lookupErr = store.MarkAutomationRunRunning(ctx, run.ID, session.ID, now)
				if lookupErr != nil {
					problems = append(problems, lookupErr)
				}
			} else if ok && run.LeaseExpiresAt != nil && !run.LeaseExpiresAt.After(now) {
				if lookupErr = s.rollbackIncompleteLaunch(ctx, store, run, session, now); lookupErr != nil {
					problems = append(problems, lookupErr)
				}
			} else if !ok && run.LeaseExpiresAt != nil && !run.LeaseExpiresAt.After(now) {
				// The daemon died between claiming and session creation, and
				// startup reconciliation ran before the lease expired. Release
				// the claim so later occurrences are not blocked forever.
				if _, lookupErr = store.ReleaseAutomationRun(ctx, run.ID, "Recovered expired spawn claim", now); lookupErr != nil {
					problems = append(problems, lookupErr)
				}
			}
			continue
		}
		var session domain.SessionRecord
		var ok bool
		if run.SessionID != nil {
			session, ok, err = store.GetSession(ctx, *run.SessionID)
		} else {
			session, ok, err = store.GetSessionByAutomationRunID(ctx, run.ID)
		}
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if !ok {
			_, failErr := store.FailAutomationRun(ctx, run.ID, "Linked session is missing", now)
			if failErr != nil {
				problems = append(problems, failErr)
			}
			continue
		}
		if run.SessionID == nil {
			if _, err := store.MarkAutomationRunRunning(ctx, run.ID, session.ID, now); err != nil {
				problems = append(problems, err)
			}
		}
		if session.IsTerminated {
			if _, err := store.CompleteAutomationRun(ctx, run.ID, now); err != nil {
				problems = append(problems, err)
			}
		}
	}
	return errors.Join(problems...)
}

func (s *Service) rollbackIncompleteLaunch(ctx context.Context, store schedulerStore, run domain.AutomationRun, session domain.SessionRecord, now time.Time) error {
	if s.spawner == nil {
		return fmt.Errorf("cannot roll back incomplete automation session %s: session spawner is unavailable", session.ID)
	}
	if _, err := s.spawner.Kill(ctx, session.ID); err != nil {
		return fmt.Errorf("roll back incomplete automation session %s: %w", session.ID, err)
	}
	_, err := store.FailAutomationRun(ctx, run.ID, "Recovered incomplete session launch", now)
	return err
}

func runError(err error) string {
	message := strings.Join(strings.Fields(err.Error()), " ")
	if len(message) > maxRunErrorBytes {
		message = message[:maxRunErrorBytes]
	}
	return message
}
