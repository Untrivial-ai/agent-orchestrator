package sqlite

import "testing"

func TestMigration0150ReviewNotificationsRoundTrip(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 149)
	const timestamp = "2026-09-23T08:00:00Z"
	if _, err := db.Exec(`
INSERT INTO sessions (id, num, kind, activity_last_at, created_at, updated_at)
VALUES ('standalone-1', 1, 'worker', ?, ?, ?);
INSERT INTO notifications (
    id, session_id, project_id, pr_url, type, title, body, status,
    created_at, resolved_at, dismissed_at
) VALUES (
    'notice-1', 'standalone-1', NULL, 'https://github.com/acme/app/pull/42',
    'ready_to_merge', 'Ready', 'Existing notification', 'read', ?, ?, ?
);`, timestamp, timestamp, timestamp, timestamp, timestamp, timestamp); err != nil {
		t.Fatal(err)
	}
	upTo(t, db, 150)

	assertReviewNotificationSchema := func() {
		t.Helper()
		var nullableProject, sourceKey int
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('notifications') WHERE name = 'project_id' AND "notnull" = 0`).Scan(&nullableProject); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('notifications') WHERE name = 'source_key' AND "notnull" = 1`).Scan(&sourceKey); err != nil {
			t.Fatal(err)
		}
		if nullableProject != 1 || sourceKey != 1 {
			t.Fatalf("notification schema project nullable=%d source key=%d", nullableProject, sourceKey)
		}
		var sourceIndex int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_notifications_source_dedupe'`).Scan(&sourceIndex); err != nil {
			t.Fatal(err)
		}
		if sourceIndex != 1 {
			t.Fatal("review notification source dedupe index is missing")
		}
		var historyIndex, obsoleteStatusIndex int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_notifications_status_history'`).Scan(&historyIndex); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_notifications_status'`).Scan(&obsoleteStatusIndex); err != nil {
			t.Fatal(err)
		}
		if historyIndex != 1 || obsoleteStatusIndex != 0 {
			t.Fatalf("notification status indexes history=%d obsolete=%d", historyIndex, obsoleteStatusIndex)
		}
	}
	assertReviewNotificationSchema()
	assertExistingNotification := func() {
		t.Helper()
		var sessionID, prURL, typ, title, body, status string
		var projectID any
		if err := db.QueryRow(`
SELECT session_id, project_id, pr_url, type, title, body, status
FROM notifications WHERE id = 'notice-1'`).Scan(
			&sessionID, &projectID, &prURL, &typ, &title, &body, &status,
		); err != nil {
			t.Fatal(err)
		}
		if sessionID != "standalone-1" || projectID != nil || prURL != "https://github.com/acme/app/pull/42" ||
			typ != "ready_to_merge" || title != "Ready" || body != "Existing notification" || status != "read" {
			t.Fatalf("existing notification changed during migration: session=%q project=%#v pr=%q type=%q title=%q body=%q status=%q",
				sessionID, projectID, prURL, typ, title, body, status)
		}
	}
	assertExistingNotification()

	downTo(t, db, 149)
	var sourceKey int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('notifications') WHERE name = 'source_key'`).Scan(&sourceKey); err != nil {
		t.Fatal(err)
	}
	if sourceKey != 0 {
		t.Fatal("source_key survived migration rollback")
	}
	var historyIndex int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_notifications_status_history'`).Scan(&historyIndex); err != nil {
		t.Fatal(err)
	}
	if historyIndex != 1 {
		t.Fatal("status history index was not restored after rollback")
	}
	assertExistingNotification()

	upTo(t, db, 150)
	assertReviewNotificationSchema()
	assertExistingNotification()
}
