package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// GetTmuxServerClient returns the recorded compatible client for a tmux socket.
func (s *Store) GetTmuxServerClient(ctx context.Context, socketName string) (binaryPath, binarySHA256 string, managedRetained, found bool, err error) {
	row, err := s.qr.GetTmuxServerClient(ctx, socketName)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, false, nil
	}
	if err != nil {
		return "", "", false, false, fmt.Errorf("read tmux server client for %s: %w", socketName, err)
	}
	return row.BinaryPath, row.BinarySha256, row.ManagedRetained, true, nil
}

// UpsertTmuxServerClient records the client confirmed compatible with a tmux socket.
func (s *Store) UpsertTmuxServerClient(ctx context.Context, socketName, binaryPath, binarySHA256 string, managedRetained bool, confirmedAt time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.UpsertTmuxServerClient(ctx, gen.UpsertTmuxServerClientParams{
		SocketName:      socketName,
		BinaryPath:      binaryPath,
		BinarySha256:    binarySHA256,
		ManagedRetained: managedRetained,
		ConfirmedAt:     confirmedAt,
	}); err != nil {
		return fmt.Errorf("write tmux server client for %s: %w", socketName, err)
	}
	return nil
}

// DeleteTmuxServerClient removes the recorded compatible client for a tmux socket.
func (s *Store) DeleteTmuxServerClient(ctx context.Context, socketName string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.DeleteTmuxServerClient(ctx, socketName); err != nil {
		return fmt.Errorf("delete tmux server client for %s: %w", socketName, err)
	}
	return nil
}
