package cua

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

func TestEnsureDriverDoesNotAdmitRefusedPermissions(t *testing.T) {
	for _, failure := range []string{"accessibility", "screen recording", "provider error"} {
		t.Run(failure, func(t *testing.T) {
			born := time.Now().UTC()
			granted, alive := false, false
			permissionCalls, windowCalls := 0, 0
			var a *Adapter
			var socket net.Listener
			fake := &fakeRunner{}
			fake.hook = func(executable string, args []string) (Output, error) {
				switch executable {
				case "/usr/sbin/ioreg":
					return Output{Stdout: []byte("<plist><dict><key>IOConsoleLocked</key><false/></dict></plist>")}, nil
				case "/usr/bin/codesign":
					return Output{Stderr: []byte("Identifier=com.trycua.driver\nTeamIdentifier=YCK386LBJ7\n")}, nil
				case "/bin/launchctl":
					return Output{}, nil
				case "/usr/bin/open":
					alive = true
					if err := os.WriteFile(a.pidFile(), []byte("99"), 0o600); err != nil {
						t.Fatal(err)
					}
					var err error
					socket, err = net.Listen("unix", a.socket())
					if err != nil {
						t.Fatal(err)
					}
					return Output{}, nil
				}
				if reflect.DeepEqual(args, []string{"--version"}) {
					return Output{Stdout: []byte("cua-driver 0.34.0\n")}, nil
				}
				if len(args) == 7 && args[4] == "stop" && args[6] == "99" {
					alive = false
					if err := socket.Close(); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(a.pidFile()); err != nil {
						t.Fatal(err)
					}
					return Output{}, nil
				}
				if len(args) == 5 && args[2] == "call" {
					switch args[3] {
					case "start_session":
						return jsonOutput(map[string]any{"active": true, "revived": false}), nil
					case "check_permissions":
						permissionCalls++
						if failure == "provider error" && !granted {
							return Output{}, errors.New("permission probe failed")
						}
						return jsonOutput(map[string]bool{"accessibility": granted || failure != "accessibility", "screen_recording": granted || failure != "screen recording"}), nil
					case "list_windows":
						windowCalls++
						return jsonOutput(map[string]any{"windows": []window{{PID: 123, ID: 456, Layer: 0, Bounds: domain.TestWindowBounds{Width: 640, Height: 400}, OnScreen: true}}}), nil
					case "end_session":
						return jsonOutput(map[string]any{}), nil
					}
				}
				t.Fatalf("unexpected command %s %v", executable, args)
				return Output{}, nil
			}
			var err error
			a, err = New(Config{DataDir: shortRoot(t), Runner: fake, ProcessStartedAt: func(_ context.Context, pid int) (time.Time, error) {
				if pid == 123 || (pid == 99 && alive) {
					return born, nil
				}
				return time.Time{}, process.ErrNotRunning
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if alive {
					if err := a.Close(context.Background()); err != nil {
						t.Error(err)
					}
				}
			})
			target := domain.TestTargetIdentity{ID: "target", LaunchID: "launch", Generation: 1, ElectronPID: 123, ElectronStartedAt: born, DataDir: "/scratch/target"}
			for retry := 0; retry < 2; retry++ {
				if err := a.ensureDriver(context.Background()); err == nil {
					t.Fatal("refused permissions admitted a window")
				}
				if a.driver.pid != 0 || a.pendingDriver.pid != 99 || permissionCalls != retry+1 || windowCalls != 0 {
					t.Fatalf("refusal lost ownership or bypassed checks: driver %+v pending %+v permissions %d windows %d", a.driver, a.pendingDriver, permissionCalls, windowCalls)
				}
			}
			if err := socket.Close(); err != nil {
				t.Fatal(err)
			}
			if err := a.ensureDriver(context.Background()); !errors.Is(err, ErrRefused) || permissionCalls != 2 || windowCalls != 0 {
				t.Fatalf("missing socket bypassed checks: %v", err)
			}
			socket, err = net.Listen("unix", a.socket())
			if err != nil {
				t.Fatal(err)
			}
			if failure == "provider error" {
				// Shutdown must still reap the owned process after a failed probe.
				if err := a.Close(context.Background()); err != nil || alive || a.pendingDriver.pid != 0 {
					t.Fatalf("pending driver cleanup: %v, alive %v", err, alive)
				}
				return
			}
			granted = true
			bound, err := a.BindWindow(context.Background(), target)
			if err != nil || bound.WindowID != "456" || a.driver.pid != 99 || a.pendingDriver.pid != 0 || permissionCalls != 3 || windowCalls != 1 {
				t.Fatalf("permission retry failed: bound %+v, err %v", bound, err)
			}
		})
	}
}

func TestControllersHavePrivateDriverPaths(t *testing.T) {
	root := shortRoot(t)
	first, err := New(Config{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(Config{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if first.root == second.root || first.socket() == second.socket() || first.pidFile() == second.pidFile() {
		t.Fatal("controllers share driver paths")
	}
	// A stale controller cannot block or be adopted by the next controller.
	if err := os.MkdirAll(first.root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first.pidFile(), []byte("99"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket, err := net.Listen("unix", first.socket())
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	for _, path := range []string{second.socket(), second.pidFile()} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("new controller path exists: %s", path)
		}
	}
	if err := second.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(first.pidFile()); err != nil || string(data) != "99" {
		t.Fatalf("other controller's PID file changed: %v", err)
	}
}

func TestLongDataDirectoryUsesPrivateShortControlPaths(t *testing.T) {
	dataDir := filepath.Join(shortRoot(t), strings.Repeat("x", 120))
	runner := &fakeRunner{hook: func(string, []string) (Output, error) {
		t.Fatal("constructor launched a command")
		return Output{}, nil
	}}
	first, err := New(Config{DataDir: dataDir, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(Config{DataDir: dataDir, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.root, dataDir+string(os.PathSeparator)) ||
		!strings.HasPrefix(first.controlRoot, filepath.Join(home, ".ao", "dev", "cua")+string(os.PathSeparator)) ||
		len(first.socket()) > 103 || first.socket() == second.socket() || first.pidFile() == second.pidFile() {
		t.Fatalf("invalid private runtime paths: %s %s %s", first.root, first.controlRoot, second.controlRoot)
	}
	for _, path := range []string{first.root, first.controlRoot} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("constructor created runtime storage: %s: %v", path, err)
		}
	}
}

func TestCloseRequiresPositiveProcessAbsence(t *testing.T) {
	f := newFixture(t)
	stopped := false
	provider := f.runner.hook
	f.runner.hook = func(executable string, args []string) (Output, error) {
		if len(args) == 7 && args[4] == "stop" {
			stopped = true
			return Output{}, os.Remove(f.adapter.pidFile())
		}
		if len(args) == 5 && args[3] == "end_session" {
			return jsonOutput(map[string]any{}), nil
		}
		return provider(executable, args)
	}
	f.adapter.started = func(_ context.Context, pid int) (time.Time, error) {
		if pid == 99 && stopped {
			return time.Time{}, errors.New("probe unavailable")
		}
		return f.born, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := f.adapter.Close(ctx); err == nil || f.adapter.driver.pid != 99 {
		t.Fatalf("unknown probe reported shutdown: %v", err)
	}
	f.adapter.started = func(_ context.Context, pid int) (time.Time, error) { return time.Time{}, process.ErrNotRunning }
	if err := f.adapter.Close(context.Background()); err != nil || f.adapter.driver.pid != 0 {
		t.Fatalf("proved shutdown did not clear ownership: %v", err)
	}
}

func TestCanceledLaunchKeepsDriverOwnershipForCleanup(t *testing.T) {
	f := newFixture(t)
	f.adapter.driver = driverIdentity{}
	if err := os.Remove(f.adapter.pidFile()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	alive := false
	var socket net.Listener
	f.runner.hook = func(executable string, args []string) (Output, error) {
		switch executable {
		case "/usr/bin/codesign":
			return Output{Stderr: []byte("Identifier=com.trycua.driver\nTeamIdentifier=YCK386LBJ7\n")}, nil
		case "/bin/launchctl":
			return Output{}, nil
		case "/usr/bin/open":
			alive = true
			if err := os.WriteFile(f.adapter.pidFile(), []byte("99"), 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			socket, err = net.Listen("unix", f.adapter.socket())
			if err != nil {
				t.Fatal(err)
			}
			cancel()
			return Output{}, nil
		}
		if reflect.DeepEqual(args, []string{"--version"}) {
			return Output{Stdout: []byte("cua-driver 0.34.0\n")}, nil
		}
		if len(args) == 5 && args[3] == "end_session" {
			return jsonOutput(map[string]any{}), nil
		}
		if len(args) == 7 && args[4] == "stop" {
			if args[5] != "--expected-pid" || args[6] != "99" {
				t.Fatal("stop escaped its owned driver", args)
			}
			alive = false
			return Output{}, errors.Join(socket.Close(), os.Remove(f.adapter.pidFile()))
		}
		t.Fatal("unexpected command", executable, args)
		return Output{}, nil
	}
	f.adapter.started = func(ctx context.Context, pid int) (time.Time, error) {
		if err := ctx.Err(); err != nil {
			return time.Time{}, err
		}
		if pid == 99 && !alive {
			return time.Time{}, process.ErrNotRunning
		}
		return f.born, nil
	}
	if err := f.adapter.ensureDriver(ctx); !errors.Is(err, context.Canceled) || f.adapter.pendingDriver.pid != 99 {
		t.Fatal("startup cancellation discarded ownership or cancellation", err)
	}
	if err := f.adapter.Close(context.Background()); err != nil || alive || f.adapter.pendingDriver.pid != 0 || f.adapter.launchIssued {
		t.Fatal("canceled launch was not reaped", err)
	}
}

func TestUnobservedLaunchCannotReportCleanupCompleteOrLaunchAgain(t *testing.T) {
	f := newFixture(t)
	f.adapter.driver = driverIdentity{}
	f.adapter.launchIssued = true
	if err := os.Remove(f.adapter.pidFile()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := len(f.runner.calls)
	if err := f.adapter.ensureDriver(ctx); !errors.Is(err, context.Canceled) || len(f.runner.calls) != before {
		t.Fatal("unobserved accepted launch was repeated", err)
	}
	if err := f.adapter.Close(ctx); !errors.Is(err, context.Canceled) || !f.adapter.launchIssued {
		t.Fatal("unknown launch incorrectly reported complete cleanup", err)
	}
}
