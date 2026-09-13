package store

import (
	"database/sql"
	"testing"
	"time"
)

func TestUTCTimePreservesZeroAndNullSemantics(t *testing.T) {
	local := time.Date(2026, time.January, 2, 7, 0, 0, 0, time.FixedZone("UTC-05", -5*60*60))

	if got := utcTime(time.Time{}); !got.IsZero() {
		t.Fatalf("utcTime(zero) = %v, want zero", got)
	}
	if got := utcTime(local); !got.Equal(local) || got.Location() != time.UTC {
		t.Fatalf("utcTime(local) = %v (%v), want same instant in UTC", got, got.Location())
	}
	invalid := sql.NullTime{Time: local, Valid: false}
	if got := utcNullTime(invalid); got != invalid {
		t.Fatalf("utcNullTime(invalid) = %#v, want unchanged %#v", got, invalid)
	}
	valid := utcNullTime(sql.NullTime{Time: local, Valid: true})
	if !valid.Valid || !valid.Time.Equal(local) || valid.Time.Location() != time.UTC {
		t.Fatalf("utcNullTime(valid) = %#v, want same instant in UTC", valid)
	}
}
