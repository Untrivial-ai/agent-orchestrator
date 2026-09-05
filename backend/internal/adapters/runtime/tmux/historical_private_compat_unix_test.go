//go:build !windows

package tmux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestSocketTargetReproducesHistoricalLongPrivateAddress(t *testing.T) {
	targetDir := filepath.Join(t.TempDir(), strings.Repeat("deep-runtime-directory-", 6))
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rawSocket := filepath.Join(targetDir, "tmux-0123456789abcdef0123456789abcdef.sock")
	if len([]byte(rawSocket)) <= maxUnixSocketPathBytes {
		t.Fatalf("precondition: raw socket path is only %d bytes: %q", len([]byte(rawSocket)), rawSocket)
	}

	argv, err := (socketTarget{kind: socketTargetPath, value: rawSocket}).argv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	canonicalDir, err := filepath.EvalSymlinks(targetDir)
	if err != nil {
		t.Fatal(err)
	}
	canonicalSocket := filepath.Join(canonicalDir, filepath.Base(rawSocket))
	digest := sha256.Sum256([]byte(canonicalSocket))
	wantAddress := filepath.Join(
		"/tmp",
		"ao-tmux-"+strconv.Itoa(os.Getuid()),
		hex.EncodeToString(digest[:socketAliasIdentityBytes]),
		filepath.Base(rawSocket),
	)
	want := []string{"-S", wantAddress, "-f", os.DevNull}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("socket argv = %#v, want historical alias %#v", argv, want)
	}
	if len([]byte(wantAddress)) > maxUnixSocketPathBytes {
		t.Fatalf("alias path is %d bytes, maximum is %d", len([]byte(wantAddress)), maxUnixSocketPathBytes)
	}
	aliasDir := filepath.Dir(wantAddress)
	t.Cleanup(func() { _ = os.Remove(aliasDir) })
	if got, err := os.Readlink(aliasDir); err != nil || got != canonicalDir {
		t.Fatalf("alias target = %q, err=%v; want %q", got, err, canonicalDir)
	}

	// Address selection is deterministic across daemon replacements.
	second, err := (socketTarget{kind: socketTargetPath, value: rawSocket}).argv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("replacement socket argv = %#v, want %#v", second, want)
	}
}

func TestSocketTargetRejectsPrecreatedForeignHistoricalAlias(t *testing.T) {
	targetDir := filepath.Join(t.TempDir(), strings.Repeat("deep-runtime-directory-", 6))
	foreignDir := t.TempDir()
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	rawSocket := filepath.Join(targetDir, "tmux-0123456789abcdef0123456789abcdef.sock")
	argv, err := (socketTarget{kind: socketTargetPath, value: rawSocket}).argv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	aliasDir := filepath.Dir(argv[1])
	if err := os.Remove(aliasDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreignDir, aliasDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(aliasDir) })

	_, err = (socketTarget{kind: socketTargetPath, value: rawSocket}).argv(context.Background())
	if err == nil || !strings.Contains(err.Error(), "points to") {
		t.Fatalf("foreign historical alias error = %v, want target mismatch", err)
	}
}

func TestResolveRuntimeHandleRecognizesExactPrivateReleaseGrammar(t *testing.T) {
	const (
		runFile          = "/tmp/ao/running.json"
		historicalSocket = "/tmp/ao/tmux-historical.sock"
		sessionID        = "sess-1"
		launchID         = "launch-owned"
	)
	command := historicalPrivatePaneCommand("/tmp/worktree", "/opt/ao", sessionID, launchID)
	if strings.Contains(command, "export AO_") {
		t.Fatalf("historical fixture unexpectedly embeds AO environment: %q", command)
	}
	if _, ok := paneSupervisorIdentity(command, sessionID); ok {
		t.Fatal("no-export historical grammar was treated as modern full provenance")
	}
	if got, ok := historicalPrivatePaneSupervisorIdentity(command, sessionID); !ok || got != launchID {
		t.Fatalf("historical private identity = (%q, %v), want (%q, true)", got, ok, launchID)
	}

	missing := fakeRunnerResult{out: []byte("can't find session: " + sessionID), err: &exec.ExitError{}}
	fr := &namespaceProbeRunner{results: map[string]fakeRunnerResult{
		"named:ao":                 missing,
		"path:" + historicalSocket: {out: []byte(command + "\n")},
		"default":                  missing,
	}}
	r := New(Options{
		Binary:           "bundled-tmux-test",
		LegacyBinary:     "system-tmux-test",
		SocketName:       "ao",
		LegacySocketPath: historicalSocket,
		RunFilePath:      runFile,
		Timeout:          time.Second,
	})
	r.runner = fr
	owner := ports.SupervisedProcessRef{SessionID: sessionID, LaunchID: launchID}
	resolved, found, err := r.ResolveRuntimeHandle(
		context.Background(),
		ports.RuntimeHandle{ID: sessionID},
		owner,
	)
	if err != nil || !found {
		t.Fatalf("ResolveRuntimeHandle = (%q, %v, %v), want exact historical owner", resolved.ID, found, err)
	}
	route, err := decodeRuntimeHandle(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if route.target.kind != socketTargetPath || route.target.value != historicalSocket || route.owner != owner {
		t.Fatalf("resolved route = %+v, want owner-fenced historical socket", route)
	}
}

func TestHistoricalPrivateGrammarNeverAdoptsStaleLaunchOrDestroysReplacement(t *testing.T) {
	const (
		runFile          = "/tmp/ao/running.json"
		historicalSocket = "/tmp/ao/tmux-historical.sock"
		sessionID        = "sess-1"
		durableLaunch    = "launch-durable"
		foreignLaunch    = "launch-foreign"
	)
	owner := ports.SupervisedProcessRef{SessionID: sessionID, LaunchID: durableLaunch}
	handle, err := qualifiedRuntimeHandleForOwner(
		sessionID,
		socketTarget{kind: socketTargetPath, value: historicalSocket},
		owner,
	)
	if err != nil {
		t.Fatal(err)
	}
	missing := fakeRunnerResult{out: []byte("can't find session: " + sessionID), err: &exec.ExitError{}}
	foreignCommand := historicalPrivatePaneCommand("/tmp/worktree", "/opt/ao", sessionID, foreignLaunch)
	fr := &namespaceCommandRunner{results: map[string]fakeRunnerResult{
		"path:" + historicalSocket + "/has-session": {},
		"path:" + historicalSocket + "/list-panes":  {out: []byte(foreignCommand + "\n")},
		"named:ao/has-session":                      missing,
		"default/has-session":                       missing,
	}}
	r := New(Options{
		Binary:           "bundled-tmux-test",
		LegacyBinary:     "system-tmux-test",
		SocketName:       "ao",
		LegacySocketPath: historicalSocket,
		RunFilePath:      runFile,
		Timeout:          time.Second,
	})
	r.runner = fr

	err = r.Destroy(context.Background(), handle)
	if !errors.Is(err, ports.ErrRuntimeProbeInconclusive) {
		t.Fatalf("Destroy error = %v, want stale-launch ownership failure", err)
	}
	for _, call := range fr.calls {
		if tmuxSubcommand(call.args) == "kill-session" {
			t.Fatalf("foreign replacement was destroyed: %+v", call)
		}
	}
}

func TestRuntimeIntegrationRecoversPrivateReleaseThroughHistoricalLongAlias(t *testing.T) {
	systemTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}

	fixtureRoot, err := os.MkdirTemp("/tmp", "ao-tmux-long-private-recovery-")
	if err != nil {
		t.Fatal(err)
	}
	deepRuntimeDir := filepath.Join(fixtureRoot, strings.Repeat("deep-runtime-directory-", 6))
	if err := os.MkdirAll(deepRuntimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runFile := filepath.Join(deepRuntimeDir, "running.json")
	rawSocket := historicalRawSocketPath(runFile)
	if len([]byte(rawSocket)) <= maxUnixSocketPathBytes {
		t.Fatalf("precondition: historical raw socket is only %d bytes: %q", len([]byte(rawSocket)), rawSocket)
	}
	address, err := privateSocketAddress(context.Background(), rawSocket)
	if err != nil {
		t.Fatal(err)
	}

	tmuxTmpDir := filepath.Join(fixtureRoot, "tmux-tmp")
	if err := os.Mkdir(tmuxTmpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", tmuxTmpDir)
	sessionID := fmt.Sprintf("long-private-%d", os.Getpid())
	launchID := "private-release-launch"
	socketName := fmt.Sprintf("ao-long-private-%d", os.Getpid())
	fakeAO := filepath.Join(fixtureRoot, "ao-fixture")
	if err := os.WriteFile(fakeAO, []byte("#!/bin/sh\nexec /bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	command := historicalPrivatePaneCommand(fixtureRoot, fakeAO, sessionID, launchID)

	t.Cleanup(func() {
		_ = exec.Command(systemTmux, "-S", address, "-f", os.DevNull, "kill-server").Run()
		_ = exec.Command(systemTmux, "-L", socketName, "kill-server").Run()
		_ = os.Remove(filepath.Dir(address))
		_ = os.RemoveAll(fixtureRoot)
	})
	if out, startErr := exec.Command(
		systemTmux,
		"-S", address,
		"-f", os.DevNull,
		"new-session", "-d", "-s", sessionID,
		"-x", "220", "-y", "50", "-c", fixtureRoot,
		"/bin/sh", "-c", command,
	).CombinedOutput(); startErr != nil {
		t.Fatalf("start historical long-path tmux session: %v: %s", startErr, out)
	}
	// Same-name foreign state in today's namespace proves that recovery is
	// selecting immutable provenance, not merely the tmux session name.
	if out, startErr := exec.Command(
		systemTmux,
		"-L", socketName,
		"new-session", "-d", "-s", sessionID,
		"/bin/sleep", "60",
	).CombinedOutput(); startErr != nil {
		t.Fatalf("start foreign named-socket session: %v: %s", startErr, out)
	}
	if _, err := os.Lstat(rawSocket); err != nil {
		t.Fatalf("historical socket inode was not created beside running.json: %v", err)
	}

	r := New(Options{
		Binary:           systemTmux,
		LegacyBinary:     systemTmux,
		SocketName:       socketName,
		LegacySocketPath: rawSocket,
		RunFilePath:      runFile,
		Timeout:          5 * time.Second,
	})
	r.enterDelay = 0
	owner := ports.SupervisedProcessRef{SessionID: domain.SessionID(sessionID), LaunchID: launchID}
	resolved, found, err := r.ResolveRuntimeHandle(
		context.Background(),
		ports.RuntimeHandle{ID: sessionID},
		owner,
	)
	if err != nil || !found {
		t.Fatalf("ResolveRuntimeHandle = (%q, %v, %v), want long-path historical owner", resolved.ID, found, err)
	}
	route, err := decodeRuntimeHandle(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if route.target.value != rawSocket || route.owner != owner {
		t.Fatalf("resolved route = %+v, want raw durable socket and exact owner", route)
	}
	attachCtx, cancelAttach := context.WithCancel(context.Background())
	stream, err := r.Attach(attachCtx, resolved, 50, 220)
	if err != nil {
		cancelAttach()
		t.Fatalf("Attach through immutable recovered session id: %v", err)
	}
	if err := stream.Close(); err != nil {
		cancelAttach()
		t.Fatalf("close historical attach stream: %v", err)
	}
	cancelAttach()
	if err := r.SendMessage(context.Background(), resolved, "echo historical-long-alias-ok"); err != nil {
		t.Fatalf("SendMessage through recovered alias: %v", err)
	}
	out := waitForOutput(t, r, resolved, "historical-long-alias-ok", 5*time.Second)
	if !strings.Contains(out, "historical-long-alias-ok") {
		t.Fatalf("historical private output = %q, want marker", out)
	}

	if err := r.Destroy(context.Background(), resolved); err != nil {
		t.Fatalf("destroy exact recovered owner: %v", err)
	}
	if out, err := exec.Command(systemTmux, "-L", socketName, "has-session", "-t", "="+sessionID).CombinedOutput(); err != nil {
		t.Fatalf("foreign same-name session was not preserved: %v: %s", err, out)
	}
}

func historicalRawSocketPath(runFile string) string {
	absRunFile, err := filepath.Abs(runFile)
	if err != nil {
		absRunFile = filepath.Clean(runFile)
	}
	digest := sha256.Sum256([]byte(absRunFile))
	return filepath.Join(filepath.Dir(absRunFile), "tmux-"+hex.EncodeToString(digest[:16])+".sock")
}

// historicalPrivatePaneCommand is derived from buildLaunchCommand at #4393.
// AO-specific values lived in tmux's session environment, so this immutable
// pane command contains supervisor argv but no AO_* exports.
func historicalPrivatePaneCommand(workspace, executable, sessionID, launchID string) string {
	argv := []string{
		executable,
		"agent-process", "supervise",
		"--session", sessionID,
		"--launch", launchID,
		"--", "/bin/sh",
	}
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = shellQuote(arg)
	}
	return "cd " + shellQuote(workspace) + " || exit; " +
		"unset NO_COLOR; " +
		"export COLORTERM='truecolor'; " +
		strings.Join(quoted, " ") +
		"; exec cat >/dev/null"
}
