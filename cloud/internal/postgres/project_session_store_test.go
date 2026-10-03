package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
	"github.com/google/uuid"
)

func TestCreateSessionReturnsCompleteSession(t *testing.T) {
	store, _, fixture := openNotificationTestStore(t)

	session, err := store.CreateSession(
		context.Background(),
		domain.Principal{UserID: fixture.userID, Provider: "local"},
		fixture.orgID,
		"create-session-"+uuid.NewString(),
		10,
		domain.CreateSession{
			ProjectID:   fixture.projectID,
			Kind:        "orchestrator",
			Harness:     "codex",
			DisplayName: "Test orchestrator",
			Provider:    "docker",
		},
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if session.ID == "" {
		t.Fatal("created session has no ID")
	}
	if !session.AutoInjectCI || !session.AutoInjectReview {
		t.Fatalf(
			"created session policies = ci:%v review:%v, want both enabled",
			session.AutoInjectCI,
			session.AutoInjectReview,
		)
	}
	if session.RuntimeConnected {
		t.Fatal("new session is unexpectedly runtime-connected")
	}
	if session.SandboxProvider != "docker" {
		t.Fatalf("sandbox provider = %q, want docker", session.SandboxProvider)
	}
}

func TestQueuedTurnDoesNotOverrideIdleWorkerActivity(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	if _, err := admin.Exec(ctx,
		`INSERT INTO ao_events (org_id, session_id, sequence, type)
		VALUES ($1, $2, 1, 'chat.user_message')`,
		fixture.orgID, fixture.sessionID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx,
		`INSERT INTO ao_turns (org_id, session_id, user_message_sequence)
		VALUES ($1, $2, 1)`,
		fixture.orgID, fixture.sessionID,
	); err != nil {
		t.Fatal(err)
	}
	for _, turnState := range []string{"queued", "running"} {
		t.Run(turnState, func(t *testing.T) {
			if _, err := admin.Exec(ctx,
				`UPDATE ao_turns SET state = $1 WHERE org_id = $2 AND session_id = $3`,
				turnState, fixture.orgID, fixture.sessionID,
			); err != nil {
				t.Fatal(err)
			}
			session, err := store.GetSession(ctx,
				domain.Principal{UserID: fixture.userID, Provider: "local"},
				fixture.orgID, fixture.sessionID,
			)
			if err != nil {
				t.Fatal(err)
			}
			if got := session.Status(time.Now(), nil); got != contract.StatusIdle {
				t.Fatalf("status = %q, want idle despite %s turn", got, turnState)
			}
		})
	}
}
