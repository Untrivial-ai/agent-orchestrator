package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// MigrateLegacyCodexAccountSwitch imports the old SQLite-only switch journal
// into the filesystem-owned Accounts Manager store, then removes the legacy
// table. It is deliberately a startup migration method, not a normal store
// boundary: new account-switch reads and writes never use SQLite.
func (s *Store) MigrateLegacyCodexAccountSwitch(ctx context.Context, target ports.CodexAccountSwitchStore) error {
	if target == nil {
		return errors.New("codex account switch migration target is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.readDB.QueryContext(ctx, `
SELECT id, source_kind, source_account_id, target_account_id,
       idempotency_key, phase, failure_code, credentials_committed_at,
       created_at, updated_at, completed_at
FROM codex_account_switches
ORDER BY created_at`)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return nil
		}
		return fmt.Errorf("read legacy Codex account switches: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var records []domain.CodexAccountSwitch
	for rows.Next() {
		var (
			record                 domain.CodexAccountSwitch
			sourceKind             string
			credentialsCommittedAt sql.NullTime
			completedAt            sql.NullTime
		)
		if err := rows.Scan(
			&record.ID, &sourceKind, &record.SourceAccountID, &record.TargetAccountID,
			&record.IdempotencyKey, &record.Phase, &record.FailureCode,
			&credentialsCommittedAt, &record.CreatedAt, &record.UpdatedAt, &completedAt,
		); err != nil {
			return fmt.Errorf("scan legacy Codex account switch: %w", err)
		}
		record.SourceKind = domain.CodexAccountSwitchSourceKind(sourceKind)
		if credentialsCommittedAt.Valid {
			value := credentialsCommittedAt.Time
			record.CredentialsCommittedAt = &value
		}
		if completedAt.Valid {
			value := completedAt.Time
			record.CompletedAt = &value
		}
		record.CreatedAt = record.CreatedAt.UTC()
		record.UpdatedAt = record.UpdatedAt.UTC()
		record.SourceKind = defaultSwitchSourceKind(record.SourceKind)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read legacy Codex account switches: %w", err)
	}
	// The filesystem store permits only one nonterminal record. Import terminal
	// history first so a historical active row cannot block later completed rows.
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].Phase.Terminal() && !records[j].Phase.Terminal()
	})
	for _, record := range records {
		if _, _, err := target.CreateCodexAccountSwitch(ctx, record); err != nil {
			return fmt.Errorf("import legacy Codex account switch %s: %w", record.ID, err)
		}
	}
	if _, err := s.writeDB.ExecContext(ctx, `DROP TABLE codex_account_switches`); err != nil {
		return fmt.Errorf("remove legacy Codex account switch table: %w", err)
	}
	return nil
}

func defaultSwitchSourceKind(kind domain.CodexAccountSwitchSourceKind) domain.CodexAccountSwitchSourceKind {
	if kind == "" {
		return domain.CodexAccountSwitchSourceManaged
	}
	return kind
}
