package docker

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/google/uuid"
)

func TestDockerRejectedCreateWorkspaceCleanup(t *testing.T) {
	owner := os.Getenv("AO_SESSION_ID")
	if os.Getenv("AO_TEST_DOCKER") != "1" || owner == "" {
		t.Skip("requires an opted-in Docker Engine and session owner")
	}
	id := uuid.NewString()
	client, err := New(Config{
		WorkerImage: "ao-rejected-create-test:" + id, Namespace: "review-" + id,
		ExtraLabels: map[string]string{"ao.session": owner},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	spec := sandbox.Spec{SessionID: id, OrgID: uuid.NewString()}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := client.CleanupSession(cleanup, spec.OrgID, spec.SessionID); err != nil {
			t.Error(err)
		}
	})
	_, err = client.Create(ctx, spec)
	if !errors.Is(err, sandbox.ErrCreateRejected) {
		t.Fatalf("missing-image rejection=%v", err)
	}
	name := workspaceName(client.namespace, spec.SessionID)
	var volume volumeView
	if err := client.do(ctx, "GET", "/volumes/"+name, nil, &volume); err != nil {
		t.Fatal(err)
	}
	if volume.Labels["ao.session"] != owner {
		t.Fatal("workspace missing session ownership label")
	}
	if _, found, err := client.FindBySession(ctx, spec.SessionID); err != nil || found {
		t.Fatalf("unexpected container: found=%v err=%v", found, err)
	}
	if err := client.CleanupSession(ctx, spec.OrgID, spec.SessionID); err != nil {
		t.Fatal(err)
	}
	if err := client.do(ctx, "GET", "/volumes/"+name, nil, &volume); !errors.Is(err, sandbox.ErrNotFound) {
		t.Fatalf("workspace remained after cleanup: %v", err)
	}
}
