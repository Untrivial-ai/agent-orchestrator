package push

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

// Subscriber is the notification fan-out source the dispatcher listens on,
// satisfied by *notify.Hub. Empty projectID receives all projects.
type Subscriber interface {
	Subscribe(projectID domain.ProjectID) (<-chan domain.NotificationEvent, func())
}

// DeviceStore is the registered-device view the dispatcher needs: enumerate
// targets and prune dead tokens. Satisfied by *mobilebridge.DeviceRegistry.
type DeviceStore interface {
	List() []mobilebridge.PushDevice
	UnregisterToken(token string) error
}

// Sender delivers Expo messages and fetches delivery receipts. Satisfied by
// *ExpoClient.
type Sender interface {
	Send(ctx context.Context, messages []Message) ([]Ticket, error)
	GetReceipts(ctx context.Context, ids []string) (map[string]Receipt, error)
}

// androidChannelID is the single high-importance channel the client registers so
// needs-input notifications actually buzz.
const androidChannelID = "default"

const (
	// receiptDelay is how long after send we wait before a ticket's receipt is
	// worth fetching (Expo needs time to attempt delivery).
	receiptDelay = 15 * time.Minute
	// receiptSweepInterval is how often the sweep runs.
	receiptSweepInterval = 5 * time.Minute
	// receiptMaxAge drops a pending ticket even if never resolved, so the
	// in-memory set can't grow unbounded if receipts stop coming.
	receiptMaxAge = time.Hour
	// maxPendingReceipts caps the in-memory set; oldest are dropped past this.
	maxPendingReceipts = 10000
)

// sentTicket is an accepted ("ok") ticket awaiting its delivery receipt, kept in
// memory only (a daemon restart drops these — acceptable, since a live token is
// resurrected by the client's foreground re-register).
type sentTicket struct {
	id     string
	token  string
	sentAt time.Time
}

// Dispatcher subscribes to the notification hub and, per new notification, sends
// an OS push to every registered device via Expo, pruning tokens Expo reports as
// dead. It is an additive hub subscriber: SSE and the persistence path are
// untouched, and a slow/failing Expo call can never stall a notification insert.
type Dispatcher struct {
	sub     Subscriber
	devices DeviceStore
	sender  Sender
	log     *slog.Logger
	clock   func() time.Time

	mu      sync.Mutex
	pending []sentTicket // accepted tickets awaiting a delivery receipt
}

// NewDispatcher constructs a Dispatcher. A nil logger is tolerated (discarded).
func NewDispatcher(sub Subscriber, devices DeviceStore, sender Sender, log *slog.Logger) *Dispatcher {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Dispatcher{sub: sub, devices: devices, sender: sender, log: log, clock: time.Now}
}

// Run subscribes and dispatches until ctx is cancelled. It blocks, so callers run
// it in a goroutine. A periodic receipt sweep runs on the same goroutine (no
// extra concurrency) to prune tokens that die after Expo accepts the message.
func (d *Dispatcher) Run(ctx context.Context) {
	ch, unsubscribe := d.sub.Subscribe("")
	defer unsubscribe()
	ticker := time.NewTicker(receiptSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			// Only a new notification buzzes a phone. Resolution events exist so
			// open dashboards can drop a row; there is nothing to push about.
			if event.Kind != domain.NotificationCreated {
				continue
			}
			d.dispatch(ctx, event.Record)
		case <-ticker.C:
			d.sweepReceipts(ctx)
		}
	}
}

// dispatch sends one notification record to every registered device and prunes
// any token Expo reports as no longer registered.
func (d *Dispatcher) dispatch(ctx context.Context, rec domain.NotificationRecord) {
	devices := d.devices.List()
	if len(devices) == 0 {
		return
	}
	messages := make([]Message, 0, len(devices))
	for _, dev := range devices {
		// Muted devices stay registered and listed on the desktop but receive
		// nothing. Filtering here (rather than after Send) keeps tickets 1:1 with
		// messages by index, which the pruning loop below depends on.
		if dev.Muted {
			continue
		}
		// A row is a paired phone, not a push registration: it may have no token
		// (permission not granted yet, or a build that can't mint one). Skip it
		// here, before Send, for the same reason muted devices are skipped above —
		// keeping this filter ahead of Send preserves the 1:1 ticket-to-message
		// index correspondence the pruning loop below depends on.
		if dev.Token == "" {
			continue
		}
		messages = append(messages, messageFor(rec, dev.Token))
	}
	if len(messages) == 0 {
		return
	}
	tickets, err := d.sender.Send(ctx, messages)
	if err != nil {
		d.log.Warn("push send failed", "err", err, "notification", rec.ID, "devices", len(messages))
		return
	}
	// Tickets are 1:1 with messages, in order. Prune tokens Expo already knows are
	// dead, and remember accepted tickets so the sweep can check their receipts.
	now := d.clock()
	var accepted []sentTicket
	for i, t := range tickets {
		if i >= len(messages) {
			break
		}
		token := messages[i].To
		if t.IsDeviceNotRegistered() {
			d.prune(token)
			continue
		}
		if t.Status == "ok" && t.ID != "" {
			accepted = append(accepted, sentTicket{id: t.ID, token: token, sentAt: now})
		}
	}
	d.trackAccepted(accepted)
}

// prune removes a dead token from the registry, logging the outcome.
func (d *Dispatcher) prune(token string) {
	if err := d.devices.UnregisterToken(token); err != nil {
		d.log.Warn("prune dead push token failed", "err", err)
	} else {
		d.log.Info("pruned dead push token")
	}
}

// trackAccepted records accepted tickets for later receipt checking, bounding the
// in-memory set (oldest dropped past the cap).
func (d *Dispatcher) trackAccepted(tickets []sentTicket) {
	if len(tickets) == 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending = append(d.pending, tickets...)
	if over := len(d.pending) - maxPendingReceipts; over > 0 {
		d.pending = d.pending[over:]
	}
}

// sweepReceipts fetches receipts for tickets old enough to have one, prunes any
// token reported DeviceNotRegistered, and drops resolved/expired tickets from the
// pending set (D8: catches tokens that die after Expo accepts the message).
func (d *Dispatcher) sweepReceipts(ctx context.Context) {
	now := d.clock()

	// Split pending into "due" (old enough to check or expired) and "keep".
	d.mu.Lock()
	var due, keep []sentTicket
	for _, t := range d.pending {
		if now.Sub(t.sentAt) >= receiptDelay {
			due = append(due, t)
		} else {
			keep = append(keep, t)
		}
	}
	d.pending = keep
	d.mu.Unlock()
	if len(due) == 0 {
		return
	}

	// Expired tickets (no receipt after receiptMaxAge) are dropped, not queried.
	ids := make([]string, 0, len(due))
	tokenOf := make(map[string]string, len(due))
	for _, t := range due {
		if now.Sub(t.sentAt) >= receiptMaxAge {
			continue
		}
		ids = append(ids, t.id)
		tokenOf[t.id] = t.token
	}
	if len(ids) == 0 {
		return
	}

	receipts, err := d.sender.GetReceipts(ctx, ids)
	if err != nil {
		d.log.Warn("fetch push receipts failed", "err", err, "tickets", len(ids))
		return
	}
	for id, r := range receipts {
		if r.IsDeviceNotRegistered() {
			d.prune(tokenOf[id])
		} else if r.Status == "error" {
			d.log.Warn("push delivery error", "err", r.Details.Error, "message", r.Message)
		}
	}
}

// messageFor builds the Expo message for one device from a notification record.
// The data blob carries exactly what the app needs to deep-link on tap and to
// mark the record read; nothing secret beyond the human-readable title/body.
func messageFor(rec domain.NotificationRecord, token string) Message {
	return Message{
		To:        token,
		Title:     rec.Title,
		Body:      rec.Body,
		Sound:     "default",
		Priority:  "high",
		ChannelID: androidChannelID,
		Data: map[string]any{
			"type":           string(rec.Type),
			"sessionId":      string(rec.SessionID),
			"projectId":      string(rec.ProjectID),
			"prUrl":          rec.PRURL,
			"notificationId": rec.ID,
		},
	}
}
