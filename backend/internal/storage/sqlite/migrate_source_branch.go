package sqlite

import "database/sql"

// prepareSessionSourceBranchMigration preserves preview databases that applied
// source_branch as version 126, 129, 140, or 141 and import identity indexes as
// version 142. Main now owns those versions. Record source_branch at 156, release
// the reused import-index ledger entry so main's checkpoint migrations replay,
// and let the idempotent index migration run at 157.
// A preview also briefly used version 140 for this column before main shipped
// standalone sessions at 140. Release that ledger entry when project_id is
// still NOT NULL so the real standalone migration can run.
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
	var legacySourceVersion, checkpointStateColumn int
	if err := tx.QueryRow(`SELECT COALESCE((SELECT is_applied FROM goose_db_version WHERE version_id=141 ORDER BY id DESC LIMIT 1),0)`).Scan(&legacySourceVersion); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='conversation_checkpoint_state'`).Scan(&checkpointStateColumn); err != nil {
		return err
	}
	if legacySourceVersion != 0 && checkpointStateColumn == 0 {
		if _, err := tx.Exec(`DELETE FROM goose_db_version WHERE version_id=141`); err != nil {
			return err
		}
	}
	var legacyImportVersion, checkpointUnsettledColumn, importIndexes int
	if err := tx.QueryRow(`SELECT COALESCE((SELECT is_applied FROM goose_db_version WHERE version_id=142 ORDER BY id DESC LIMIT 1),0)`).Scan(&legacyImportVersion); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('sessions') WHERE name='conversation_checkpoint_unsettled'`).Scan(&checkpointUnsettledColumn); err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name IN ('sessions_import_conversation','sessions_import_agent')`).Scan(&importIndexes); err != nil {
		return err
	}
	if legacyImportVersion != 0 && checkpointUnsettledColumn == 0 && importIndexes == 2 {
		if _, err := tx.Exec(`DELETE FROM goose_db_version WHERE version_id=142`); err != nil {
			return err
		}
	}
	var applied int
	if err := tx.QueryRow(`SELECT COALESCE((SELECT is_applied FROM goose_db_version WHERE version_id=156 ORDER BY id DESC LIMIT 1),0)`).Scan(&applied); err != nil {
		return err
	}
	if applied != 0 {
		return tx.Commit()
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
	var projectIDNotNull int
	if err := tx.QueryRow(`SELECT "notnull" FROM pragma_table_info('sessions') WHERE name='project_id'`).Scan(&projectIDNotNull); err != nil {
		return err
	}
	if projectIDNotNull != 0 {
		if _, err := tx.Exec(`DELETE FROM goose_db_version WHERE version_id=140`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO goose_db_version(version_id,is_applied) VALUES(156,1)`); err != nil {
		return err
	}
	return tx.Commit()
}
