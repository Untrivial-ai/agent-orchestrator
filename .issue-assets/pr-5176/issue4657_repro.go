package store

import "context"

// Test-overlay-only access to the single writer's SQLite change counter.
func (s *Store) Issue4657TotalChanges(ctx context.Context) (int64, error) {
	var n int64
	err := s.writeDB.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&n)
	return n, err
}
