package session

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestContextPressureComesFromTheWiredSource(t *testing.T) {
	reading := &domain.ContextPressure{ContextUsedPercent: 88, Source: "test", ObservedAt: time.Unix(5, 0)}
	for _, tc := range []struct {
		name       string
		source     func(domain.SessionID) *domain.ContextPressure
		terminated bool
		want       *domain.ContextPressure
	}{
		{"no source wired", nil, false, nil},
		{"source has no reading", func(domain.SessionID) *domain.ContextPressure { return nil }, false, nil},
		{"reading reported", func(id domain.SessionID) *domain.ContextPressure {
			if id == "s" {
				return reading
			}
			return nil
		}, false, reading},
		{"terminated session ignores a stale reading", func(domain.SessionID) *domain.ContextPressure { return reading }, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{clock: func() time.Time { return time.Unix(1, 0) }}
			if tc.source != nil {
				svc.SetContextPressureSource(tc.source)
			}
			session, err := svc.toSessionWithFacts(domain.SessionRecord{
				ID: "s", Harness: domain.HarnessPi, IsTerminated: tc.terminated,
			}, nil, nil)
			if err != nil || session.ContextPressure != tc.want {
				t.Fatalf("pressure=%+v, want %+v; err=%v", session.ContextPressure, tc.want, err)
			}
		})
	}
}
