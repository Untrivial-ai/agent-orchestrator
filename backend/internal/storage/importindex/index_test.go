package importindex

import (
	"context"
	"fmt"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/sessionimport"
	"path/filepath"
	"testing"
	"time"
)

func put(t testing.TB, i *Index, id, title, root, generation string, activity int64) {
	t.Helper()
	s := sessionimport.ImportableSession{Provider: domain.HarnessCodex, NativeSessionID: id, ConfigDir: root, TranscriptPath: filepath.Join(root, id), Title: title, LastActivity: time.Unix(activity, 0)}
	if err := i.Put(context.Background(), root, s.TranscriptPath, 1, 1, generation, s); err != nil {
		t.Fatal(err)
	}
}
func TestSearchRankingUnicodePaginationAndReopen(t *testing.T) {
	dir := t.TempDir()
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	put(t, i, "exact", "Payment processing", "root", "1", 1)
	put(t, i, "phrase", "Improve payment processing", "root", "1", 900)
	put(t, i, "fuzzy", "Payment procesing", "root", "1", 999)
	put(t, i, "unicode", "ＣＡＦÉ tools", "root", "1", 50)
	put(t, i, "exact", "Different root", "other", "1", 1000)
	rows, more, err := i.Search(ctx, "payment processing", 1, 0)
	if err != nil || !more || len(rows) != 1 || rows[0].Session.Title != "Payment processing" {
		t.Fatalf("first=%+v more=%v err=%v", rows, more, err)
	}
	rows, _, err = i.Search(ctx, "payment processing", 5, 1)
	if err != nil || len(rows) != 2 || rows[0].Session.Title != "Improve payment processing" || rows[1].Session.Title != "Payment procesing" {
		t.Fatalf("remaining=%+v err=%v", rows, err)
	}
	rows, _, err = i.Search(ctx, "cafe\u0301", 50, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("unicode=%+v err=%v", rows, err)
	}
	if err = i.Close(); err != nil {
		t.Fatal(err)
	}
	i, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = i.Close() }()
	rows, _, err = i.Search(ctx, "", 50, 0)
	if err != nil || len(rows) != 5 {
		t.Fatalf("reopened rows=%d err=%v", len(rows), err)
	}
}
func TestGroupingFingerprintAndCompleteScan(t *testing.T) {
	i, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = i.Close() }()
	ctx := context.Background()
	s := sessionimport.ImportableSession{Provider: domain.HarnessCodex, NativeSessionID: "thread", Title: "old", LastActivity: time.Unix(1, 0)}
	if err = i.Put(ctx, "root", "old", 1, 1, "one", s); err != nil {
		t.Fatal(err)
	}
	s.Title = "new"
	s.LastActivity = time.Unix(2, 0)
	if err = i.Put(ctx, "root", "new", 1, 2, "one", s); err != nil {
		t.Fatal(err)
	}
	rows, _, _ := i.Search(ctx, "", 50, 0)
	if len(rows) != 1 || rows[0].Session.Title != "new" {
		t.Fatal(rows)
	}
	cached, err := i.Seen(ctx, "root", "old", 1, 1, "two")
	if err != nil || !cached {
		t.Fatal(cached, err)
	}
	// Interrupted refresh does not call Complete: the newest segment stays visible.
	rows, _, _ = i.Search(ctx, "", 50, 0)
	if rows[0].Session.Title != "new" {
		t.Fatal(rows)
	}
	if err = i.Complete(ctx, "root", "two"); err != nil {
		t.Fatal(err)
	}
	rows, _, _ = i.Search(ctx, "", 50, 0)
	if len(rows) != 1 || rows[0].Session.Title != "old" {
		t.Fatal(rows)
	}
	if err = i.Title(ctx, "root", "thread", "Renamed"); err != nil {
		t.Fatal(err)
	}
	rows, _, _ = i.Search(ctx, "renamed", 50, 0)
	if len(rows) != 1 {
		t.Fatal(rows)
	}
	if err = i.Complete(ctx, "root", "three"); err != nil {
		t.Fatal(err)
	}
	rows, _, _ = i.Search(ctx, "", 50, 0)
	if len(rows) != 0 {
		t.Fatal(rows)
	}
}
func BenchmarkSearch10000(b *testing.B) {
	i, err := Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = i.Close() }()
	for n := 0; n < 10000; n++ {
		put(b, i, fmt.Sprint(n), fmt.Sprintf("Conversation %d about payment processing", n), "root", "one", int64(n))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		if _, _, err := i.Search(context.Background(), "payment procesing", 50, 0); err != nil {
			b.Fatal(err)
		}
	}
}
