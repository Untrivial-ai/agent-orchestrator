package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestWorkerReportCarriesAutomationCardMetadata(t *testing.T) {
	store, _, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	childID := uuid.NewString()
	if err := store.withOrg(ctx, fixture.orgID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE ao_sessions SET kind = 'orchestrator' WHERE org_id = $1 AND id = $2`, fixture.orgID, fixture.sessionID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO ao_sessions (id, org_id, project_id, parent_session_id, kind, harness, display_name, branch, created_by_user_id)
			VALUES ($1, $2, $3, $4, 'worker', 'codex', 'Builder', 'child', $5)`, childID, fixture.orgID, fixture.projectID, fixture.sessionID, fixture.userID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	event, err := store.ReportToOrchestrator(ctx, fixture.orgID, childID, uuid.NewString(), "Full prompt")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct{ Text, Origin, SenderLabel, DisplayText string }
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Origin != "automation" || payload.SenderLabel != "Worker · Builder" || payload.DisplayText != "Full prompt" {
		t.Fatalf("worker report metadata = %+v", payload)
	}
	if payload.Text == payload.DisplayText {
		t.Fatal("agent prompt lost worker provenance")
	}
}
