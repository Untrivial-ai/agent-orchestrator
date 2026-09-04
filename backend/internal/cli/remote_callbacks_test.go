package cli

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The daemon-local callbacks — `ao hooks` and `ao agent-process supervise` —
// report on a session owned by the daemon on THIS machine. They must never
// follow a remote target.
//
// Every assertion below that matters is on the REMOTE request log being empty.
// Output cannot distinguish "ignored the remote target" from "sent to the wrong
// host and got a plausible answer": measured, a hook against a remote daemon
// prints SESSION_NOT_FOUND and exits 0, which is indistinguishable from a
// harmless no-op unless you count requests.

// countingRemote is a stand-in remote daemon that answers nothing and counts
// every request that reaches it.
func countingRemote(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// superviseArgs builds an `ao agent-process supervise` invocation. The child is
// deliberately a command that exists on no OS: the supervisor publishes the
// exit report whether the child fails to start or runs to completion
// (runSupervisedProcess calls reportSupervisedExit on both paths), and WHERE
// that report goes is the whole subject of these tests. Nothing is spawned, so
// there is no binary to build, no PATH assumption, and no per-OS command.
func superviseArgs(extra ...string) []string {
	args := []string{"agent-process", "supervise", "--session", "ao-7", "--launch", "launch-1"}
	args = append(args, extra...)
	return append(args, "--", "ao-test-no-such-agent-command")
}

func hooksLogContents(t *testing.T, cfg testConfig) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cfg.dataDir, hooksLogName))
	if err != nil {
		t.Fatalf("hooks.log not written: %v", err)
	}
	return string(data)
}

func requireNoHooksLog(t *testing.T, cfg testConfig) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(cfg.dataDir, hooksLogName))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("hooks.log should not exist, got err=%v data=%q", err, data)
	}
}

// An exported AO_URL is ignored, not obeyed and not refused: these commands are
// spawned by the harness, so refusing would break the user's agent — the one
// thing hooks are designed never to do. The report goes to the LOCAL daemon and
// the ignore is recorded in hooks.log.
func TestDaemonLocalCallbacksIgnoreAOURL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		wantLog string
	}{
		{"hooks", []string{"hooks", "claude-code", "session-end"}, "ao hooks"},
		{"agent-process supervise", superviseArgs(), "ao agent-process supervise"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aoHome(t)
			cfg := setConfigEnv(t)
			t.Setenv("AO_SESSION_ID", "ao-7")
			t.Setenv("AO_TOKEN", "tok")
			remote, remoteHits := countingRemote(t)
			t.Setenv("AO_URL", remote.URL)

			local, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
			writeRunFileFor(t, cfg, local)

			_, errOut, err := executeCLI(t, Deps{
				In:           strings.NewReader(`{"reason":"logout"}`),
				ProcessAlive: func(int) bool { return true },
			}, tc.args...)
			if err != nil {
				t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
			}
			if *remoteHits != 0 {
				t.Errorf("%d request(s) reached the remote daemon, want 0", *remoteHits)
			}
			if capture.hits != 1 {
				t.Fatalf("local daemon got %d activity request(s), want 1", capture.hits)
			}
			if capture.path != "/api/v1/sessions/ao-7/activity" {
				t.Errorf("path = %q, want /api/v1/sessions/ao-7/activity", capture.path)
			}
			log := hooksLogContents(t, cfg)
			for _, want := range []string{tc.wantLog, "AO_URL=" + remote.URL, "ignored"} {
				if !strings.Contains(log, want) {
					t.Errorf("hooks.log missing %q:\n%s", want, log)
				}
			}
		})
	}
}

// A typed --url on a hidden, agent-invoked command is always misuse: refuse it,
// exit 2, and name the flag and the URL. Nothing is sent anywhere, and for
// supervise the child process is never started.
func TestDaemonLocalCallbacksRefuseURLFlag(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    func(url string) []string
		wantCmd string
	}{
		{
			name:    "hooks",
			args:    func(url string) []string { return []string{"hooks", "claude-code", "session-end", "--url", url} },
			wantCmd: "ao hooks",
		},
		{
			name:    "agent-process supervise",
			args:    func(url string) []string { return superviseArgs("--url", url) },
			wantCmd: "ao agent-process supervise",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aoHome(t)
			cfg := setConfigEnv(t)
			t.Setenv("AO_SESSION_ID", "ao-7")
			t.Setenv("AO_TOKEN", "tok")
			remote, remoteHits := countingRemote(t)

			local, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
			writeRunFileFor(t, cfg, local)

			_, _, err := executeCLI(t, Deps{
				In:           strings.NewReader(`{"reason":"logout"}`),
				ProcessAlive: func(int) bool { return true },
			}, tc.args(remote.URL)...)
			if err == nil {
				t.Fatalf("%s --url succeeded, want a refusal", tc.wantCmd)
			}
			for _, want := range []string{"--url", remote.URL, tc.wantCmd} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
			if got := ExitCode(err); got != 2 {
				t.Errorf("exit code = %d, want 2 (usage)", got)
			}
			if *remoteHits != 0 {
				t.Errorf("%d request(s) reached the remote daemon, want 0", *remoteHits)
			}
			if capture.hits != 0 {
				t.Errorf("%d request(s) reached the local daemon, want 0", capture.hits)
			}
			// A refusal is not an ignore, so it leaves no ignore line behind.
			requireNoHooksLog(t, cfg)
		})
	}
}

// The property that must not regress: an ignored AO_URL plus a dead local
// daemon still exits 0. Both facts land in hooks.log.
func TestDaemonLocalCallbacksStayBestEffortUnderIgnoredAOURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"hooks", []string{"hooks", "claude-code", "session-end"}},
		{"agent-process supervise", superviseArgs()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aoHome(t)
			cfg := setConfigEnv(t) // no run-file: the local daemon is down
			t.Setenv("AO_SESSION_ID", "ao-7")
			t.Setenv("AO_TOKEN", "tok")
			remote, remoteHits := countingRemote(t)
			t.Setenv("AO_URL", remote.URL)

			_, _, err := executeCLI(t, Deps{
				In: strings.NewReader(`{"reason":"logout"}`),
			}, tc.args...)
			if err != nil {
				t.Fatalf("must exit 0 when the local daemon is down, got: %v", err)
			}
			if *remoteHits != 0 {
				t.Errorf("%d request(s) reached the remote daemon, want 0", *remoteHits)
			}
			log := hooksLogContents(t, cfg)
			for _, want := range []string{"AO_URL=" + remote.URL, "not running"} {
				if !strings.Contains(log, want) {
					t.Errorf("hooks.log missing %q:\n%s", want, log)
				}
			}
		})
	}
}

// With no remote target, behavior is what it was: the report reaches the local
// daemon and nothing is written to hooks.log.
func TestDaemonLocalCallbacksUnchangedWithoutRemoteTarget(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"hooks", []string{"hooks", "claude-code", "session-end"}},
		{"agent-process supervise", superviseArgs()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aoHome(t) // clears AO_URL / AO_TOKEN
			cfg := setConfigEnv(t)
			t.Setenv("AO_SESSION_ID", "ao-7")
			local, capture := activityServer(t, http.StatusOK, `{"ok":true}`)
			writeRunFileFor(t, cfg, local)

			_, errOut, err := executeCLI(t, Deps{
				In:           strings.NewReader(`{"reason":"logout"}`),
				ProcessAlive: func(int) bool { return true },
			}, tc.args...)
			if err != nil {
				t.Fatalf("unexpected error: %v\nstderr=%s", err, errOut)
			}
			if capture.hits != 1 || capture.path != "/api/v1/sessions/ao-7/activity" {
				t.Fatalf("local daemon got %d request(s) at %q, want 1 at /api/v1/sessions/ao-7/activity",
					capture.hits, capture.path)
			}
			requireNoHooksLog(t, cfg)
		})
	}
}
