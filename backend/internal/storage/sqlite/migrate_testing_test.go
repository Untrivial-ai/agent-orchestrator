package sqlite

import (
	"testing"
)

func TestTestingMigrationPreservesCDCSequenceTriggersAndRollback(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 194)
	if _, err := db.Exec(`INSERT INTO projects(id,path,registered_at) VALUES('testing-project','/isolated','2026-10-06T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO change_log(seq,project_id,event_type,payload) VALUES(9000,'testing-project','session_updated','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM change_log WHERE seq=9000`); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND sql LIKE '%change_log%'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 195)
	var after int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND sql LIKE '%change_log%'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before+9 {
		t.Fatal("CDC triggers lost during migration", before, after)
	}
	if _, err := db.Exec(`INSERT INTO test_runs(id,project_id,issue_url,issue_snapshot,commit_sha,recipe_snapshot,requester,created_at) VALUES('run','testing-project','','"issue"','abc','{}','maintainer','2026-10-06T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	var seq int64
	if err := db.QueryRow(`SELECT seq FROM change_log WHERE event_type='testing_updated'`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if seq <= 9000 {
		t.Fatal("CDC sequence regressed", seq)
	}
	downTo(t, db, 194)
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='trigger' AND sql LIKE '%change_log%'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("rollback lost existing CDC triggers")
	}
	if _, err := db.Exec(`INSERT INTO change_log(project_id,event_type,payload) VALUES('testing-project','session_updated','{}')`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT max(seq) FROM change_log`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if seq <= 9001 {
		t.Fatal("rollback regressed sequence", seq)
	}
	upTo(t, db, 195)
}
