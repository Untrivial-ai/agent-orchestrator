// host_main.go is the RunHost entrypoint for the "ao pty-host" subcommand.
// It is cross-platform: the loopback TCP bind and signal wiring work on all
// OSes; native PTY creation is OS-gated via build tags.
package conpty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// RunHost is the "ao pty-host" entrypoint. argv is everything after the
// subcommand name: <sessionId> <cwd> <shellCmd> [shellArg...]
//
// It binds 127.0.0.1:0 (OS assigns the port), creates the native PTY, prints
// "READY:<pid> <port>\n" to stdout (the parent process reads this to learn the
// port), installs SIGTERM/SIGINT handlers, then runs Serve. Returns a process
// exit code.
//
// ponytail: loopback bind only. The immutable per-host random token prevents a
// stale registry route from authorizing an unrelated listener; same-user local
// processes remain inside AO's host trust boundary.
func RunHost(args []string, stdout io.Writer) int {
	if len(args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: ao pty-host <sessionId> <cwd> <shellCmd> [shellArg...]\n")
		return 1
	}

	sessionID := args[0]
	cwd := args[1]
	shellCmd := args[2]
	shellArgs := args[3:]
	hostToken, identityErr := ptyHostTokenFromEnvironment()
	if identityErr != nil {
		fmt.Fprintf(os.Stderr, "pty-host [%s]: %v\n", sessionID, identityErr)
		return 1
	}
	launchID := strings.TrimSpace(os.Getenv(runtimeLaunchIDEnv))
	// The token authenticates the host process, not the supervised workload.
	// Remove it before creating the PTY so agent processes cannot inherit the
	// recovery credential.
	scrubPTYHostTokenEnvironment()
	if err := os.Chdir(cwd); err != nil {
		fmt.Fprintf(os.Stderr, "pty-host [%s]: chdir %s: %v\n", sessionID, cwd, err)
		return 1
	}

	// Bind before creating the PTY so we can report READY atomically.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "pty-host [%s]: listen: %v\n", sessionID, err)
		return 1
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		fmt.Fprintf(os.Stderr, "pty-host [%s]: listener is not TCP\n", sessionID)
		return 1
	}
	port := tcpAddr.Port

	pty, err := newConPTY(cwd, shellCmd, shellArgs)
	if err != nil {
		_ = ln.Close()
		fmt.Fprintf(os.Stderr, "pty-host [%s]: newConPTY: %v\n", sessionID, err)
		return 1
	}

	// Print READY after both the listener and the PTY are up.
	_, _ = fmt.Fprintf(stdout, "READY:%d %d\n", pty.PID(), port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Install signal handlers so SIGTERM/SIGINT trigger graceful shutdown.
	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		select {
		case sig := <-sigC:
			fmt.Fprintf(os.Stderr, "pty-host [%s]: signal %v, shutting down\n", sessionID, sig)
			cancel()
		case <-ctx.Done():
		}
	}()

	ring := NewRing()
	cfg := ServeConfig{
		SessionID: sessionID,
		LaunchID:  launchID,
		HostPID:   os.Getpid(),
		HostToken: hostToken,
		Listener:  ln,
		PTY:       pty,
		Ring:      ring,
	}
	if err := Serve(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "pty-host [%s]: serve: %v\n", sessionID, err)
		return 1
	}
	return 0
}

// ptyHostTokenFromEnvironment keeps the production rule strict: every newly
// launched host requires the random token that its registry entry persists.
// Historical protocol-v2 behavior belongs only in the external test fixture.
func ptyHostTokenFromEnvironment() (string, error) {
	hostToken := strings.TrimSpace(os.Getenv(runtimeHostTokenEnv))
	if hostToken != "" {
		return hostToken, nil
	}
	return "", errors.New("missing host identity token")
}

func scrubPTYHostTokenEnvironment() {
	_ = os.Unsetenv(runtimeHostTokenEnv)
}
