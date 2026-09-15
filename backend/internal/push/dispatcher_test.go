package push

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

type fakeSubscriber struct {
	ch chan domain.NotificationEvent
}

func (f *fakeSubscriber) Subscribe(domain.ProjectID) (<-chan domain.NotificationEvent, func()) {
	return f.ch, func() {}
}

func created(rec domain.NotificationRecord) domain.NotificationEvent {
	return domain.NotificationEvent{Kind: domain.NotificationCreated, Record: rec}
}

type fakeDeviceStore struct {
	mu      sync.Mutex
	devices []mobilebridge.PushDevice
	deleted []string
}

func (f *fakeDeviceStore) List() []mobilebridge.PushDevice {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mobilebridge.PushDevice(nil), f.devices...)
}

func (f *fakeDeviceStore) UnregisterToken(token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, token)
	return nil
}

type fakeSender struct {
	mu          sync.Mutex
	gotMsgs     []Message
	tickets     []Ticket
	sendErr     error
	sentCond    *sync.Cond
	sent        bool
	gotIDs      []string           // ids passed to GetReceipts
	receipts    map[string]Receipt // returned by GetReceipts
	receiptErr  error
	receiptHook func()
}

func newFakeSender(tickets []Ticket) *fakeSender {
	f := &fakeSender{tickets: tickets}
	f.sentCond = sync.NewCond(&f.mu)
	return f
}

func (f *fakeSender) Send(_ context.Context, messages []Message) ([]Ticket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotMsgs = append(f.gotMsgs, messages...)
	f.sent = true
	f.sentCond.Broadcast()
	return f.tickets, f.sendErr
}

func (f *fakeSender) GetReceipts(_ context.Context, ids []string) (map[string]Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotIDs = append(f.gotIDs, ids...)
	if f.receiptHook != nil {
		f.receiptHook()
	}
	return f.receipts, f.receiptErr
}

func (f *fakeSender) waitSent(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		f.mu.Lock()
		for !f.sent {
			f.sentCond.Wait()
		}
		f.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for send")
	}
}

func TestDispatcherSendsToAllDevicesWithDataBlob(t *testing.T) {
	sub := &fakeSubscriber{ch: make(chan domain.NotificationEvent, 1)}
	store := &fakeDeviceStore{devices: []mobilebridge.PushDevice{
		{Token: "ExponentPushToken[a]"},
		{Token: "ExponentPushToken[b]"},
	}}
	sender := newFakeSender([]Ticket{{Status: "ok"}, {Status: "ok"}})
	d := NewDispatcher(sub, store, sender, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	sub.ch <- created(domain.NotificationRecord{
		ID:        "ntf_1",
		SessionID: "sess_9",
		ProjectID: "proj_7",
		PRURL:     "https://example.com/pr/3",
		Type:      domain.NotificationNeedsInput,
		Title:     "sess needs input",
		Body:      "The agent is waiting for your response.",
	})
	sender.waitSent(t)

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.gotMsgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(sender.gotMsgs))
	}
	m := sender.gotMsgs[0]
	if m.Title != "sess needs input" || m.Body == "" {
		t.Fatalf("message copy = %+v", m)
	}
	if m.Priority != "high" || m.Sound != "default" || m.ChannelID != "default" {
		t.Fatalf("channel/priority/sound = %+v", m)
	}
	if m.Data["type"] != "needs_input" || m.Data["sessionId"] != "sess_9" ||
		m.Data["projectId"] != "proj_7" || m.Data["prUrl"] != "https://example.com/pr/3" ||
		m.Data["notificationId"] != "ntf_1" {
		t.Fatalf("data blob = %+v", m.Data)
	}
}

func TestDispatcherPrunesDeadTokens(t *testing.T) {
	sub := &fakeSubscriber{ch: make(chan domain.NotificationEvent, 1)}
	store := &fakeDeviceStore{devices: []mobilebridge.PushDevice{
		{Token: "ExponentPushToken[live]"},
		{Token: "ExponentPushToken[dead]"},
	}}
	// Second ticket reports the token is gone.
	dead := Ticket{Status: "error"}
	dead.Details.Error = "DeviceNotRegistered"
	sender := newFakeSender([]Ticket{{Status: "ok"}, dead})
	d := NewDispatcher(sub, store, sender, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	sub.ch <- created(domain.NotificationRecord{ID: "ntf_1", Type: domain.NotificationNeedsInput, Title: "t", Body: "b"})
	sender.waitSent(t)

	// Give dispatch() a beat to finish the prune after Send returned.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		n := len(store.deleted)
		store.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.deleted) != 1 || store.deleted[0] != "ExponentPushToken[dead]" {
		t.Fatalf("deleted = %v, want [ExponentPushToken[dead]]", store.deleted)
	}
}

func TestDispatcherNoDevicesIsNoop(t *testing.T) {
	sub := &fakeSubscriber{ch: make(chan domain.NotificationEvent, 1)}
	store := &fakeDeviceStore{}
	sender := newFakeSender(nil)
	d := NewDispatcher(sub, store, sender, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	sub.ch <- created(domain.NotificationRecord{ID: "ntf_1", Type: domain.NotificationNeedsInput, Title: "t", Body: "b"})
	// No devices → sender must never be called. Give the loop a moment.
	time.Sleep(100 * time.Millisecond)
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.sent {
		t.Fatal("sender was called despite no registered devices")
	}
}

// Resolution events exist so open dashboards can drop a row. Nothing new
// happened for the user, so a phone must not buzz for one.
func TestDispatcherIgnoresResolvedEvents(t *testing.T) {
	sub := &fakeSubscriber{ch: make(chan domain.NotificationEvent, 1)}
	store := &fakeDeviceStore{devices: []mobilebridge.PushDevice{{Token: "ExponentPushToken[a]"}}}
	sender := newFakeSender([]Ticket{{Status: "ok"}})
	d := NewDispatcher(sub, store, sender, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	sub.ch <- domain.NotificationEvent{
		Kind:   domain.NotificationResolved,
		Record: domain.NotificationRecord{ID: "ntf_1", Type: domain.NotificationNeedsInput, Title: "t", Body: "b"},
	}
	time.Sleep(100 * time.Millisecond)
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.sent {
		t.Fatal("sender was called for a resolution event")
	}
}

func TestDispatcherSweepPrunesOnReceipt(t *testing.T) {
	store := &fakeDeviceStore{devices: []mobilebridge.PushDevice{{Token: "ExponentPushToken[dead]"}}}
	sender := newFakeSender(nil)
	dead := Receipt{Status: "error"}
	dead.Details.Error = "DeviceNotRegistered"
	sender.receipts = map[string]Receipt{"tk1": dead}
	d := NewDispatcher(&fakeSubscriber{ch: make(chan domain.NotificationEvent)}, store, sender, nil)

	base := time.Now()
	d.clock = func() time.Time { return base }
	// A ticket sent 16 minutes ago is due for a receipt check.
	d.trackAccepted([]sentTicket{{id: "tk1", token: "ExponentPushToken[dead]", sentAt: base.Add(-16 * time.Minute)}})
	d.sweepReceipts(context.Background())

	sender.mu.Lock()
	queried := append([]string(nil), sender.gotIDs...)
	sender.mu.Unlock()
	if len(queried) != 1 || queried[0] != "tk1" {
		t.Fatalf("queried ids = %v, want [tk1]", queried)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.deleted) != 1 || store.deleted[0] != "ExponentPushToken[dead]" {
		t.Fatalf("deleted = %v, want [ExponentPushToken[dead]]", store.deleted)
	}
}

func TestDispatchSkipsMutedDevices(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeDeviceStore{devices: []mobilebridge.PushDevice{
		{InstallID: "i1", Token: "ExponentPushToken[live]", CreatedAt: now, LastSeenAt: now},
		{InstallID: "i2", Token: "ExponentPushToken[muted]", Muted: true, CreatedAt: now, LastSeenAt: now},
	}}
	sender := newFakeSender([]Ticket{{Status: "ok"}})
	d := NewDispatcher(nil, store, sender, slog.New(slog.NewTextHandler(io.Discard, nil)))

	d.dispatch(context.Background(), domain.NotificationRecord{ID: "n1", Title: "hi"})

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.gotMsgs) != 1 {
		t.Fatalf("messages = %d, want 1 (the muted device must be skipped)", len(sender.gotMsgs))
	}
	if sender.gotMsgs[0].To != "ExponentPushToken[live]" {
		t.Fatalf("sent to %q, want the unmuted device", sender.gotMsgs[0].To)
	}
}

// TestDispatchAllMutedNeverCallsSend pins the early return in dispatch: when
// every registered device is muted, the filtered messages slice is empty and
// Send must never be called at all (not called-with-empty-slice, which would
// draw a 400 from Expo). TestDispatchSkipsMutedDevices only covers the mixed
// case, so a regression that dropped the `len(messages) == 0` guard would slip
// past it silently.
func TestDispatchAllMutedNeverCallsSend(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeDeviceStore{devices: []mobilebridge.PushDevice{
		{InstallID: "i1", Token: "ExponentPushToken[muted1]", Muted: true, CreatedAt: now, LastSeenAt: now},
		{InstallID: "i2", Token: "ExponentPushToken[muted2]", Muted: true, CreatedAt: now, LastSeenAt: now},
	}}
	sender := newFakeSender(nil)
	d := NewDispatcher(nil, store, sender, slog.New(slog.NewTextHandler(io.Discard, nil)))

	d.dispatch(context.Background(), domain.NotificationRecord{ID: "n1", Title: "hi"})

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.sent {
		t.Fatal("Send was called despite every device being muted")
	}
}

// TestDispatchSkipsDevicesWithoutToken pins the third dispatcher-side guard: a
// row can now represent a paired phone that never minted a push token (no
// permission granted, or a build that can't mint one). Sending to an empty
// token would be a wasted/erroring Expo call, so those rows must be filtered
// out exactly like muted ones, and the survivor must still be the tokened one.
func TestDispatchSkipsDevicesWithoutToken(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeDeviceStore{devices: []mobilebridge.PushDevice{
		{InstallID: "i1", Token: "", CreatedAt: now, LastSeenAt: now},
		{InstallID: "i2", Token: "ExponentPushToken[live]", CreatedAt: now, LastSeenAt: now},
	}}
	sender := newFakeSender([]Ticket{{Status: "ok"}})
	d := NewDispatcher(nil, store, sender, slog.New(slog.NewTextHandler(io.Discard, nil)))

	d.dispatch(context.Background(), domain.NotificationRecord{ID: "n1", Title: "hi"})

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.gotMsgs) != 1 {
		t.Fatalf("messages = %d, want 1 (the tokenless device must be skipped)", len(sender.gotMsgs))
	}
	if sender.gotMsgs[0].To != "ExponentPushToken[live]" {
		t.Fatalf("sent to %q, want the tokened device", sender.gotMsgs[0].To)
	}
}

func TestDispatcherSweepSkipsFreshAndDropsExpired(t *testing.T) {
	store := &fakeDeviceStore{}
	sender := newFakeSender(nil)
	d := NewDispatcher(&fakeSubscriber{ch: make(chan domain.NotificationEvent)}, store, sender, nil)

	base := time.Now()
	d.clock = func() time.Time { return base }
	d.trackAccepted([]sentTicket{
		{id: "fresh", token: "ExponentPushToken[a]", sentAt: base.Add(-1 * time.Minute)}, // too new to check
		{id: "expired", token: "ExponentPushToken[b]", sentAt: base.Add(-2 * time.Hour)}, // past max age → dropped
	})
	d.sweepReceipts(context.Background())

	sender.mu.Lock()
	nQueried := len(sender.gotIDs)
	sender.mu.Unlock()
	if nQueried != 0 {
		t.Fatalf("queried %d ids, want 0 (fresh kept, expired dropped un-queried)", nQueried)
	}
	d.mu.Lock()
	pending := len(d.pending)
	d.mu.Unlock()
	if pending != 1 {
		t.Fatalf("pending = %d, want 1 (only the fresh ticket remains)", pending)
	}
}

func TestDispatcherPartialSendTracksAcceptedPrefix(t *testing.T) {
	now := time.Unix(10000, 0)
	store := &fakeDeviceStore{devices: []mobilebridge.PushDevice{
		{Token: "muted", Muted: true},
		{Token: ""},
		{Token: "accepted"},
		{Token: "dead"},
		{Token: "unsent"},
	}}
	dead := Ticket{Status: "error"}
	dead.Details.Error = "DeviceNotRegistered"
	sender := newFakeSender([]Ticket{{Status: "ok", ID: "accepted-ticket"}, dead})
	sender.sendErr = errors.New("later batch failed")
	d := NewDispatcher(nil, store, sender, nil)
	d.clock = func() time.Time { return now }

	d.dispatch(t.Context(), domain.NotificationRecord{ID: "notification"})
	want := []sentTicket{{id: "accepted-ticket", token: "accepted", sentAt: now}}
	if !slices.Equal(d.pending, want) {
		t.Errorf("pending = %+v, want %+v", d.pending, want)
	}
	if !slices.Equal(store.deleted, []string{"dead"}) {
		t.Errorf("pruned = %v, want [dead]", store.deleted)
	}
	if len(sender.gotMsgs) != 3 || sender.gotMsgs[0].To != "accepted" || sender.gotMsgs[1].To != "dead" {
		t.Errorf("messages = %+v, want accepted/dead/unsent once", sender.gotMsgs)
	}

	now = now.Add(receiptDelay)
	receipt := Receipt{Status: "error"}
	receipt.Details.Error = "DeviceNotRegistered"
	sender.receipts = map[string]Receipt{"accepted-ticket": receipt}
	d.sweepReceipts(t.Context())
	if !slices.Equal(store.deleted, []string{"dead", "accepted"}) || len(d.pending) != 0 {
		t.Errorf("receipt tracking: pruned = %v, pending = %+v", store.deleted, d.pending)
	}
	if len(sender.gotMsgs) != 3 {
		t.Error("receipt sweep resent messages")
	}
}

func TestDispatcherReceiptRetryPreservesPartialResults(t *testing.T) {
	dead := Receipt{Status: "error"}
	dead.Details.Error = "DeviceNotRegistered"
	for _, tt := range []struct {
		name       string
		receipts   map[string]Receipt
		err        error
		keepFirst  bool
		pruneFirst bool
	}{
		{name: "transport error", err: errors.New("offline"), keepFirst: true},
		{name: "omitted receipt", receipts: map[string]Receipt{"first": {Status: "ok"}}},
		{name: "partial error", receipts: map[string]Receipt{"first": dead}, err: errors.New("later batch failed"), pruneFirst: true},
		{name: "partial success", receipts: map[string]Receipt{"first": dead}, pruneFirst: true},
		{name: "unknown status", receipts: map[string]Receipt{"first": {Status: "ok"}, "retry": {}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(10000, 0)
			store := &fakeDeviceStore{}
			sender := newFakeSender(nil)
			sender.receipts, sender.receiptErr = tt.receipts, tt.err
			d := NewDispatcher(nil, store, sender, nil)
			d.clock = func() time.Time { return now }
			first := sentTicket{id: "first", token: "first-token", sentAt: now.Add(-20 * time.Minute)}
			retry := sentTicket{id: "retry", token: "retry-token", sentAt: now.Add(-16 * time.Minute)}
			d.trackAccepted([]sentTicket{first, retry})

			d.sweepReceipts(t.Context())
			wantPending := []sentTicket{retry}
			if tt.keepFirst {
				wantPending = []sentTicket{first, retry}
			}
			if !slices.Equal(d.pending, wantPending) {
				t.Errorf("pending = %+v, want original tickets %+v", d.pending, wantPending)
			}
			var wantPruned []string
			if tt.pruneFirst {
				wantPruned = []string{"first-token"}
			}
			if !slices.Equal(store.deleted, wantPruned) {
				t.Errorf("pruned = %v, want %v", store.deleted, wantPruned)
			}

			now = now.Add(receiptSweepInterval)
			sender.receipts = map[string]Receipt{"first": {Status: "ok"}, "retry": dead}
			sender.receiptErr = nil
			sender.gotIDs = nil
			d.sweepReceipts(t.Context())
			if len(d.pending) != 0 {
				t.Errorf("resolved tickets still pending: %+v", d.pending)
			}
			wantPruned = append(wantPruned, "retry-token")
			if !slices.Equal(store.deleted, wantPruned) {
				t.Errorf("pruned after retry = %v, want %v", store.deleted, wantPruned)
			}
			wantIDs := []string{"retry"}
			if tt.keepFirst {
				wantIDs = []string{"first", "retry"}
			}
			if !slices.Equal(sender.gotIDs, wantIDs) {
				t.Errorf("retried IDs = %v, want %v", sender.gotIDs, wantIDs)
			}
			d.sweepReceipts(t.Context())
			if !slices.Equal(sender.gotIDs, wantIDs) || !slices.Equal(store.deleted, wantPruned) {
				t.Error("resolved tickets were processed again")
			}
		})
	}
}

func TestDispatcherReceiptExpiryUsesOriginalSendTime(t *testing.T) {
	now := time.Unix(10000, 0)
	sender := newFakeSender(nil)
	d := NewDispatcher(nil, &fakeDeviceStore{}, sender, nil)
	d.clock = func() time.Time { return now }
	original := sentTicket{id: "retry", token: "token", sentAt: now.Add(-receiptDelay)}
	d.trackAccepted([]sentTicket{original})
	d.sweepReceipts(t.Context())
	if !slices.Equal(d.pending, []sentTicket{original}) {
		t.Errorf("missing receipt lost or changed original ticket: %+v", d.pending)
	}
	now = original.sentAt.Add(receiptMaxAge)
	sender.gotIDs = nil
	d.sweepReceipts(t.Context())
	if len(d.pending) != 0 || len(sender.gotIDs) != 0 {
		t.Errorf("expired ticket retained or queried: pending = %+v, queried = %v", d.pending, sender.gotIDs)
	}
}

func TestDispatcherReceiptExpiresDuringFetch(t *testing.T) {
	for _, response := range []string{"missing", "error", "dead"} {
		t.Run(response, func(t *testing.T) {
			now := time.Unix(10000, 0)
			store := &fakeDeviceStore{}
			sender := newFakeSender(nil)
			d := NewDispatcher(nil, store, sender, nil)
			d.clock = func() time.Time { return now }
			d.trackAccepted([]sentTicket{{id: "expires", token: "token", sentAt: now.Add(-receiptMaxAge + time.Minute)}})
			sender.receiptHook = func() { now = now.Add(time.Minute) }
			if response == "error" {
				sender.receiptErr = errors.New("offline")
			}
			if response == "dead" {
				dead := Receipt{Status: "error"}
				dead.Details.Error = "DeviceNotRegistered"
				sender.receipts = map[string]Receipt{"expires": dead}
			}
			d.sweepReceipts(t.Context())
			if len(d.pending) != 0 || len(store.deleted) != 0 {
				t.Errorf("expired during fetch: pending = %+v, pruned = %v", d.pending, store.deleted)
			}
		})
	}
}

func TestDispatcherReceiptRetriesPreserveCapacityOrder(t *testing.T) {
	for _, duringFetch := range []bool{false, true} {
		t.Run(fmt.Sprintf("new ticket during fetch %t", duringFetch), func(t *testing.T) {
			now := time.Unix(10000, 0)
			sender := newFakeSender(nil)
			d := NewDispatcher(nil, &fakeDeviceStore{}, sender, nil)
			d.clock = func() time.Time { return now }
			tickets := []sentTicket{{id: "old-retry", token: "old-token", sentAt: now.Add(-receiptDelay)}}
			for i := 1; i < maxPendingReceipts; i++ {
				tickets = append(tickets, sentTicket{id: fmt.Sprintf("fresh-%d", i), token: "fresh-token", sentAt: now})
			}
			d.trackAccepted(tickets)
			newest := sentTicket{id: "newest", token: "newest-token", sentAt: now}
			if duringFetch {
				sender.receiptHook = func() { d.trackAccepted([]sentTicket{newest}) }
			}
			d.sweepReceipts(t.Context())
			if !duringFetch {
				if len(d.pending) != maxPendingReceipts || d.pending[0].id != "old-retry" {
					t.Errorf("sweep lost or reordered the oldest unresolved ticket; pending count = %d", len(d.pending))
				}
				d.trackAccepted([]sentTicket{newest})
			}
			if len(d.pending) != maxPendingReceipts {
				t.Fatalf("pending = %d, want cap %d", len(d.pending), maxPendingReceipts)
			}
			if d.pending[0].id != "fresh-1" || d.pending[len(d.pending)-1].id != "newest" {
				t.Errorf("cap evicted a newer ticket: first = %s, last = %s", d.pending[0].id, d.pending[len(d.pending)-1].id)
			}
		})
	}
}

func TestDispatcherReceiptDuplicatesDoNotMisattributeTokens(t *testing.T) {
	for _, tt := range []struct {
		name   string
		token  string
		fresh  bool
		pruned []string
	}{
		{name: "same token", token: "first-token", pruned: []string{"first-token"}},
		{name: "different tokens", token: "other-token"},
		{name: "fresh collision", token: "other-token", fresh: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(10000, 0)
			store := &fakeDeviceStore{}
			sender := newFakeSender(nil)
			dead := Receipt{Status: "error"}
			dead.Details.Error = "DeviceNotRegistered"
			sender.receipts = map[string]Receipt{"duplicate": dead, "unsolicited": dead, "": dead}
			d := NewDispatcher(nil, store, sender, nil)
			d.clock = func() time.Time { return now }
			second := sentTicket{id: "duplicate", token: tt.token, sentAt: now.Add(-receiptDelay)}
			if tt.fresh {
				second.sentAt = now
			}
			d.trackAccepted([]sentTicket{
				{id: "duplicate", token: "first-token", sentAt: now.Add(-receiptDelay)}, second,
			})
			d.sweepReceipts(t.Context())
			if !slices.Equal(sender.gotIDs, []string{"duplicate"}) {
				t.Errorf("queried = %v, want unique [duplicate]", sender.gotIDs)
			}
			if !slices.Equal(store.deleted, tt.pruned) {
				t.Errorf("pruned = %v, want %v", store.deleted, tt.pruned)
			}
			var wantPending []sentTicket
			if tt.fresh {
				wantPending = []sentTicket{second}
			}
			if len(d.pending) != len(wantPending) || (tt.fresh &&
				(d.pending[0].id != second.id || d.pending[0].token != second.token || d.pending[0].sentAt != second.sentAt)) {
				t.Errorf("pending = %+v, want %+v", d.pending, wantPending)
			}
		})
	}
}

func TestDispatcherReceiptAmbiguitySurvivesConflictingTicketExpiry(t *testing.T) {
	now := time.Unix(10000, 0)
	store := &fakeDeviceStore{}
	sender := newFakeSender(nil)
	d := NewDispatcher(nil, store, sender, nil)
	d.clock = func() time.Time { return now }
	d.trackAccepted([]sentTicket{
		{id: "duplicate", token: "old-token", sentAt: now.Add(-receiptMaxAge + time.Minute)},
		{id: "duplicate", token: "newer-token", sentAt: now.Add(-receiptDelay)},
	})
	d.sweepReceipts(t.Context()) // Neither conflicting record has a receipt yet.
	now = now.Add(time.Minute)
	d.sweepReceipts(t.Context()) // The older conflicting record expires.
	if len(d.pending) != 1 || d.pending[0].token != "newer-token" {
		t.Fatalf("expected only the unexpired conflicting ticket: %+v", d.pending)
	}
	dead := Receipt{Status: "error"}
	dead.Details.Error = "DeviceNotRegistered"
	sender.receipts = map[string]Receipt{"duplicate": dead}
	d.sweepReceipts(t.Context())
	if len(store.deleted) != 0 || len(d.pending) != 0 {
		t.Errorf("ambiguous receipt after expiry: pruned = %v, pending = %+v", store.deleted, d.pending)
	}
}

func TestDispatcherReceiptIgnoresUnqueriedPendingTickets(t *testing.T) {
	for _, duringFetch := range []bool{false, true} {
		t.Run(fmt.Sprintf("registered during fetch %t", duringFetch), func(t *testing.T) {
			now := time.Unix(10000, 0)
			store := &fakeDeviceStore{}
			sender := newFakeSender(nil)
			d := NewDispatcher(nil, store, sender, nil)
			d.clock = func() time.Time { return now }
			d.trackAccepted([]sentTicket{{id: "queried", token: "queried-token", sentAt: now.Add(-receiptDelay)}})
			fresh := sentTicket{id: "unqueried", token: "fresh-token", sentAt: now}
			if duringFetch {
				sender.receiptHook = func() { d.trackAccepted([]sentTicket{fresh}) }
			} else {
				d.trackAccepted([]sentTicket{fresh})
			}
			dead := Receipt{Status: "error"}
			dead.Details.Error = "DeviceNotRegistered"
			sender.receipts = map[string]Receipt{"queried": {Status: "ok"}, "unqueried": dead}
			d.sweepReceipts(t.Context())
			if !slices.Equal(sender.gotIDs, []string{"queried"}) || !slices.Equal(d.pending, []sentTicket{fresh}) || len(store.deleted) != 0 {
				t.Errorf("unqueried receipt affected pending: queried = %v, pending = %+v, pruned = %v", sender.gotIDs, d.pending, store.deleted)
			}
		})
	}
}

func TestDispatcherReceiptAmbiguitySurvivesCapacityEviction(t *testing.T) {
	now := time.Unix(10000, 0)
	store := &fakeDeviceStore{}
	sender := newFakeSender(nil)
	d := NewDispatcher(nil, store, sender, nil)
	d.clock = func() time.Time { return now }
	tickets := []sentTicket{{id: "duplicate", token: "old-token", sentAt: now.Add(-receiptDelay)}}
	for i := 1; i < maxPendingReceipts; i++ {
		tickets = append(tickets, sentTicket{id: fmt.Sprintf("fresh-%d", i), token: "fresh-token", sentAt: now})
	}
	d.trackAccepted(tickets)
	// The old conflicting record is evicted before the first receipt sweep.
	d.trackAccepted([]sentTicket{{id: "duplicate", token: "newer-token", sentAt: now}})
	now = now.Add(receiptDelay)
	dead := Receipt{Status: "error"}
	dead.Details.Error = "DeviceNotRegistered"
	sender.receipts = map[string]Receipt{"duplicate": dead}
	d.sweepReceipts(t.Context())
	if len(store.deleted) != 0 {
		t.Errorf("ambiguous receipt after cap eviction pruned %q", store.deleted)
	}
	if len(d.pending) != maxPendingReceipts-1 {
		t.Errorf("pending = %d, want %d unresolved fresh tickets", len(d.pending), maxPendingReceipts-1)
	}
}
