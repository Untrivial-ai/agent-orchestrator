package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
)

type legacySwitchTarget struct {
	records []domain.CodexAccountSwitch
}

func (s *legacySwitchTarget) CreateCodexAccountSwitch(_ context.Context, record domain.CodexAccountSwitch) (domain.CodexAccountSwitch, bool, error) {
	s.records = append(s.records, record)
	return record, true, nil
}
func (s *legacySwitchTarget) GetCodexAccountSwitch(context.Context, string) (domain.CodexAccountSwitch, bool, error) {
	return domain.CodexAccountSwitch{}, false, nil
}
func (s *legacySwitchTarget) GetCodexAccountSwitchByIdempotency(context.Context, string) (domain.CodexAccountSwitch, bool, error) {
	return domain.CodexAccountSwitch{}, false, nil
}
func (s *legacySwitchTarget) GetActiveCodexAccountSwitch(context.Context) (domain.CodexAccountSwitch, bool, error) {
	return domain.CodexAccountSwitch{}, false, nil
}
func (s *legacySwitchTarget) UpdateCodexAccountSwitch(context.Context, domain.CodexAccountSwitch, domain.CodexAccountSwitchPhase) (bool, error) {
	return false, nil
}

func TestMigrateLegacyCodexAccountSwitchRemovesSQLiteTable(t *testing.T) {
	dataDir := t.TempDir()
	store, err := sqlite.Open(dataDir)
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	defer func() { _ = store.Close() }()
	db, err := sql.Open("sqlite", "file:"+dataDir+"/ao.db?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	defer func() { _ = db.Close() }()
	now := time.Now().UTC().Truncate(time.Second)
	for _, record := range []struct {
		id, phase string
	}{
		{id: "completed", phase: string(domain.CodexAccountSwitchCompleted)},
		{id: "active", phase: string(domain.CodexAccountSwitchActivatingAccount)},
	} {
		if _, err := db.Exec(`INSERT INTO codex_account_switches
			(id, source_account_id, target_account_id, idempotency_key, phase,
			 failure_code, credentials_committed_at, created_at, updated_at, completed_at, source_kind)
			VALUES (?, 'source', 'target', ?, ?, '', NULL, ?, ?, NULL, 'managed')`,
			record.id, record.id+"-request", record.phase, now, now); err != nil {
			t.Fatalf("seed %s: %v", record.id, err)
		}
	}
	target := &legacySwitchTarget{}
	if err := store.MigrateLegacyCodexAccountSwitch(context.Background(), target); err != nil {
		t.Fatalf("MigrateLegacyCodexAccountSwitch: %v", err)
	}
	if len(target.records) != 2 {
		t.Fatalf("imported records = %d, want 2", len(target.records))
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'codex_account_switches'`).Scan(&tables); err != nil {
		t.Fatalf("check legacy table: %v", err)
	}
	if tables != 0 {
		t.Fatal("legacy codex_account_switches table still exists")
	}
}

var _ ports.CodexAccountSwitchStore = (*legacySwitchTarget)(nil)
