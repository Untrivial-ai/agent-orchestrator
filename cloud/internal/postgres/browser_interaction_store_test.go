package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/google/uuid"
)

func TestBrowserInteractionPreventsIdlePauseAndFencesAccess(t *testing.T) {
	store, admin, record := recoveryStore(t)
	ctx := context.Background()
	userID := uuid.NewString()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := admin.pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO ao_users(id, auth_provider, external_user_id, email) VALUES ($1::uuid, 'workos', $1::text, $2)`, userID, userID+"@example.test")
	t.Cleanup(func() { _, _ = admin.pool.Exec(context.Background(), `DELETE FROM ao_users WHERE id = $1`, userID) })
	exec(`INSERT INTO ao_org_memberships(org_id, user_id, role) VALUES ($1, $2, 'member')`, record.OrgID, userID)
	exec(`UPDATE ao_sessions SET created_at = now() - interval '2 hours', activity_state = 'idle' WHERE id = $1`, record.SessionID)
	exec(`UPDATE ao_sandboxes SET desired_state = 'running', observed_state = 'running' WHERE session_id = $1`, record.SessionID)
	exec(`INSERT INTO ao_worker_connections(session_id, org_id, sandbox_id, epoch, worker_id, version)
		VALUES ($1, $2, $1, 7, 'worker', 'test')`, record.SessionID, record.OrgID)
	principal := domain.Principal{UserID: userID}
	refresh := func() error {
		return store.RefreshBrowserInteraction(ctx, principal, record.OrgID, record.SessionID, 7)
	}
	lease := func() time.Time {
		t.Helper()
		var until time.Time
		if err := admin.pool.QueryRow(ctx, `SELECT interactive_until FROM ao_sandboxes WHERE session_id = $1`, record.SessionID).Scan(&until); err != nil {
			t.Fatal(err)
		}
		return until
	}
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	first := lease()
	if !first.After(time.Now().Add(110 * time.Second)) {
		t.Fatal("browser input did not extend the lease")
	}
	if paused, err := store.PauseIfIdle(ctx, record.OrgID, record.SessionID, time.Hour); err != nil || paused {
		t.Fatalf("active browser paused=%v err=%v", paused, err)
	}
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	if !lease().Equal(first) {
		t.Fatal("lease writes were not throttled")
	}
	exec(`UPDATE ao_sandboxes SET interactive_until = now() + interval '10 seconds' WHERE session_id = $1`, record.SessionID)
	if err := refresh(); err != nil {
		t.Fatal(err)
	}
	if !lease().After(time.Now().Add(110 * time.Second)) {
		t.Fatal("continued input did not renew lease")
	}
	for _, test := range []struct {
		name, setup, reset string
		want               error
	}{
		{"read only", `UPDATE ao_sessions SET mode = 'read-only' WHERE id = $1`, `UPDATE ao_sessions SET mode = 'standard' WHERE id = $1`, ErrForbidden},
		{"terminated", `UPDATE ao_sessions SET is_terminated = true WHERE id = $1`, `UPDATE ao_sessions SET is_terminated = false WHERE id = $1`, ErrWorkerUnavailable},
		{"paused", `UPDATE ao_sandboxes SET desired_state = 'paused' WHERE session_id = $1`, `UPDATE ao_sandboxes SET desired_state = 'running' WHERE session_id = $1`, ErrWorkerUnavailable},
		{"stale worker", `UPDATE ao_worker_connections SET disconnected_at = now() WHERE session_id = $1`, `UPDATE ao_worker_connections SET disconnected_at = NULL WHERE session_id = $1`, ErrStaleWorker},
	} {
		t.Run(test.name, func(t *testing.T) {
			exec(`UPDATE ao_sandboxes SET interactive_until = now() - interval '1 minute' WHERE session_id = $1`, record.SessionID)
			before := lease()
			exec(test.setup, record.SessionID)
			defer exec(test.reset, record.SessionID)
			if err := refresh(); !errors.Is(err, test.want) {
				t.Fatalf("refresh=%v, want %v", err, test.want)
			}
			if !lease().Equal(before) {
				t.Fatal("rejected input extended lease")
			}
		})
	}
	other := domain.Principal{UserID: uuid.NewString()}
	if err := store.RefreshBrowserInteraction(ctx, other, record.OrgID, record.SessionID, 7); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-user refresh=%v", err)
	}
	if paused, err := store.PauseIfIdle(ctx, record.OrgID, record.SessionID, time.Hour); err != nil || !paused {
		t.Fatalf("abandoned browser paused=%v err=%v", paused, err)
	}
}
