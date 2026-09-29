package mobilebridge

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// KeepAwakeSupported reports whether this platform can hold the machine awake
// for Connect Mobile. Only macOS: it is where idle sleep cuts a paired phone
// off, and caffeinate ships with every install.
func KeepAwakeSupported() bool { return runtime.GOOS == "darwin" }

// KeepAwake prevents idle system sleep while Connect Mobile is on, so a phone
// can keep reaching the daemon after the user walks away from the desk.
//
// It runs `caffeinate -i -w <daemon pid>`: -i holds only the idle-system-sleep
// assertion, so the display can still turn off and the screen can still lock,
// and -w ends caffeinate on its own if the daemon dies without calling Stop.
// A closed MacBook lid still sleeps; nothing here overrides that.
type KeepAwake struct {
	binary     string
	ownerPID   int
	hasBattery bool

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{}
}

// NewKeepAwake returns a controller that ties its assertion to ownerPID.
func NewKeepAwake(ownerPID int) *KeepAwake {
	return &KeepAwake{binary: "caffeinate", ownerPID: ownerPID, hasBattery: detectBattery()}
}

// detectBattery reports whether this Mac has an internal battery, which is how
// the settings page knows to warn that closing the lid still sleeps it.
func detectBattery() bool {
	ctx, cancel := context.WithTimeout(context.Background(), processProbeTimeout)
	defer cancel()
	out, err := aoprocess.CommandContext(ctx, "pmset", "-g", "batt").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "InternalBattery")
}

// HasBattery reports whether this machine is a laptop.
func (k *KeepAwake) HasBattery() bool { return k.hasBattery }

// Active reports whether the sleep assertion is currently held.
func (k *KeepAwake) Active() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.cmd != nil
}

// Start holds the assertion. Idempotent.
func (k *KeepAwake) Start() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cmd != nil {
		return nil
	}
	cmd := aoprocess.Command(k.binary, "-i", "-w", strconv.Itoa(k.ownerPID))
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	k.cmd, k.done = cmd, done
	go func() {
		_ = cmd.Wait()
		k.mu.Lock()
		if k.cmd == cmd {
			k.cmd, k.done = nil, nil
		}
		k.mu.Unlock()
		close(done)
	}()
	return nil
}

// Stop releases the assertion. Idempotent.
func (k *KeepAwake) Stop() {
	k.mu.Lock()
	cmd, done := k.cmd, k.done
	k.mu.Unlock()
	if cmd == nil {
		return
	}
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(processProbeTimeout):
	}
}
