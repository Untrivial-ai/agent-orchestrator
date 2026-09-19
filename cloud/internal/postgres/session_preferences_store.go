package postgres

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

// SessionPreferencesUpdate uses pointers so a caller can change one inspector
// control without racing another control that was updated concurrently.
type SessionPreferencesUpdate struct {
	ReviewerHarness    *string
	AutoInjectCI       *bool
	AutoInjectReview   *bool
	TerminateOnPRMerge *bool
}

var cloudReviewerHarnesses = []string{"claude-code", "codex", "cursor"}

// AvailableSessionReviewerHarnesses returns only providers whose valid default
// connection can actually be redeemed by this session's worker: an org default
// wins, otherwise the session creator's personal default is used.
func (s *Store) AvailableSessionReviewerHarnesses(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID string,
) ([]string, error) {
	available := make([]string, 0, len(cloudReviewerHarnesses))
	err := s.withSessionAccess(ctx, principal, orgID, sessionID, func(tx pgx.Tx, _ sessionAccess) error {
		createdByUserID, err := sessionCreatorID(ctx, tx, orgID, sessionID)
		if err != nil {
			return err
		}
		providers, err := availableReviewerHarnesses(ctx, tx, orgID, createdByUserID)
		if err != nil {
			return err
		}
		available = providers
		return nil
	})
	return available, err
}

func (s *Store) UpdateSessionPreferences(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID string,
	update SessionPreferencesUpdate,
) (domain.Session, error) {
	var session domain.Session
	err := s.withSessionAccess(ctx, principal, orgID, sessionID, func(tx pgx.Tx, access sessionAccess) error {
		if access.Role == "viewer" {
			return ErrForbidden
		}
		var mode string
		if err := tx.QueryRow(ctx, `SELECT mode FROM ao_sessions WHERE org_id = $1 AND id = $2`, orgID, sessionID).Scan(&mode); err != nil {
			return err
		}
		if effectiveMode(mode, access.ModeCap) == "read-only" {
			return ErrForbidden
		}
		if update.ReviewerHarness != nil && *update.ReviewerHarness != "" {
			creatorID, err := sessionCreatorID(ctx, tx, orgID, sessionID)
			if err != nil {
				return err
			}
			available, err := availableReviewerHarnesses(ctx, tx, orgID, creatorID)
			if err != nil {
				return err
			}
			if !containsHarness(available, *update.ReviewerHarness) {
				return fmt.Errorf("%w: reviewer harness is not connected for this session", ErrInvalid)
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE ao_sessions
			SET reviewer_harness = COALESCE($3, reviewer_harness),
				auto_inject_ci = COALESCE($4, auto_inject_ci),
				auto_inject_review = COALESCE($5, auto_inject_review),
				terminate_on_pr_merge = COALESCE($6, terminate_on_pr_merge),
				updated_at = now()
			WHERE org_id = $1 AND id = $2`,
			orgID, sessionID, update.ReviewerHarness, update.AutoInjectCI,
			update.AutoInjectReview, update.TerminateOnPRMerge); err != nil {
			return err
		}
		return getSession(ctx, tx, orgID, sessionID, &session)
	})
	return session, err
}

func sessionCreatorID(ctx context.Context, tx pgx.Tx, orgID, sessionID string) (*string, error) {
	var creatorID *string
	if err := tx.QueryRow(ctx, `SELECT created_by_user_id::text FROM ao_sessions WHERE org_id = $1 AND id = $2`, orgID, sessionID).Scan(&creatorID); err != nil {
		return nil, err
	}
	if creatorID != nil {
		if _, err := tx.Exec(ctx, `SELECT set_config('ao.user_id', $1, true)`, *creatorID); err != nil {
			return nil, err
		}
	}
	return creatorID, nil
}

func availableReviewerHarnesses(ctx context.Context, tx pgx.Tx, orgID string, creatorID *string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT provider FROM ao_provider_connections
		WHERE org_id = $1 AND label = 'default' AND validation_state = 'valid'
		UNION
		SELECT provider FROM ao_user_provider_connections
		WHERE user_id = $2::uuid AND label = 'default' AND validation_state = 'valid'
		ORDER BY provider`, orgID, creatorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	connected := make(map[string]bool)
	for rows.Next() {
		var provider string
		if err := rows.Scan(&provider); err != nil {
			return nil, err
		}
		connected[provider] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	available := make([]string, 0, len(cloudReviewerHarnesses))
	for _, provider := range cloudReviewerHarnesses {
		if connected[provider] {
			available = append(available, provider)
		}
	}
	return available, nil
}

func containsHarness(harnesses []string, wanted string) bool {
	for _, harness := range harnesses {
		if harness == wanted {
			return true
		}
	}
	return false
}
