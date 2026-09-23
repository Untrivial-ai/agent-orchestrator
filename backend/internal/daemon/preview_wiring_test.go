package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
)

type fakePreviewExitSessions struct {
	sessions   map[domain.SessionID]domain.Session
	getErr     error
	setErr     error
	clearedIDs []domain.SessionID
}

func (f *fakePreviewExitSessions) Get(_ context.Context, id domain.SessionID) (domain.Session, error) {
	if f.getErr != nil {
		return domain.Session{}, f.getErr
	}
	sess, ok := f.sessions[id]
	if !ok {
		return domain.Session{}, errors.New("session not found")
	}
	return sess, nil
}

func (f *fakePreviewExitSessions) SetPreview(_ context.Context, id domain.SessionID, previewURL string) (domain.Session, error) {
	if f.setErr != nil {
		return domain.Session{}, f.setErr
	}
	sess, ok := f.sessions[id]
	if !ok {
		return domain.Session{}, errors.New("session not found")
	}
	sess.Metadata.PreviewURL = previewURL
	f.sessions[id] = sess
	if previewURL == "" {
		f.clearedIDs = append(f.clearedIDs, id)
	}
	return sess, nil
}

func fakePreviewSession(id domain.SessionID, previewURL string) domain.Session {
	return domain.Session{SessionRecord: domain.SessionRecord{
		ID: id,
		Metadata: domain.SessionMetadata{
			PreviewURL: previewURL,
		},
	}}
}

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, nil))
}

func TestManagedPreviewExitClearsMatchingPreviewURL(t *testing.T) {
	fake := &fakePreviewExitSessions{sessions: map[domain.SessionID]domain.Session{
		"ao-1": fakePreviewSession("ao-1", "http://127.0.0.1:4173/"),
	}}
	callback := managedPreviewExitFunc(fake, testLogger(&bytes.Buffer{}))

	callback(context.Background(), "ao-1", previewserver.Status{State: previewserver.StateFailed, URL: "http://127.0.0.1:4173/", Error: "exit status 1"})

	if len(fake.clearedIDs) != 1 || fake.clearedIDs[0] != domain.SessionID("ao-1") {
		t.Fatalf("cleared = %v, want [ao-1]", fake.clearedIDs)
	}
}

func TestManagedPreviewExitLeavesUnrelatedPreviewURLAlone(t *testing.T) {
	// The session's preview URL no longer points at the failed server: either
	// the user replaced it with a static-file preview or the server was
	// restarted on a different URL before the exit callback ran.
	fake := &fakePreviewExitSessions{sessions: map[domain.SessionID]domain.Session{
		"ao-1": fakePreviewSession("ao-1", "file:///tmp/index.html"),
		"ao-2": fakePreviewSession("ao-2", "http://127.0.0.1:9999/"),
	}}
	callback := managedPreviewExitFunc(fake, testLogger(&bytes.Buffer{}))

	callback(context.Background(), "ao-1", previewserver.Status{State: previewserver.StateFailed, URL: "http://127.0.0.1:4173/"})
	callback(context.Background(), "ao-2", previewserver.Status{State: previewserver.StateFailed, URL: "http://127.0.0.1:4173/"})
	callback(context.Background(), "ao-1", previewserver.Status{State: previewserver.StateFailed, URL: ""})

	if len(fake.clearedIDs) != 0 {
		t.Fatalf("cleared = %v, want none", fake.clearedIDs)
	}
}

func TestManagedPreviewExitLogsGetFailureAndDoesNotClear(t *testing.T) {
	getErr := errors.New("store unavailable")
	fake := &fakePreviewExitSessions{sessions: map[domain.SessionID]domain.Session{
		"ao-1": fakePreviewSession("ao-1", "http://127.0.0.1:4173/"),
	}, getErr: getErr}
	buf := &bytes.Buffer{}
	callback := managedPreviewExitFunc(fake, testLogger(buf))

	callback(context.Background(), "ao-1", previewserver.Status{State: previewserver.StateFailed, URL: "http://127.0.0.1:4173/"})

	if len(fake.clearedIDs) != 0 {
		t.Fatalf("cleared = %v, want none: a failed Get must not clear the preview URL", fake.clearedIDs)
	}
	if out := buf.String(); !strings.Contains(out, "fetch session to clear preview URL") || !strings.Contains(out, "store unavailable") {
		t.Fatalf("log output %q does not mention the Get failure", out)
	}
}

func TestManagedPreviewExitLogsSetPreviewFailure(t *testing.T) {
	fake := &fakePreviewExitSessions{sessions: map[domain.SessionID]domain.Session{
		"ao-1": fakePreviewSession("ao-1", "http://127.0.0.1:4173/"),
	}, setErr: errors.New("store read-only")}
	buf := &bytes.Buffer{}
	callback := managedPreviewExitFunc(fake, testLogger(buf))

	callback(context.Background(), "ao-1", previewserver.Status{State: previewserver.StateFailed, URL: "http://127.0.0.1:4173/"})

	if out := buf.String(); !strings.Contains(out, "clear preview URL after managed preview crash") || !strings.Contains(out, "store read-only") {
		t.Fatalf("log output %q does not mention the SetPreview failure", out)
	}
}
