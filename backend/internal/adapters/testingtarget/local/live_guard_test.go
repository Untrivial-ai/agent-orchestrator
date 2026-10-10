package local

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/runfile"
)

func TestStartRefusesLiveDaemonPort(t *testing.T) {
	f, spec := preparedStartFixture(t, false)
	path := filepath.Join(f.s.root, ".ao", "running.json")
	if err := runfile.Write(path, runfile.Info{PID: 99, Port: f.s.port, StartedAt: f.times[99]}); err != nil {
		t.Fatal(err)
	}
	f.a.ops.start = func(string, string, []string, *os.File) (int, error) {
		t.Fatal("live-port collision launched Electron")
		return 0, nil
	}
	if _, err := f.a.Start(t.Context(), spec); err == nil || err.Error() != "target port conflicts with live AO daemon" {
		t.Fatalf("live-port collision accepted or lost its cause: %v", err)
	}
	if len(f.signals) != 0 {
		t.Fatal("live-port rejection signalled a process")
	}
}

func TestStartFailsIfLiveDaemonBirthChanges(t *testing.T) {
	f, spec := preparedStartFixture(t, false)
	path := filepath.Join(f.s.root, ".ao", "running.json")
	if err := runfile.Write(path, runfile.Info{PID: 99, Port: 3001, StartedAt: f.times[99]}); err != nil {
		t.Fatal(err)
	}
	start := f.a.ops.start
	f.a.ops.start = func(exe, cwd string, env []string, log *os.File) (int, error) {
		pid, err := start(exe, cwd, env, log)
		f.times[99] = f.times[99].Add(time.Second)
		return pid, err
	}
	t.Cleanup(func() {
		if f.s.log != nil {
			_ = f.s.log.Close()
		}
	})
	if _, err := f.a.Start(t.Context(), spec); err == nil || !strings.Contains(err.Error(), "live AO daemon guard: kernel process identity changed; stopping target") {
		t.Fatalf("admission accepted changed live daemon birth: %v", err)
	}
	for _, pid := range f.signals {
		if pid == 99 {
			t.Fatal("failed admission signalled live daemon")
		}
	}
}

func TestLiveDaemonGuardDetectsReplacementAndPIDReuse(t *testing.T) {
	for _, change := range []string{"pid", "birth", "recorded start", "removed"} {
		t.Run(change, func(t *testing.T) {
			f := fixture(t)
			path := filepath.Join(f.s.root, ".ao", "running.json")
			info := runfile.Info{PID: 99, Port: 3001, StartedAt: f.times[99]}
			if err := runfile.Write(path, info); err != nil {
				t.Fatal(err)
			}
			g, err := f.a.observeLiveDaemon()
			if err != nil {
				t.Fatal(err)
			}
			if err := f.a.checkLiveDaemon(g); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "pid":
				info.PID = 12
			case "birth":
				f.times[99] = f.times[99].Add(time.Second)
			case "recorded start":
				info.StartedAt = info.StartedAt.Add(time.Second)
			case "removed":
				if err := runfile.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if change != "removed" {
				if err := runfile.Write(path, info); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.a.checkLiveDaemon(g); err == nil || !strings.Contains(err.Error(), "identity changed") {
				t.Fatalf("guard admitted %s: %v", change, err)
			}
			if len(f.signals) != 0 {
				t.Fatal("guard signalled a process")
			}
		})
	}
}

func TestCleanupFailsIfLiveDaemonChangedWithoutSignallingIt(t *testing.T) {
	f := fixture(t)
	path := filepath.Join(f.s.root, ".ao", "running.json")
	if err := runfile.Write(path, runfile.Info{PID: 99, Port: 3001, StartedAt: f.times[99]}); err != nil {
		t.Fatal(err)
	}
	g, err := f.a.observeLiveDaemon()
	if err != nil {
		t.Fatal(err)
	}
	f.s.liveGuard = g
	f.times[99] = f.times[99].Add(time.Second)
	result, err := f.a.Stop(context.Background(), f.s.target)
	if err == nil || !strings.Contains(err.Error(), "live AO daemon guard: kernel process identity changed; stopping target") || len(result.Leftovers) == 0 {
		t.Fatalf("changed live daemon accepted: %+v %v", result, err)
	}
	for _, pid := range f.signals {
		if pid == 99 {
			t.Fatal("cleanup touched live daemon")
		}
	}
}
