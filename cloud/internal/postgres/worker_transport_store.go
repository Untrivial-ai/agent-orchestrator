package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

const (
	maxOutstandingWorkerRequests    = 10
	maxOutstandingWorkspaceRequests = 6
	maxTerminalOutputBytes          = 4 << 20
	// interactiveSessionLease prevents the idle scanner from pausing a sandbox
	// while a user is connecting to either terminal surface.
	interactiveSessionLease = 2 * time.Minute
)

func (s *Store) CreateWorkspaceRequest(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID, kind string,
	payload json.RawMessage,
	ttl time.Duration,
) (domain.WorkerRequest, error) {
	var request domain.WorkerRequest
	err := s.withSessionAccess(ctx, principal, orgID, sessionID, func(tx pgx.Tx, access sessionAccess) error {
		if access.Role == "viewer" && kind == "workspace.write" {
			return ErrForbidden
		}
		var err error
		request, err = createWorkerRequest(ctx, tx, orgID, sessionID, kind, payload, ttl, access.ModeCap)
		return err
	})
	return request, err
}

func createWorkerRequest(
	ctx context.Context,
	tx pgx.Tx,
	orgID, sessionID, kind string,
	payload json.RawMessage,
	ttl time.Duration,
	modeCap string,
) (domain.WorkerRequest, error) {
	if ttl <= 0 {
		return domain.WorkerRequest{}, ErrInvalid
	}
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	var epoch int64
	var terminated bool
	var mode string
	if err := tx.QueryRow(ctx,
		`SELECT worker.epoch, session.is_terminated, session.mode
		FROM ao_sessions session
		JOIN ao_worker_connections worker
		  ON worker.org_id = session.org_id
		 AND worker.session_id = session.id
		 AND worker.disconnected_at IS NULL
		WHERE session.org_id = $1 AND session.id = $2
		FOR UPDATE OF session`,
		orgID, sessionID,
	).Scan(&epoch, &terminated, &mode); errors.Is(err, pgx.ErrNoRows) {
		return domain.WorkerRequest{}, ErrWorkerUnavailable
	} else if err != nil {
		return domain.WorkerRequest{}, err
	}
	if terminated {
		return domain.WorkerRequest{}, ErrWorkerUnavailable
	}
	if kind == "workspace.write" && effectiveMode(mode, modeCap) == "read-only" {
		return domain.WorkerRequest{}, ErrWorkspaceReadOnly
	}
	var outstanding int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM ao_worker_requests
		WHERE org_id = $1 AND session_id = $2
		  AND status IN ('pending', 'claimed') AND expires_at > now()`,
		orgID, sessionID,
	).Scan(&outstanding); err != nil {
		return domain.WorkerRequest{}, err
	}
	limit := maxOutstandingWorkspaceRequests
	if strings.HasPrefix(kind, "terminal.") {
		limit = maxOutstandingWorkerRequests
	}
	if outstanding >= limit {
		return domain.WorkerRequest{}, ErrConflict
	}
	request := domain.WorkerRequest{
		OrgID: orgID, SessionID: sessionID, WorkerEpoch: epoch,
		Kind: kind, Payload: payload,
	}
	err := scanWorkerRequest(tx.QueryRow(ctx,
		`INSERT INTO ao_worker_requests (
			org_id, session_id, worker_epoch, kind, payload, expires_at
		) VALUES ($1, $2, $3, $4, $5, now() + $6::interval)
		RETURNING id, org_id, session_id, worker_epoch, kind, payload, status,
			response, error_code, error_message, attempt_count, expires_at`,
		orgID, sessionID, epoch, kind, payload, intervalString(ttl),
	), &request)
	return request, normalizeConstraintError(err)
}

func (s *Store) GetWorkspaceRequest(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID, requestID string,
) (domain.WorkerRequest, error) {
	var request domain.WorkerRequest
	// A workspace request created via CreateWorkspaceRequest (which grants
	// access to share-grant holders, not just org members) must remain
	// pollable by that same caller — withTenant would 403 a non-member
	// recipient here even though they were the one who created the
	// request, leaving it stuck "pending" forever and eventually tripping
	// the outstanding-request cap below.
	err := s.withSessionAccess(ctx, principal, orgID, sessionID, func(tx pgx.Tx, _ sessionAccess) error {
		err := scanWorkerRequest(tx.QueryRow(ctx,
			`SELECT id, org_id, session_id, worker_epoch, kind, payload, status,
				response, error_code, error_message, attempt_count, expires_at
			FROM ao_worker_requests
			WHERE org_id = $1 AND session_id = $2 AND id = $3`,
			orgID, sessionID, requestID,
		), &request)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return request, err
}

func (s *Store) CancelWorkspaceRequest(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID, requestID string,
) error {
	return s.withSessionAccess(ctx, principal, orgID, sessionID, func(tx pgx.Tx, _ sessionAccess) error {
		_, err := tx.Exec(ctx,
			`UPDATE ao_worker_requests
			SET status = 'cancelled', completed_at = now(), updated_at = now()
			WHERE org_id = $1 AND session_id = $2 AND id = $3
			  AND status IN ('pending', 'claimed')`,
			orgID, sessionID, requestID,
		)
		return err
	})
}

func (s *Store) ClaimWorkerRequest(
	ctx context.Context,
	orgID, sessionID, workerID string,
	epoch int64,
	lease time.Duration,
) (domain.WorkerRequest, bool, error) {
	var request domain.WorkerRequest
	var found bool
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		current, err := workerConnectionCurrent(ctx, tx, orgID, sessionID, workerID, epoch)
		if err != nil {
			return err
		}
		if !current {
			return ErrStaleWorker
		}
		err = scanWorkerRequest(tx.QueryRow(ctx,
			`WITH candidate AS (
				SELECT id
				FROM ao_worker_requests
				WHERE org_id = $1 AND session_id = $2 AND worker_epoch = $3
				  AND expires_at > now()
				  AND (
					status = 'pending'
					OR (status = 'claimed' AND lease_until < now())
				  )
				  AND attempt_count < 3
				ORDER BY
					CASE
						WHEN kind = 'terminal.close' THEN 0
						WHEN kind = 'terminal.open' THEN 1
						WHEN kind IN ('terminal.input', 'terminal.resize') THEN 2
						ELSE 3
					END,
					created_at,
					id
				FOR UPDATE SKIP LOCKED
				LIMIT 1
			)
			UPDATE ao_worker_requests request
			SET status = 'claimed',
				attempt_count = request.attempt_count + 1,
				lease_until = now() + $4::interval,
				updated_at = now()
			FROM candidate
			WHERE request.id = candidate.id
			RETURNING request.id, request.org_id, request.session_id,
				request.worker_epoch, request.kind, request.payload, request.status,
				request.response, request.error_code, request.error_message,
				request.attempt_count, request.expires_at`,
			orgID, sessionID, epoch, intervalString(lease),
		), &request)
		if errors.Is(err, pgx.ErrNoRows) {
			_, cleanupErr := tx.Exec(ctx,
				`UPDATE ao_worker_requests
				SET status = 'failed', error_code = 'TRANSPORT_TIMEOUT',
					error_message = 'The worker request expired before completion.',
					completed_at = now(), updated_at = now()
				WHERE org_id = $1 AND session_id = $2 AND worker_epoch = $3
				  AND status IN ('pending', 'claimed')
				  AND (expires_at <= now() OR attempt_count >= 3)`,
				orgID, sessionID, epoch,
			)
			return cleanupErr
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return request, found, err
}

func (s *Store) CompleteWorkerRequest(
	ctx context.Context,
	orgID, sessionID, workerID, requestID string,
	epoch int64,
	attempt int,
	response json.RawMessage,
) error {
	if len(response) == 0 {
		response = json.RawMessage(`{}`)
	}
	return s.finishWorkerRequest(
		ctx, orgID, sessionID, workerID, requestID, epoch,
		attempt, "succeeded", response, "", "",
	)
}

func (s *Store) FailWorkerRequest(
	ctx context.Context,
	orgID, sessionID, workerID, requestID string,
	epoch int64,
	attempt int,
	code, message string,
) error {
	return s.finishWorkerRequest(
		ctx, orgID, sessionID, workerID, requestID, epoch,
		attempt, "failed", json.RawMessage(`{}`), code, message,
	)
}

func (s *Store) finishWorkerRequest(
	ctx context.Context,
	orgID, sessionID, workerID, requestID string,
	epoch int64,
	attempt int,
	status string,
	response json.RawMessage,
	code, message string,
) error {
	if attempt <= 0 {
		return ErrInvalid
	}
	return s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		current, err := workerConnectionCurrent(ctx, tx, orgID, sessionID, workerID, epoch)
		if err != nil {
			return err
		}
		if !current {
			return ErrStaleWorker
		}
		var kind string
		var payload []byte
		err = tx.QueryRow(ctx,
			`UPDATE ao_worker_requests
			SET status = $1, response = $2, error_code = $3, error_message = $4,
				lease_until = NULL, completed_at = now(), updated_at = now()
			WHERE org_id = $5 AND session_id = $6 AND id = $7
			  AND worker_epoch = $8 AND attempt_count = $9
			  AND status = 'claimed' AND expires_at > now() AND lease_until > now()
			RETURNING kind, payload`,
			status, response, code, message,
			orgID, sessionID, requestID, epoch, attempt,
		).Scan(&kind, &payload)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTransportExpired
		}
		if err != nil {
			return err
		}
		if kind == "terminal.open" {
			var command struct {
				TerminalID string `json:"terminalId"`
			}
			if json.Unmarshal(payload, &command) == nil && command.TerminalID != "" {
				state := "open"
				if status == "failed" {
					state = "failed"
				}
				_, err = tx.Exec(ctx,
					`UPDATE ao_terminal_sessions
					SET state = $1, error_message = $2, updated_at = now()
					WHERE org_id = $3 AND session_id = $4 AND id = $5
					  AND worker_epoch = $6 AND state = 'opening'`,
					state, message, orgID, sessionID, command.TerminalID, epoch,
				)
			}
		}
		return err
	})
}

func (s *Store) IssueTerminalTicket(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID, kind string,
	ttl time.Duration,
) (string, []string, error) {
	// Terminal access is an explicit proof of life. Wake an idle-paused sandbox
	// and reserve a short interaction lease in a committed transaction before
	// checking worker readiness so the idle scanner cannot immediately undo the
	// wake while the browser waits for the worker. The browser retries ticket
	// creation while the reconciler resumes the provider and the worker
	// heartbeats again.
	if err := s.withSessionAccess(ctx, principal, orgID, sessionID, func(tx pgx.Tx, _ sessionAccess) error {
		_, err := tx.Exec(ctx,
			`UPDATE ao_sandboxes
			SET desired_state = CASE WHEN desired_state = 'paused' THEN 'running' ELSE desired_state END,
				reconcile_after = CASE WHEN desired_state = 'paused' THEN now() ELSE reconcile_after END,
				startup_started_at = CASE
					WHEN desired_state = 'paused' THEN now()
					ELSE startup_started_at
				END,
				interactive_until = CASE
					WHEN interactive_until IS NULL OR interactive_until < now() + $3::interval
						THEN now() + $3::interval
					ELSE interactive_until
				END,
				updated_at = now()
			WHERE org_id = $1 AND session_id = $2`,
			orgID, sessionID, intervalString(interactiveSessionLease),
		)
		return err
	}); err != nil {
		return "", nil, err
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("generate terminal ticket: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	var scopes []string
	err := s.withSessionAccess(ctx, principal, orgID, sessionID, func(tx pgx.Tx, access sessionAccess) error {
		var epoch int64
		var mode string
		var terminated bool
		var deniedCommands []string
		err := tx.QueryRow(ctx,
			`SELECT worker.epoch, session.mode, session.is_terminated,
				session.denied_commands
			FROM ao_sessions session
			JOIN ao_sandboxes sandbox
			  ON sandbox.org_id = session.org_id
			 AND sandbox.session_id = session.id
			JOIN ao_worker_connections worker
			  ON worker.org_id = session.org_id
			 AND worker.session_id = session.id
			 AND worker.disconnected_at IS NULL
			 AND worker.ready_at IS NOT NULL
			WHERE session.org_id = $1 AND session.id = $2
			  AND sandbox.desired_state = 'running'
			  AND sandbox.observed_state = 'running'`,
			orgID, sessionID,
		).Scan(&epoch, &mode, &terminated, &deniedCommands)
		if errors.Is(err, pgx.ErrNoRows) || terminated {
			return ErrWorkerUnavailable
		}
		if err != nil {
			return err
		}
		mode = effectiveMode(mode, access.ModeCap)
		deniedCommands = effectiveDeniedCommands(deniedCommands, access.DeniedCommands)
		scopes = []string{"terminal:read"}
		// Authorized users may always observe a terminal. Operating a workspace
		// shell is only safe in trusted mode, and neither terminal surface can
		// faithfully enforce AO's command-prefix deny rules.
		canOperate := access.Role != "viewer" &&
			mode != "read-only" &&
			len(deniedCommands) == 0 &&
			(kind != "workspace" || mode == "trusted")
		if canOperate {
			scopes = append(scopes, "terminal:operate")
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO ao_access_tickets (
				org_id, session_id, purpose, scopes, token_hash, worker_epoch, expires_at
			) VALUES ($1, $2, $3, $4, $5, $6, now() + $7::interval)`,
			orgID, sessionID, "terminal:"+kind, scopes, hash[:], epoch, intervalString(ttl),
		)
		return err
	})
	if err != nil {
		return "", nil, err
	}
	return token, scopes, nil
}

// RefreshTerminalInteraction extends the short wake lease for a visible
// workspace terminal. Agent streams are retained in the browser for output
// continuity, so they deliberately do not prevent normal idle pause.
func (s *Store) RefreshTerminalInteraction(
	ctx context.Context,
	terminal domain.TerminalSession,
	ttl time.Duration,
) error {
	if terminal.Kind != "workspace" {
		return nil
	}
	return s.withOrg(ctx, terminal.OrgID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE ao_sandboxes
			SET interactive_until = now() + $1::interval, updated_at = now()
			WHERE org_id = $2 AND session_id = $3
			  AND desired_state = 'running'
			  AND EXISTS (
				SELECT 1 FROM ao_terminal_sessions
				WHERE org_id = $2 AND session_id = $3 AND id = $4
				  AND worker_epoch = $5 AND kind = 'workspace'
				  AND state IN ('opening', 'open') AND expires_at > now()
			  )`,
			intervalString(ttl), terminal.OrgID, terminal.SessionID,
			terminal.ID, terminal.WorkerEpoch,
		)
		if err != nil {
			return fmt.Errorf("refresh terminal interaction: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrWorkerUnavailable
		}
		return nil
	})
}

func (s *Store) EnsureWorkerAgentTerminal(
	ctx context.Context,
	orgID, sessionID, workerID string,
	epoch int64,
	ttl time.Duration,
) (domain.TerminalSession, error) {
	terminal := domain.TerminalSession{
		OrgID: orgID, SessionID: sessionID, WorkerEpoch: epoch, Kind: "agent",
		Scopes: []string{"terminal:read", "terminal:operate"},
	}
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		current, err := workerConnectionCurrent(
			ctx, tx, orgID, sessionID, workerID, epoch,
		)
		if err != nil {
			return err
		}
		if !current {
			return ErrStaleWorker
		}
		err = tx.QueryRow(ctx,
			`UPDATE ao_terminal_sessions
			SET expires_at = now() + $1::interval, updated_at = now()
			WHERE id = (
				SELECT id FROM ao_terminal_sessions
				WHERE org_id = $2 AND session_id = $3 AND worker_epoch = $4
				  AND kind = 'agent' AND state IN ('opening', 'open')
				  AND expires_at > now()
				ORDER BY created_at DESC
				LIMIT 1
			)
			RETURNING id, state, expires_at`,
			intervalString(ttl), orgID, sessionID, epoch,
		).Scan(&terminal.ID, &terminal.State, &terminal.ExpiresAt)
		if err == nil {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return tx.QueryRow(ctx,
			`INSERT INTO ao_terminal_sessions (
				org_id, session_id, worker_epoch, kind, state, expires_at
			) VALUES ($1, $2, $3, 'agent', 'open', now() + $4::interval)
			RETURNING id, state, expires_at`,
			orgID, sessionID, epoch, intervalString(ttl),
		).Scan(&terminal.ID, &terminal.State, &terminal.ExpiresAt)
	})
	return terminal, err
}

func (s *Store) OpenTerminal(
	ctx context.Context,
	token, kind string,
	ttl time.Duration,
) (domain.TerminalSession, error) {
	hash := sha256.Sum256([]byte(token))
	var ticket domain.AccessTicket
	err := s.withService(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`UPDATE ao_access_tickets
			SET consumed_at = now()
			WHERE token_hash = $1 AND purpose = $2
			  AND consumed_at IS NULL AND expires_at > now()
			RETURNING id, org_id, session_id, purpose, scopes,
				COALESCE(worker_epoch, 0), expires_at`,
			hash[:], "terminal:"+kind,
		).Scan(
			&ticket.ID, &ticket.OrgID, &ticket.SessionID, &ticket.Purpose,
			&ticket.Scopes, &ticket.WorkerEpoch, &ticket.ExpiresAt,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return classifyInvalidTerminalTicket(ctx, tx, hash[:], "terminal:"+kind)
		}
		return err
	})
	if err != nil {
		return domain.TerminalSession{}, err
	}

	terminal := domain.TerminalSession{
		OrgID: ticket.OrgID, SessionID: ticket.SessionID,
		WorkerEpoch: ticket.WorkerEpoch, Kind: kind, Scopes: ticket.Scopes,
	}
	err = s.withOrg(ctx, ticket.OrgID, func(tx pgx.Tx) error {
		current, err := workerEpochCurrent(
			ctx, tx, ticket.OrgID, ticket.SessionID, ticket.WorkerEpoch,
		)
		if err != nil {
			return err
		}
		if !current {
			return ErrStaleWorker
		}
		if kind == "agent" {
			err := tx.QueryRow(ctx,
				`UPDATE ao_terminal_sessions
				SET expires_at = now() + $1::interval, updated_at = now()
				WHERE id = (
					SELECT id FROM ao_terminal_sessions
					WHERE org_id = $2 AND session_id = $3
					  AND worker_epoch = $4 AND kind = 'agent'
					  AND state IN ('opening', 'open') AND expires_at > now()
					ORDER BY created_at DESC
					LIMIT 1
				)
				RETURNING id, state, expires_at`,
				intervalString(ttl), ticket.OrgID, ticket.SessionID, ticket.WorkerEpoch,
			).Scan(&terminal.ID, &terminal.State, &terminal.ExpiresAt)
			if err == nil {
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		if kind == "workspace" || kind == "agent" {
			var retiredIDs []string
			if err := tx.QueryRow(ctx,
				`WITH retired AS (
					UPDATE ao_terminal_sessions
					SET state = 'closed', closed_at = now(), updated_at = now()
					WHERE org_id = $1 AND session_id = $2 AND worker_epoch = $3
					  AND kind = $4 AND state IN ('opening', 'open')
					RETURNING id
				)
				SELECT COALESCE(array_agg(id::text), ARRAY[]::text[]) FROM retired`,
				ticket.OrgID, ticket.SessionID, ticket.WorkerEpoch, kind,
			).Scan(&retiredIDs); err != nil {
				return err
			}
			for _, terminalID := range retiredIDs {
				if _, err := tx.Exec(ctx,
					`UPDATE ao_worker_requests
					SET status = 'cancelled', updated_at = now()
					WHERE org_id = $1 AND session_id = $2 AND worker_epoch = $3
					  AND status IN ('pending', 'claimed')
					  AND payload->>'terminalId' = $4`,
					ticket.OrgID, ticket.SessionID, ticket.WorkerEpoch, terminalID,
				); err != nil {
					return err
				}
				payload, _ := json.Marshal(map[string]any{"terminalId": terminalID})
				if _, err := tx.Exec(ctx,
					`INSERT INTO ao_worker_requests (
						org_id, session_id, worker_epoch, kind, payload, expires_at
					) VALUES ($1, $2, $3, 'terminal.close', $4, now() + interval '15 seconds')`,
					ticket.OrgID, ticket.SessionID, ticket.WorkerEpoch, payload,
				); err != nil {
					return err
				}
			}
		}
		var active int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM ao_terminal_sessions
			WHERE org_id = $1 AND session_id = $2
			  AND worker_epoch = $3
			  AND state IN ('opening', 'open') AND expires_at > now()`,
			ticket.OrgID, ticket.SessionID, ticket.WorkerEpoch,
		).Scan(&active); err != nil {
			return err
		}
		if active >= 2 {
			return ErrConflict
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO ao_terminal_sessions (
				org_id, session_id, worker_epoch, kind, expires_at
			) VALUES ($1, $2, $3, $4, now() + $5::interval)
			RETURNING id, state, expires_at`,
			ticket.OrgID, ticket.SessionID, ticket.WorkerEpoch, kind, intervalString(ttl),
		).Scan(&terminal.ID, &terminal.State, &terminal.ExpiresAt); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"terminalId": terminal.ID,
			"kind":       kind,
		})
		_, err = createWorkerRequest(
			ctx, tx, ticket.OrgID, ticket.SessionID, "terminal.open", payload, 15*time.Second, "",
		)
		return err
	})
	return terminal, err
}

func classifyInvalidTerminalTicket(
	ctx context.Context,
	tx pgx.Tx,
	tokenHash []byte,
	expectedPurpose string,
) error {
	var purpose string
	var consumedAt *time.Time
	var expiresAt time.Time
	err := tx.QueryRow(ctx,
		`SELECT purpose, consumed_at, expires_at
		FROM ao_access_tickets
		WHERE token_hash = $1`,
		tokenHash,
	).Scan(&purpose, &consumedAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: not_found", ErrInvalidTicket)
	}
	if err != nil {
		return err
	}
	if purpose != expectedPurpose {
		return fmt.Errorf("%w: purpose_mismatch", ErrInvalidTicket)
	}
	if consumedAt != nil {
		return fmt.Errorf("%w: already_consumed", ErrInvalidTicket)
	}
	if !expiresAt.After(time.Now()) {
		return fmt.Errorf("%w: expired", ErrInvalidTicket)
	}
	return fmt.Errorf("%w: predicate_mismatch", ErrInvalidTicket)
}

func (s *Store) QueueTerminalInput(
	ctx context.Context,
	terminal domain.TerminalSession,
	inputID string,
	data []byte,
) error {
	payload, _ := json.Marshal(map[string]any{
		"terminalId": terminal.ID,
		"inputId":    inputID,
		"data":       data,
	})
	return s.queueTerminalRequest(ctx, terminal, "terminal.input", inputID, payload)
}

func (s *Store) QueueTerminalResize(
	ctx context.Context,
	terminal domain.TerminalSession,
	columns, rows uint16,
) error {
	payload, _ := json.Marshal(map[string]any{
		"terminalId": terminal.ID,
		"columns":    columns,
		"rows":       rows,
	})
	return s.queueTerminalRequest(ctx, terminal, "terminal.resize", "", payload)
}

func (s *Store) queueTerminalRequest(
	ctx context.Context,
	terminal domain.TerminalSession,
	kind string,
	idempotencyKey string,
	payload []byte,
) error {
	return s.withOrg(ctx, terminal.OrgID, func(tx pgx.Tx) error {
		var current bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM ao_terminal_sessions terminal
				JOIN ao_worker_connections worker
				  ON worker.org_id = terminal.org_id
				 AND worker.session_id = terminal.session_id
				 AND worker.epoch = terminal.worker_epoch
				 AND worker.disconnected_at IS NULL
				WHERE terminal.org_id = $1 AND terminal.session_id = $2
				  AND terminal.id = $3 AND terminal.worker_epoch = $4
				  AND terminal.state = 'open' AND terminal.expires_at > now()
			)`,
			terminal.OrgID, terminal.SessionID, terminal.ID, terminal.WorkerEpoch,
		).Scan(&current); err != nil {
			return err
		}
		if !current {
			return ErrWorkerUnavailable
		}
		if idempotencyKey != "" {
			var exists bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (
					SELECT 1 FROM ao_worker_requests
					WHERE org_id = $1 AND session_id = $2 AND kind = $3
					  AND payload->>'terminalId' = $4
					  AND payload->>'inputId' = $5
				)`,
				terminal.OrgID, terminal.SessionID, kind, terminal.ID, idempotencyKey,
			).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return nil
			}
		}
		if _, err := createWorkerRequest(
			ctx, tx, terminal.OrgID, terminal.SessionID, kind, payload, 15*time.Second, "",
		); err != nil {
			return err
		}
		if kind == "terminal.input" {
			// Wake the replica holding this terminal's worker stream so it can
			// claim and push the keystroke immediately instead of waiting for
			// the worker's next transport poll. The queue row above stays the
			// durable fallback either way.
			if _, err := tx.Exec(ctx,
				`SELECT pg_notify('ao_terminal_input', $1)`, terminal.ID,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// ClaimTerminalInput claims the oldest pending terminal.input request for one
// terminal on behalf of its worker, so a control-plane replica holding the
// worker's terminal stream can push it down immediately. It uses the same
// lease semantics as ClaimWorkerRequest, so it never races the worker's own
// transport poll into a double delivery.
func (s *Store) ClaimTerminalInput(
	ctx context.Context,
	orgID, sessionID, workerID string,
	epoch int64,
	terminalID string,
	lease time.Duration,
) (domain.WorkerRequest, bool, error) {
	var request domain.WorkerRequest
	var found bool
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		current, err := workerConnectionCurrent(ctx, tx, orgID, sessionID, workerID, epoch)
		if err != nil {
			return err
		}
		if !current {
			return ErrStaleWorker
		}
		err = scanWorkerRequest(tx.QueryRow(ctx,
			`WITH candidate AS (
				SELECT id
				FROM ao_worker_requests
				WHERE org_id = $1 AND session_id = $2 AND worker_epoch = $3
				  AND kind = 'terminal.input'
				  AND payload->>'terminalId' = $4
				  AND expires_at > now()
				  AND (
					status = 'pending'
					OR (status = 'claimed' AND lease_until < now())
				  )
				  AND attempt_count < 3
				ORDER BY created_at, id
				FOR UPDATE SKIP LOCKED
				LIMIT 1
			)
			UPDATE ao_worker_requests request
			SET status = 'claimed',
				attempt_count = request.attempt_count + 1,
				lease_until = now() + $5::interval,
				updated_at = now()
			FROM candidate
			WHERE request.id = candidate.id
			RETURNING request.id, request.org_id, request.session_id,
				request.worker_epoch, request.kind, request.payload, request.status,
				request.response, request.error_code, request.error_message,
				request.attempt_count, request.expires_at`,
			orgID, sessionID, epoch, terminalID, intervalString(lease),
		), &request)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return request, found, err
}

func (s *Store) CloseTerminal(ctx context.Context, terminal domain.TerminalSession) error {
	return s.withOrg(ctx, terminal.OrgID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`UPDATE ao_terminal_sessions
			SET state = 'closed', closed_at = now(), updated_at = now()
			WHERE org_id = $1 AND session_id = $2 AND id = $3
			  AND worker_epoch = $4 AND state IN ('opening', 'open')`,
			terminal.OrgID, terminal.SessionID, terminal.ID, terminal.WorkerEpoch,
		)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		payload, _ := json.Marshal(map[string]any{"terminalId": terminal.ID})
		_, err = tx.Exec(ctx,
			`INSERT INTO ao_worker_requests (
				org_id, session_id, worker_epoch, kind, payload, expires_at
			)
			SELECT $1, $2, $3, 'terminal.close', $4, now() + interval '15 seconds'
			WHERE EXISTS (
				SELECT 1 FROM ao_worker_connections
				WHERE org_id = $1 AND session_id = $2 AND epoch = $3
				  AND disconnected_at IS NULL
			)`,
			terminal.OrgID, terminal.SessionID, terminal.WorkerEpoch, payload,
		)
		return err
	})
}

func (s *Store) AppendTerminalOutput(
	ctx context.Context,
	orgID, sessionID, workerID, terminalID string,
	epoch int64,
	data []byte,
) (int64, error) {
	var sequence int64
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		current, err := workerConnectionCurrent(ctx, tx, orgID, sessionID, workerID, epoch)
		if err != nil {
			return err
		}
		if !current {
			return ErrStaleWorker
		}
		err = tx.QueryRow(ctx,
			`UPDATE ao_terminal_sessions
			SET next_output_sequence = next_output_sequence + 1,
				output_bytes = output_bytes + $1,
				updated_at = now()
			WHERE org_id = $2 AND session_id = $3 AND id = $4
			  AND worker_epoch = $5 AND state IN ('opening', 'open')
			  AND expires_at > now()
			  AND output_bytes + $1 <= $6
			RETURNING next_output_sequence - 1`,
			len(data), orgID, sessionID, terminalID, epoch, maxTerminalOutputBytes,
		).Scan(&sequence)
		if errors.Is(err, pgx.ErrNoRows) {
			_, _ = tx.Exec(ctx,
				`UPDATE ao_terminal_sessions
				SET state = 'failed',
					error_message = 'Terminal output exceeded its limit.',
					closed_at = now(), updated_at = now()
				WHERE org_id = $1 AND session_id = $2 AND id = $3
				  AND worker_epoch = $4 AND state IN ('opening', 'open')
				  AND output_bytes + $5 > $6`,
				orgID, sessionID, terminalID, epoch, len(data), maxTerminalOutputBytes,
			)
			return ErrTransportExpired
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO ao_terminal_output (
				terminal_id, org_id, session_id, sequence, data
			) VALUES ($1, $2, $3, $4, $5)`,
			terminalID, orgID, sessionID, sequence, data,
		)
		if err != nil {
			return err
		}
		// Wake any control-plane replica streaming this terminal to a client.
		// Delivered on commit, so subscribers always find the row.
		_, err = tx.Exec(ctx, `SELECT pg_notify('ao_terminal_output', $1)`, terminalID)
		return err
	})
	return sequence, err
}

func (s *Store) MarkTerminalExited(
	ctx context.Context,
	orgID, sessionID, workerID, terminalID string,
	epoch int64,
	exitCode int,
) error {
	return s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		current, err := workerConnectionCurrent(
			ctx, tx, orgID, sessionID, workerID, epoch,
		)
		if err != nil {
			return err
		}
		if !current {
			return ErrStaleWorker
		}
		state := "closed"
		message := ""
		if exitCode != 0 {
			state = "failed"
			message = fmt.Sprintf("Terminal process exited with status %d.", exitCode)
		}
		tag, err := tx.Exec(ctx,
			`UPDATE ao_terminal_sessions
			SET state = $1, error_message = $2, closed_at = now(), updated_at = now()
			WHERE org_id = $3 AND session_id = $4 AND id = $5
			  AND worker_epoch = $6 AND state IN ('opening', 'open')`,
			state, message, orgID, sessionID, terminalID, epoch,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrTransportExpired
		}
		_, err = tx.Exec(ctx,
			`UPDATE ao_sessions session
			SET activity_state = 'exited',
				activity_blocked_tool_name = '',
				activity_blocked_tool_use_id = '',
				updated_at = now()
			WHERE session.org_id = $1 AND session.id = $2
			  AND EXISTS (
				SELECT 1 FROM ao_terminal_sessions terminal
				WHERE terminal.org_id = session.org_id
				  AND terminal.session_id = session.id
				  AND terminal.id = $3 AND terminal.kind = 'agent'
			  )`,
			orgID, sessionID, terminalID,
		)
		return err
	})
}

func (s *Store) ListTerminalOutput(
	ctx context.Context,
	terminal domain.TerminalSession,
	after int64,
	limit int,
) ([]domain.TerminalOutput, string, error) {
	var output []domain.TerminalOutput
	var state string
	err := s.withOrg(ctx, terminal.OrgID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT state FROM ao_terminal_sessions
			WHERE org_id = $1 AND session_id = $2 AND id = $3
			  AND worker_epoch = $4`,
			terminal.OrgID, terminal.SessionID, terminal.ID, terminal.WorkerEpoch,
		).Scan(&state); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			`SELECT sequence, data FROM ao_terminal_output
			WHERE org_id = $1 AND session_id = $2 AND terminal_id = $3
			  AND sequence > $4
			ORDER BY sequence
			LIMIT $5`,
			terminal.OrgID, terminal.SessionID, terminal.ID, after, limit,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var frame domain.TerminalOutput
			if err := rows.Scan(&frame.Sequence, &frame.Data); err != nil {
				return err
			}
			output = append(output, frame)
		}
		return rows.Err()
	})
	return output, state, err
}

func workerEpochCurrent(
	ctx context.Context,
	tx pgx.Tx,
	orgID, sessionID string,
	epoch int64,
) (bool, error) {
	var current bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM ao_worker_connections
			WHERE org_id = $1 AND session_id = $2 AND epoch = $3
			  AND disconnected_at IS NULL
		)`,
		orgID, sessionID, epoch,
	).Scan(&current)
	return current, err
}

func workerConnectionCurrent(
	ctx context.Context,
	tx pgx.Tx,
	orgID, sessionID, workerID string,
	epoch int64,
) (bool, error) {
	var current bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM ao_worker_connections
			WHERE org_id = $1 AND session_id = $2 AND worker_id = $3
			  AND epoch = $4 AND disconnected_at IS NULL
		)`,
		orgID, sessionID, workerID, epoch,
	).Scan(&current)
	return current, err
}

func scanWorkerRequest(row scanner, request *domain.WorkerRequest) error {
	return row.Scan(
		&request.ID, &request.OrgID, &request.SessionID, &request.WorkerEpoch,
		&request.Kind, &request.Payload, &request.Status, &request.Response,
		&request.ErrorCode, &request.ErrorMessage, &request.Attempt,
		&request.ExpiresAt,
	)
}
