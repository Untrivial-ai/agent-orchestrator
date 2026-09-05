//go:build darwin

package conpty

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func TestDarwinLegacyRevalidationBindsLiveStatusToCurrentListenerOwner(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})

	hostIdentity, err := darwinProcessIdentity(context.Background(), os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	childIdentity, err := darwinProcessIdentity(context.Background(), child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	sess := &hostSession{pid: os.Getpid(), addr: listener.Addr().String()}
	status := StatusPayload{Alive: true, PID: child.Process.Pid, ProtocolVersion: 2}
	proof := legacyHostIdentityFingerprint{
		hostPID:        os.Getpid(),
		hostStartedAt:  hostIdentity.startedAt,
		childPID:       child.Process.Pid,
		childStartedAt: childIdentity.startedAt,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := revalidateLegacyHostIdentity(ctx, sess, status, proof); err != nil {
		t.Fatalf("revalidate live legacy host: %v", err)
	}

	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := revalidateLegacyHostIdentity(ctx, sess, status, proof); err == nil {
		t.Fatal("live legacy status remained trusted after its verified listener disappeared")
	}
}

// TestDarwinLegacyIdentityExternal is an opt-in upgrade probe for an actual
// protocol-v2 host left alive by a released desktop build. It is read-only:
// the test sends STATUS_REQ and verifies OS identity without terminal input or
// teardown.
func TestDarwinLegacyIdentityExternal(t *testing.T) {
	pidText := os.Getenv("AO_TEST_LEGACY_PTY_PID")
	if pidText == "" {
		t.Skip("set AO_TEST_LEGACY_PTY_* to probe a released protocol-v2 host")
	}
	pid, err := strconv.Atoi(pidText)
	if err != nil {
		t.Fatal(err)
	}
	sess := &hostSession{
		sessionID:    os.Getenv("AO_TEST_LEGACY_PTY_SESSION"),
		addr:         os.Getenv("AO_TEST_LEGACY_PTY_ADDR"),
		pid:          pid,
		launchID:     os.Getenv("AO_TEST_LEGACY_PTY_LAUNCH"),
		registeredAt: os.Getenv("AO_TEST_LEGACY_PTY_REGISTERED_AT"),
	}
	runtime := New(Options{})
	runtime.pidLiveness = func(candidate int) (bool, error) { return candidate == pid, nil }
	host, alive, err := runtime.connectVerifiedHost(context.Background(), sess, isAliveTimeout)
	if host != nil {
		_ = host.conn.Close()
	}
	if err != nil || !alive {
		t.Fatalf("released protocol-v2 host = (%v, %v), want authenticated alive", alive, err)
	}
}
