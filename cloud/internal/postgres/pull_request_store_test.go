package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/cloud/internal/domain"
)

func TestRecordPullRequestOpenedPersistsRecipientNotification(t *testing.T) {
	store, _, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	pullRequest, err := store.CreatePullRequestRecord(
		ctx, fixture.orgID, fixture.sessionID, "github", "octo/widgets", "octocat", 19,
		"https://github.test/octo/widgets/pull/19", "feature", "main", "def456",
		"Notify me", 1, 0, 1,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.RecordPullRequestOpened(ctx, fixture.orgID, pullRequest, "delivery-19"); err != nil {
		t.Fatalf("record opened notification: %v", err)
	}
	page, err := store.ListNotifications(
		ctx,
		domain.Principal{UserID: fixture.userID, Provider: "local"},
		fixture.orgID,
		domain.NotificationFilter{Status: domain.NotificationStatusUnread, Limit: 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Type != "pr_opened" || page.Items[0].Source != "cloud" {
		t.Fatalf("notifications = %+v, want one cloud pr_opened item", page.Items)
	}
	if err := store.RecordPullRequestOpened(ctx, fixture.orgID, pullRequest, "delivery-20"); err != nil {
		t.Fatalf("repeat opened notification: %v", err)
	}
	events, _, err := store.ListNotificationEvents(
		ctx, domain.Principal{UserID: fixture.userID, Provider: "local"}, fixture.orgID, 0, 20,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("notification events = %d, want one for repeated PR-opened delivery", len(events))
	}
}

func TestPullRequestByGitHubReferenceSupportsProjectCreatedBeforeAppInstall(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	repositoryID := time.Now().UnixNano()
	fullName := fmt.Sprintf("octo/legacy-%d", repositoryID)
	if err := admin.QueryRow(ctx, `
		INSERT INTO ao_github_repositories (
			github_repository_id, github_owner_account_id, name, full_name,
			html_url, clone_url, visibility
		) VALUES ($1, 1, 'legacy', $2, 'https://github.test/legacy',
			'https://github.test/legacy.git', 'private')
		RETURNING github_repository_id`, repositoryID, fullName).Scan(&repositoryID); err != nil {
		t.Fatal(err)
	}
	pullRequest, err := store.CreatePullRequestRecord(
		ctx, fixture.orgID, fixture.sessionID, "github", fullName, "octocat", 17,
		"https://github.test/"+fullName+"/pull/17", "feature", "main", "abc123",
		"Legacy project PR", 1, 0, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM ao_github_repositories WHERE github_repository_id = $1`, repositoryID)
	})

	got, err := store.PullRequestByGitHubReference(ctx, fixture.orgID, repositoryID, 17)
	if err != nil {
		t.Fatalf("resolve legacy project PR: %v", err)
	}
	if got.ID != pullRequest.ID {
		t.Fatalf("resolved PR id = %q, want %q", got.ID, pullRequest.ID)
	}
}

func TestSessionForGitHubPullRequestHeadMatchesOnlyOwningBranch(t *testing.T) {
	store, admin, fixture := openNotificationTestStore(t)
	ctx := context.Background()
	repositoryID := time.Now().UnixNano()
	fullName := fmt.Sprintf("octo/branch-%d", repositoryID)
	url := "https://github.com/" + fullName
	if _, err := admin.Exec(ctx, `INSERT INTO ao_github_repositories
		(github_repository_id, github_owner_account_id, name, full_name, html_url, clone_url, visibility)
		VALUES ($1, 1, 'branch', $2, $3, $4, 'private')`, repositoryID, fullName, url, url+".git"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), `DELETE FROM ao_github_repositories WHERE github_repository_id = $1`, repositoryID)
	})
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('ao.org_id', $1, true)`, fixture.orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ao_projects SET repository_url = $1 WHERE id = $2`, url, fixture.projectID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ao_sessions SET branch = 'ao/session-1' WHERE id = $1`, fixture.sessionID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := store.SessionForGitHubPullRequestHead(ctx, fixture.orgID, repositoryID, "ao/session-1", "abc123")
	if err != nil || got != fixture.sessionID {
		t.Fatalf("matched session %q, error %v; want %q", got, err, fixture.sessionID)
	}
	if _, err := store.SessionForGitHubPullRequestHead(ctx, fixture.orgID, repositoryID, "someone-else", "abc123"); err != ErrNotFound {
		t.Fatalf("foreign branch error = %v, want not found", err)
	}
	if err := store.ReplaceWorkerGitRefs(ctx, fixture.orgID, fixture.sessionID, repositoryID, []domain.WorkerGitRef{{Branch: "docs/new", SHA: "def456"}}); err != nil {
		t.Fatal(err)
	}
	got, err = store.SessionForGitHubPullRequestHead(ctx, fixture.orgID, repositoryID, "docs/new", "def456")
	if err != nil || got != fixture.sessionID {
		t.Fatalf("custom branch match = %q, %v", got, err)
	}
	if _, err := store.SessionForGitHubPullRequestHead(ctx, fixture.orgID, repositoryID, "docs/new", "other-sha"); err != ErrNotFound {
		t.Fatalf("wrong commit error = %v, want not found", err)
	}
}
