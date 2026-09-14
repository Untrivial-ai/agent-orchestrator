package sqlite

import (
	"database/sql"
	"fmt"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/session"
)

func TestMigratePRReviewPartialUpgrade(t *testing.T) {
	for _, tt := range []struct {
		name          string
		baseVersion   int64
		legacyVersion int64
		burned130     bool
		wantPartial   bool
	}{
		{name: "intermediate_130_default_false", baseVersion: 129, legacyVersion: 130, wantPartial: true},
		{name: "legacy_123_default_false", baseVersion: 122, legacyVersion: 123, wantPartial: true},
		{name: "main_without_column", baseVersion: 129, wantPartial: true},
		{name: "healthy_130_complete_observation", baseVersion: 130},
		{name: "burned_130_without_column", baseVersion: 129, burned130: true, wantPartial: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			db, err := sql.Open("sqlite", databaseURI(dataDir)+pragmas)
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = db.Close() })
			upTo(t, db, tt.baseVersion)
			if tt.legacyVersion != 0 {
				// Reproduce the migration actually shipped in intermediate PR
				// builds, including its applied Goose version and FALSE default.
				func() {
					gooseMu.Lock()
					defer gooseMu.Unlock()
					goose.SetBaseFS(fstest.MapFS{
						fmt.Sprintf("migrations/%04d_pr_review_partial.sql", tt.legacyVersion): &fstest.MapFile{
							Data: []byte("-- +goose Up\nALTER TABLE pr ADD COLUMN review_partial BOOLEAN NOT NULL DEFAULT FALSE;\n-- +goose Down\n"),
						},
					})
					if err := goose.Up(db, "migrations"); err != nil {
						t.Fatalf("apply intermediate migration: %v", err)
					}
				}()
			}
			if tt.burned130 {
				if _, err := db.Exec(`INSERT INTO goose_db_version (version_id, is_applied) VALUES (130, 1)`); err != nil {
					t.Fatal(err)
				}
			}
			const prURL = "https://github.com/acme/repo/pull/42"
			observedAt := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
			for _, query := range []string{
				`INSERT INTO projects (id, path, registered_at) VALUES ('project', '/tmp/project', CURRENT_TIMESTAMP)`,
				`INSERT INTO sessions (id, project_id, num, activity_last_at, created_at, updated_at)
				 VALUES ('session', 'project', 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			} {
				if _, err := db.Exec(query); err != nil {
					t.Fatalf("seed session: %v", err)
				}
			}
			if _, err := db.Exec(`INSERT INTO pr (url, session_id, number, updated_at, review_observed_at)
			 VALUES (?, 'session', 42, ?, ?)`, prURL, observedAt, observedAt); err != nil {
				t.Fatalf("seed PR: %v", err)
			}
			if tt.baseVersion == 130 {
				if _, err := db.Exec(`UPDATE pr SET review_partial = FALSE WHERE url = ?`, prURL); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`INSERT INTO pr_review_threads (pr_url, thread_id, updated_at)
			 VALUES (?, 'thread-1', ?)`, prURL, observedAt); err != nil {
				t.Fatalf("seed review thread: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			store, err := Open(dataDir)
			if err != nil {
				t.Fatalf("open upgraded database: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })
			assertObservation := func(wantPartial bool, wantObservedAt time.Time) domain.PullRequest {
				t.Helper()
				pr, ok, err := store.GetPR(t.Context(), prURL)
				if err != nil || !ok {
					t.Fatalf("get PR: found=%v err=%v", ok, err)
				}
				if pr.ReviewPartial != wantPartial || !pr.ReviewObservedAt.Equal(wantObservedAt) {
					t.Fatalf("review observation = (%v, %v), want (%v, %v)", pr.ReviewPartial, pr.ReviewObservedAt, wantPartial, wantObservedAt)
				}
				threads, err := store.ListPRReviewThreads(t.Context(), prURL)
				if err != nil || len(threads) != 1 || threads[0].ThreadID != "thread-1" {
					t.Fatalf("review threads = %v, err=%v; want preserved thread-1", threads, err)
				}
				summaries, err := session.NewWithDeps(session.Deps{Store: store}).ListPRSummaries(t.Context(), "session")
				if err != nil || len(summaries) != 1 {
					t.Fatalf("PR summaries = %v, err=%v", summaries, err)
				}
				count := summaries[0].Review.UnresolvedThreadCount
				if wantPartial {
					if count != nil {
						t.Fatalf("unverified review count = %d, want unknown", *count)
					}
				} else if count == nil || *count != 1 {
					t.Fatalf("complete review count = %v, want 1", count)
				}
				return pr
			}
			pr := assertObservation(tt.wantPartial, observedAt)

			// A successful full refresh restores an exact count. Reopening must
			// not invalidate that observation again, even on the FALSE schema.
			pr.ReviewPartial = false
			pr.ReviewObservedAt = observedAt.Add(time.Hour)
			threads := []domain.PullRequestReviewThread{{ThreadID: "thread-1", UpdatedAt: pr.ReviewObservedAt}}
			if err := store.WriteSCMObservation(t.Context(), pr, nil, nil, threads, nil, ports.ReviewWriteReplace); err != nil {
				t.Fatalf("write complete observation: %v", err)
			}
			assertObservation(false, pr.ReviewObservedAt)
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(dataDir)
			if err != nil {
				t.Fatalf("reopen upgraded database: %v", err)
			}
			store = reopened
			assertObservation(false, pr.ReviewObservedAt)
		})
	}
}
