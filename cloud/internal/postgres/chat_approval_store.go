package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/aoagents/agent-orchestrator/cloud/internal/worker"
	"github.com/jackc/pgx/v5"
)

// The command row is both the durable request and the single-use decision receipt.
func (s *Store) CreateWorkerChatApproval(ctx context.Context, orgID, sessionID, workerID string, epoch int64, request worker.ChatApproval) error {
	return s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		if err := requireCurrentWorker(ctx, tx, orgID, sessionID, workerID, epoch); err != nil {
			return err
		}
		if err := requireActiveTurnFence(ctx, tx, orgID, sessionID, request.TurnID, epoch, request.Attempt); err != nil {
			return err
		}
		request.WorkerEpoch = epoch
		payload, err := json.Marshal(request)
		if err != nil {
			return err
		}
		var inserted string
		err = tx.QueryRow(ctx, `INSERT INTO ao_commands (id, org_id, session_id, idempotency_key, kind, payload)
			VALUES ($1, $2, $3, $4, 'chat.approval', $5)
			ON CONFLICT (org_id, idempotency_key) DO NOTHING RETURNING id`,
			request.RequestID, orgID, sessionID, "approval:"+request.RequestID, payload).Scan(&inserted)
		if errors.Is(err, pgx.ErrNoRows) {
			var stored []byte
			if err := tx.QueryRow(ctx, `SELECT payload FROM ao_commands WHERE org_id = $1 AND session_id = $2 AND id = $3 AND kind = 'chat.approval'`,
				orgID, sessionID, request.RequestID).Scan(&stored); err != nil {
				return ErrIdempotencyMismatch
			}
			if !jsonEqual(stored, payload) {
				return ErrIdempotencyMismatch
			}
			return nil
		}
		if err != nil {
			return normalizeConstraintError(err)
		}
		return appendTypedEvent(ctx, tx, orgID, sessionID, "chat.approval_requested", map[string]any{
			"requestId": request.RequestID, "turnId": request.TurnID, "attempt": request.Attempt,
			"summary": request.Summary, "toolKind": request.ToolKind, "decisions": request.Decisions,
		})
	})
}

func (s *Store) WorkerChatApprovalDecision(ctx context.Context, orgID, sessionID, workerID string, epoch int64, turnID string, attempt int, requestID string) (string, error) {
	var decision string
	err := s.withOrg(ctx, orgID, func(tx pgx.Tx) error {
		if err := requireCurrentWorker(ctx, tx, orgID, sessionID, workerID, epoch); err != nil {
			return err
		}
		if err := requireActiveTurnFence(ctx, tx, orgID, sessionID, turnID, epoch, attempt); err != nil {
			return err
		}
		var payload []byte
		var status string
		if err := tx.QueryRow(ctx, `SELECT status, payload, COALESCE(result->>'decision', '')
			FROM ao_commands WHERE org_id = $1 AND session_id = $2 AND id = $3 AND kind = 'chat.approval'`,
			orgID, sessionID, requestID).Scan(&status, &payload, &decision); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		var request worker.ChatApproval
		if err := json.Unmarshal(payload, &request); err != nil {
			return err
		}
		if request.TurnID != turnID || request.Attempt != attempt {
			return ErrStaleTurn
		}
		if status != "succeeded" {
			decision = ""
		}
		return nil
	})
	return decision, err
}

func (s *Store) DecideChatApproval(ctx context.Context, principal domain.Principal, orgID, sessionID, requestID, decision string) error {
	return s.withSessionAccess(ctx, principal, orgID, sessionID, func(tx pgx.Tx, access sessionAccess) error {
		if access.Role == "viewer" {
			return ErrForbidden
		}
		var payload []byte
		var status string
		err := tx.QueryRow(ctx, `SELECT status, payload FROM ao_commands
			WHERE org_id = $1 AND session_id = $2 AND id = $3 AND kind = 'chat.approval' FOR UPDATE`,
			orgID, sessionID, requestID).Scan(&status, &payload)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var request worker.ChatApproval
		if err := json.Unmarshal(payload, &request); err != nil {
			return err
		}
		var options []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(request.Decisions, &options); err != nil {
			return err
		}
		offered := false
		for _, option := range options {
			if option.ID == decision {
				offered = true
				break
			}
		}
		if !offered {
			return ErrInvalid
		}
		if status == "succeeded" {
			var previous string
			if err := tx.QueryRow(ctx, `SELECT result->>'decision' FROM ao_commands WHERE id = $1`, requestID).Scan(&previous); err != nil {
				return err
			}
			if previous == decision {
				return nil
			}
			return ErrConflict
		}
		if status != "accepted" {
			return ErrTurnFinished
		}
		var state string
		var ownerEpoch int64
		var ownerAttempt int
		if err := tx.QueryRow(ctx, `SELECT state, worker_epoch, attempt_count FROM ao_turns WHERE org_id = $1 AND session_id = $2 AND id = $3`,
			orgID, sessionID, request.TurnID).Scan(&state, &ownerEpoch, &ownerAttempt); err != nil {
			return err
		}
		if state != "running" || ownerEpoch != request.WorkerEpoch || ownerAttempt != request.Attempt {
			return ErrTurnFinished
		}
		if _, err := tx.Exec(ctx, `UPDATE ao_commands SET status = 'succeeded', result = jsonb_build_object('decision', $1::text), updated_at = now() WHERE id = $2`,
			decision, requestID); err != nil {
			return err
		}
		return appendTypedEvent(ctx, tx, orgID, sessionID, "chat.approval_decided", map[string]any{
			"requestId": requestID, "turnId": request.TurnID, "decision": decision,
		})
	})
}
