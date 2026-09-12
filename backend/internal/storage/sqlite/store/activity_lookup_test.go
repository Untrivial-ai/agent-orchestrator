package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

// Inspect the SQL actually issued by sqlc, not a copy of the production query.
type activityPlanDB struct {
	*sql.DB
	t *testing.T
}

func (db activityPlanDB) check(ctx context.Context, query string, args ...any) {
	db.t.Helper()
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		db.t.Fatal(err)
	}
	defer rows.Close()
	indexed := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			db.t.Fatal(err)
		}
		indexed = indexed || (strings.Contains(detail, "idx_conversation_activities_provider_item") && strings.Contains(detail, "provider_item_id=?"))
	}
	if err := rows.Err(); err != nil {
		db.t.Fatal(err)
	}
	if !indexed {
		db.t.Fatal("history activity lookup must seek by provider item, not scan the conversation")
	}
}
func (db activityPlanDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	db.check(ctx, query, args...)
	return db.DB.QueryRowContext(ctx, query, args...)
}
func (db activityPlanDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	db.check(ctx, query, args...)
	return db.DB.ExecContext(ctx, query, args...)
}
func TestHistoryActivityQueriesUseProviderItemIndex(t *testing.T) {
	dir := t.TempDir()
	_ = sqlitetest.MustOpenAt(t, dir)
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "ao.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	q := gen.New(activityPlanDB{DB: raw, t: t})
	exists, err := q.ConversationActivityExistsByProviderItem(context.Background(), gen.ConversationActivityExistsByProviderItemParams{ConversationID: "conversation", ProviderItemID: "item"})
	if err != nil || exists {
		t.Fatalf("missing activity: exists=%v err=%v", exists, err)
	}
	_, err = q.SettleConversationActivityStreamedText(context.Background(), gen.SettleConversationActivityStreamedTextParams{ConversationID: "conversation", ProviderItemID: "item"})
	if err != nil {
		t.Fatal(err)
	}
}
