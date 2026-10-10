package cua

import (
	"context"
	"errors"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestBindWindowDoesNotAdmitRefusedDriver(t *testing.T) {
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
				return time.Time{}, errors.New("missing")
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
				if _, err := a.BindWindow(context.Background(), target); err == nil {
					t.Fatal("refused permissions admitted a window")
				}
				if a.driver.pid != 0 || a.pendingDriver.pid != 99 || permissionCalls != retry+1 || windowCalls != 0 {
					t.Fatalf("refusal lost ownership or bypassed checks: driver %+v pending %+v permissions %d windows %d", a.driver, a.pendingDriver, permissionCalls, windowCalls)
				}
			}
			if err := socket.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := a.BindWindow(context.Background(), target); !errors.Is(err, ErrRefused) || permissionCalls != 2 || windowCalls != 0 {
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
