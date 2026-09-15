package terminal

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/codexops"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/systeminstall"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

type reviewerUpdateFixture struct {
	ports.CommandRunner
	running atomic.Bool
	updated atomic.Bool
	entered chan struct{}
	finish  chan struct{}
	fail    bool
}

func (f *reviewerUpdateFixture) ListAllSessions(context.Context) ([]domain.SessionRecord, error) {
	return []domain.SessionRecord{{ID: "worker", Harness: domain.HarnessClaudeCode}}, nil
}
func (f *reviewerUpdateFixture) SnapshotCodexReviewer(ctx context.Context, _ domain.SessionID) (ports.CodexReviewerControllerSnapshot, error) {
	return ports.CodexReviewerControllerSnapshot{HandleID: "ptyhost-v1:review-worker", NativeSessionID: "history", Running: f.running.Load()}, ctx.Err()
}
func (f *reviewerUpdateFixture) Resolve(ctx context.Context) (ports.CodexInstallation, error) {
	version := "1.0.0"
	if f.updated.Load() {
		version = "1.1.0"
	}
	return ports.CodexInstallation{Path: "/fixture/codex", RealPath: "/fixture/codex", Version: version, Source: "npm", Scope: "fixture", Fingerprint: version, Command: ports.InstallCommand{Argv: []string{"/fixture/npm", "update"}}}, ctx.Err()
}
func (*reviewerUpdateFixture) Latest(ctx context.Context, _ ports.CodexInstallation) (string, error) {
	return "1.1.0", ctx.Err()
}
func (f *reviewerUpdateFixture) RunInstall(ctx context.Context, _ ports.InstallCommand, _, _ io.Writer) error {
	close(f.entered)
	select {
	case <-f.finish:
	case <-ctx.Done():
		return ctx.Err()
	}
	if f.fail {
		return errors.New("fixture installer failed")
	}
	f.updated.Store(true)
	return nil
}

func newReviewerUpdateFixture(t *testing.T) (*reviewerUpdateFixture, *systeminstall.Service, *sessionmanager.Manager) {
	t.Helper()
	f := &reviewerUpdateFixture{entered: make(chan struct{}), finish: make(chan struct{})}
	input := sessionmanager.New(sessionmanager.Deps{DataDir: t.TempDir()})
	s := systeminstall.NewWithDeps(nil, f, systeminstall.Deps{ReviewerInput: input, Sessions: f, CodexReviewers: f, CodexMaintenance: f, CodexOperationGate: codexops.NewGate(), RefreshCodex: func(ctx context.Context) error { return ctx.Err() }})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return f, s, input
}

// completedInputLease reports after the mux has returned from PTY.Write and
// released its input lease. The fake PTY stores bytes but never consumes them.
type completedInputLease struct {
	*sessionmanager.Manager
	admitted chan struct{}
	released chan struct{}
}

func (l completedInputLease) AcquireSessionInput(id domain.SessionID) (func(), bool) {
	release, ok := l.Manager.AcquireSessionInput(id)
	if !ok {
		return nil, false
	}
	select {
	case l.admitted <- struct{}{}:
	default:
	}
	return func() {
		release()
		select {
		case l.released <- struct{}{}:
		default:
		}
	}, true
}

func TestCodexUpdateBlocksWrittenButUnconsumedReviewerInput(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(map[bool]string{false: "attached_PTY", true: "buffered_during_attach"}[buffered], func(t *testing.T) {
			f, s, input := newReviewerUpdateFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			barrier := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(barrier) }) }
			defer unblock()
			pty := newFakePTY()
			src := &fakeSource{alive: true, attachFn: func(ctx context.Context, _, _ uint16) (ports.Stream, error) {
				if buffered {
					select {
					case <-barrier:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return pty, nil
			}}
			mgr := NewManager(src, nil, testLogger(), WithHeartbeat(0))
			defer mgr.Close()
			lease := completedInputLease{input, make(chan struct{}, 1), make(chan struct{}, 1)}
			mgr.SetSessionInputLease(lease)
			conn := newFakeConn()
			go mgr.Serve(ctx, conn)
			id := "ptyhost-v1:review-worker"
			conn.in <- clientMsg{Ch: chTerminal, ID: id, Type: msgOpen}
			if !buffered {
				recv(t, conn, chTerminal, msgOpened, time.Second)
			}
			conn.in <- clientMsg{Ch: chTerminal, ID: id, Type: msgData, Data: base64.StdEncoding.EncodeToString([]byte("codex\n"))}
			select {
			case <-lease.admitted:
			case <-time.After(time.Second):
				t.Fatal("raw input was not admitted")
			}
			unblock()
			select {
			case <-lease.released:
			case <-time.After(time.Second):
				t.Fatal("PTY write did not return and release its lease")
			}
			if got := string(pty.writtenBytes()); got != "codex\n" {
				t.Fatalf("written bytes = %q", got)
			}
			// There is deliberately no shell consumption yet. A successful write
			// and a stopped workload snapshot must not admit executable replacement.
			if f.running.Load() {
				t.Fatal("fixture consumed input prematurely")
			}
			if _, err := s.StartCodexUpdate(ctx, "1.0.0"); !errors.Is(err, systeminstall.ErrHarnessActive) {
				t.Fatalf("unconsumed input admitted installer: %v", err)
			}
			select {
			case <-f.entered:
				t.Fatal("installer ran with retained terminal")
			default:
			}
			select {
			case <-pty.closed:
				t.Fatal("updater implicitly closed terminal")
			default:
			}
			if input.SessionMutationInProgress(domain.SessionID(id)) {
				t.Fatal("rejection leaked an input reservation")
			}
			// Later consumption can launch Codex; the existing terminal still blocks.
			f.running.Store(true)
			if _, err := s.StartCodexUpdate(ctx, "1.0.0"); !errors.Is(err, systeminstall.ErrHarnessActive) {
				t.Fatal(err)
			}
		})
	}
}
