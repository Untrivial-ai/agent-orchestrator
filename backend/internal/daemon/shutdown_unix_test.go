//go:build !windows

package daemon

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	telemetryadapter "github.com/aoagents/agent-orchestrator/backend/internal/adapters/telemetry"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/testingevidence"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/observe/sentryobs"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	testingsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/testing"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

type shutdownTarget struct {
	ports.TestingTargetEnvironment
	stop func(context.Context) error
}

func (target shutdownTarget) ReadLogs(context.Context, domain.TestTargetIdentity, domain.TestReadLogsRequest) (domain.TestLogResult, error) {
	return domain.TestLogResult{Text: "target log"}, nil
}

func (target shutdownTarget) Stop(ctx context.Context, _ domain.TestTargetIdentity) (ports.TestingCleanupResult, error) {
	return ports.TestingCleanupResult{State: domain.TestCleanupComplete}, target.stop(ctx)
}

func TestShutdownSignalsAllowTestingCloseToFinish(t *testing.T) {
	if mode := os.Getenv("AO_SHUTDOWN_HELPER"); mode != "" {
		if err := withDaemonContext(func(ctx context.Context, cancelWorkers context.CancelFunc) error {
			return shutdownSignalHelper(ctx, t, cancelWorkers)
		}); err != nil {
			t.Fatal(err)
		}
		fmt.Println("final defers complete")
		return
	}

	for _, tc := range []struct {
		name  string
		first syscall.Signal
		last  syscall.Signal
	}{
		{name: "SIGINT then SIGTERM", first: syscall.SIGINT, last: syscall.SIGTERM},
		{name: "SIGINT repeated", first: syscall.SIGINT, last: syscall.SIGINT},
		{name: "SIGTERM repeated", first: syscall.SIGTERM, last: syscall.SIGTERM},
		{name: "HTTP then SIGINT", last: syscall.SIGINT},
		{name: "HTTP then SIGTERM", last: syscall.SIGTERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Migrate once in the parent, not in every race-instrumented child.
			dataDir := t.TempDir()
			store, err := sqlitetest.Open(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestShutdownSignalsAllowTestingCloseToFinish$")
			mode := "http"
			if tc.first != 0 {
				mode = strconv.Itoa(int(tc.first))
			}
			cmd.Env = append(os.Environ(), "AO_SHUTDOWN_HELPER="+mode, "AO_SHUTDOWN_HELPER_DATA="+dataDir)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cancel()
				_ = input.Close()
				_ = cmd.Wait()
			})
			reader := bufio.NewReader(output)
			read := func(want string) {
				t.Helper()
				line, err := reader.ReadString('\n')
				if err != nil || line != want+"\n" {
					rest, _ := io.ReadAll(reader)
					waitErr := cmd.Wait()
					t.Fatalf("wanted %q, got %q: %v; helper exit: %v\n%s%s", want, line, err, waitErr, rest, stderr.String())
				}
			}
			ready, err := reader.ReadString('\n')
			if err != nil || !strings.HasPrefix(ready, "ready http://127.0.0.1:") {
				t.Fatalf("helper not ready: %q, %v", ready, err)
			}
			if tc.first == 0 {
				client := &http.Client{Timeout: time.Second}
				resp, postErr := client.Post(strings.TrimSpace(strings.TrimPrefix(ready, "ready "))+"/shutdown", "application/json", nil)
				if postErr != nil {
					t.Fatal(postErr)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusAccepted {
					t.Fatalf("HTTP shutdown status = %d", resp.StatusCode)
				}
			} else {
				err = cmd.Process.Signal(tc.first)
			}
			if err != nil {
				t.Fatal(err)
			}
			read("cleanup running")
			if err := cmd.Process.Signal(tc.last); err != nil {
				t.Fatal(err)
			}
			if _, err := input.Write([]byte("finish\n")); err != nil {
				t.Fatal(err)
			}
			read("cleanup complete")
			for _, phase := range []string{"sentry flush", "telemetry close", "sqlite close"} {
				read(phase + " running")
				if err := cmd.Process.Signal(tc.last); err != nil {
					t.Fatal(err)
				}
				if _, err := input.Write([]byte("finish\n")); err != nil {
					t.Fatal(err)
				}
				read(phase + " complete")
			}
			read("final defers complete")
			if err := cmd.Wait(); err != nil {
				t.Fatalf("shutdown helper exited before final defers finished: %v\n%s", err, stderr.String())
			}
			store, err = sqlite.OpenPreMigrated(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			rec, _, err := store.GetTestAttempt(context.Background(), "attempt")
			if err != nil || rec.CleanupState != domain.TestCleanupComplete || rec.Outcome != domain.TestOutcomeCancelled {
				t.Fatal("final defers lost persisted cleanup", rec, err)
			}
			events, err := store.ListTelemetryEventsSince(context.Background(), time.Time{}, 10)
			if err != nil || len(events) != 1 || events[0].Name != "shutdown proof" {
				t.Fatal("telemetry close did not persist the queued event", events, err)
			}
		})
	}
}

func shutdownSignalHelper(ctx context.Context, t *testing.T, cancelWorkers context.CancelFunc) error {
	t.Helper()
	input := bufio.NewReader(os.Stdin)
	store, err := sqlite.OpenPreMigrated(os.Getenv("AO_SHUTDOWN_HELPER_DATA"))
	if err != nil {
		t.Fatal(err)
	}
	// Gate every final defer so the parent sends a signal while it runs.
	finalCleanup := func(name string, closeResource func() error) {
		fmt.Println(name + " running")
		if _, err := input.ReadString('\n'); err != nil {
			t.Error(err)
		}
		if err := closeResource(); err != nil {
			t.Error(err)
		}
		fmt.Println(name + " complete")
	}
	defer finalCleanup("sqlite close", store.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sink := telemetryadapter.NewLocalSQLiteSink(store, log)
	sink.Emit(context.Background(), ports.TelemetryEvent{Name: "shutdown proof", Source: "daemon", Level: ports.TelemetryLevelInfo, OccurredAt: time.Now().UTC()})
	defer finalCleanup("telemetry close", func() error { return sink.Close(context.Background()) })
	defer finalCleanup("sentry flush", func() error { sentryobs.Flush(time.Second); return nil })
	dir, now := t.TempDir(), time.Now().UTC()
	if err := store.UpsertProject(context.Background(), domain.ProjectRecord{ID: "testing", Path: dir, RegisteredAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTestRun(context.Background(), domain.TestRunRecord{ID: "run", ProjectID: "testing", IssueSnapshot: `{}`, RecipeSnapshot: `{}`, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	attempt, err := store.CreateTestAttempt(context.Background(), domain.TestAttemptRecord{ID: "attempt", RunID: "run", CreatedAt: now, Deadline: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	attempt.Phase, attempt.Target = domain.TestAttemptActive, domain.TestTargetIdentity{ID: "target"}
	if err := store.UpdateTestAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	closed := false
	svc := testingsvc.New(testingsvc.Deps{
		Store: store, Evidence: testingevidence.New(dir, store),
		Target: shutdownTarget{stop: func(context.Context) error {
			rec, _, err := store.GetTestAttempt(context.Background(), attempt.ID)
			if err != nil {
				return err
			}
			if rec.CleanupState != domain.TestCleanupRunning {
				return fmt.Errorf("cleanup did not enter running: %+v", rec)
			}
			fmt.Println("cleanup running")
			_, err = input.ReadString('\n')
			return err
		}},
		CloseDesktop: func(context.Context) error { closed = true; return nil },
	})
	// Exercise the real loopback HTTP shutdown route as well as signals.
	srv, err := httpd.NewWithDeps(config.Config{
		Host: config.LoopbackHost, Port: 0, ShutdownTimeout: time.Second,
		RunFilePath: filepath.Join(dir, "running.json"),
	}, log, nil, httpd.APIDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.RunWithReady(ctx, func() { fmt.Println("ready http://" + srv.Addr().String()) }); err != nil {
		t.Fatal(err)
	}
	// Run cancels workers before joining the testing service's cleanup.
	cancelWorkers()
	if ctx.Err() != context.Canceled {
		t.Fatal("shutdown did not cancel workers", ctx.Err())
	}
	// A cleanup started by cancellation must survive the subsequent Close too.
	if _, err := svc.Cancel(context.Background(), attempt.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Close(); err != nil || !closed {
		t.Fatal("testing cleanup did not finish", err)
	}
	rec, _, err := store.GetTestAttempt(context.Background(), attempt.ID)
	if err != nil || rec.Phase != domain.TestAttemptFinished || rec.Outcome != domain.TestOutcomeCancelled || rec.CancelledAt == nil || rec.CleanupState != domain.TestCleanupComplete {
		t.Fatal("shutdown did not persist terminal cleanup", rec, err)
	}
	fmt.Println("cleanup complete")
	return nil
}
