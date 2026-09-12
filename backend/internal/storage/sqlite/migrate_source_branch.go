package sqlite

import "database/sql"

// prepareSessionSourceBranchMigration preserves preview databases that applied
// source_branch as version 126 or 129. Main now owns those versions for
// canonical repository identity and the CDC retention index. Record the existing
// column at 140, replay idempotent 126, and release 129 only if its index is absent.
func prepareSessionSourceBranchMigration(db *sql.DB) error {
	var ledger, column int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='goose_db_version'`).Scan(&ledger); err != nil {
		return err
	}
	if ledger == 0 {
		return nil
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='source_branch'`).Scan(&column); err != nil {
		return err
	}
	if column == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var applied int
	if err := tx.QueryRow(`SELECT COALESCE((SELECT is_applied FROM goose_db_version WHERE version_id=140 ORDER BY id DESC LIMIT 1),0)`).Scan(&applied); err != nil {
		return err
	}
	if applied != 0 {
		return nil
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_source_branch ON sessions(source_branch) WHERE source_branch <> ''`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM goose_db_version WHERE version_id=126`); err != nil {
		return err
	}
	var retentionIndex int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_change_log_created_at_seq'`).Scan(&retentionIndex); err != nil {
		return err
	}
	if retentionIndex == 0 {
		if _, err := tx.Exec(`DELETE FROM goose_db_version WHERE version_id=129`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO goose_db_version(version_id,is_applied) VALUES(140,1)`); err != nil {
		return err
	}
	return tx.Commit()
}
