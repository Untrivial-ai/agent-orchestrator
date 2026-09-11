package session

import (
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestChatProviderPreservationIsDerivedFromLiveOwnership(t *testing.T) {
	no, yes := false, true
	for _, tc := range []struct {
		name       string
		mode       domain.SessionMode
		terminated bool
		observed   *bool
		want       bool
	}{
		{"unknown", domain.SessionModeChat, false, nil, false},
		{"daemon owned", domain.SessionModeChat, false, &no, false},
		{"persistent", domain.SessionModeChat, false, &yes, true},
		{"terminated", domain.SessionModeChat, true, &yes, false},
		{"terminal", domain.SessionModeTUI, false, &yes, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &Service{clock: func() time.Time { return time.Unix(1, 0) }}
			if tc.observed != nil {
				svc.SetChatProviderPreserver(func(id domain.SessionID) bool { return id == "s" && *tc.observed })
			}
			session, err := svc.toSessionWithFacts(domain.SessionRecord{
				ID: "s", Harness: domain.HarnessPi, Mode: tc.mode, IsTerminated: tc.terminated,
			}, nil, nil)
			if err != nil || session.ChatProviderPreserved != tc.want {
				t.Fatalf("preserved=%v, want %v; err=%v", session.ChatProviderPreserved, tc.want, err)
			}
		})
	}
}
