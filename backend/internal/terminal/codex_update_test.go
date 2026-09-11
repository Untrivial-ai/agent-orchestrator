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

func TestCodexUpdateRejectsRawReviewerInputDuringReplacement(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "shutdown"} {
		t.Run(outcome, func(t *testing.T) {
			f, s, input := newReviewerUpdateFixture(t)
			f.fail = outcome == "failure"
			pty := newFakePTY()
			mgr := NewManager(&fakeSource{alive: true, spawner: &fakeSpawner{ptys: []*fakePTY{pty}}}, nil, testLogger(), WithHeartbeat(0))
			mgr.SetSessionInputLease(input)
			defer mgr.Close()
			conn := newFakeConn()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go mgr.Serve(ctx, conn)
			id := "ptyhost-v1:review-worker"
			conn.in <- clientMsg{Ch: chTerminal, ID: id, Type: msgOpen}
			recv(t, conn, chTerminal, msgOpened, time.Second)
			if _, err := s.StartCodexUpdate(ctx, "1.0.0"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-f.entered:
			case <-time.After(time.Second):
				t.Fatal("installer not reached")
			}
			conn.in <- clientMsg{Ch: chTerminal, ID: id, Type: msgData, Data: base64.StdEncoding.EncodeToString([]byte("codex\n"))}
			recv(t, conn, chTerminal, msgError, time.Second)
			if got := string(pty.writtenBytes()); got != "" {
				t.Fatalf("input reached reviewer during replacement: %q", got)
			}
			if outcome == "shutdown" {
				if err := s.Close(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				close(f.finish)
			}
			eventually(t, time.Second, func() bool {
				j, err := s.Status(ctx, systeminstall.TargetCodex)
				return err == nil && (j.Status == systeminstall.StatusSucceeded || j.Status == systeminstall.StatusFailed || j.Status == systeminstall.StatusInterrupted)
			})
			conn.in <- clientMsg{Ch: chTerminal, ID: id, Type: msgData, Data: base64.StdEncoding.EncodeToString([]byte("after\n"))}
			eventually(t, time.Second, func() bool { return string(pty.writtenBytes()) == "after\n" })
		})
	}
}

// relaunchPTY models the process becoming live only when admitted input actually
// reaches the PTY, rather than when the WebSocket frame was received.
type relaunchPTY struct {
	*fakePTY
	running *atomic.Bool
}

func (p relaunchPTY) Write(b []byte) (int, error) {
	n, err := p.fakePTY.Write(b)
	if err == nil {
		p.running.Store(true)
	}
	return n, err
}

type reportedInputLease struct {
	*sessionmanager.Manager
	admitted chan struct{}
}

func (l reportedInputLease) AcquireSessionInput(id domain.SessionID) (func(), bool) {
	release, ok := l.Manager.AcquireSessionInput(id)
	if ok {
		select {
		case l.admitted <- struct{}{}:
		default:
		}
	}
	return release, ok
}

func TestCodexUpdateDrainsAdmittedReviewerInputBeforeReprobe(t *testing.T) {
	for _, buffered := range []bool{false, true} {
		t.Run(map[bool]string{false: "in_flight_PTY_write", true: "buffered_during_attach"}[buffered], func(t *testing.T) {
			f, s, input := newReviewerUpdateFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			barrier := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(barrier) }) }
			defer unblock()
			pty := newFakePTY()
			if !buffered {
				pty.writeUnblock = barrier
			}
			src := &fakeSource{alive: true, attachFn: func(ctx context.Context, _, _ uint16) (ports.Stream, error) {
				if buffered {
					select {
					case <-barrier:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return relaunchPTY{pty, &f.running}, nil
			}}
			mgr := NewManager(src, nil, testLogger(), WithHeartbeat(0))
			defer mgr.Close()
			lease := reportedInputLease{input, make(chan struct{}, 1)}
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
				t.Fatal("raw input not admitted")
			}
			if _, err := s.StartCodexUpdate(ctx, "1.0.0"); err != nil {
				t.Fatal(err)
			}
			eventually(t, time.Second, func() bool { return input.SessionMutationInProgress(domain.SessionID(id)) })
			select {
			case <-f.entered:
				t.Fatal("installer overtook an admitted pane write")
			default:
			}
			if release, ok := input.AcquireSessionInput(domain.SessionID(id)); ok {
				release()
				t.Fatal("late input admitted during drain")
			}
			unblock()
			eventually(t, time.Second, func() bool {
				j, err := s.Status(ctx, systeminstall.TargetCodex)
				return err == nil && j.Status == systeminstall.StatusFailed
			})
			select {
			case <-f.entered:
				t.Fatal("installer ignored reviewer relaunched by admitted input")
			default:
			}
			if !f.running.Load() {
				t.Fatal("raw input never reached PTY")
			}
			eventually(t, time.Second, func() bool { return !input.SessionMutationInProgress(domain.SessionID(id)) })
			release, ok := input.AcquireSessionInput(domain.SessionID(id))
			if !ok {
				t.Fatal("failed recheck retained input reservation")
			}
			release()
		})
	}
}
