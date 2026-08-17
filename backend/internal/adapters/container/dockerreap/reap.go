// Package dockerreap implements ports.ContainerReaper by shelling out to the
// docker CLI. It force-removes containers labeled ao.session=<id>, skipping
// any also labeled ao.spare=true — the opt-out for deliberately shared
// substrate (a shared postgres, a registry) a worker's task explicitly wants
// to survive session end.
//
// This is the container leg of "tear down what the worker owns on session
// end" (see the process reaper in observe/reaper and the worktree teardown in
// session_manager.Manager.Kill). Every failure mode here is designed to spare
// rather than guess: a missing docker binary, a probe error, or an ambiguous
// label state all resolve to "remove nothing" — never to a false-positive
// reap. A live worker's database container surviving a bug is recoverable;
// force-removing a live worker's database is not.
package dockerreap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// SessionLabel is the label key a worker's own `docker run` should set to
// `--label ao.session=$AO_SESSION_ID` so AO can identify containers it owns.
const SessionLabel = "ao.session"

// SpareLabel opts a container out of reaping even though it carries
// SessionLabel — set `--label ao.spare=true` on a deliberately shared
// container (a shared postgres, a registry) that must survive the session
// that happened to start it.
const SpareLabel = "ao.spare"

// reapTimeout bounds every docker CLI call this adapter makes. Kill is
// user-facing and synchronous, and the reap runs between runtime destroy and
// workspace teardown -- an unbounded call against a wedged daemon would block
// the whole teardown on nothing else being able to make progress. A timeout
// is treated the same as isDockerUnavailable: reap nothing, per this
// package's spare-on-ambiguity bias.
const reapTimeout = 10 * time.Second

// commandRunner abstracts exec.Command for tests.
type commandRunner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := aoprocess.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.Bytes(), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// Reaper implements ports.ContainerReaper over the docker CLI.
type Reaper struct {
	run commandRunner
}

// New constructs a Reaper. Production callers use the zero value's default
// (real docker CLI); tests inject a fake commandRunner via newWithRunner.
func New() *Reaper {
	return &Reaper{run: execRunner{}}
}

func newWithRunner(r commandRunner) *Reaper {
	return &Reaper{run: r}
}

// ReapSessionContainers force-removes every container labeled
// ao.session=<id> that is not also labeled ao.spare=true.
//
// Any failure to enumerate or inspect containers spares everything found so
// far and returns the error — reaping is only ever additive-on-certainty,
// never a best-guess sweep. A missing docker binary is NOT an error: most AO
// installs have no Docker at all, and that must not surface as a kill
// failure.
func (r *Reaper) ReapSessionContainers(ctx context.Context, id domain.SessionID) (int, error) {
	sessionID := strings.TrimSpace(string(id))
	if sessionID == "" {
		return 0, nil
	}

	ctx, cancel := context.WithTimeout(ctx, reapTimeout)
	defer cancel()

	out, err := r.run.Output(ctx, "docker", "ps", "-a",
		"--filter", fmt.Sprintf("label=%s=%s", SessionLabel, sessionID),
		"--format", "{{.ID}}\t{{.Label \""+SpareLabel+"\"}}")
	if err != nil {
		if isDockerUnavailable(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("dockerreap: list containers for session %s: %w", sessionID, err)
	}

	removed := 0
	var errs []error
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		containerID := strings.TrimSpace(fields[0])
		if containerID == "" {
			continue
		}
		spared := len(fields) > 1 && strings.EqualFold(strings.TrimSpace(fields[1]), "true")
		if spared {
			continue
		}
		if _, err := r.run.Output(ctx, "docker", "rm", "-f", containerID); err != nil {
			// One container's removal failing must not abort the rest — each
			// container is independent. Accumulate the error and keep going so a
			// single stuck/permission-denied container never leaks its siblings;
			// the caller sees the joined error plus the true partial-removal
			// count.
			errs = append(errs, fmt.Errorf("dockerreap: remove container %s (session %s): %w", containerID, sessionID, err))
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}

// isDockerUnavailable reports whether err indicates the docker CLI itself is
// not installed/runnable, as opposed to a real command failure against a
// present daemon. Most AO installs run without Docker at all; that must
// resolve to "nothing to reap," not a kill-blocking error.
func isDockerUnavailable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		// A wedged docker daemon must resolve to "reap nothing," not a
		// kill-blocking error -- same bias as every other ambiguous failure
		// mode this package handles.
		return true
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "executable file not found") ||
		strings.Contains(msg, "cannot connect to the docker daemon")
}
