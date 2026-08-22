// Package store contains SQLite-backed table stores built on sqlc-generated
// queries.
package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// Store is the SQLite-backed persistence layer. It routes writes to a single
// writer connection (qw) and reads to a reader pool (qr) — see Open. writeMu
// guards the read-modify-write write methods (e.g. CreateSession's
// next-num-then-insert) so concurrent writes can't interleave them.
//
// CDC is captured by DB triggers (migration 0001), NOT by this layer: the store
// never writes change_log, it only reads it for the CDC poller.
type Store struct {
	writeDB *sql.DB
	readDB  *sql.DB
	qw      *gen.Queries // bound to the single writer connection
	qr      *gen.Queries // bound to the reader pool
	writeMu *contextMutex
}

type contextMutex struct {
	token chan struct{}
}

func newContextMutex() *contextMutex {
	mutex := &contextMutex{token: make(chan struct{}, 1)}
	mutex.token <- struct{}{}
	return mutex
}

func (m *contextMutex) Lock() {
	<-m.token
}

func (m *contextMutex) LockContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.token:
		if err := ctx.Err(); err != nil {
			m.token <- struct{}{}
			return err
		}
		return nil
	}
}

func (m *contextMutex) Unlock() {
	m.token <- struct{}{}
}

type conversationProjectionTxKey struct{}

// conversationWriter returns the transaction-bound query set when a provider
// event projection is in progress. Otherwise it acquires the ordinary single
// writer lock. This makes the existing focused store methods composable inside
// one archive+projection transaction without exposing SQLite transactions above
// the adapter boundary.
func (s *Store) conversationWriter(ctx context.Context) (*gen.Queries, func()) {
	if q, ok := ctx.Value(conversationProjectionTxKey{}).(*gen.Queries); ok && q != nil {
		return q, func() {}
	}
	s.writeMu.Lock()
	return s.qw, s.writeMu.Unlock
}

func (s *Store) conversationReader(ctx context.Context) *gen.Queries {
	if q, ok := ctx.Value(conversationProjectionTxKey{}).(*gen.Queries); ok && q != nil {
		return q
	}
	return s.qr
}

// NewStore wraps an opened writer + reader *sql.DB (see Open) as a Store.
func NewStore(writeDB, readDB *sql.DB) *Store {
	return &Store{
		writeDB: writeDB,
		readDB:  readDB,
		qw:      gen.New(writeDB),
		qr:      gen.New(readDB),
		writeMu: newContextMutex(),
	}
}

// Close closes both pools.
func (s *Store) Close() error {
	err := s.writeDB.Close()
	if e := s.readDB.Close(); e != nil && err == nil {
		err = e
	}
	return err
}

// inTx runs fn inside a single write transaction on the writer connection,
// rolling back on error. The caller must already hold writeMu.
func (s *Store) inTx(ctx context.Context, what string, fn func(*gen.Queries) error) error {
	if q, ok := ctx.Value(conversationProjectionTxKey{}).(*gen.Queries); ok && q != nil {
		if err := fn(q); err != nil {
			return fmt.Errorf("%s: %w", what, err)
		}
		return nil
	}
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin %s: %w", what, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(s.qw.WithTx(tx)); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return tx.Commit()
}
