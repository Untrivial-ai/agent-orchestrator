package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestStartupWritesRespectCleanupDeadlineWhileWriterIsBusy(t *testing.T) {
	tests := map[string]func(context.Context, *Store) error{
		"create seed": func(ctx context.Context, s *Store) error {
			_, err := s.CreateSession(ctx, domain.SessionRecord{})
			return err
		},
		"lifecycle update": func(ctx context.Context, s *Store) error { return s.UpdateSession(ctx, domain.SessionRecord{}) },
		"launch commit":    func(ctx context.Context, s *Store) error { return s.CommitSessionSpawn(ctx, domain.SessionRecord{}) },
		"startup journal": func(ctx context.Context, s *Store) error {
			_, err := s.UpdateSessionStartup(ctx, domain.SessionRecord{}, "operation", domain.SessionControllerOwner{})
			return err
		},
		"seed deletion": func(ctx context.Context, s *Store) error { _, err := s.DeleteSession(ctx, "session"); return err },
		"worktree registry": func(ctx context.Context, s *Store) error {
			return s.UpsertSessionWorktree(ctx, domain.SessionWorktreeRecord{})
		},
		"worktree deletion": func(ctx context.Context, s *Store) error { return s.DeleteSessionWorktrees(ctx, "session") },
		"controller credential": func(ctx context.Context, s *Store) error {
			_, err := s.UpdateBrowserCapabilityVerifier(ctx, "session", domain.SessionControllerOwner{}, "verifier", time.Now())
			return err
		},
	}
	for name, write := range tests {
		t.Run(name, func(t *testing.T) {
			s := &Store{writeMu: newContextMutex()}
			s.writeMu.Lock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			admission := make(chan struct{})
			observed := &lockAdmissionContext{Context: ctx, admission: admission}
			result := make(chan error, 1)
			go func() { result <- write(observed, s) }()
			select {
			case <-admission:
			case <-time.After(time.Second):
				t.Fatal("write ignored context while waiting for writer")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("write error=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("write remained blocked after cleanup cancellation")
			}
			s.writeMu.Unlock()
		})
	}
}
