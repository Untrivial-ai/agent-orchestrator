package accountsmanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCodexAccountSwitchStorePersistsIdempotencyAndCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "switch-state.json")
	store, err := newCodexAccountSwitchStore(path)
	if err != nil {
		t.Fatalf("newCodexAccountSwitchStore: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	record := domain.CodexAccountSwitch{
		ID: "switch-a", SourceKind: domain.CodexAccountSwitchSourceDevice,
		TargetAccountID: "account-b", IdempotencyKey: "request-a",
		Phase: domain.CodexAccountSwitchActivatingAccount, CreatedAt: now, UpdatedAt: now,
	}
	created, inserted, err := store.CreateCodexAccountSwitch(context.Background(), record)
	if err != nil || !inserted || created.ID != record.ID {
		t.Fatalf("create switch: got=%+v inserted=%v err=%v", created, inserted, err)
	}
	replayed, inserted, err := store.CreateCodexAccountSwitch(context.Background(), record)
	if err != nil || inserted || replayed.ID != record.ID {
		t.Fatalf("replay switch: got=%+v inserted=%v err=%v", replayed, inserted, err)
	}
	conflict := record
	conflict.ID = "switch-b"
	conflict.TargetAccountID = "account-c"
	if _, _, err := store.CreateCodexAccountSwitch(context.Background(), conflict); !errors.Is(err, ports.ErrCodexAccountSwitchIdempotencyConflict) {
		t.Fatalf("idempotency conflict = %v", err)
	}
	other := record
	other.ID = "switch-c"
	other.IdempotencyKey = "request-c"
	if _, _, err := store.CreateCodexAccountSwitch(context.Background(), other); !errors.Is(err, ports.ErrCodexAccountSwitchInProgress) {
		t.Fatalf("active switch conflict = %v", err)
	}

	record.Phase = domain.CodexAccountSwitchCompleted
	record.CompletedAt = &now
	record.UpdatedAt = now.Add(time.Second)
	if ok, err := store.UpdateCodexAccountSwitch(context.Background(), record, domain.CodexAccountSwitchActivatingAccount); err != nil || !ok {
		t.Fatalf("CAS transition: ok=%v err=%v", ok, err)
	}
	if ok, err := store.UpdateCodexAccountSwitch(context.Background(), record, domain.CodexAccountSwitchActivatingAccount); err != nil || ok {
		t.Fatalf("stale CAS transition: ok=%v err=%v", ok, err)
	}

	reloaded, err := newCodexAccountSwitchStore(path)
	if err != nil {
		t.Fatalf("reload switch store: %v", err)
	}
	got, found, err := reloaded.GetCodexAccountSwitch(context.Background(), record.ID)
	if err != nil || !found || got.Phase != domain.CodexAccountSwitchCompleted {
		t.Fatalf("reloaded switch: got=%+v found=%v err=%v", got, found, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat switch state: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("switch state permissions = %o, want 600", got)
	}
}
