package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestNotificationStore_InsertListAndDedupe(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	sess, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	rec := domain.NotificationRecord{
		ID:        "ntf_1",
		SessionID: sess.ID,
		ProjectID: sess.ProjectID,
		Type:      domain.NotificationNeedsInput,
		Title:     "checkout-flow needs input",
		Status:    domain.NotificationUnread,
		CreatedAt: now,
	}
	created, inserted, err := s.CreateNotification(ctx, rec)
	if err != nil || !inserted {
		t.Fatalf("CreateNotification inserted=%v err=%v", inserted, err)
	}
	if created.ID != rec.ID || created.Title != rec.Title {
		t.Fatalf("created = %+v", created)
	}
	dup := rec
	dup.ID = "ntf_2"
	_, inserted, err = s.CreateNotification(ctx, dup)
	if err != nil || inserted {
		t.Fatalf("duplicate inserted=%v err=%v, want false nil", inserted, err)
	}
	rows, err := s.ListNotifications(ctx, domain.NotificationListUnread, time.Time{}, "", 10)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "ntf_1" {
		t.Fatalf("rows = %+v", rows)
	}
}

// Seeing a notification is not the same as fixing what it reported. Dedupe is
// keyed on the issue being open, so the same session cannot be re-notified
// about a pause it is still sitting in — only resolving it reopens dedupe.
func TestNotificationStore_ResolutionNotReadReopensDedupe(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	sess, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	rec := domain.NotificationRecord{
		ID:        "ntf_1",
		SessionID: sess.ID,
		ProjectID: sess.ProjectID,
		Type:      domain.NotificationNeedsInput,
		Title:     "checkout-flow needs input",
		Status:    domain.NotificationUnread,
		CreatedAt: now,
	}
	if _, inserted, err := s.CreateNotification(ctx, rec); err != nil || !inserted {
		t.Fatalf("CreateNotification inserted=%v err=%v", inserted, err)
	}
	read, ok, err := s.MarkNotificationRead(ctx, rec.ID)
	if err != nil || !ok {
		t.Fatalf("MarkNotificationRead ok=%v err=%v", ok, err)
	}
	if read.Status != domain.NotificationRead {
		t.Fatalf("status = %q, want read", read.Status)
	}
	rows, err := s.ListNotifications(ctx, domain.NotificationListUnread, time.Time{}, "", 10)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
	again := rec
	again.ID = "ntf_2"
	again.CreatedAt = now.Add(time.Minute)
	if _, inserted, err := s.CreateNotification(ctx, again); err != nil || inserted {
		t.Fatalf("CreateNotification while unresolved inserted=%v err=%v, want false", inserted, err)
	}

	resolvedAt := now.Add(2 * time.Minute)
	resolved, err := s.ResolveSessionNotifications(ctx, sess.ID, domain.NotificationNeedsInput, resolvedAt)
	if err != nil {
		t.Fatalf("ResolveSessionNotifications: %v", err)
	}
	if len(resolved) != 1 || resolved[0].ID != rec.ID || !resolved[0].ResolvedAt.Equal(resolvedAt) {
		t.Fatalf("resolved = %+v", resolved)
	}
	if _, inserted, err := s.CreateNotification(ctx, again); err != nil || !inserted {
		t.Fatalf("CreateNotification after resolve inserted=%v err=%v, want true", inserted, err)
	}
}

// The unresolved list is independent of the seen state and excludes terminal
// facts: a merged PR is worth seeing once, never worth resolving.
func TestNotificationStore_ListUnresolvedIgnoresSeenAndTerminalTypes(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	sess, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	seed := func(id string, typ domain.NotificationType, prURL string) domain.NotificationRecord {
		rec := domain.NotificationRecord{
			ID: id, SessionID: sess.ID, ProjectID: sess.ProjectID, PRURL: prURL, Type: typ,
			Title: id, Status: domain.NotificationUnread, CreatedAt: now,
		}
		if _, inserted, err := s.CreateNotification(ctx, rec); err != nil || !inserted {
			t.Fatalf("CreateNotification %s inserted=%v err=%v", id, inserted, err)
		}
		return rec
	}
	seed("ntf_input", domain.NotificationNeedsInput, "")
	seed("ntf_ready", domain.NotificationReadyToMerge, "https://github.com/o/r/pull/1")
	seed("ntf_merged", domain.NotificationPRMerged, "https://github.com/o/r/pull/2")
	if _, err := s.MarkAllNotificationsRead(ctx); err != nil {
		t.Fatalf("MarkAllNotificationsRead: %v", err)
	}

	rows, err := s.ListNotifications(ctx, domain.NotificationListUnresolved, time.Time{}, "", 10)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	if len(ids) != 2 || ids[0] == "ntf_merged" || ids[1] == "ntf_merged" {
		t.Fatalf("unresolved ids = %v, want the two open issues", ids)
	}
	count, err := s.CountUnresolvedNotifications(ctx)
	if err != nil || count != 2 {
		t.Fatalf("CountUnresolvedNotifications = %d err=%v, want 2", count, err)
	}

	// Resolving by PR follows the PR, not the session.
	resolved, err := s.ResolvePRNotifications(ctx, "https://github.com/o/r/pull/1", domain.NotificationReadyToMerge, now)
	if err != nil || len(resolved) != 1 || resolved[0].ID != "ntf_ready" {
		t.Fatalf("ResolvePRNotifications = %+v err=%v", resolved, err)
	}
	if count, err := s.CountUnresolvedNotifications(ctx); err != nil || count != 1 {
		t.Fatalf("CountUnresolvedNotifications = %d err=%v, want 1", count, err)
	}
}

func TestNotificationStore_MarkReadMissing(t *testing.T) {
	s := newTestStore(t)
	_, ok, err := s.MarkNotificationRead(context.Background(), "missing")
	if err != nil || ok {
		t.Fatalf("MarkNotificationRead ok=%v err=%v, want false nil", ok, err)
	}
}

func TestNotificationStore_MarkAllRead(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	sess, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	base := time.Now().UTC().Truncate(time.Second)
	for _, rec := range []domain.NotificationRecord{
		{ID: "ntf_1", SessionID: sess.ID, ProjectID: sess.ProjectID, Type: domain.NotificationNeedsInput, Title: "one", Status: domain.NotificationUnread, CreatedAt: base},
		{ID: "ntf_2", SessionID: sess.ID, ProjectID: sess.ProjectID, PRURL: "https://github.com/o/r/pull/1", Type: domain.NotificationReadyToMerge, Title: "two", Status: domain.NotificationUnread, CreatedAt: base.Add(time.Minute)},
	} {
		if _, inserted, err := s.CreateNotification(ctx, rec); err != nil || !inserted {
			t.Fatalf("insert %s inserted=%v err=%v", rec.ID, inserted, err)
		}
	}
	updated, err := s.MarkAllNotificationsRead(ctx)
	if err != nil {
		t.Fatalf("MarkAllNotificationsRead: %v", err)
	}
	if updated != 2 {
		t.Fatalf("updated = %d, want 2", updated)
	}
	updated, err = s.MarkAllNotificationsRead(ctx)
	if err != nil || updated != 0 {
		t.Fatalf("second mark-all updated=%d err=%v, want 0 nil", updated, err)
	}
	rows, err := s.ListNotifications(ctx, domain.NotificationListUnread, time.Time{}, "", 10)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("unread rows = %+v, want none", rows)
	}
}

func TestNotificationStore_ListUnreadNewestFirstAcrossProjects(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	seedProject(t, s, "ao")
	mer, _ := s.CreateSession(ctx, sampleRecord("mer"))
	ao, _ := s.CreateSession(ctx, sampleRecord("ao"))
	base := time.Now().UTC().Truncate(time.Second)
	for _, rec := range []domain.NotificationRecord{
		{ID: "old", SessionID: mer.ID, ProjectID: mer.ProjectID, Type: domain.NotificationNeedsInput, Title: "old", Status: domain.NotificationUnread, CreatedAt: base},
		{ID: "new", SessionID: mer.ID, ProjectID: mer.ProjectID, PRURL: "https://github.com/o/r/pull/1", Type: domain.NotificationReadyToMerge, Title: "new", Status: domain.NotificationUnread, CreatedAt: base.Add(time.Minute)},
		{ID: "other", SessionID: ao.ID, ProjectID: ao.ProjectID, Type: domain.NotificationNeedsInput, Title: "other", Status: domain.NotificationUnread, CreatedAt: base.Add(2 * time.Minute)},
	} {
		if _, inserted, err := s.CreateNotification(ctx, rec); err != nil || !inserted {
			t.Fatalf("insert %s inserted=%v err=%v", rec.ID, inserted, err)
		}
	}
	rows, err := s.ListNotifications(ctx, domain.NotificationListUnread, time.Time{}, "", 2)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != "other" || rows[1].ID != "new" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestNotificationStore_ListAllUsesStableCursorWithoutAgeCutoff(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	sess, _ := s.CreateSession(ctx, sampleRecord("mer"))
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	for _, rec := range []domain.NotificationRecord{
		{ID: "same-z", SessionID: sess.ID, ProjectID: sess.ProjectID, Type: domain.NotificationNeedsInput, Title: "same z", Status: domain.NotificationUnread, CreatedAt: now},
		{ID: "same-a", SessionID: sess.ID, ProjectID: sess.ProjectID, PRURL: "https://github.com/o/r/pull/1", Type: domain.NotificationPRMerged, Title: "same a", Status: domain.NotificationRead, CreatedAt: now},
		{ID: "old", SessionID: sess.ID, ProjectID: sess.ProjectID, PRURL: "https://github.com/o/r/pull/old", Type: domain.NotificationPRClosedUnmerged, Title: "old", Status: domain.NotificationUnread, CreatedAt: now.Add(-30 * 24 * time.Hour)},
	} {
		if _, inserted, err := s.CreateNotification(ctx, rec); err != nil || !inserted {
			t.Fatalf("insert %s inserted=%v err=%v", rec.ID, inserted, err)
		}
	}

	rows, err := s.ListNotifications(ctx, domain.NotificationListAll, time.Time{}, "", 2)
	if err != nil {
		t.Fatalf("ListNotifications: %v", err)
	}
	if len(rows) != 2 || rows[0].ID != "same-z" || rows[1].ID != "same-a" {
		t.Fatalf("rows = %+v", rows)
	}
	older, err := s.ListNotifications(ctx, domain.NotificationListAll, rows[1].CreatedAt, rows[1].ID, 2)
	if err != nil {
		t.Fatalf("ListNotifications older: %v", err)
	}
	if len(older) != 1 || older[0].ID != "old" {
		t.Fatalf("older = %+v", older)
	}
	count, err := s.CountUnreadNotifications(ctx)
	if err != nil || count != 2 {
		t.Fatalf("unread count=%d err=%v", count, err)
	}
}

func TestNotificationStore_CheckConstraintRejectsInvalidStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	sess, _ := s.CreateSession(ctx, sampleRecord("mer"))
	_, _, err := s.CreateNotification(ctx, domain.NotificationRecord{
		ID: "bad", SessionID: sess.ID, ProjectID: sess.ProjectID, Type: domain.NotificationNeedsInput,
		Title: "bad", Status: "archived", CreatedAt: time.Now(),
	})
	if !errors.Is(err, domain.ErrInvalidNotificationStatus) {
		t.Fatalf("err = %v, want invalid status", err)
	}
}

// Readiness is more than open/closed. The SCM observer persists PR facts before
// it asks lifecycle to resolve, so a crash in that window leaves an open PR
// whose CI (or review, or mergeability) already moved against it. Startup
// reconciliation has to apply the same rule the live path does.
func TestNotificationStore_ReconcileAppliesFullReadyToMergePredicate(t *testing.T) {
	ready := domain.PullRequest{
		CI: domain.CIPassing, Review: domain.ReviewApproved, Mergeability: domain.MergeMergeable,
	}
	withPR := func(mutate func(*domain.PullRequest)) domain.PullRequest {
		pr := ready
		mutate(&pr)
		return pr
	}
	for _, tt := range []struct {
		name         string
		pr           domain.PullRequest
		comments     []domain.PullRequestComment
		wantResolved bool
	}{
		{name: "still ready", pr: ready},
		{name: "merged", pr: withPR(func(p *domain.PullRequest) { p.Merged = true }), wantResolved: true},
		{name: "closed", pr: withPR(func(p *domain.PullRequest) { p.Closed = true }), wantResolved: true},
		{name: "draft", pr: withPR(func(p *domain.PullRequest) { p.Draft = true }), wantResolved: true},
		{name: "ci failing", pr: withPR(func(p *domain.PullRequest) { p.CI = domain.CIFailing }), wantResolved: true},
		{name: "ci pending", pr: withPR(func(p *domain.PullRequest) { p.CI = domain.CIPending }), wantResolved: true},
		{
			name:         "changes requested",
			pr:           withPR(func(p *domain.PullRequest) { p.Review = domain.ReviewChangesRequest }),
			wantResolved: true,
		},
		{
			name:         "not mergeable",
			pr:           withPR(func(p *domain.PullRequest) { p.Mergeability = domain.MergeConflicting }),
			wantResolved: true,
		},
		{
			name:         "unresolved human comment",
			pr:           ready,
			comments:     []domain.PullRequestComment{{ID: "c1", Author: "dev", Body: "please fix", CreatedAt: time.Now().UTC()}},
			wantResolved: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			seedProject(t, s, "mer")
			sess, err := s.CreateSession(ctx, sampleRecord("mer"))
			if err != nil {
				t.Fatalf("create session: %v", err)
			}
			now := time.Now().UTC().Truncate(time.Second)
			prURL := "https://github.com/o/r/pull/1"
			pr := tt.pr
			pr.URL = prURL
			pr.SessionID = sess.ID
			pr.UpdatedAt = now
			if err := s.WritePR(ctx, pr, nil, tt.comments); err != nil {
				t.Fatalf("WritePR: %v", err)
			}
			rec := domain.NotificationRecord{
				ID: "ntf_ready", SessionID: sess.ID, ProjectID: sess.ProjectID, PRURL: prURL,
				Type: domain.NotificationReadyToMerge, Title: "ready", Status: domain.NotificationUnread, CreatedAt: now,
			}
			if _, inserted, err := s.CreateNotification(ctx, rec); err != nil || !inserted {
				t.Fatalf("CreateNotification inserted=%v err=%v", inserted, err)
			}

			resolved, err := s.ReconcileResolvedNotifications(ctx, now.Add(time.Minute))
			if err != nil {
				t.Fatalf("ReconcileResolvedNotifications: %v", err)
			}
			gotResolved := false
			for _, r := range resolved {
				if r.ID == "ntf_ready" {
					gotResolved = true
				}
			}
			if gotResolved != tt.wantResolved {
				t.Fatalf("resolved = %v, want %v", gotResolved, tt.wantResolved)
			}
			count, err := s.CountUnresolvedNotifications(ctx)
			if err != nil {
				t.Fatalf("CountUnresolvedNotifications: %v", err)
			}
			wantCount := int64(1)
			if tt.wantResolved {
				wantCount = 0
			}
			if count != wantCount {
				t.Fatalf("unresolved count = %d, want %d", count, wantCount)
			}
		})
	}
}

// Acknowledging must be scoped to the rows a client actually rendered.
func TestNotificationStore_MarkNotificationsReadOnlyTouchesGivenIDs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedProject(t, s, "mer")
	sess, err := s.CreateSession(ctx, sampleRecord("mer"))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for i, id := range []string{"ntf_1", "ntf_2", "ntf_3"} {
		rec := domain.NotificationRecord{
			ID: id, SessionID: sess.ID, ProjectID: sess.ProjectID,
			PRURL: "https://github.com/o/r/pull/" + id,
			Type:  domain.NotificationPRMerged, Title: id,
			Status: domain.NotificationUnread, CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		}
		if _, inserted, err := s.CreateNotification(ctx, rec); err != nil || !inserted {
			t.Fatalf("CreateNotification %s inserted=%v err=%v", id, inserted, err)
		}
	}

	updated, err := s.MarkNotificationsRead(ctx, []string{"ntf_1", "ntf_2"})
	if err != nil || updated != 2 {
		t.Fatalf("MarkNotificationsRead updated=%d err=%v, want 2", updated, err)
	}
	count, err := s.CountUnreadNotifications(ctx)
	if err != nil || count != 1 {
		t.Fatalf("unread count = %d err=%v, want 1 (ntf_3 must stay reachable)", count, err)
	}
}
