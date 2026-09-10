package store_test

import (
	"context"
	"testing"
)

func TestSessionClaudeProfileRoundTrip(t *testing.T) {
	s := newTestStore(t)
	seedProject(t, s, "profiles")
	ctx := context.Background()
	for _, value := range []string{"", "/profiles/arbitrary-name"} {
		rec := sampleRecord("profiles")
		rec.Metadata.ClaudeConfigDir = &value
		created, err := s.CreateSession(ctx, rec)
		if err != nil {
			t.Fatal(err)
		}
		got, ok, err := s.GetSession(ctx, created.ID)
		if err != nil || !ok {
			t.Fatalf("get: %v %v", ok, err)
		}
		if got.Metadata.ClaudeConfigDir == nil || *got.Metadata.ClaudeConfigDir != value {
			t.Fatalf("profile = %v, want %q", got.Metadata.ClaudeConfigDir, value)
		}
		got.Metadata.Model = "changed"
		if err := s.UpdateSession(ctx, got); err != nil {
			t.Fatal(err)
		}
		sessions, err := s.ListSessions(ctx, "profiles")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, session := range sessions {
			if session.ID == created.ID {
				found = true
				if session.Metadata.ClaudeConfigDir == nil || *session.Metadata.ClaudeConfigDir != value {
					t.Fatal("profile lost after update/list")
				}
			}
		}
		if !found {
			t.Fatal("session missing")
		}
	}
}
