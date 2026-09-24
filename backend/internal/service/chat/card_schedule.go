package chat

import (
	"context"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var (
	cardFirstRefreshDelay = 500 * time.Millisecond
	// Leave room for the configured/local model race after each collection
	// window, so a busy card visibly refreshes every two to three seconds.
	cardRefreshDelay = time.Second
	// A heartbeat renders the latest factual batch while a provider is working
	// silently (for example, during a long test command). Together with the
	// provider-selection budget it keeps visible card updates near three seconds.
	cardHeartbeatRefreshDelay = 2 * time.Second
	cardSummaryFreshWindow    = 10 * time.Second
)

// scheduleCardRefresh coalesces provider progress events into one configured-
// model summary call. The first signal is deliberately quick so a newly
// created card becomes specific almost immediately; later signals are kept to
// a modest cadence while tools stream continuously.
func (s *Service) scheduleCardRefresh(ctx context.Context, id domain.SessionID, evidence string) {
	if s.onAssistantMessage == nil {
		return
	}
	s.mu.Lock()
	// Collect the complete response window. A worker update may arrive as
	// several prose deltas, several tool activities, or a mixture of both; the
	// configured model must summarize that batch rather than one raw event.
	s.assistantEvidence[id] = append(s.assistantEvidence[id], strings.TrimSpace(evidence))
	if len(s.assistantEvidence[id]) > 32 {
		s.assistantEvidence[id] = s.assistantEvidence[id][len(s.assistantEvidence[id])-32:]
	}
	if s.keepCardSummaryFresh {
		s.assistantFreshUntil[id] = time.Now().Add(cardSummaryFreshWindow)
	}
	// Keep the existing window open while events continue arriving. Resetting
	// the timer here would debounce forever during a busy tool stream; a fixed
	// one-second window leaves enough time for inference while keeping the
	// rendered card on a two-to-three-second cadence.
	if s.assistantTimers[id] != nil {
		s.mu.Unlock()
		return
	}
	delay := cardRefreshDelay
	if !s.assistantSeen[id] {
		delay = cardFirstRefreshDelay
		s.assistantSeen[id] = true
	}
	s.scheduleCardTimerLocked(ctx, id, delay)
	s.mu.Unlock()
}

func (s *Service) scheduleCardTimerLocked(ctx context.Context, id domain.SessionID, delay time.Duration) {
	s.assistantTimers[id] = time.AfterFunc(delay, func() {
		s.mu.Lock()
		batch := strings.Join(s.assistantEvidence[id], "\n")
		delete(s.assistantEvidence, id)
		delete(s.assistantTimers, id)
		if strings.TrimSpace(batch) != "" {
			s.assistantLatest[id] = batch
		} else {
			batch = s.assistantLatest[id]
		}
		if s.keepCardSummaryFresh && strings.TrimSpace(batch) != "" && time.Now().Before(s.assistantFreshUntil[id]) {
			s.scheduleCardTimerLocked(ctx, id, cardHeartbeatRefreshDelay)
		} else if s.keepCardSummaryFresh {
			// The worker has been quiet for the bounded freshness window. Let a
			// later real event start a new quick cycle without retaining per-card
			// timer state indefinitely.
			delete(s.assistantLatest, id)
			delete(s.assistantFreshUntil, id)
			delete(s.assistantSeen, id)
		}
		s.mu.Unlock()
		if strings.TrimSpace(batch) != "" {
			s.onAssistantMessage(context.WithoutCancel(ctx), id, batch)
		}
	})
}
