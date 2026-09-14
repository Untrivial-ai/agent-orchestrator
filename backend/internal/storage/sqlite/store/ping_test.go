package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestPingChecksWriterAndReaderPools(t *testing.T) {
	writer, reader := openPingDatabases(t)
	st := NewStore(writer, reader)

	if err := st.Ping(context.Background()); err != nil {
		t.Fatalf("ping healthy pools: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if err := st.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "writer") {
		t.Fatalf("ping after writer close = %v, want writer failure", err)
	}
}

func TestPingHonorsContextWhileWriterPoolIsBusy(t *testing.T) {
	writer, reader := openPingDatabases(t)
	writer.SetMaxOpenConns(1)
	st := NewStore(writer, reader)

	conn, err := writer.Conn(context.Background())
	if err != nil {
		t.Fatalf("reserve writer connection: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = st.Ping(ctx)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("ping with busy writer = %v, want context deadline", err)
	}
}

func openPingDatabases(t *testing.T) (*sql.DB, *sql.DB) {
	t.Helper()
	dsn := "file:" + t.TempDir() + "/ao.db?_pragma=busy_timeout(100)"
	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	reader, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = writer.Close()
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
	})
	return writer, reader
}
