package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/jackc/pgx/v5"
)

// PutSessionTranscript upserts the latest captured harness transcript and
// preserved git ref for a session. It is the worker capture path: the worker
// periodically pushes its transcript blob so a later RESTORE can rehydrate a
// fresh sandbox under the same session_id. Scoped to the worker's own
// organization (withOrg), so row-level security still confines the write to one
// tenant even though there is no user principal on this path.
func (s *Store) PutSessionTranscript(
	ctx context.Context,
	orgID, sessionID, agentSessionID, harness string,
	transcript []byte,
	preservedGitRef string,
) error {
	return s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		// INSERT ... SELECT FROM ao_sessions gates the write on the session
		// still existing, so a capture for a hard-deleted session reports
		// ErrNotFound instead of tripping the foreign key as an opaque error.
		tag, err := tx.Exec(
			ctx,
			`INSERT INTO ao_session_transcripts (
				org_id, session_id, agent_session_id, harness,
				transcript, preserved_git_ref, captured_at, updated_at
			)
			SELECT $1, $2, $3, $4, $5, $6, now(), now()
			FROM ao_sessions
			WHERE id = $2 AND org_id = $1
			ON CONFLICT (org_id, session_id) DO UPDATE
			SET agent_session_id = EXCLUDED.agent_session_id,
				harness = EXCLUDED.harness,
				transcript = EXCLUDED.transcript,
				preserved_git_ref = EXCLUDED.preserved_git_ref,
				captured_at = now(),
				updated_at = now()`,
			orgID, sessionID, agentSessionID, harness, transcript, preservedGitRef,
		)
		if err != nil {
			return fmt.Errorf("put session transcript: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// GetSessionTranscript returns the most recent captured transcript for a
// session, or ErrNotFound when none has been captured yet.
func (s *Store) GetSessionTranscript(
	ctx context.Context,
	orgID, sessionID string,
) (agentSessionID, harness string, transcript []byte, preservedGitRef string, err error) {
	err = s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		scanErr := tx.QueryRow(
			ctx,
			`SELECT agent_session_id, harness, transcript, preserved_git_ref
			FROM ao_session_transcripts
			WHERE org_id = $1 AND session_id = $2`,
			orgID, sessionID,
		).Scan(&agentSessionID, &harness, &transcript, &preservedGitRef)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if scanErr != nil {
			return fmt.Errorf("get session transcript: %w", scanErr)
		}
		return nil
	})
	return agentSessionID, harness, transcript, preservedGitRef, err
}

// TerminateSession performs a cloud delete: it marks the session terminated AND
// requests its sandbox teardown, atomically. Terminating the session row up
// front (rather than only setting the sandbox desired_state and waiting for the
// reconciler) is what makes a delete land on the first click: the board archives
// a session by is_terminated, and the idle scanner can otherwise race a
// sandbox-only delete by resetting desired_state back to 'paused', which left
// the session active and the card re-appearing on the next poll. This is the
// exact inverse of RestoreSession, so delete and restore are symmetric.
func (s *Store) TerminateSession(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID string,
) error {
	return s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(
			ctx,
			`UPDATE ao_sessions
			SET is_terminated = true,
				activity_state = 'exited',
				updated_at = now()
			WHERE id = $1 AND org_id = $2`,
			sessionID, orgID,
		)
		if err != nil {
			return normalizeConstraintError(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		// Tear the sandbox down. A session may have no sandbox row yet (created
		// but never provisioned), so this update affecting no rows is fine — the
		// session is already terminated, which is what archives it.
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_sandboxes
			SET desired_state = 'deleted',
				preparation_generation = preparation_generation + CASE
					WHEN preparation_expires_at IS NULL THEN 0 ELSE 1
				END,
				deletion_requested_at = COALESCE(deletion_requested_at, now()),
				startup_started_at = NULL,
				reconcile_after = now(),
				reconcile_lease_owner = '',
				reconcile_lease_until = NULL,
				updated_at = now()
			WHERE session_id = $1 AND org_id = $2`,
			sessionID, orgID,
		); err != nil {
			return fmt.Errorf("request sandbox deletion: %w", err)
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_access_tickets
			SET consumed_at = COALESCE(consumed_at, now())
			WHERE org_id = $1 AND session_id = $2 AND consumed_at IS NULL`,
			orgID, sessionID,
		); err != nil {
			return fmt.Errorf("invalidate deleted session tickets: %w", err)
		}
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_worker_connections
			SET disconnected_at = COALESCE(disconnected_at, now())
			WHERE org_id = $1 AND session_id = $2 AND disconnected_at IS NULL`,
			orgID, sessionID,
		); err != nil {
			return fmt.Errorf("disconnect deleted session workers: %w", err)
		}
		return notifySandboxReconcile(ctx, tx)
	})
}

// RestoreSession reverses a cloud delete: it un-terminates the SAME session_id
// and queues a fresh sandbox provision, mirroring SetSandboxDesiredState's
// 'running' intent. Preserving the session_id keeps parent_session_id links
// valid so a restored orchestrator re-discovers its workers. Runs in one tenant
// transaction so a caller never observes a half-restored session.
func (s *Store) RestoreSession(
	ctx context.Context,
	principal domain.Principal,
	orgID, sessionID string,
) error {
	return s.withTenant(ctx, principal, orgID, func(tx pgx.Tx) error {
		// Restore reverses a delete, so it is only valid on a TERMINATED session.
		// Gating on is_terminated=true (with the row lock this UPDATE takes) makes
		// restore idempotent: once the first restore un-terminates the session, a
		// rapid second restore matches 0 rows and returns without re-arming the
		// sandbox — which would otherwise fence the first restore's in-flight
		// provision and start a second one, crossing the session/workspace mapping.
		tag, err := tx.Exec(
			ctx,
			`UPDATE ao_sessions
			SET is_terminated = false,
				activity_state = 'idle',
				updated_at = now()
			WHERE id = $1 AND org_id = $2 AND is_terminated = true`,
			sessionID, orgID,
		)
		if err != nil {
			// A live orchestrator already occupies this project (the
			// one-active-orchestrator unique index) surfaces as ErrConflict.
			return normalizeConstraintError(err)
		}
		if tag.RowsAffected() == 0 {
			// Either the session does not exist, or it is already active (a restore
			// is already in progress, or it was never deleted). Restoring an
			// already-active session is an idempotent no-op: return without
			// re-arming the sandbox, so any in-flight provision is left untouched.
			var exists bool
			if err := tx.QueryRow(
				ctx,
				`SELECT true FROM ao_sessions WHERE id = $1 AND org_id = $2`,
				sessionID, orgID,
			).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			} else if err != nil {
				return err
			}
			return nil
		}
		// Re-arm the sandbox for reconciliation. A deleted sandbox row survives
		// with observed_state='deleted' and reconcile_after ~100 years out, so
		// resetting desired_state, the startup window, and the deletion/failure
		// artifacts is what lets the reconciler pick it up and provision anew.
		// Do NOT touch the reconcile lease: an expired/free lease is already
		// claimable (ClaimSandboxes keys on reconcile_lease_until, not owner), and
		// clearing a LIVE lease would fence whichever reconciler currently holds it
		// — the exact stomp that let a rapid second restore start a parallel
		// provision. A stuck lease expires within its TTL, so the worst case is a
		// slightly slower re-provision, never a fenced in-flight operation.
		if _, err := tx.Exec(
			ctx,
			`UPDATE ao_sandboxes
			SET desired_state = 'running',
				startup_started_at = now(),
				reconcile_after = now(),
				deletion_requested_at = NULL,
				consecutive_failures = 0,
				startup_attempts = 0,
				last_error = '',
				updated_at = now()
			WHERE session_id = $1 AND org_id = $2`,
			sessionID, orgID,
		); err != nil {
			return fmt.Errorf("restore session sandbox: %w", err)
		}
		return notifySandboxReconcile(ctx, tx)
	})
}

// SessionTranscriptStore adapts *Store to the narrow Put/Get transcript-blob
// abstraction the HTTP layer depends on. Keeping the handlers behind this
// interface lets the Postgres-backed store be swapped for an object-store (S3)
// implementation later without touching the handlers.
type SessionTranscriptStore struct{ store *Store }

// SessionTranscripts returns the Postgres-backed transcript blob store.
func (s *Store) SessionTranscripts() *SessionTranscriptStore {
	return &SessionTranscriptStore{store: s}
}

// Put stores the latest captured transcript for a session.
func (t *SessionTranscriptStore) Put(
	ctx context.Context,
	orgID, sessionID, agentSessionID, harness string,
	transcript []byte,
	preservedGitRef string,
) error {
	return t.store.PutSessionTranscript(
		ctx, orgID, sessionID, agentSessionID, harness, transcript, preservedGitRef,
	)
}

// Get returns the latest captured transcript for a session, or ErrNotFound.
func (t *SessionTranscriptStore) Get(
	ctx context.Context,
	orgID, sessionID string,
) (agentSessionID, harness string, transcript []byte, preservedGitRef string, err error) {
	return t.store.GetSessionTranscript(ctx, orgID, sessionID)
}
