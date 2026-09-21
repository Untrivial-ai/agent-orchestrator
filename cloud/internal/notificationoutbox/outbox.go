package notificationoutbox

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const (
	MaxRows  = 1000
	MaxBytes = 8 << 20
)

var (
	ErrFull     = errors.New("notification outbox is full")
	ErrNotFound = errors.New("notification outbox event not found")
	ErrConflict = errors.New("notification outbox event id has different contents")
)

type Event struct {
	EventID      string
	EventType    string
	Payload      []byte
	OccurredAt   time.Time
	WorkerEpoch  int64
	AttemptCount int
	NextAttempt  time.Time
	CreatedAt    time.Time
}

type Outbox struct {
	db *sql.DB
}

func Open(path string) (*Outbox, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("notification outbox path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create notification outbox directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	statements := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=2000`,
		`PRAGMA synchronous=NORMAL`,
		`CREATE TABLE IF NOT EXISTS notification_outbox (
			event_id TEXT NOT NULL,
			worker_epoch INTEGER NOT NULL CHECK (worker_epoch > 0),
			event_type TEXT NOT NULL CHECK (trim(event_type) <> ''),
			payload BLOB NOT NULL,
			occurred_at_ms INTEGER NOT NULL,
			attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
			next_attempt_at_ms INTEGER NOT NULL,
			created_at_ms INTEGER NOT NULL,
			PRIMARY KEY (worker_epoch, event_id)
		)`,
		`CREATE INDEX IF NOT EXISTS notification_outbox_ready_idx
			ON notification_outbox(next_attempt_at_ms, created_at_ms)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, fmt.Errorf("initialize notification outbox: %w", err)
		}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure notification outbox: %w", err)
	}
	return &Outbox{db: db}, nil
}

func (o *Outbox) Close() error {
	return o.db.Close()
}

func (o *Outbox) Enqueue(ctx context.Context, event Event) error {
	event.EventID = strings.TrimSpace(event.EventID)
	if event.EventID == "" {
		event.EventID = "evt_" + uuid.NewString()
	}
	event.EventType = strings.TrimSpace(event.EventType)
	if event.EventType == "" || event.WorkerEpoch <= 0 {
		return errors.New("notification event type and positive worker epoch are required")
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	now := time.Now().UTC()
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	if event.NextAttempt.IsZero() {
		event.NextAttempt = now
	}

	tx, err := o.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existingType string
	var existingPayload []byte
	var existingOccurred int64
	err = tx.QueryRowContext(ctx, `
		SELECT event_type, payload, occurred_at_ms
		FROM notification_outbox WHERE worker_epoch = ? AND event_id = ?`,
		event.WorkerEpoch, event.EventID,
	).Scan(&existingType, &existingPayload, &existingOccurred)
	if err == nil {
		if existingType != event.EventType || !bytes.Equal(existingPayload, event.Payload) || existingOccurred != event.OccurredAt.UnixMilli() {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var count, used int64
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*), COALESCE(sum(length(event_id) + length(event_type) + length(payload)), 0)
		FROM notification_outbox`).Scan(&count, &used); err != nil {
		return err
	}
	rowBytes := int64(len(event.EventID) + len(event.EventType) + len(event.Payload))
	if count >= MaxRows || rowBytes > MaxBytes || used+rowBytes > MaxBytes {
		return ErrFull
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO notification_outbox (
			event_id, worker_epoch, event_type, payload, occurred_at_ms,
			attempt_count, next_attempt_at_ms, created_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.WorkerEpoch, event.EventType, event.Payload,
		event.OccurredAt.UnixMilli(), event.AttemptCount,
		event.NextAttempt.UnixMilli(), event.CreatedAt.UnixMilli(),
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (o *Outbox) Ready(ctx context.Context, now time.Time, limit int) ([]Event, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := o.db.QueryContext(ctx, `
		SELECT event_id, event_type, payload, occurred_at_ms, worker_epoch,
			attempt_count, next_attempt_at_ms, created_at_ms
		FROM notification_outbox
		WHERE next_attempt_at_ms <= ?
		ORDER BY next_attempt_at_ms, created_at_ms, event_id
		LIMIT ?`, now.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]Event, 0, limit)
	for rows.Next() {
		var event Event
		var occurredAt, nextAttempt, createdAt int64
		if err := rows.Scan(&event.EventID, &event.EventType, &event.Payload, &occurredAt,
			&event.WorkerEpoch, &event.AttemptCount, &nextAttempt, &createdAt); err != nil {
			return nil, err
		}
		event.OccurredAt = time.UnixMilli(occurredAt).UTC()
		event.NextAttempt = time.UnixMilli(nextAttempt).UTC()
		event.CreatedAt = time.UnixMilli(createdAt).UTC()
		events = append(events, event)
	}
	return events, rows.Err()
}

func (o *Outbox) MarkRetry(ctx context.Context, eventID string, epoch int64, attempt int, next time.Time) error {
	result, err := o.db.ExecContext(ctx, `
		UPDATE notification_outbox
		SET attempt_count = ?, next_attempt_at_ms = ?
		WHERE worker_epoch = ? AND event_id = ?`, attempt, next.UnixMilli(), epoch, eventID)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func (o *Outbox) Delete(ctx context.Context, eventID string, epoch int64) error {
	result, err := o.db.ExecContext(ctx, `DELETE FROM notification_outbox WHERE worker_epoch = ? AND event_id = ?`, epoch, eventID)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func (o *Outbox) Usage(ctx context.Context) (int, int64, error) {
	var count int
	var used int64
	err := o.db.QueryRowContext(ctx, `
		SELECT count(*), COALESCE(sum(length(event_id) + length(event_type) + length(payload)), 0)
		FROM notification_outbox`).Scan(&count, &used)
	return count, used, err
}

func requireAffected(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}
