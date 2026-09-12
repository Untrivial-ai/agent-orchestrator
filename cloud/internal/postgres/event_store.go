package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

var clientEventTypes = []string{
	"agent.activity",
	"worker.connected",
	"worker.ready",
	"sandbox.provisioning",
	"workspace.changed",
	"pull_request.created",
	"pull_request.claimed",
	"review.submitted",
	"chat.user_message",
	"chat.assistant_delta",
	"chat.turn_started",
	"chat.turn_completed",
	"chat.turn_interrupted",
	"chat.turn_aborted",
	"chat.interrupt_requested",
}

func (s *Store) SendMessage(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	sessionID string,
	idempotencyKey string,
	text string,
) (domain.ClientEvent, error) {
	var event domain.ClientEvent
	err := s.withSessionAccess(ctx, principal, orgID, sessionID, func(tx pgx.Tx, access sessionAccess) error {
		if access.Role == "viewer" {
			return ErrForbidden
		}
		var err error
		event, err = sendMessageTx(
			ctx, tx, orgID, sessionID, idempotencyKey, text, principal.UserID, "",
			access.ModeCap, access.DeniedCommands,
		)
		return err
	})
	return event, err
}

func sendMessageTx(
	ctx context.Context,
	tx pgx.Tx,
	orgID, sessionID, idempotencyKey, text, actorUserID, actorSessionID string,
	modeCap string,
	deniedCommands []string,
) (domain.ClientEvent, error) {
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return domain.ClientEvent{}, err
	}
	var commandID string
	err = tx.QueryRow(
		ctx,
		`INSERT INTO ao_commands (
			org_id, session_id, idempotency_key, kind, payload
		) VALUES ($1, $2, $3, 'message.send', $4)
		ON CONFLICT (org_id, idempotency_key) DO NOTHING
		RETURNING id`,
		orgID, sessionID, idempotencyKey, payload,
	).Scan(&commandID)
	if errors.Is(err, pgx.ErrNoRows) {
		var event domain.ClientEvent
		err := loadIdempotentMessage(
			ctx, tx, orgID, sessionID, idempotencyKey, payload, &event,
		)
		return event, err
	}
	if err != nil {
		return domain.ClientEvent{}, normalizeConstraintError(err)
	}
	event, err := appendUserMessage(ctx, tx, orgID, sessionID, text, modeCap, deniedCommands)
	if err != nil {
		return domain.ClientEvent{}, err
	}
	// A user message is proof of life: wake a sandbox the idle-pause scanner
	// paused for silence. No-op (0 rows) for a sandbox that was never paused,
	// or paused for another reason (deleted, user-stopped) — this only ever
	// widens desired_state from 'paused' to 'running', never overrides a
	// desired 'stopped' or 'deleted' set explicitly elsewhere.
	if _, err := tx.Exec(
		ctx,
		`UPDATE ao_sandboxes
		SET desired_state = 'running', startup_started_at = now(),
			reconcile_after = now(), updated_at = now()
		WHERE session_id = $1 AND org_id = $2 AND desired_state = 'paused'`,
		sessionID, orgID,
	); err != nil {
		return domain.ClientEvent{}, err
	}
	if _, err := tx.Exec(
		ctx,
		`UPDATE ao_commands
		SET status = 'succeeded',
			result = jsonb_build_object('eventSequence', $1::bigint),
			updated_at = now()
		WHERE id = $2`,
		event.Sequence, commandID,
	); err != nil {
		return domain.ClientEvent{}, err
	}
	auditSQL := `INSERT INTO ao_audit_events (
		org_id, actor_user_id, action, resource_type, resource_id, metadata
	) VALUES (
		$1, $2, 'session.message_queued', 'session', $3,
		jsonb_build_object('sequence', $4::bigint)
	)`
	auditArgs := []any{orgID, actorUserID, sessionID, event.Sequence}
	if actorSessionID != "" {
		auditSQL = `INSERT INTO ao_audit_events (
			org_id, action, resource_type, resource_id, metadata
		) VALUES (
			$1, 'session.message_queued', 'session', $2,
			jsonb_build_object(
				'sequence', $3::bigint,
				'actorSessionId', $4::text
			)
		)`
		auditArgs = []any{orgID, sessionID, event.Sequence, actorSessionID}
	}
	_, err = tx.Exec(ctx, auditSQL, auditArgs...)
	return event, err
}

func loadIdempotentMessage(
	ctx context.Context,
	tx pgx.Tx,
	orgID string,
	sessionID string,
	idempotencyKey string,
	payload []byte,
	event *domain.ClientEvent,
) error {
	var storedPayload []byte
	var storedSessionID, kind, status string
	var sequence int64
	err := tx.QueryRow(
		ctx,
		`SELECT session_id, kind, status, payload,
			(result->>'eventSequence')::bigint
		FROM ao_commands
		WHERE org_id = $1 AND idempotency_key = $2`,
		orgID,
		idempotencyKey,
	).Scan(&storedSessionID, &kind, &status, &storedPayload, &sequence)
	if err != nil {
		return err
	}
	if storedSessionID != sessionID ||
		kind != "message.send" ||
		status != "succeeded" ||
		!jsonEqual(storedPayload, payload) {
		return ErrIdempotencyMismatch
	}
	return scanClientEvent(tx.QueryRow(
		ctx,
		`SELECT session_id, sequence, type, payload, created_at
		FROM ao_events
		WHERE org_id = $1 AND session_id = $2 AND sequence = $3`,
		orgID,
		sessionID,
		sequence,
	), event)
}

// AppendSessionEvent records a control-plane or worker event on a session
// stream. Unlike a user message it does not open a turn, and it is allowed on a
// terminated session so lifecycle history stays complete.
func (s *Store) AppendSessionEvent(
	ctx context.Context,
	orgID string,
	sessionID string,
	eventType string,
	payload json.RawMessage,
) (domain.ClientEvent, error) {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	var event domain.ClientEvent
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		var sequence int64
		err := tx.QueryRow(
			ctx,
			`UPDATE ao_sessions
			SET next_sequence = next_sequence + 1,
				updated_at = now()
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
		return scanClientEvent(tx.QueryRow(
			ctx,
			`INSERT INTO ao_events (
				org_id, session_id, sequence, type, payload
			) VALUES ($1, $2, $3, $4, $5)
			RETURNING session_id, sequence, type, payload, created_at`,
			orgID,
			sessionID,
			sequence,
			eventType,
			payload,
		), &event)
	})
	if err != nil {
		return domain.ClientEvent{}, err
	}
	return event, nil
}

// appendUserMessage records a user message and, depending on how the
// session is currently running, either injects it directly into an
// already-open interactive agent terminal or queues it as a new turn.
// modeCap/deniedCommands are the sender's share-grant cap, if any (zero
// values for an org member or an unshared session) — a live terminal was
// opened under whatever mode was in force when it started, so a capped
// sender's message is re-checked against that same cap here rather than
// trusted just because the terminal already exists; a queued turn instead
// carries the cap forward onto the ao_turns row for ClaimWorkerTurn to
// apply against the session's mode at claim time.
func appendUserMessage(
	ctx context.Context,
	tx pgx.Tx,
	orgID string,
	sessionID string,
	text string,
	modeCap string,
	deniedCommands []string,
) (domain.ClientEvent, error) {
	event, err := appendUserMessageEvent(ctx, tx, orgID, sessionID, text)
	if err != nil {
		return domain.ClientEvent{}, err
	}
	var terminalID string
	var workerEpoch int64
	var sessionMode string
	var sessionDeniedCommands []string
	err = tx.QueryRow(ctx,
		`SELECT terminal.id, terminal.worker_epoch, session.mode, session.denied_commands
		FROM ao_terminal_sessions terminal
		JOIN ao_sessions session
			ON session.org_id = terminal.org_id AND session.id = terminal.session_id
		WHERE terminal.org_id = $1 AND terminal.session_id = $2 AND terminal.kind = 'agent'
		  AND terminal.state = 'open' AND terminal.expires_at > now()
		ORDER BY terminal.created_at DESC
		LIMIT 1`,
		orgID, sessionID,
	).Scan(&terminalID, &workerEpoch, &sessionMode, &sessionDeniedCommands)
	if err == nil {
		effective := effectiveMode(sessionMode, modeCap)
		effectiveDenied := effectiveDeniedCommands(sessionDeniedCommands, deniedCommands)
		if effective == "read-only" || len(effectiveDenied) != 0 {
			return domain.ClientEvent{}, ErrForbidden
		}
		payload, marshalErr := json.Marshal(map[string]any{
			"terminalId": terminalID,
			"data":       []byte(text + "\r"),
		})
		if marshalErr != nil {
			return domain.ClientEvent{}, marshalErr
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO ao_worker_requests (
				org_id, session_id, worker_epoch, kind, payload, expires_at
			) VALUES ($1, $2, $3, 'terminal.input', $4, now() + interval '15 seconds')`,
			orgID, sessionID, workerEpoch, payload,
		); err != nil {
			return domain.ClientEvent{}, err
		}
		// A live agent terminal does not create an ao_turn. Mark the session
		// active at enqueue time so status/sidebar projections cannot claim it
		// is idle while the worker still has ordered terminal input pending.
		if _, err := tx.Exec(ctx,
			`UPDATE ao_sessions
			SET activity_state = 'active', updated_at = now()
			WHERE org_id = $1 AND id = $2 AND is_terminated = false`,
			orgID, sessionID,
		); err != nil {
			return domain.ClientEvent{}, err
		}
		return event, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.ClientEvent{}, err
	}
	if _, err := tx.Exec(
		ctx,
		`INSERT INTO ao_turns (
			org_id, session_id, user_message_sequence, mode_cap, denied_commands
		) VALUES ($1, $2, $3, NULLIF($4, ''), $5)`,
		orgID,
		sessionID,
		event.Sequence,
		modeCap,
		nonNilStrings(deniedCommands),
	); err != nil {
		return domain.ClientEvent{}, normalizeConstraintError(err)
	}
	return event, nil
}

func appendUserMessageEvent(
	ctx context.Context,
	tx pgx.Tx,
	orgID string,
	sessionID string,
	text string,
) (domain.ClientEvent, error) {
	var sequence int64
	err := tx.QueryRow(
		ctx,
		`UPDATE ao_sessions
		SET next_sequence = next_sequence + 1, updated_at = now(), last_user_message_at = now()
		WHERE org_id = $1 AND id = $2 AND is_terminated = false
		RETURNING next_sequence - 1`,
		orgID,
		sessionID,
	).Scan(&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		var terminated bool
		if err := tx.QueryRow(
			ctx,
			`SELECT is_terminated
			FROM ao_sessions
			WHERE org_id = $1 AND id = $2`,
			orgID,
			sessionID,
		).Scan(&terminated); errors.Is(err, pgx.ErrNoRows) {
			return domain.ClientEvent{}, ErrNotFound
		} else if err != nil {
			return domain.ClientEvent{}, err
		}
		if terminated {
			return domain.ClientEvent{}, ErrConflict
		}
	}
	if err != nil {
		return domain.ClientEvent{}, err
	}

	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return domain.ClientEvent{}, err
	}
	var event domain.ClientEvent
	err = scanClientEvent(tx.QueryRow(
		ctx,
		`INSERT INTO ao_events (
			org_id, session_id, sequence, type, payload
		) VALUES ($1, $2, $3, 'chat.user_message', $4)
		RETURNING session_id, sequence, type, payload, created_at`,
		orgID,
		sessionID,
		sequence,
		payload,
	), &event)
	if err != nil {
		return domain.ClientEvent{}, err
	}
	return event, nil
}

func (s *Store) ListClientEvents(
	ctx context.Context,
	principal domain.Principal,
	orgID string,
	sessionID string,
	after int64,
	limit int,
) ([]domain.ClientEvent, bool, error) {
	var events []domain.ClientEvent
	err := s.withSessionAccess(ctx, principal, orgID, sessionID, func(tx pgx.Tx, _ sessionAccess) error {
		var exists bool
		if err := tx.QueryRow(
			ctx,
			`SELECT EXISTS (
				SELECT 1 FROM ao_sessions WHERE org_id = $1 AND id = $2
			)`,
			orgID,
			sessionID,
		).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		rows, err := tx.Query(
			ctx,
			`SELECT session_id, sequence, type, payload, created_at
			FROM ao_events
			WHERE org_id = $1
			  AND session_id = $2
			  AND sequence > $3
			  AND type = ANY($4)
			ORDER BY sequence
			LIMIT $5`,
			orgID,
			sessionID,
			after,
			clientEventTypes,
			limit+1,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var event domain.ClientEvent
			if err := scanClientEvent(rows, &event); err != nil {
				return err
			}
			events = append(events, event)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	return events, hasMore, nil
}

func scanClientEvent(row scanner, event *domain.ClientEvent) error {
	return row.Scan(
		&event.SessionID,
		&event.Sequence,
		&event.Type,
		&event.Payload,
		&event.CreatedAt,
	)
}
