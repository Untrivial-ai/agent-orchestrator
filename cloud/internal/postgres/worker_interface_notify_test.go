package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCoordinatedInterfaceRequestWakesWaitingWorker(t *testing.T) {
	store, _, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	listener, err := pgx.Connect(ctx, os.Getenv("AO_CLOUD_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close(ctx)
	if _, err := listener.Exec(ctx, `LISTEN ao_worker_work`); err != nil {
		t.Fatal(err)
	}

	if _, err := store.CreateCoordinatedInterfaceRequest(
		ctx, fixture.orgID, fixture.sessionID, "interface.inspect", json.RawMessage(`{}`),
	); err != nil {
		t.Fatal(err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for {
		notification, err := listener.WaitForNotification(waitCtx)
		if err != nil {
			t.Fatalf("worker was not woken when interface request was queued: %v", err)
		}
		if notification.Payload == fixture.sessionID {
			return
		}
	}
}
