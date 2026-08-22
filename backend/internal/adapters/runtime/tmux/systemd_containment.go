package tmux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const processContainmentSystemd = "systemd"

// processContainment is deliberately private to the tmux adapter. The runtime
// port carries generation identity, while Linux containment mechanics remain
// an implementation detail of the opt-in tmux backend.
type processContainment interface {
	Validate(ctx context.Context) error
	WrapCommand(shellPath, launchCmd, unit string, stopGrace time.Duration) string
	WaitActive(ctx context.Context, unit string) error
	Release(ctx context.Context, unit string) error
}

type unavailableContainment struct {
	reason string
}

func (u unavailableContainment) Validate(context.Context) error {
	return errors.New(u.reason)
}

func (u unavailableContainment) WrapCommand(_, launchCmd, _ string, _ time.Duration) string {
	return launchCmd
}

func (u unavailableContainment) WaitActive(context.Context, string) error {
	return errors.New(u.reason)
}

func (u unavailableContainment) Release(context.Context, string) error {
	return errors.New(u.reason)
}

type systemdContainment struct {
	runner    runner
	timeout   time.Duration
	stopGrace time.Duration
	poll      time.Duration
}

func newSystemdContainment(r runner, timeout, stopGrace time.Duration) *systemdContainment {
	return &systemdContainment{
		runner:    r,
		timeout:   timeout,
		stopGrace: stopGrace,
		poll:      50 * time.Millisecond,
	}
}

// Validate checks the explicit backend before tmux creates a session. It does
// not create a unit: systemd-run is exercised only by the worker pane itself.
func (s *systemdContainment) Validate(ctx context.Context) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("systemd user scopes are Linux-only (GOOS=%s)", runtime.GOOS)
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return fmt.Errorf("systemd-run is unavailable: %w", err)
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemctl is unavailable: %w", err)
	}
	if _, err := s.run(ctx, "systemctl", "--user", "show-environment"); err != nil {
		return fmt.Errorf("systemd user manager is unavailable: %w", err)
	}
	return nil
}

// WrapCommand replaces the pane's shell command with a systemd-run scope. The
// existing launch command remains one shell argument, preserving all existing
// environment filtering, cwd guards, supervisor arguments, and keep-alive
// behavior.
func (s *systemdContainment) WrapCommand(shellPath, launchCmd, unit string, stopGrace time.Duration) string {
	return strings.Join([]string{
		"exec",
		"systemd-run",
		"--user",
		"--scope",
		"--collect",
		"--unit=" + unit,
		"--property=KillMode=control-group",
		"--property=TimeoutStopSec=" + stopGrace.String(),
		"--property=SendSIGKILL=yes",
		"--",
		shellQuote(shellPath),
		"-c",
		shellQuote(launchCmd),
	}, " ")
}

// WaitActive verifies that the scope started by the pane is the expected
// systemd unit before Create reports success. A short activating/not-found
// window is normal while systemd processes the transient-unit request.
func (s *systemdContainment) WaitActive(ctx context.Context, unit string) error {
	waitCtx := ctx
	if s.timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	for {
		state, err := s.state(waitCtx, unit)
		if err != nil {
			if waitCtx.Err() != nil {
				return fmt.Errorf("scope %s did not become active: %w", unit, waitCtx.Err())
			}
			return fmt.Errorf("read scope state: %w", err)
		}
		if state.active() {
			return nil
		}
		if !state.starting() {
			return fmt.Errorf("scope %s is %s/%s/%s, want active", unit, state.load, state.activeState, state.sub)
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("scope %s did not become active: %w", unit, waitCtx.Err())
		case <-time.After(s.poll):
		}
	}
}

// Release asks systemd to terminate the exact scope. systemd owns the
// TERM/grace/KILL policy; AO only waits for an authoritative inactive/dead or
// unloaded state. A missing unit is idempotent success.
func (s *systemdContainment) Release(ctx context.Context, unit string) error {
	releaseCtx := ctx
	if s.timeout > 0 || s.stopGrace > 0 {
		budget := s.timeout + s.stopGrace + time.Second
		if budget <= 0 {
			budget = 10 * time.Second
		}
		var cancel context.CancelFunc
		releaseCtx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}
	_, stopErr := s.run(releaseCtx, "systemctl", "--user", "stop", unit)
	for {
		state, stateErr := s.state(releaseCtx, unit)
		if stateErr == nil {
			if state.released() {
				return nil
			}
			if !state.known() {
				return fmt.Errorf("scope %s has malformed state %s/%s/%s", unit, state.load, state.activeState, state.sub)
			}
		} else if stopErr != nil {
			return fmt.Errorf("stop scope: %w; read final state: %w", stopErr, stateErr)
		} else {
			return fmt.Errorf("read final scope state: %w", stateErr)
		}

		select {
		case <-releaseCtx.Done():
			if stopErr != nil {
				return fmt.Errorf("stop scope: %w; final state did not release: %w", stopErr, releaseCtx.Err())
			}
			return fmt.Errorf("scope %s remained %s/%s/%s: %w", unit, state.load, state.activeState, state.sub, releaseCtx.Err())
		case <-time.After(s.poll):
		}
	}
}

type systemdUnitState struct {
	load        string
	activeState string
	sub         string
}

func (s systemdUnitState) active() bool {
	return s.load == "loaded" && s.activeState == "active"
}

func (s systemdUnitState) starting() bool {
	return s.load == "not-found" || s.activeState == "activating"
}

func (s systemdUnitState) released() bool {
	return s.load == "not-found" || s.activeState == "inactive" || s.sub == "dead"
}

func (s systemdUnitState) known() bool {
	return s.load != "" && s.activeState != "" && s.sub != ""
}

func (s *systemdContainment) state(ctx context.Context, unit string) (systemdUnitState, error) {
	out, err := s.run(ctx, "systemctl", "--user", "show", "--no-pager", "--property=LoadState", "--property=ActiveState", "--property=SubState", unit)
	if err != nil {
		return systemdUnitState{}, err
	}
	return parseSystemdUnitState(string(out))
}

func parseSystemdUnitState(output string) (systemdUnitState, error) {
	var state systemdUnitState
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			return systemdUnitState{}, fmt.Errorf("invalid systemd state line %q", line)
		}
		switch key {
		case "LoadState":
			state.load = value
		case "ActiveState":
			state.activeState = value
		case "SubState":
			state.sub = value
		}
	}
	if !state.known() {
		return systemdUnitState{}, fmt.Errorf("missing systemd state fields in %q", strings.TrimSpace(output))
	}
	return state, nil
}

func (s *systemdContainment) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	commandCtx := ctx
	if s.timeout > 0 {
		var cancel context.CancelFunc
		commandCtx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	out, err := s.runner.Run(commandCtx, nil, name, args...)
	if commandCtx.Err() != nil {
		return out, commandCtx.Err()
	}
	if err != nil {
		return out, commandError{err: err, output: strings.TrimSpace(string(out))}
	}
	return out, nil
}

func containmentUnitName(id, launchID string) string {
	sum := sha256.Sum256([]byte(launchID))
	return "ao-session-" + SessionName(id) + "-" + hex.EncodeToString(sum[:8]) + ".scope"
}
