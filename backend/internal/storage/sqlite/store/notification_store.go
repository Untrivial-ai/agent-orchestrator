package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	moderncsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// CreateNotification inserts one unread notification. It returns created=false
// when the open dedupe index already has a matching row — open meaning unseen
// or still unresolved, so a notification the user has already looked at is not
// re-raised while its underlying issue is unchanged.
func (s *Store) CreateNotification(ctx context.Context, rec domain.NotificationRecord) (domain.NotificationRecord, bool, error) {
	if err := rec.Validate(); err != nil {
		return domain.NotificationRecord{}, false, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if existing, ok, err := s.getOpenNotificationByDedupe(ctx, rec); err != nil {
		return domain.NotificationRecord{}, false, err
	} else if ok {
		return existing, false, nil
	}
	row, err := s.qw.CreateNotification(ctx, gen.CreateNotificationParams{
		ID:        rec.ID,
		SessionID: rec.SessionID,
		ProjectID: rec.ProjectID,
		PRURL:     rec.PRURL,
		Type:      rec.Type,
		Title:     rec.Title,
		Body:      rec.Body,
		Status:    rec.Status,
		CreatedAt: rec.CreatedAt,
	})
	if err != nil {
		if isSQLiteUnique(err) {
			if existing, ok, lookupErr := s.getOpenNotificationByDedupe(ctx, rec); lookupErr != nil {
				return domain.NotificationRecord{}, false, lookupErr
			} else if ok {
				return existing, false, nil
			}
		}
		return domain.NotificationRecord{}, false, fmt.Errorf("create notification %s: %w", rec.ID, err)
	}
	return notificationFromGen(row), true, nil
}

// ListNotifications returns one stable newest-first page of notifications.
func (s *Store) ListNotifications(
	ctx context.Context,
	status domain.NotificationListStatus,
	beforeCreatedAt time.Time,
	beforeID string,
	limit int,
) ([]domain.NotificationRecord, error) {
	var (
		rows []gen.Notification
		err  error
	)
	switch status {
	case domain.NotificationListUnread:
		rows, err = s.qr.ListUnreadNotificationsPage(ctx, gen.ListUnreadNotificationsPageParams{
			BeforeID:        beforeID,
			BeforeCreatedAt: beforeCreatedAt,
			PageLimit:       int64(limit),
		})
	case domain.NotificationListUnresolved:
		rows, err = s.qr.ListUnresolvedNotificationsPage(ctx, gen.ListUnresolvedNotificationsPageParams{
			BeforeID:        beforeID,
			BeforeCreatedAt: beforeCreatedAt,
			PageLimit:       int64(limit),
		})
	default:
		rows, err = s.qr.ListNotificationsPage(ctx, gen.ListNotificationsPageParams{
			BeforeID:        beforeID,
			BeforeCreatedAt: beforeCreatedAt,
			PageLimit:       int64(limit),
		})
	}
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	return notificationsFromGen(rows), nil
}

// CountUnreadNotifications returns the exact unread badge count independently
// from the bounded history page.
func (s *Store) CountUnreadNotifications(ctx context.Context) (int64, error) {
	count, err := s.qr.CountUnreadNotifications(ctx)
	if err != nil {
		return 0, fmt.Errorf("count unread notifications: %w", err)
	}
	return count, nil
}

// CountUnresolvedNotifications returns how many notifications still have an
// open underlying issue, independently from the bounded history page.
func (s *Store) CountUnresolvedNotifications(ctx context.Context) (int64, error) {
	count, err := s.qr.CountUnresolvedNotifications(ctx)
	if err != nil {
		return 0, fmt.Errorf("count unresolved notifications: %w", err)
	}
	return count, nil
}

// ResolveSessionNotifications marks every open notification of one type on a
// session resolved and returns the rows it changed.
func (s *Store) ResolveSessionNotifications(
	ctx context.Context,
	id domain.SessionID,
	typ domain.NotificationType,
	at time.Time,
) ([]domain.NotificationRecord, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.ResolveSessionNotificationsByType(ctx, gen.ResolveSessionNotificationsByTypeParams{
		ResolvedAt: nullTime(at),
		SessionID:  id,
		Type:       typ,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve session notifications %s/%s: %w", id, typ, err)
	}
	return notificationsFromGen(rows), nil
}

// ResolvePRNotifications marks every open notification of one type on a PR
// resolved and returns the rows it changed.
func (s *Store) ResolvePRNotifications(
	ctx context.Context,
	prURL string,
	typ domain.NotificationType,
	at time.Time,
) ([]domain.NotificationRecord, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.ResolvePRNotificationsByType(ctx, gen.ResolvePRNotificationsByTypeParams{
		ResolvedAt: nullTime(at),
		PRURL:      prURL,
		Type:       typ,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve pr notifications %s/%s: %w", prURL, typ, err)
	}
	return notificationsFromGen(rows), nil
}

// ReconcileResolvedNotifications closes open notifications whose resolving
// transition happened while the daemon was down, by re-reading the durable
// session and PR facts. Without it a restart can strand a needs-input row in
// the unresolved list forever.
//
// Ready-to-merge is judged in Go rather than SQL: readiness is more than
// open/closed, and the SCM observer persists PR facts before it asks lifecycle
// to resolve, so a crash in that window leaves an open PR whose CI already went
// red. Both paths run domain.MergeReadiness so they cannot disagree.
func (s *Store) ReconcileResolvedNotifications(ctx context.Context, at time.Time) ([]domain.NotificationRecord, error) {
	candidates, err := s.qr.ListOpenReadyToMergeNotifications(ctx)
	if err != nil {
		return nil, fmt.Errorf("list open ready-to-merge notifications: %w", err)
	}
	seen := map[string]bool{}
	stalePRs := make([]string, 0, len(candidates))
	for _, row := range candidates {
		if row.PRURL == "" || seen[row.PRURL] {
			continue
		}
		seen[row.PRURL] = true
		pr, ok, err := s.GetPR(ctx, row.PRURL)
		if err != nil {
			return nil, fmt.Errorf("reconcile read pr %s: %w", row.PRURL, err)
		}
		if !ok {
			// No durable PR facts to judge against. Leave it for the next SCM
			// observation rather than guessing.
			continue
		}
		comments, err := s.ListPRComments(ctx, row.PRURL)
		if err != nil {
			return nil, fmt.Errorf("reconcile read pr comments %s: %w", row.PRURL, err)
		}
		unresolved := false
		for _, c := range comments {
			if !c.Resolved && !c.IsBot {
				unresolved = true
				break
			}
		}
		if !domain.MergeReadinessOf(pr, unresolved).ReadyToMerge() {
			stalePRs = append(stalePRs, row.PRURL)
		}
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	needsInput, err := s.qw.ResolveStaleNeedsInputNotifications(ctx, nullTime(at))
	if err != nil {
		return nil, fmt.Errorf("reconcile needs-input notifications: %w", err)
	}
	resolved := notificationsFromGen(needsInput)
	for _, prURL := range stalePRs {
		rows, err := s.qw.ResolvePRNotificationsByType(ctx, gen.ResolvePRNotificationsByTypeParams{
			ResolvedAt: nullTime(at),
			PRURL:      prURL,
			Type:       domain.NotificationReadyToMerge,
		})
		if err != nil {
			return nil, fmt.Errorf("reconcile ready-to-merge notifications %s: %w", prURL, err)
		}
		resolved = append(resolved, notificationsFromGen(rows)...)
	}
	return resolved, nil
}

// MarkNotificationsRead acknowledges exactly the notifications the panel
// rendered. Clearing every unread row instead would strand anything past the
// first page — the client never held a cursor for it, and terminal types are
// not reachable through the unresolved list.
func (s *Store) MarkNotificationsRead(ctx context.Context, ids []string) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var updated int64
	for _, id := range ids {
		if _, err := s.qw.MarkNotificationRead(ctx, id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return updated, fmt.Errorf("mark notification read %s: %w", id, err)
		}
		updated++
	}
	return updated, nil
}

// MarkNotificationRead marks one unread notification read.
func (s *Store) MarkNotificationRead(ctx context.Context, id string) (domain.NotificationRecord, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	row, err := s.qw.MarkNotificationRead(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NotificationRecord{}, false, nil
	}
	if err != nil {
		return domain.NotificationRecord{}, false, fmt.Errorf("mark notification read %s: %w", id, err)
	}
	return notificationFromGen(row), true, nil
}

// MarkAllNotificationsRead marks every unread notification read.
func (s *Store) MarkAllNotificationsRead(ctx context.Context) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	count, err := s.qw.MarkAllNotificationsRead(ctx)
	if err != nil {
		return 0, fmt.Errorf("mark all notifications read: %w", err)
	}
	return count, nil
}

func (s *Store) getOpenNotificationByDedupe(ctx context.Context, rec domain.NotificationRecord) (domain.NotificationRecord, bool, error) {
	row, err := s.qw.GetOpenNotificationByDedupe(ctx, gen.GetOpenNotificationByDedupeParams{
		SessionID: rec.SessionID,
		Type:      rec.Type,
		PRURL:     rec.PRURL,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.NotificationRecord{}, false, nil
	}
	if err != nil {
		return domain.NotificationRecord{}, false, fmt.Errorf("lookup open notification dedupe: %w", err)
	}
	return notificationFromGen(row), true, nil
}

func isSQLiteUnique(err error) bool {
	var sqliteErr *moderncsqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}

func notificationFromGen(row gen.Notification) domain.NotificationRecord {
	return domain.NotificationRecord{
		ID:         row.ID,
		SessionID:  row.SessionID,
		ProjectID:  row.ProjectID,
		PRURL:      row.PRURL,
		Type:       row.Type,
		Title:      row.Title,
		Body:       row.Body,
		Status:     row.Status,
		CreatedAt:  row.CreatedAt,
		ResolvedAt: timeFromNull(row.ResolvedAt),
	}
}

func notificationsFromGen(rows []gen.Notification) []domain.NotificationRecord {
	out := make([]domain.NotificationRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, notificationFromGen(row))
	}
	return out
}
