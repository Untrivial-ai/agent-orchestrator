package acp

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// idleActivityTimeout bounds how long an ACP turn may go with no provider signal
// before AO surfaces a non-terminal "waiting on the agent" row. Native agents
// differ in how they behave when a connection drops mid-turn: some return a
// terminal error, but others (observed with OpenCode) leave the prompt call open
// and emit nothing, so AO's turn stays Working with no explanation. This bound
// makes that stall visible without cancelling the turn, because a slow-but-legit
// turn also streams no updates while the agent is thinking. It is a variable so
// the watchdog can be exercised without real-time delays in tests.
var idleActivityTimeout = 3 * time.Minute

// idleStallItemID is the stable provider item id for a turn's idle-stall row so
// repeated evaluations collapse onto a single activity instead of stacking rows.
func idleStallItemID(turnID string) string {
	if turnID == "" {
		return "acp-idle"
	}
	return "acp-idle:" + turnID
}

// noteActivity records that the provider produced a signal on the active turn. If
// a stall is currently surfaced it also wakes the watchdog so the row settles as
// soon as output resumes, rather than lingering until the timer next fires.
func (c *conversation) noteActivity() {
	c.lastActivityNanos.Store(time.Now().UnixNano())
	if !c.stallOpen.Load() {
		return
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// surfaceIdleStall emits the non-terminal "waiting" row exactly once for the given
// turn. Marking and emitting happen under idleMu so settlement (which also takes
// idleMu) cannot interleave between them; that guarantees the started event is never
// stranded after a completed one. It refuses once the turn is no longer active or has
// begun settling, so a tick in flight cannot re-surface a stall finishPrompt settled.
func (c *conversation) surfaceIdleStall(turnID string, idle time.Duration) {
	c.idleMu.Lock()
	defer c.idleMu.Unlock()
	c.mu.Lock()
	ok := !c.idleStalled && c.activeTurn == turnID && c.settlingTurn != turnID
	if ok {
		c.idleStalled = true
	}
	c.mu.Unlock()
	if !ok {
		return
	}
	c.stallOpen.Store(true)
	c.emitIdleStall(turnID, idle)
}

// settleIdleStallIfOpen closes a surfaced stall with the given terminal-for-the-row
// status, doing nothing when no stall is open. It takes idleMu so it is ordered
// against surfaceIdleStall: whether it runs before or after a concurrent surface, the
// completed event never precedes the started one and the row is never settled twice.
func (c *conversation) settleIdleStallIfOpen(turnID string, status domain.ActivityStatus) {
	c.idleMu.Lock()
	defer c.idleMu.Unlock()
	c.mu.Lock()
	open := c.idleStalled
	if open {
		c.idleStalled = false
	}
	c.mu.Unlock()
	if !open {
		return
	}
	c.stallOpen.Store(false)
	c.settleIdleStall(turnID, status)
}

// watchTurnIdle surfaces, but never cancels, a turn whose agent has gone silent.
// It runs for the life of one turn (ctx is cancelled when runTurn returns or the
// turn is interrupted). On crossing the idle bound with no activity it emits a
// single running system row; if activity resumes it settles that row as recovered
// and keeps watching. finishPrompt settles a still-open row on turn completion.
func (c *conversation) watchTurnIdle(ctx context.Context, turnID string) {
	timeout := idleActivityTimeout
	if timeout <= 0 {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
			// Activity resumed while a stall was surfaced. Settle the row now as
			// recovered instead of leaving it "waiting" until the timer next fires,
			// which could be up to a full idle window away, and start a fresh window
			// from this activity.
			c.settleIdleStallIfOpen(turnID, domain.ActivityStatusRecovered)
			timer.Reset(timeout)
		case <-timer.C:
			idle := time.Duration(time.Now().UnixNano() - c.lastActivityNanos.Load())
			if idle < timeout {
				// Activity resumed inside the window. Settle any surfaced stall as
				// recovered, then wait out only the remaining idle time.
				c.settleIdleStallIfOpen(turnID, domain.ActivityStatusRecovered)
				timer.Reset(timeout - idle)
				continue
			}
			c.surfaceIdleStall(turnID, idle)
			timer.Reset(timeout)
		}
	}
}

// emitIdleStall surfaces the non-terminal "the agent has gone quiet" row.
func (c *conversation) emitIdleStall(turnID string, idle time.Duration) {
	detail, _ := json.Marshal(map[string]any{
		"event":       "provider.idle",
		"idleSeconds": int(idle.Seconds()),
	})
	c.emit(ports.ChatEvent{
		Kind:           ports.ChatEventActivityStarted,
		ProviderTurnID: turnID,
		ProviderItemID: idleStallItemID(turnID),
		ActivityKind:   domain.ActivityKindSystem,
		ActivityStatus: domain.ActivityStatusRunning,
		Summary:        "Waiting for the agent to respond",
		Detail:         detail,
	})
}

// settleIdleStall closes the idle row with the given terminal-for-the-row status.
func (c *conversation) settleIdleStall(turnID string, status domain.ActivityStatus) {
	c.emit(ports.ChatEvent{
		Kind:           ports.ChatEventActivityCompleted,
		ProviderTurnID: turnID,
		ProviderItemID: idleStallItemID(turnID),
		ActivityKind:   domain.ActivityKindSystem,
		ActivityStatus: status,
		Summary:        "Waiting for the agent to respond",
	})
}
