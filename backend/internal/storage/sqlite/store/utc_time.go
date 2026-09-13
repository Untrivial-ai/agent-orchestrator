package store

import (
	"database/sql"
	"time"
)

// utcTime is the SQLite write-boundary invariant for non-null timestamps.
// modernc SQLite preserves a time.Time's location in its textual encoding, so
// equivalent instants from different host zones do not reliably compare or
// sort together unless writers canonicalize them first.
//
// Call this only while constructing persisted values. Read cursors and expected
// legacy-row predicates stay in the caller's representation. A few generated
// updates deliberately reuse one argument for both SET and a monotonic guard;
// SQLite necessarily observes the same canonical value in both positions.
func utcTime(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.UTC()
}

func utcNullTime(value sql.NullTime) sql.NullTime {
	if !value.Valid {
		return value
	}
	value.Time = utcTime(value.Time)
	return value
}
