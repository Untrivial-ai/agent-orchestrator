package notify

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type fakeStore struct {
	rows      []domain.NotificationRecord
	duplicate bool
	err       error
}

func (f *fakeStore) CreateNotification(_ context.Context, rec domain.NotificationRecord) (domain.NotificationRecord, bool, error) {
	if f.err != nil {
		return domain.NotificationRecord{}, false, f.err
	}
	if f.duplicate {
		return domain.NotificationRecord{}, false, nil
	}
	f.rows = append(f.rows, rec)
	return rec, true, nil
}

func (f *fakeStore) ResolveSessionNotifications(
	_ context.Context,
	id domain.SessionID,
	typ domain.NotificationType,
	at time.Time,
) ([]domain.NotificationRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resolveMatching(at, func(rec domain.NotificationRecord) bool {
		return rec.SessionID == id && rec.Type == typ
	}), nil
}

func (f *fakeStore) ResolvePRNotifications(
	_ context.Context,
	prURL string,
	typ domain.NotificationType,
	at time.Time,
) ([]domain.NotificationRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resolveMatching(at, func(rec domain.NotificationRecord) bool {
		return rec.PRURL == prURL && rec.Type == typ
	}), nil
}

func (f *fakeStore) ReconcileResolvedNotifications(_ context.Context, at time.Time) ([]domain.NotificationRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resolveMatching(at, func(rec domain.NotificationRecord) bool { return rec.Type.NeedsResolution() }), nil
}

func (f *fakeStore) resolveMatching(at time.Time, match func(domain.NotificationRecord) bool) []domain.NotificationRecord {
	var out []domain.NotificationRecord
	for i, rec := range f.rows {
		if rec.Resolved() || !match(rec) {
			continue
		}
		f.rows[i].ResolvedAt = at
		out = append(out, f.rows[i])
	}
	return out
}

func TestManagerNotifyPersistsThenPublishes(t *testing.T) {
	st := &fakeStore{}
	hub := NewHub()
	ch, unsub := hub.Subscribe("")
	defer unsub()
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	mgr := New(Deps{Store: st, Publisher: hub, Clock: func() time.Time { return now }, NewID: func() string { return "ntf_1" }})

	if err := mgr.Notify(context.Background(), Intent{Type: domain.NotificationNeedsInput, SessionID: "mer-1", ProjectID: "mer", SessionDisplayName: "checkout-flow"}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(st.rows) != 1 {
		t.Fatalf("stored rows = %d, want 1", len(st.rows))
	}
	if got := st.rows[0]; got.ID != "ntf_1" || got.CreatedAt != now || got.Status != domain.NotificationUnread || got.Title != "checkout-flow needs your input" {
		t.Fatalf("stored notification = %+v", got)
	}
	select {
	case got := <-ch:
		if got.Kind != domain.NotificationCreated || got.Record.ID != "ntf_1" {
			t.Fatalf("published = %+v", got)
		}
	default:
		t.Fatal("expected published notification")
	}
}

func TestManagerNotifyDuplicateDoesNotPublish(t *testing.T) {
	st := &fakeStore{duplicate: true}
	hub := NewHub()
	ch, unsub := hub.Subscribe("")
	defer unsub()
	mgr := New(Deps{Store: st, Publisher: hub, Clock: func() time.Time { return time.Now() }, NewID: func() string { return "ntf_1" }})

	if err := mgr.Notify(context.Background(), Intent{Type: domain.NotificationNeedsInput, SessionID: "mer-1", ProjectID: "mer", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("Notify duplicate: %v", err)
	}
	select {
	case got := <-ch:
		t.Fatalf("duplicate published %+v", got)
	default:
	}
}

func TestManagerNotifyRejectsUnknownType(t *testing.T) {
	mgr := New(Deps{Store: &fakeStore{}, Clock: func() time.Time { return time.Now() }})
	err := mgr.Notify(context.Background(), Intent{Type: "surprise", SessionID: "mer-1", ProjectID: "mer"})
	if !errors.Is(err, domain.ErrInvalidNotificationType) {
		t.Fatalf("err = %v, want invalid type", err)
	}
}

func TestHubProjectFilter(t *testing.T) {
	hub := NewHub()
	ch, unsub := hub.Subscribe("mer")
	defer unsub()
	_ = hub.Publish(context.Background(), domain.NotificationEvent{Record: domain.NotificationRecord{ID: "skip", ProjectID: "ao"}})
	_ = hub.Publish(context.Background(), domain.NotificationEvent{Record: domain.NotificationRecord{ID: "keep", ProjectID: "mer"}})
	select {
	case got := <-ch:
		if got.Record.ID != "keep" {
			t.Fatalf("published = %+v", got)
		}
	default:
		t.Fatal("expected filtered notification")
	}
}

func TestHubGlobalClearReachesEveryProjectSubscriber(t *testing.T) {
	hub := NewHub()
	projectCh, unsubscribeProject := hub.Subscribe("mer")
	defer unsubscribeProject()
	allCh, unsubscribeAll := hub.Subscribe("")
	defer unsubscribeAll()

	if err := hub.Publish(context.Background(), domain.NotificationEvent{Kind: domain.NotificationCleared}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	for name, ch := range map[string]<-chan domain.NotificationEvent{"project": projectCh, "all": allCh} {
		select {
		case got := <-ch:
			if got.Kind != domain.NotificationCleared {
				t.Fatalf("%s subscriber received %+v", name, got)
			}
		default:
			t.Fatalf("%s subscriber did not receive global clear", name)
		}
	}
}

// Resolution is how a notification leaves the unresolved list: AO observed the
// underlying issue going away. There is no user-facing resolve action.
func TestManagerResolveClosesSessionNotificationsAndPublishes(t *testing.T) {
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	st := &fakeStore{rows: []domain.NotificationRecord{
		{ID: "ntf_1", SessionID: "mer-1", ProjectID: "mer", Type: domain.NotificationNeedsInput},
		{ID: "ntf_2", SessionID: "mer-2", ProjectID: "mer", Type: domain.NotificationNeedsInput},
	}}
	hub := NewHub()
	ch, unsub := hub.Subscribe("")
	defer unsub()
	mgr := New(Deps{Store: st, Publisher: hub, Clock: func() time.Time { return now }})

	if err := mgr.Resolve(context.Background(), Resolution{Type: domain.NotificationNeedsInput, SessionID: "mer-1"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !st.rows[0].Resolved() || st.rows[0].ResolvedAt != now {
		t.Fatalf("row 0 = %+v, want resolved at %v", st.rows[0], now)
	}
	if st.rows[1].Resolved() {
		t.Fatalf("row 1 resolved, want untouched: %+v", st.rows[1])
	}
	select {
	case got := <-ch:
		if got.Kind != domain.NotificationResolved || got.Record.ID != "ntf_1" {
			t.Fatalf("published = %+v", got)
		}
	default:
		t.Fatal("expected published resolution")
	}
}

// A PR-scoped resolution follows the PR, not the session: the same PR's
// ready-to-merge ping must clear even when it is keyed on another session row.
func TestManagerResolvePrefersPRScope(t *testing.T) {
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	st := &fakeStore{rows: []domain.NotificationRecord{
		{ID: "ntf_1", SessionID: "mer-9", PRURL: "https://x/pull/1", Type: domain.NotificationReadyToMerge},
	}}
	mgr := New(Deps{Store: st, Clock: func() time.Time { return now }})

	res := Resolution{Type: domain.NotificationReadyToMerge, SessionID: "mer-1", PRURL: "https://x/pull/1"}
	if err := mgr.Resolve(context.Background(), res); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !st.rows[0].Resolved() {
		t.Fatalf("row = %+v, want resolved", st.rows[0])
	}
}

func TestManagerResolveRejectsUnknownType(t *testing.T) {
	mgr := New(Deps{Store: &fakeStore{}, Clock: func() time.Time { return time.Now() }})
	err := mgr.Resolve(context.Background(), Resolution{Type: "surprise", SessionID: "mer-1"})
	if !errors.Is(err, domain.ErrInvalidNotificationType) {
		t.Fatalf("err = %v, want invalid type", err)
	}
}

// A transition observed while the daemon was down never reaches lifecycle, so
// startup re-checks open rows against the durable facts.
func TestManagerReconcilePublishesRecoveredResolutions(t *testing.T) {
	now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
	st := &fakeStore{rows: []domain.NotificationRecord{
		{ID: "ntf_1", SessionID: "mer-1", ProjectID: "mer", Type: domain.NotificationNeedsInput},
		{ID: "ntf_2", SessionID: "mer-2", ProjectID: "mer", Type: domain.NotificationPRMerged},
	}}
	hub := NewHub()
	ch, unsub := hub.Subscribe("")
	defer unsub()
	mgr := New(Deps{Store: st, Publisher: hub, Clock: func() time.Time { return now }})

	if err := mgr.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !st.rows[0].Resolved() || st.rows[1].Resolved() {
		t.Fatalf("rows = %+v", st.rows)
	}
	select {
	case got := <-ch:
		if got.Kind != domain.NotificationResolved || got.Record.ID != "ntf_1" {
			t.Fatalf("published = %+v", got)
		}
	default:
		t.Fatal("expected published resolution")
	}
}
