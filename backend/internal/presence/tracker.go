// Package presence tracks which mobile devices are currently running the app.
//
// Liveness is derived from the phone's existing REST poll rather than from any
// dedicated heartbeat or socket: every authenticated request carries the device's
// install id, and a request is proof the app is running. The mux WebSocket is
// deliberately not used — it is only open while the user is on a terminal screen,
// so it would report "viewing a terminal", not "app is open".
package presence

import (
	"sort"
	"sync"
	"time"
)

// TTL is how long a device stays live after its last request. The phone polls
// every 8s, so two intervals plus slack means one dropped request does not blink
// the dot off, while a backgrounded or killed app goes dark within 20s.
const TTL = 20 * time.Second

// Retention is how long a last-seen timestamp is remembered after the device
// stops being live. It is far longer than TTL on purpose: the roster's fallback
// label ("last seen 3 hours ago") needs the moment the phone actually went
// quiet, which TTL-scoped expiry would have thrown away 20 seconds in. Presence
// is in-memory, so this only ever spans one daemon's lifetime — past it, the
// registry's own LastSeenAt takes over.
const Retention = 7 * 24 * time.Hour

// maxTracked bounds the map regardless of age. The install id arrives in a
// client-supplied header, so a caller cycling ids must not be able to grow
// memory unboundedly between prunes; the oldest entries are evicted first.
const maxTracked = 512

// Tracker records the last time each install id was seen. It holds no goroutines
// and no timers: expiry is computed at read time, so there is nothing to leak and
// nothing to shut down.
type Tracker struct {
	mu   sync.RWMutex
	seen map[string]time.Time

	// Now is the clock, injectable so tests control expiry without sleeping.
	Now func() time.Time
}

// NewTracker returns an empty tracker using the wall clock.
func NewTracker() *Tracker {
	return &Tracker{seen: map[string]time.Time{}, Now: time.Now}
}

func (t *Tracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Touch records that installID was just seen. Empty ids are ignored so requests
// from the desktop or an older mobile build never create phantom entries.
//
// Expired entries are swept here as well as in Live. The install id arrives in a
// client-supplied header, so a caller sending a fresh id per request would
// otherwise grow the map without bound on a daemon whose Devices section is
// never opened — Live, the only other sweeper, would never run.
func (t *Tracker) Touch(installID string) {
	if installID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seen[installID] = t.now()
	// Prune after inserting, so the cap holds once Touch returns rather than
	// leaving room for this one entry to exceed it. The entry just written is the
	// newest, so oldest-first eviction can never discard it.
	t.pruneLocked()
}

// Live returns the set of install ids seen within the TTL. It only filters:
// entries past the TTL are kept so LastSeen can still report when that device
// went quiet. Sweeping is Touch's job.
func (t *Tracker) Live() map[string]bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	cutoff := t.now().Add(-TTL)
	live := make(map[string]bool, len(t.seen))
	for id, at := range t.seen {
		if !at.Before(cutoff) {
			live[id] = true
		}
	}
	return live
}

// LastSeen reports when installID last made a request, and whether the tracker
// still holds that information. The roster prefers it over the registry's stored
// LastSeenAt, which only advances on register/announce and so goes stale during
// a long foreground session.
func (t *Tracker) LastSeen(installID string) (time.Time, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	at, ok := t.seen[installID]
	return at, ok
}

// pruneLocked drops entries past Retention, then evicts oldest-first if the map
// is still over maxTracked. Callers must hold t.mu.
func (t *Tracker) pruneLocked() {
	cutoff := t.now().Add(-Retention)
	for id, at := range t.seen {
		if at.Before(cutoff) {
			delete(t.seen, id)
		}
	}
	if len(t.seen) <= maxTracked {
		return
	}
	type entry struct {
		id string
		at time.Time
	}
	entries := make([]entry, 0, len(t.seen))
	for id, at := range t.seen {
		entries = append(entries, entry{id, at})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].at.Before(entries[j].at) })
	for _, e := range entries[:len(entries)-maxTracked] {
		delete(t.seen, e.id)
	}
}

// Size reports how many entries the tracker is holding. Test-facing: it is the
// only way to observe that expiry actually reclaims memory rather than merely
// hiding entries from Live.
func (t *Tracker) Size() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.seen)
}
