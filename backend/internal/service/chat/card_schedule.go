package chat

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

const (
	maxCardEvidenceFragmentBytes = 2048
	maxCardEvidenceBatchBytes    = 8192
)

var (
	cardFirstRefreshDelay = 500 * time.Millisecond
	cardRefreshDelay      = time.Second
)

// scheduleCardRefresh coalesces provider progress into one local inference
// window. A quiet worker does not produce repeated writes or model requests.
func (s *Service) scheduleCardRefresh(ctx context.Context, id domain.SessionID, evidence string) {
	if s.onAssistantMessage == nil {
		return
	}
	s.mu.Lock()
	// A worker update may arrive as prose, tools, or a mixture of both.
	evidence = strings.TrimSpace(evidence)
	if len(evidence) > maxCardEvidenceFragmentBytes {
		end := maxCardEvidenceFragmentBytes
		for !utf8.RuneStart(evidence[end]) {
			end--
		}
		evidence = evidence[:end]
	}
	s.assistantEvidence[id] = append(s.assistantEvidence[id], evidence)
	if len(s.assistantEvidence[id]) > 32 {
		s.assistantEvidence[id] = s.assistantEvidence[id][len(s.assistantEvidence[id])-32:]
	}
	batchBytes := 0
	for _, fragment := range s.assistantEvidence[id] {
		batchBytes += len(fragment)
	}
	for batchBytes > maxCardEvidenceBatchBytes {
		batchBytes -= len(s.assistantEvidence[id][0])
		s.assistantEvidence[id] = s.assistantEvidence[id][1:]
	}
	// Keep the existing window open while events continue arriving. Resetting
	// the timer here would debounce forever during a busy tool stream; a fixed
	// one-second window keeps the rendered card responsive.
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
		s.mu.Unlock()
		if strings.TrimSpace(batch) != "" {
			s.onAssistantMessage(context.WithoutCancel(ctx), id, batch)
		}
	})
}
