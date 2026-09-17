package sqlite

import (
	"testing"
	"time"
)

// Rows written before the connection pinned _timezone=UTC can hold a
// time.Time.String() value with a numeric zone abbreviation, which the driver
// cannot scan. Migration 0148 must rewrite them as the same instant in UTC and
// leave already-UTC rows untouched.
func TestProjectTimestampRepairNormalizesLocalZoneValues(t *testing.T) {
	db := openMigratedDatabaseCopy(t, 140)
	upTo(t, db, 147)
	if _, err := db.Exec(`
INSERT INTO projects (id, path, registered_at, archived_at) VALUES
 ('tainted', '/tainted', '2026-09-11 20:39:10.511227327 +0530 +0530 m=+73.186830353', '2026-09-12 01:15:00 +0530 +0530'),
 ('west', '/west', '2026-09-11 08:00:00.25 -0300 -03', NULL),
 ('clean', '/clean', '2026-09-11 15:07:58.532901487 +0000 UTC', NULL);
`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, tc := range []struct {
		id, column string
		want       time.Time
	}{
		{"tainted", "registered_at", time.Date(2026, 9, 11, 15, 9, 10, 511227327, time.UTC)},
		{"tainted", "archived_at", time.Date(2026, 9, 11, 19, 45, 0, 0, time.UTC)},
		{"west", "registered_at", time.Date(2026, 9, 11, 11, 0, 0, 250000000, time.UTC)},
		{"clean", "registered_at", time.Date(2026, 9, 11, 15, 7, 58, 532901487, time.UTC)},
	} {
		var got time.Time
		if err := db.QueryRow(`SELECT `+tc.column+` FROM projects WHERE id = ?`, tc.id).Scan(&got); err != nil {
			t.Fatalf("%s.%s does not scan after repair: %v", tc.id, tc.column, err)
		}
		if !got.Equal(tc.want) {
			t.Fatalf("%s.%s = %v, want %v", tc.id, tc.column, got, tc.want)
		}
	}
}
