package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

var (
	// ErrStaleTurn fences callbacks from a worker epoch or attempt that no
	// longer owns the turn.
	ErrStaleTurn = errors.New("turn is owned by another worker epoch or attempt")
	// ErrTurnFinished reports an operation that requires an active turn.
	ErrTurnFinished = errors.New("turn is already finished")
)

const defaultWorkerCredentialLabel = "default"

// ClaimWorkerTurn atomically claims the queued turn for this session, or
// reclaims work left by an older worker epoch. The turn-start event and the
// ownership update commit together.
func (s *Store) ClaimWorkerTurn(
	ctx context.Context,
	orgID, sessionID, workerID string,
	epoch int64,
) (domain.WorkerTurn, bool, error) {
	var turn domain.WorkerTurn
	var claimed bool
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		if err := requireCurrentWorker(ctx, tx, orgID, sessionID, workerID, epoch); err != nil {
			return err
		}

		var state string
		var turnModeCap string
		var turnDeniedCommands []string
		err := tx.QueryRow(
			ctx,
			`WITH candidate AS (
				SELECT turn.id
				FROM ao_turns turn
				JOIN ao_sessions session
					ON session.org_id = turn.org_id AND session.id = turn.session_id
				WHERE turn.org_id = $1
					AND turn.session_id = $2
					AND session.is_terminated = false
					AND (
						turn.state = 'queued'
						OR (
							turn.worker_epoch < $3
							AND turn.state IN ('provisioning', 'running', 'cancel_requested')
						)
					)
				ORDER BY turn.created_at, turn.id
				FOR UPDATE OF turn SKIP LOCKED
				LIMIT 1
			),
			claimed AS (
				UPDATE ao_turns turn
				SET state = CASE
						WHEN turn.state = 'cancel_requested' THEN 'cancel_requested'
						ELSE 'running'
					END,
					worker_epoch = $3,
					attempt_count = turn.attempt_count + 1,
					started_at = now(),
					error_message = '',
					updated_at = now()
				FROM candidate
				WHERE turn.id = candidate.id
				RETURNING turn.id, turn.session_id, turn.user_message_sequence,
					turn.state, turn.attempt_count, turn.worker_epoch
			)
			SELECT claimed.id, claimed.session_id, event.payload->>'text',
				session.mode, session.denied_commands, session.harness,
				claimed.attempt_count, claimed.worker_epoch,
				claimed.state, session.agent_session_id,
				claimed.user_message_sequence,
				COALESCE(claimed_turn.mode_cap, ''), COALESCE(claimed_turn.denied_commands, ARRAY[]::text[])
			FROM claimed
			JOIN ao_sessions session
				ON session.org_id = $1 AND session.id = claimed.session_id
			JOIN ao_events event
				ON event.org_id = $1
				AND event.session_id = claimed.session_id
				AND event.sequence = claimed.user_message_sequence
			JOIN ao_turns claimed_turn
				ON claimed_turn.id = claimed.id`,
			orgID,
			sessionID,
			epoch,
		).Scan(
			&turn.ID,
			&turn.SessionID,
			&turn.Prompt,
			&turn.Mode,
			&turn.DeniedCommands,
			&turn.Harness,
			&turn.Attempt,
			&turn.WorkerEpoch,
			&state,
			&turn.AgentSessionID,
			&turn.UserEventSequence,
			&turnModeCap,
			&turnDeniedCommands,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim worker turn: %w", err)
		}
		// The session's own mode/denied_commands are the ceiling; a turn
		// created from a capped share-grant holder's message narrows that
		// ceiling further, never loosens it. See effectiveMode.
		turn.Mode = effectiveMode(turn.Mode, turnModeCap)
		turn.DeniedCommands = effectiveDeniedCommands(turn.DeniedCommands, turnDeniedCommands)
		turn.CancelRequested = state == "cancel_requested"
		claimed = true
		return appendTypedEvent(ctx, tx, orgID, sessionID, "chat.turn_started", map[string]any{
			"turnId":      turn.ID,
			"attempt":     turn.Attempt,
			"workerEpoch": epoch,
		})
	})
	return turn, claimed, err
}

// RequestTurnCancellation records user intent exactly once. Repeated requests
// are idempotent and do not create duplicate chat events.
func (s *Store) RequestTurnCancellation(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID, turnID string,
) error {
	return s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		var state string
		err := tx.QueryRow(
			ctx,
			`SELECT state
			FROM ao_turns
			WHERE org_id = $1 AND session_id = $2 AND id = $3
			FOR UPDATE`,
			orgID,
			sessionID,
			turnID,
		).Scan(&state)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		switch state {
		case "completed", "failed":
			return ErrTurnFinished
		case "cancel_requested":
			return nil
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_turns
			SET state = 'cancel_requested', updated_at = now()
			WHERE org_id = $1 AND session_id = $2 AND id = $3`,
			orgID,
			sessionID,
			turnID,
		); err != nil {
			return err
		}
		return appendTypedEvent(ctx, tx, orgID, sessionID, "chat.interrupt_requested", map[string]any{
			"turnId": turnID,
		})
	})
}

// WorkerTurnCancellationRequested observes cancellation only when the caller
// still owns the exact worker epoch and attempt.
func (s *Store) WorkerTurnCancellationRequested(
	ctx context.Context,
	orgID, sessionID, workerID, turnID string,
	epoch int64,
	attempt int,
) (bool, error) {
	var requested bool
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		if err := requireCurrentWorker(ctx, tx, orgID, sessionID, workerID, epoch); err != nil {
			return err
		}
		var state string
		var ownerEpoch int64
		var ownerAttempt int
		err := tx.QueryRow(
			ctx,
			`SELECT state, worker_epoch, attempt_count
			FROM ao_turns
			WHERE org_id = $1 AND session_id = $2 AND id = $3`,
			orgID,
			sessionID,
			turnID,
		).Scan(&state, &ownerEpoch, &ownerAttempt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if ownerEpoch != epoch || ownerAttempt != attempt {
			return ErrStaleTurn
		}
		if state == "completed" || state == "failed" {
			return ErrTurnFinished
		}
		requested = state == "cancel_requested"
		return nil
	})
	return requested, err
}

// AppendWorkerTurnOutput appends one bounded, typed assistant output event
// while the exact worker epoch and attempt still own the active turn.
func (s *Store) AppendWorkerTurnOutput(
	ctx context.Context,
	orgID, sessionID, workerID, turnID string,
	epoch int64,
	attempt int,
	stream, text string,
) error {
	return s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		if err := requireCurrentWorker(ctx, tx, orgID, sessionID, workerID, epoch); err != nil {
			return err
		}
		if err := requireActiveTurnFence(
			ctx, tx, orgID, sessionID, turnID, epoch, attempt,
		); err != nil {
			return err
		}
		return appendTypedEvent(ctx, tx, orgID, sessionID, "chat.assistant_delta", map[string]any{
			"turnId":  turnID,
			"attempt": attempt,
			"stream":  stream,
			"text":    text,
		})
	})
}

// FinishWorkerTurn records one terminal state and its typed chat event in the
// same transaction. A retried callback for the same fence is idempotent.
func (s *Store) FinishWorkerTurn(
	ctx context.Context,
	orgID, sessionID, workerID, turnID string,
	epoch int64,
	attempt int,
	outcome, errorMessage string,
) (bool, error) {
	var alreadyFinished bool
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		if err := requireCurrentWorker(ctx, tx, orgID, sessionID, workerID, epoch); err != nil {
			return err
		}
		var state string
		var ownerEpoch int64
		var ownerAttempt int
		err := tx.QueryRow(
			ctx,
			`SELECT state, worker_epoch, attempt_count
			FROM ao_turns
			WHERE org_id = $1 AND session_id = $2 AND id = $3
			FOR UPDATE`,
			orgID,
			sessionID,
			turnID,
		).Scan(&state, &ownerEpoch, &ownerAttempt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if ownerEpoch != epoch || ownerAttempt != attempt {
			return ErrStaleTurn
		}
		if state == "completed" || state == "failed" {
			alreadyFinished = true
			return nil
		}

		nextState := "completed"
		eventType := "chat.turn_completed"
		payload := map[string]any{"turnId": turnID, "attempt": attempt}
		switch outcome {
		case "completed":
		case "cancelled":
			eventType = "chat.turn_interrupted"
		case "failed":
			nextState = "failed"
			eventType = "chat.turn_aborted"
			payload["error"] = errorMessage
		default:
			return ErrInvalid
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_turns
			SET state = $4,
				error_message = $5,
				completed_at = now(),
				updated_at = now()
			WHERE org_id = $1 AND session_id = $2 AND id = $3`,
			orgID,
			sessionID,
			turnID,
			nextState,
			errorMessage,
		); err != nil {
			return err
		}
		return appendTypedEvent(ctx, tx, orgID, sessionID, eventType, payload)
	})
	return alreadyFinished, err
}

// WorkerAgentCredential returns only the valid default credential selected by
// the current session's harness. The encrypted bytes stay opaque to the store.
func (s *Store) WorkerAgentCredential(
	ctx context.Context,
	orgID, sessionID, workerID string,
	epoch int64,
) (domain.WorkerCredential, error) {
	var credential domain.WorkerCredential
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		if err := requireCurrentWorker(ctx, tx, orgID, sessionID, workerID, epoch); err != nil {
			return err
		}
		err := tx.QueryRow(
			ctx,
			`SELECT connection.provider,
				COALESCE(connection.config->>'credentialType', ''),
				connection.encrypted_secret,
				connection.secret_nonce
			FROM ao_sessions session
			JOIN ao_provider_connections connection
				ON connection.org_id = session.org_id
				AND connection.provider = session.harness
				AND connection.label = $3
				AND connection.validation_state = 'valid'
			WHERE session.org_id = $1
				AND session.id = $2
				AND session.is_terminated = false`,
			orgID,
			sessionID,
			defaultWorkerCredentialLabel,
		).Scan(
			&credential.Provider,
			&credential.CredentialType,
			&credential.EncryptedSecret,
			&credential.Nonce,
		)
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// The org has no shared connection for this harness — fall back to
		// the session creator's own personal connection, if they have one.
		// This is what lets connecting a credential once make it usable
		// across every org a person belongs to, not just the one they
		// connected it in; it never overrides an org-level connection that
		// exists, only fills in when there isn't one.
		var harness string
		var createdByUserID *string
		if err := tx.QueryRow(
			ctx,
			`SELECT harness, created_by_user_id::text
			FROM ao_sessions
			WHERE org_id = $1 AND id = $2 AND is_terminated = false`,
			orgID, sessionID,
		).Scan(&harness, &createdByUserID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if createdByUserID == nil {
			return ErrNotFound
		}
		if _, err := tx.Exec(
			ctx, `SELECT set_config('ao.user_id', $1, true)`, *createdByUserID,
		); err != nil {
			return err
		}
		err = tx.QueryRow(
			ctx,
			`SELECT connection.provider,
				COALESCE(connection.config->>'credentialType', ''),
				connection.encrypted_secret,
				connection.secret_nonce
			FROM ao_user_provider_connections connection
			WHERE connection.user_id = $1
			  AND connection.provider = $2
			  AND connection.label = $3
			  AND connection.validation_state = 'valid'`,
			*createdByUserID,
			harness,
			defaultWorkerCredentialLabel,
		).Scan(
			&credential.Provider,
			&credential.CredentialType,
			&credential.EncryptedSecret,
			&credential.Nonce,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err == nil {
			credential.OwnerUserID = *createdByUserID
		}
		return err
	})
	return credential, err
}

func requireCurrentWorker(
	ctx context.Context,
	tx pgx.Tx,
	orgID, sessionID, workerID string,
	epoch int64,
) error {
	var current bool
	if err := tx.QueryRow(
		ctx,
		`SELECT EXISTS (
			SELECT 1
			FROM ao_worker_connections
			WHERE org_id = $1
				AND session_id = $2
				AND worker_id = $3
				AND epoch = $4
				AND disconnected_at IS NULL
		)`,
		orgID,
		sessionID,
		workerID,
		epoch,
	).Scan(&current); err != nil {
		return err
	}
	if !current {
		return ErrStaleWorker
	}
	return nil
}

func requireActiveTurnFence(
	ctx context.Context,
	tx pgx.Tx,
	orgID, sessionID, turnID string,
	epoch int64,
	attempt int,
) error {
	var state string
	var ownerEpoch int64
	var ownerAttempt int
	err := tx.QueryRow(
		ctx,
		`SELECT state, worker_epoch, attempt_count
		FROM ao_turns
		WHERE org_id = $1 AND session_id = $2 AND id = $3`,
		orgID,
		sessionID,
		turnID,
	).Scan(&state, &ownerEpoch, &ownerAttempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if ownerEpoch != epoch || ownerAttempt != attempt {
		return ErrStaleTurn
	}
	if state != "running" && state != "cancel_requested" {
		return ErrTurnFinished
	}
	return nil
}

func appendTypedEvent(
	ctx context.Context,
	tx pgx.Tx,
	orgID, sessionID, eventType string,
	payload any,
) error {
	if strings.TrimSpace(eventType) == "" {
		return ErrInvalid
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var sequence int64
	err = tx.QueryRow(
		ctx,
		`UPDATE ao_sessions
		SET next_sequence = next_sequence + 1, updated_at = now()
		WHERE org_id = $1 AND id = $2
		RETURNING next_sequence - 1`,
		orgID,
		sessionID,
	).Scan(&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		ctx,
		`INSERT INTO ao_events (org_id, session_id, sequence, type, payload)
		VALUES ($1, $2, $3, $4, $5)`,
		orgID,
		sessionID,
		sequence,
		eventType,
		encoded,
	)
	return err
}
