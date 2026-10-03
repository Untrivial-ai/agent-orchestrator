package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/google/uuid"
)

func TestBrowserViewerAccessRechecksRevocation(t *testing.T) {
	for _, role := range []string{"operator", "read-only", "shared viewer"} {
		t.Run(role, func(t *testing.T) {
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
			t.Cleanup(func() {
				_, _ = admin.pool.Exec(context.Background(), `DELETE FROM ao_project_share_links WHERE created_by_user_id = $1`, userID)
				_, _ = admin.pool.Exec(context.Background(), `DELETE FROM ao_users WHERE id = $1`, userID)
			})
			if role == "shared viewer" {
				linkID := uuid.NewString()
				hash := sha256.Sum256([]byte(userID))
				exec(`INSERT INTO ao_project_share_links(id, org_id, project_id, created_by_user_id, token_hash, role)
					SELECT $1, org_id, project_id, $2, $3, 'viewer' FROM ao_sessions WHERE id = $4`, linkID, userID, hash[:], record.SessionID)
				exec(`INSERT INTO ao_project_share_grants(share_link_id, org_id, project_id, user_id, shared_by_user_id, role)
					SELECT $1, org_id, project_id, $2, $2, 'viewer' FROM ao_sessions WHERE id = $3`, linkID, userID, record.SessionID)
			} else {
				exec(`INSERT INTO ao_org_memberships(org_id, user_id, role) VALUES ($1, $2, 'member')`, record.OrgID, userID)
			}
			if role == "read-only" {
				exec(`UPDATE ao_sessions SET mode = 'read-only' WHERE id = $1`, record.SessionID)
			}
			exec(`UPDATE ao_sandboxes SET desired_state = 'running', observed_state = 'running' WHERE session_id = $1`, record.SessionID)
			exec(`INSERT INTO ao_worker_connections(session_id, org_id, sandbox_id, epoch, worker_id, version) VALUES ($1, $2, $1, 7, 'worker', 'test')`, record.SessionID, record.OrgID)
			principal := domain.Principal{UserID: userID}
			issue := func() string {
				t.Helper()
				ticket, _, err := store.IssueBrowserViewerTicket(ctx, principal, record.OrgID, record.SessionID, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				return ticket
			}
			if _, err := store.OpenBrowserViewerTicket(ctx, issue()); err != nil {
				t.Fatal(err)
			}
			ticket := issue()
			exec(`UPDATE ao_sandboxes SET interactive_until = now() - interval '1 minute' WHERE session_id = $1`, record.SessionID)
			var before, after time.Time
			lease := func(until *time.Time) {
				t.Helper()
				if err := admin.pool.QueryRow(ctx, `SELECT interactive_until FROM ao_sandboxes WHERE session_id = $1`, record.SessionID).Scan(until); err != nil {
					t.Fatal(err)
				}
			}
			lease(&before)
			if err := store.CheckBrowserViewerAccess(ctx, principal, record.OrgID, record.SessionID, role == "operator"); err != nil {
				t.Fatal(err)
			}
			lease(&after)
			if !before.Equal(after) {
				t.Fatal("passive viewing extended activity lease")
			}
			if role != "operator" {
				if err := store.CheckBrowserViewerAccess(ctx, principal, record.OrgID, record.SessionID, true); !errors.Is(err, ErrForbidden) {
					t.Fatalf("viewer operate check=%v", err)
				}
			}
			exec(`DELETE FROM ao_org_memberships WHERE org_id = $1 AND user_id = $2`, record.OrgID, userID)
			exec(`UPDATE ao_project_share_grants SET status = 'revoked' WHERE org_id = $1 AND user_id = $2`, record.OrgID, userID)
			if err := store.CheckBrowserViewerAccess(ctx, principal, record.OrgID, record.SessionID, false); !errors.Is(err, ErrForbidden) {
				t.Fatalf("revoked view check=%v", err)
			}
			if _, err := store.OpenBrowserViewerTicket(ctx, ticket); !errors.Is(err, ErrForbidden) {
				t.Fatalf("revoked ticket redemption=%v", err)
			}
			if _, err := store.OpenBrowserViewerTicket(ctx, ticket); !errors.Is(err, ErrInvalidTicket) {
				t.Fatalf("reused ticket=%v", err)
			}
		})
	}
}
