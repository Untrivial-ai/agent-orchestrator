package notify

import (
	"context"
	"sync"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const subscriberBuffer = 64

type subscription struct {
	projectID domain.ProjectID
	ch        chan domain.NotificationEvent
}

// Hub is an in-process publisher for notification SSE subscribers.
type Hub struct {
	mu     sync.RWMutex
	nextID int
	subs   map[int]subscription
}

// NewHub constructs an empty notification Hub.
func NewHub() *Hub {
	return &Hub{subs: map[int]subscription{}}
}

// Subscribe registers a live notification subscriber. Empty projectID receives all projects.
func (h *Hub) Subscribe(projectID domain.ProjectID) (<-chan domain.NotificationEvent, func()) {
	if h == nil {
		ch := make(chan domain.NotificationEvent)
		close(ch)
		return ch, func() {}
	}
	ch := make(chan domain.NotificationEvent, subscriberBuffer)
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	h.subs[id] = subscription{projectID: projectID, ch: ch}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if sub, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(sub.ch)
		}
		h.mu.Unlock()
	}
}

// Publish pushes one notification event to matching subscribers without blocking lifecycle writes.
func (h *Hub) Publish(_ context.Context, event domain.NotificationEvent) error {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, sub := range h.subs {
		if event.Kind != domain.NotificationCleared && sub.projectID != "" && sub.projectID != event.Record.ProjectID {
			continue
		}
		select {
		case sub.ch <- event:
		default:
		}
	}
	return nil
}
