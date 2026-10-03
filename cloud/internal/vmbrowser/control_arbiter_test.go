package vmbrowser

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestControlArbiterUserLeaseBlocksAgentThenExpires(t *testing.T) {
	arbiter := NewControlArbiter(nil)
	arbiter.lease = 40 * time.Millisecond
	arbiter.agentWait = 500 * time.Millisecond
	if !arbiter.TryUser(true) {
		t.Fatal("user input was rejected while idle")
	}
	release, err := arbiter.AcquireAgent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if arbiter.Owner() != ControlAgent {
		t.Fatalf("owner = %q, want agent", arbiter.Owner())
	}
}

func TestControlArbiterRejectsUserDuringAgentMutation(t *testing.T) {
	arbiter := NewControlArbiter(nil)
	release, err := arbiter.AcquireAgent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if arbiter.TryUser(true) {
		t.Fatal("user input must be rejected during an agent mutation")
	}
	release()
	if !arbiter.TryUser(true) {
		t.Fatal("user input must resume after the agent releases control")
	}
}

func TestControlArbiterTimesOutBehindActiveUser(t *testing.T) {
	arbiter := NewControlArbiter(nil)
	arbiter.lease = time.Second
	arbiter.agentWait = 30 * time.Millisecond
	arbiter.TryUser(true)
	if _, err := arbiter.AcquireAgent(context.Background()); !errors.Is(err, ErrUserControlActive) {
		t.Fatalf("error = %v, want ErrUserControlActive", err)
	}
}

func TestControlArbiterDisconnectAndCrashReleaseOwnership(t *testing.T) {
	arbiter := NewControlArbiter(nil)
	if !arbiter.TryUser(true) || arbiter.Owner() != ControlUser {
		t.Fatal("user lease was not acquired")
	}
	arbiter.ReleaseUser()
	if arbiter.Owner() != ControlIdle {
		t.Fatalf("owner after disconnect = %q", arbiter.Owner())
	}
	if !arbiter.TryUser(true) {
		t.Fatal("user lease was not reacquired")
	}
	arbiter.Reset()
	if arbiter.Owner() != ControlIdle {
		t.Fatalf("owner after crash reset = %q", arbiter.Owner())
	}
}
