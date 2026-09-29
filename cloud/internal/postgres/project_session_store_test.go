package postgres

import (
	"context"
	"testing"

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
