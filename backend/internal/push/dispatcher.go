package push

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"

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
	// pruneAmbiguous prevents pruning from an ID shared by different tokens.
	// The conflict is remembered only while a marked record remains in pending.
	pruneAmbiguous bool
}

// Dispatcher subscribes to the notification hub and, per new notification, sends
// an OS push to every registered device via Expo, pruning tokens Expo reports as
// dead. It is an additive hub subscriber: SSE and the persistence path are
// untouched, and a slow/failing Expo call can never stall a notification insert.
type Dispatcher struct {
	sub     Subscriber
	devices DeviceStore
	sender  Sender
	hostID  string
	log     *slog.Logger
	clock   func() time.Time

	mu      sync.Mutex
	pending []sentTicket // accepted tickets awaiting a delivery receipt
}

// NewDispatcher constructs a Dispatcher. A nil logger is tolerated (discarded).
func NewDispatcher(sub Subscriber, devices DeviceStore, sender Sender, hostID string, log *slog.Logger) *Dispatcher {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Dispatcher{sub: sub, devices: devices, sender: sender, hostID: hostID, log: log, clock: time.Now}
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
	// Without a host ID the phone cannot safely route a session-scoped push.
	if d.hostID == "" {
		return
	}
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
		messages = append(messages, messageFor(rec, dev.Token, d.hostID, dev.HostName))
	}
	if len(messages) == 0 {
		return
	}
	tickets, err := d.sender.Send(ctx, messages)
	if err != nil {
		d.log.Warn("push send failed", "err", err, "notification", rec.ID, "devices", len(messages))
	}
	// Even on error, earlier successful batches return a prefix of tickets in
	// message order. Prune known dead tokens and track accepted deliveries once.
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
	// Mark conflicting IDs before the cap can discard either record. An ID stays
	// ambiguous while a marked record remains in pending; after all are removed,
	// a new ticket reusing the ID is trusted again.
	tokenOf := make(map[string]string, len(d.pending))
	for _, t := range d.pending {
		token, exists := tokenOf[t.id]
		if !exists {
			tokenOf[t.id] = t.token
		}
		if t.pruneAmbiguous || (exists && token != t.token) {
			tokenOf[t.id] = ""
		}
	}
	for i := range d.pending {
		if tokenOf[d.pending[i].id] == "" {
			d.pending[i].pruneAmbiguous = true
		}
	}
	if over := len(d.pending) - maxPendingReceipts; over > 0 {
		d.pending = d.pending[over:]
	}
}

// sweepReceipts fetches receipts for tickets old enough to have one, prunes any
// token reported DeviceNotRegistered, and drops resolved/expired tickets from the
// pending set (D8: catches tokens that die after Expo accepts the message).
func (d *Dispatcher) sweepReceipts(ctx context.Context) {
	now := d.clock()

	// Leave due tickets in place while fetching so retries preserve their age
	// and position under the cap. Only records in this snapshot can be resolved.
	d.mu.Lock()
	due := make(map[sentTicket]struct{})
	var ids []string
	seen := make(map[string]struct{})
	for _, t := range d.pending {
		age := now.Sub(t.sentAt)
		if t.id == "" || age < receiptDelay || age >= receiptMaxAge {
			continue
		}
		due[t] = struct{}{}
		if _, exists := seen[t.id]; !exists {
			ids = append(ids, t.id)
			seen[t.id] = struct{}{}
		}
	}
	d.mu.Unlock()

	var receipts map[string]Receipt
	if len(ids) > 0 {
		var err error
		receipts, err = d.sender.GetReceipts(ctx, ids)
		if err != nil {
			d.log.Warn("fetch push receipts failed", "err", err, "tickets", len(ids))
		}
	}

	// Receipt fetching can cross the expiry boundary. Process partial responses
	// even on error, keeping omitted or nonterminal results at their original age.
	now = d.clock()
	d.mu.Lock()
	keep := d.pending[:0]
	var deadTokens []string
	pruned := make(map[string]struct{})
	for _, t := range d.pending {
		if now.Sub(t.sentAt) >= receiptMaxAge {
			continue
		}
		r, found := receipts[t.id]
		_, queried := due[t]
		if !queried || !found || (r.Status != "ok" && r.Status != "error") {
			keep = append(keep, t)
			continue
		}
		if r.IsDeviceNotRegistered() {
			token := t.token
			if _, seen := pruned[token]; !t.pruneAmbiguous && token != "" && !seen {
				deadTokens = append(deadTokens, token)
				pruned[token] = struct{}{}
			}
		} else if r.Status == "error" {
			d.log.Warn("push delivery error", "err", r.Details.Error, "message", r.Message)
		}
	}
	clear(d.pending[len(keep):])
	d.pending = keep
	d.mu.Unlock()
	for _, token := range deadTokens {
		d.prune(token)
	}
}

// messageFor builds the Expo message for one device from a notification record.
// The data blob carries exactly what the app needs to deep-link on tap and to
// mark the record read; nothing secret beyond the human-readable title/body.
func messageFor(rec domain.NotificationRecord, token, hostID, hostName string) Message {
	// Device-provided labels appear in an OS banner: keep them one line and
	// prevent control/format characters from altering what the user sees.
	label := strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, hostName)), " ")
	if label == "" {
		label = hostID
		if len(label) > 10 {
			label = label[:10]
		}
	}
	if runes := []rune(label); len(runes) > 40 {
		label = string(runes[:40]) + "…"
	}
	return Message{
		To:        token,
		Title:     fmt.Sprintf("%s · %s", label, rec.Title),
		Body:      rec.Body,
		Sound:     "default",
		Priority:  "high",
		ChannelID: androidChannelID,
		Data: map[string]any{
			"type":           string(rec.Type),
			"hostId":         hostID,
			"sessionId":      string(rec.SessionID),
			"projectId":      string(rec.ProjectID),
			"prUrl":          rec.PRURL,
			"notificationId": rec.ID,
		},
	}
}
