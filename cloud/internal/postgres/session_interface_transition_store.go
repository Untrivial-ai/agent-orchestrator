package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

var (
	// ErrTransitionInProgress reports a session that already owns an active
	// interface transition. Only one controller handoff may run at a time.
	ErrTransitionInProgress = errors.New("interface transition already in progress")
	// ErrTransitionNotFound reports a transition id or notice that does not
	// exist for the requested session.
	ErrTransitionNotFound = errors.New("interface transition not found")
	// ErrTransitionNotCancellable reports a transition the coordinator has
	// advanced into a phase where cancelling would leave controller ownership
	// ambiguous.
	ErrTransitionNotCancellable = errors.New("interface transition is not cancellable")
	// ErrTransitionNoticeNotAcknowledgeable reports an acknowledgement attempt
	// against a transition that is not in a failed or recovered state.
	ErrTransitionNoticeNotAcknowledgeable = errors.New("interface transition notice is not acknowledgeable")
	// ErrTransitionStale fences a phase advance from a coordinator that lost
	// ownership to a newer replica's single-owner claim.
	ErrTransitionStale = errors.New("interface transition was claimed by another coordinator")
)

// GetSessionInterfaceTransition returns one transition row by id under the
// caller's tenant context.
func (s *Store) GetSessionInterfaceTransition(
	ctx context.Context,
	principal domain.Principal,
	orgID, transitionID string,
) (domain.SessionInterfaceTransition, error) {
	var transition domain.SessionInterfaceTransition
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		var err error
		transition, err = getInterfaceTransition(ctx, tx, transitionID)
		return err
	})
	return transition, err
}

// StartSessionInterfaceTransition durably claims a session for an interface
// handoff. It returns the committed transition and whether a new row was
// created. When a session already owns an active transition, no work is done
// and ErrTransitionInProgress is returned.
func (s *Store) StartSessionInterfaceTransition(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID string,
	source, target domain.SessionInterface,
	policy domain.SessionInterfaceTransitionPolicy,
	nativeConversationID string,
) (domain.SessionInterfaceTransition, error) {
	var transition domain.SessionInterfaceTransition
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		var active bool
		if err := tx.QueryRow(
			ctx,
			`SELECT EXISTS (
				SELECT 1 FROM ao_interface_transitions t
				WHERE t.session_id = $1 AND t.org_id = $2
					AND t.phase NOT IN (
						'completed', 'failed', 'cancelled', 'recovery_required'
					)
			)`,
			sessionID,
			orgID,
		).Scan(&active); err != nil {
			return err
		}
		if active {
			return ErrTransitionInProgress
		}
		var err error
		transition, err = insertInterfaceTransition(
			ctx, tx, orgID, sessionID, source, target, policy, nativeConversationID,
		)
		return err
	})
	if err != nil {
		return domain.SessionInterfaceTransition{}, err
	}
	return transition, nil
}

// GetActiveSessionInterfaceTransition returns the active transition for a
// session, if any, under the caller's tenant context.
func (s *Store) GetActiveSessionInterfaceTransition(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID string,
) (domain.SessionInterfaceTransition, bool, error) {
	return s.getLatestSessionInterfaceTransition(ctx, principal, orgID, sessionID, false)
}

// GetLatestRelevantSessionInterfaceTransition returns the active transition or
// the latest failed/recovery-required transition for a session. Completed and
// cancelled attempts are intentionally excluded: they are audit history, not
// actionable status for a client.
func (s *Store) GetLatestRelevantSessionInterfaceTransition(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID string,
) (domain.SessionInterfaceTransition, bool, error) {
	return s.getLatestSessionInterfaceTransition(ctx, principal, orgID, sessionID, true)
}

func (s *Store) getLatestSessionInterfaceTransition(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID string,
	includeTerminal bool,
) (domain.SessionInterfaceTransition, bool, error) {
	var transition domain.SessionInterfaceTransition
	var found bool
	err := s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		phaseFilter := `t.phase NOT IN ('completed', 'failed', 'cancelled', 'recovery_required')`
		if includeTerminal {
			phaseFilter = `t.phase IN ('requested', 'preflighting', 'draining', 'source_stopping', 'source_stopped', 'target_starting', 'activating', 'failed', 'recovery_required')`
		}
		var sourceInterface string
		err := tx.QueryRow(
			ctx,
			`SELECT t.id, t.org_id, t.session_id, t.source_interface,
				t.target_interface, t.policy, t.phase, t.native_conversation_id,
				t.error_code, t.error_detail, t.notice_acknowledged_at,
				t.created_at, t.updated_at, t.completed_at
			FROM ao_interface_transitions t
			WHERE t.session_id = $1 AND t.org_id = $2
				AND `+phaseFilter+`
			ORDER BY t.created_at DESC, t.id DESC
			LIMIT 1`,
			sessionID,
			orgID,
		).Scan(
			&transition.ID,
			&transition.OrgID,
			&transition.SessionID,
			&sourceInterface,
			&transition.TargetInterface,
			&transition.Policy,
			&transition.Phase,
			&transition.NativeConversationID,
			&transition.ErrorCode,
			&transition.ErrorDetail,
			&transition.NoticeAcknowledgedAt,
			&transition.CreatedAt,
			&transition.UpdatedAt,
			&transition.CompletedAt,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		transition.SourceInterface = domain.SessionInterface(sourceInterface)
		found = true
		return nil
	})
	if err != nil {
		return domain.SessionInterfaceTransition{}, false, err
	}
	return transition, found, nil
}

// ListActiveSessionInterfaceTransitions scans sessions that own an in-progress
// transition. It runs under service context so a reconciler can repair
// transitions left mid-flight by a crashed coordinator across organizations.
func (s *Store) ListActiveSessionInterfaceTransitions(
	ctx context.Context,
) ([]domain.SessionInterfaceTransition, error) {
	var transitions []domain.SessionInterfaceTransition
	err := s.withService(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(
			ctx,
			`SELECT t.id, t.org_id, t.session_id, t.source_interface,
				t.target_interface, t.policy, t.phase, t.native_conversation_id,
				t.error_code, t.error_detail, t.notice_acknowledged_at,
				t.created_at, t.updated_at, t.completed_at
			FROM ao_interface_transitions t
			WHERE t.phase NOT IN (
				'completed', 'failed', 'cancelled', 'recovery_required'
			)`,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var transition domain.SessionInterfaceTransition
			if err := scanInterfaceTransition(rows, &transition); err != nil {
				return err
			}
			transitions = append(transitions, transition)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return transitions, nil
}

// AdvanceSessionInterfaceTransition moves a transition from one phase to the
// next under the caller's tenant context. It fails closed when the current
// phase no longer matches, so two coordinators cannot advance the same row.
func (s *Store) AdvanceSessionInterfaceTransition(
	ctx context.Context,
	principal domain.Principal,
	orgID, transitionID string,
	from, to domain.SessionInterfaceTransitionPhase,
	nativeConversationID string,
	errorCode, errorDetail string,
) error {
	return s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(
			ctx,
			`UPDATE ao_interface_transitions
			SET phase = $1,
				native_conversation_id = CASE WHEN $2 <> '' THEN $2 ELSE native_conversation_id END,
				error_code = $3,
				error_detail = $4,
				updated_at = now(),
				completed_at = CASE
					WHEN $1 IN ('completed', 'failed', 'cancelled', 'recovery_required')
						THEN now()
					ELSE completed_at
				END
			WHERE id = $5 AND org_id = $6 AND phase = $7`,
			to,
			nativeConversationID,
			errorCode,
			errorDetail,
			transitionID,
			orgID,
			from,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrTransitionStale
		}
		return nil
	})
}

// AcknowledgeSessionInterfaceTransitionNotice records that a failed or
// recovered handoff notice has been seen without deleting its audit history.
func (s *Store) AcknowledgeSessionInterfaceTransitionNotice(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID, transitionID string,
) error {
	return s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		transition, err := getInterfaceTransition(ctx, tx, transitionID)
		if err != nil {
			return err
		}
		if transition.SessionID != sessionID {
			return ErrTransitionNotFound
		}
		if transition.Phase != domain.SessionInterfaceTransitionFailed &&
			transition.Phase != domain.SessionInterfaceTransitionRecovery {
			return ErrTransitionNoticeNotAcknowledgeable
		}
		tag, err := tx.Exec(
			ctx,
			`UPDATE ao_interface_transitions
			SET notice_acknowledged_at = now(), updated_at = now()
			WHERE id = $1 AND org_id = $2 AND notice_acknowledged_at IS NULL`,
			transitionID,
			orgID,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrTransitionNoticeNotAcknowledgeable
		}
		return nil
	})
}

// EnqueueSessionInterfaceTransitionMessage holds a message while neither
// controller is allowed to accept work.
func (s *Store) EnqueueSessionInterfaceTransitionMessage(
	ctx context.Context,
	transitionID, clientMessageID, message string,
) error {
	_, err := s.pool.Exec(
		ctx,
		`INSERT INTO ao_interface_transition_messages (
			transition_id, client_message_id, message
		) VALUES ($1, $2, $3)
		ON CONFLICT (transition_id, client_message_id) DO NOTHING`,
		transitionID,
		clientMessageID,
		message,
	)
	return err
}

// CoordinatedInterfaceTransition is the service-context view the coordinator
// needs: the durable row plus the session interface values it must transition.
type CoordinatedInterfaceTransition struct {
	domain.SessionInterfaceTransition
	Harness string
}

// ClaimCoordinatedInterfaceTransitions atomically claims one active transition
// row per session for this coordinator owner and returns the session context.
// The partial unique index guarantees a single active row per session, so a
// competing replica can never claim the same session concurrently.
func (s *Store) ClaimCoordinatedInterfaceTransitions(
	ctx context.Context,
	owner string,
	limit int,
	lease time.Duration,
) ([]CoordinatedInterfaceTransition, error) {
	var transitions []CoordinatedInterfaceTransition
	err := s.withService(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(
			ctx,
			`WITH candidate AS (
				SELECT t.id, session.harness
				FROM ao_interface_transitions t
				JOIN ao_sessions session
					ON session.org_id = t.org_id AND session.id = t.session_id
				WHERE t.phase NOT IN (
						'completed', 'failed', 'cancelled', 'recovery_required'
					)
					AND (
						t.claimed_by = ''
						OR (t.claimed_by = $1 AND t.claimed_at > now() - $3::interval)
						OR t.claimed_at < now() - $3::interval
					)
				ORDER BY t.created_at, t.id
				FOR UPDATE OF t SKIP LOCKED
				LIMIT $2
			)
			UPDATE ao_interface_transitions t
			SET claimed_by = $1, claimed_at = now(), updated_at = now()
			FROM candidate
			WHERE t.id = candidate.id
			RETURNING t.id, t.org_id, t.session_id, t.source_interface,
				t.target_interface, t.policy, t.phase, t.native_conversation_id,
				t.error_code, t.error_detail, t.notice_acknowledged_at,
				t.created_at, t.updated_at, t.completed_at, candidate.harness`,
			owner,
			limit,
			lease,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var transition domain.SessionInterfaceTransition
			var harness string
			if err := rows.Scan(
				&transition.ID,
				&transition.OrgID,
				&transition.SessionID,
				&transition.SourceInterface,
				&transition.TargetInterface,
				&transition.Policy,
				&transition.Phase,
				&transition.NativeConversationID,
				&transition.ErrorCode,
				&transition.ErrorDetail,
				&transition.NoticeAcknowledgedAt,
				&transition.CreatedAt,
				&transition.UpdatedAt,
				&transition.CompletedAt,
				&harness,
			); err != nil {
				return err
			}
			transitions = append(transitions, CoordinatedInterfaceTransition{
				SessionInterfaceTransition: transition,
				Harness:                    harness,
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return transitions, nil
}

// RenewCoordinatedInterfaceClaim keeps a claimed transition leased to this
// owner while a bounded worker operation is in flight.
func (s *Store) RenewCoordinatedInterfaceClaim(
	ctx context.Context,
	owner, transitionID string,
	lease time.Duration,
) error {
	return s.withService(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE ao_interface_transitions
			SET claimed_by = $1, claimed_at = now(), updated_at = now()
			WHERE id = $2`, owner, transitionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrTransitionNotFound
		}
		return nil
	})
}

// AdvanceCoordinatedInterfaceTransition moves a claimed transition to the next
// phase under service context. It fails closed when the current phase no longer
// matches, so two coordinators cannot advance the same row.
func (s *Store) AdvanceCoordinatedInterfaceTransition(
	ctx context.Context,
	owner, transitionID string,
	from, to domain.SessionInterfaceTransitionPhase,
	nativeConversationID string,
	errorCode, errorDetail string,
) error {
	return s.withService(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE ao_interface_transitions
			SET phase = $1,
				native_conversation_id = CASE WHEN $2 <> '' THEN $2 ELSE native_conversation_id END,
				error_code = $3,
				error_detail = $4,
				updated_at = now(),
				completed_at = CASE
					WHEN $1 IN ('completed', 'failed', 'cancelled', 'recovery_required')
						THEN now()
					ELSE completed_at
				END
			WHERE id = $5 AND claimed_by = $6 AND phase = $7`,
			to, nativeConversationID, errorCode, errorDetail, transitionID, owner, from)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrTransitionStale
		}
		return nil
	})
}

// CommitCoordinatedSessionInterface commits a session's committed interface and
// returns whether the compare-and-swap succeeded. The transition coordinator
// must commit the new controller before releasing the claim so the session row
// never disagrees with the row that explains an in-progress handoff.
func (s *Store) CommitCoordinatedSessionInterface(
	ctx context.Context,
	owner, orgID, sessionID string,
	interfaceValue domain.SessionInterface,
) (bool, error) {
	var committed bool
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE ao_sessions
			SET interface = $1, updated_at = now()
			WHERE org_id = $2 AND id = $3`, interfaceValue, orgID, sessionID)
		if err != nil {
			return err
		}
		committed = tag.RowsAffected() > 0
		return nil
	})
	return committed, err
}

// ReleaseCoordinatedInterfaceClaim drops a coordinator's hold on a transition.
func (s *Store) ReleaseCoordinatedInterfaceClaim(
	ctx context.Context,
	owner, transitionID string,
) error {
	return s.withService(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE ao_interface_transitions
			SET claimed_by = '', claimed_at = NULL, updated_at = now()
			WHERE id = $1 AND claimed_by = $2`, transitionID, owner)
		return err
	})
}

func insertInterfaceTransition(
	ctx context.Context,
	tx pgx.Tx,
	orgID, sessionID string,
	source, target domain.SessionInterface,
	policy domain.SessionInterfaceTransitionPolicy,
	nativeConversationID string,
) (domain.SessionInterfaceTransition, error) {
	var transition domain.SessionInterfaceTransition
	err := tx.QueryRow(
		ctx,
		`INSERT INTO ao_interface_transitions (
			org_id, session_id, source_interface, target_interface, policy,
			phase, native_conversation_id
		) VALUES ($1, $2, $3, $4, $5, 'requested', $6)
		RETURNING id, org_id, session_id, source_interface, target_interface,
			policy, phase, native_conversation_id, error_code, error_detail,
			notice_acknowledged_at, created_at, updated_at, completed_at`,
		orgID,
		sessionID,
		source,
		target,
		policy,
		nativeConversationID,
	).Scan(
		&transition.ID,
		&transition.OrgID,
		&transition.SessionID,
		&transition.SourceInterface,
		&transition.TargetInterface,
		&transition.Policy,
		&transition.Phase,
		&transition.NativeConversationID,
		&transition.ErrorCode,
		&transition.ErrorDetail,
		&transition.NoticeAcknowledgedAt,
		&transition.CreatedAt,
		&transition.UpdatedAt,
		&transition.CompletedAt,
	)
	if err != nil {
		return domain.SessionInterfaceTransition{}, normalizeConstraintError(err)
	}
	return transition, nil
}

func getInterfaceTransition(
	ctx context.Context,
	tx pgx.Tx,
	transitionID string,
) (domain.SessionInterfaceTransition, error) {
	var transition domain.SessionInterfaceTransition
	err := tx.QueryRow(
		ctx,
		`SELECT id, org_id, session_id, source_interface, target_interface,
			policy, phase, native_conversation_id, error_code, error_detail,
			notice_acknowledged_at, created_at, updated_at, completed_at
		FROM ao_interface_transitions
		WHERE id = $1`,
		transitionID,
	).Scan(
		&transition.ID,
		&transition.OrgID,
		&transition.SessionID,
		&transition.SourceInterface,
		&transition.TargetInterface,
		&transition.Policy,
		&transition.Phase,
		&transition.NativeConversationID,
		&transition.ErrorCode,
		&transition.ErrorDetail,
		&transition.NoticeAcknowledgedAt,
		&transition.CreatedAt,
		&transition.UpdatedAt,
		&transition.CompletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SessionInterfaceTransition{}, ErrTransitionNotFound
	}
	if err != nil {
		return domain.SessionInterfaceTransition{}, fmt.Errorf("load interface transition: %w", err)
	}
	return transition, nil
}

func scanInterfaceTransition(row pgx.Row, transition *domain.SessionInterfaceTransition) error {
	return row.Scan(
		&transition.ID,
		&transition.OrgID,
		&transition.SessionID,
		&transition.SourceInterface,
		&transition.TargetInterface,
		&transition.Policy,
		&transition.Phase,
		&transition.NativeConversationID,
		&transition.ErrorCode,
		&transition.ErrorDetail,
		&transition.NoticeAcknowledgedAt,
		&transition.CreatedAt,
		&transition.UpdatedAt,
		&transition.CompletedAt,
	)
}
