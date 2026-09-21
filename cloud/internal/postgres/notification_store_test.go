package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type notificationFixture struct {
	orgID, userID, projectID, sessionID, workerID string
	epoch                                         int64
}

func TestNotificationAcceptIsIdempotentAndFencesWorkerEpoch(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	event := domain.AgentNotificationEvent{
		EventID:    "evt_01KNOTIFICATION",
		Type:       domain.NotificationTypeNeedsInput,
		OccurredAt: time.Now().UTC().Truncate(time.Second),
		Payload:    json.RawMessage(`{"activityId":"tool-1"}`),
	}

	accepted, err := store.AcceptNotificationEvent(ctx, fixture.orgID, fixture.sessionID, fixture.workerID, fixture.epoch, event)
	if err != nil {
		t.Fatalf("first accept: %v", err)
	}
	if accepted.Duplicate {
		t.Fatal("first accept reported a duplicate")
	}

	duplicate, err := store.AcceptNotificationEvent(ctx, fixture.orgID, fixture.sessionID, fixture.workerID, fixture.epoch, event)
	if err != nil {
		t.Fatalf("duplicate accept: %v", err)
	}
	if !duplicate.Duplicate || duplicate.IngressID != accepted.IngressID {
		t.Fatalf("duplicate accept = %+v, first = %+v", duplicate, accepted)
	}

	var ingressCount int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM ao_notification_ingress WHERE org_id = $1`, fixture.orgID).Scan(&ingressCount); err != nil {
		t.Fatal(err)
	}
	if ingressCount != 1 {
		t.Fatalf("ingress rows = %d, want 1", ingressCount)
	}

	mismatched := event
	mismatched.Payload = json.RawMessage(`{"activityId":"tool-2"}`)
	if _, err := store.AcceptNotificationEvent(ctx, fixture.orgID, fixture.sessionID, fixture.workerID, fixture.epoch, mismatched); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("mismatched duplicate error = %v, want ErrIdempotencyMismatch", err)
	}

	if _, err := admin.Exec(ctx, `UPDATE ao_worker_connections SET disconnected_at = now() WHERE session_id = $1 AND epoch = $2`, fixture.sessionID, fixture.epoch); err != nil {
		t.Fatal(err)
	}
	stale := event
	stale.EventID = "evt_01KSTALE"
	if _, err := store.AcceptNotificationEvent(ctx, fixture.orgID, fixture.sessionID, fixture.workerID, fixture.epoch, stale); !errors.Is(err, ErrStaleWorker) {
		t.Fatalf("stale worker error = %v, want ErrStaleWorker", err)
	}
}

func TestNotificationInboxIsRecipientScopedAndCursorOrdered(t *testing.T) {
	store, _, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	principal := domain.Principal{UserID: fixture.userID, Provider: "local"}
	for index, eventID := range []string{"evt_01KFIRST", "evt_01KSECOND"} {
		ingress, err := store.AcceptNotificationEvent(ctx, fixture.orgID, fixture.sessionID, fixture.workerID, fixture.epoch, domain.AgentNotificationEvent{
			EventID: eventID, Type: domain.NotificationTypeNeedsInput,
			OccurredAt: time.Now().UTC(), Payload: json.RawMessage(`{"activityId":"activity-` + eventID + `"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		_, changed, err := store.CreateNotificationFromIngress(ctx, domain.NotificationIngress{
			ID: ingress.IngressID, OrgID: fixture.orgID, ProjectID: fixture.projectID,
			SessionID: fixture.sessionID, RecipientUserID: fixture.userID, WorkerID: fixture.workerID,
			WorkerEpoch: fixture.epoch, Event: domain.AgentNotificationEvent{EventID: eventID}, LeaseOwner: "test",
		}, domain.Notification{
			OrgID: fixture.orgID, RecipientUserID: fixture.userID, ProjectID: fixture.projectID,
			SessionID: fixture.sessionID, Source: "cloud", Type: "needs_input",
			Title: "Agent needs input", Body: "", DedupeKey: "needs-input:" + eventID,
			Status: string(domain.NotificationStatusUnread), EventID: eventID, Metadata: json.RawMessage(`{}`),
		})
		if err != nil || !changed {
			t.Fatalf("create notification %d: changed=%v err=%v", index, changed, err)
		}
	}

	page, err := store.ListNotifications(ctx, principal, fixture.orgID, domain.NotificationFilter{Status: domain.NotificationStatusUnread, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !page.HasMore || page.NextCursor == nil || page.UnreadCount != 2 {
		t.Fatalf("first page = %+v", page)
	}
	second, err := store.ListNotifications(ctx, principal, fixture.orgID, domain.NotificationFilter{Status: domain.NotificationStatusUnread, Cursor: page.NextCursor, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID == page.Items[0].ID {
		t.Fatalf("second page = %+v, first page = %+v", second, page)
	}

	otherPrincipal := domain.Principal{UserID: uuid.NewString(), Provider: "local"}
	if _, err := store.ListNotifications(ctx, otherPrincipal, fixture.orgID, domain.NotificationFilter{Limit: 20}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other recipient error = %v, want ErrForbidden", err)
	}
}

func openNotificationTestStore(t *testing.T) (*Store, *pgxpool.Pool, notificationFixture) {
	t.Helper()
	databaseURL := os.Getenv("AO_CLOUD_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("AO_CLOUD_TEST_DATABASE_URL is not set; skipping PostgreSQL notification integration test")
	}
	ctx := context.Background()
	if err := Migrate(ctx, databaseURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	t.Cleanup(admin.Close)

	fixture := notificationFixture{
		orgID: uuid.NewString(), userID: uuid.NewString(), projectID: uuid.NewString(),
		sessionID: uuid.NewString(), workerID: "worker-" + uuid.NewString(), epoch: 41,
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO ao_users (id, auth_provider, external_user_id, email, display_name, password_hash)
			VALUES ($1, 'local', $1, $1 || '@example.test', 'Notification Test', 'hash')`, []any{fixture.userID}},
		{`INSERT INTO ao_organizations (id, auth_provider, slug, display_name, kind, owner_user_id, created_by_user_id)
			VALUES ($1, 'local', $2, 'Notification Test', 'personal', $3, $3)`, []any{fixture.orgID, "notif-" + uuid.NewString(), fixture.userID}},
		{`INSERT INTO ao_org_memberships (org_id, user_id, role) VALUES ($1, $2, 'owner')`, []any{fixture.orgID, fixture.userID}},
		{`INSERT INTO ao_projects (id, org_id, display_name, repository_url) VALUES ($1, $2, 'Project', $3)`, []any{fixture.projectID, fixture.orgID, "https://example.test/" + fixture.projectID}},
		{`INSERT INTO ao_sessions (id, org_id, project_id, kind, harness, display_name, branch, created_by_user_id)
			VALUES ($1, $2, $3, 'worker', 'codex', 'Session', 'main', $4)`, []any{fixture.sessionID, fixture.orgID, fixture.projectID, fixture.userID}},
		{`INSERT INTO ao_sandboxes (session_id, org_id, provider) VALUES ($1, $2, 'docker')`, []any{fixture.sessionID, fixture.orgID}},
		{`INSERT INTO ao_worker_connections (session_id, org_id, sandbox_id, epoch, worker_id, version)
			VALUES ($1, $2, $1, $3, $4, 'test')`, []any{fixture.sessionID, fixture.orgID, fixture.epoch, fixture.workerID}},
	}
	for _, statement := range statements {
		if _, err := admin.Exec(ctx, statement.query, statement.args...); err != nil {
			store.Close()
			admin.Close()
			t.Fatalf("seed notification fixture: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM ao_organizations WHERE id = $1`, fixture.orgID)
		_, _ = admin.Exec(context.Background(), `DELETE FROM ao_users WHERE id = $1`, fixture.userID)
	})
	return store, admin, fixture
}
