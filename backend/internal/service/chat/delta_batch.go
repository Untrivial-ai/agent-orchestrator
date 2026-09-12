package chat

import (
	"iter"
	"reflect"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	deltaFlushInterval = 50 * time.Millisecond
	deltaMaxBytes      = 16 * 1024
)

// coalesceChatDeltas keeps transport fragmentation out of the persistence path.
// Only adjacent, anonymous message deltas with identical metadata are combined:
// their text remains replayable, while native event IDs retain deduplication.
// Collection runs on the controller goroutine, outside any store transaction.
func coalesceChatDeltas(events <-chan sequencedChatEvent) iter.Seq[sequencedChatEvent] {
	return func(yield func(sequencedChatEvent) bool) {
		var pending *sequencedChatEvent
		var deadline <-chan time.Time
		timer := time.NewTimer(deltaFlushInterval)
		timer.Stop()
		defer timer.Stop()
		flush := func() bool {
			if pending == nil {
				return true
			}
			event := *pending
			pending = nil
			timer.Stop()
			deadline = nil
			return yield(event)
		}
		for {
			// A continuously ready provider must not postpone the first chunk's
			// deadline. Never reset the timer when another chunk joins a batch.
			select {
			case <-deadline:
				if !flush() {
					return
				}
			default:
			}
			select {
			case <-deadline:
				if !flush() {
					return
				}
			case event, ok := <-events:
				if !ok {
					flush()
					return
				}
				if pending != nil && (!sameMessageDelta(pending.ChatEvent, event.ChatEvent) || len(pending.Delta)+len(event.Delta) > deltaMaxBytes) {
					if !flush() {
						return
					}
				}
				if event.Kind != ports.ChatEventMessageDelta || event.ProviderEventID != "" || event.ProviderItemID == "" || event.Err != nil || len(event.Delta) >= deltaMaxBytes {
					if !yield(event) {
						return
					}
					continue
				}
				if pending == nil {
					pending = &event
					timer.Reset(deltaFlushInterval)
					deadline = timer.C
				} else {
					pending.Delta += event.Delta
					pending.sequence = event.sequence
				}
			}
		}
	}
}

func sameMessageDelta(a, b ports.ChatEvent) bool {
	if b.Kind != ports.ChatEventMessageDelta || b.ProviderEventID != "" || a.Err != nil || b.Err != nil {
		return false
	}
	// Comparing the whole envelope preserves optional metadata and keeps new
	// event fields from silently disappearing when a driver starts using them.
	a.Delta, b.Delta = "", ""
	return reflect.DeepEqual(a, b) //nolint:govet // Both Err fields are nil; compare the remaining metadata exactly.
}
