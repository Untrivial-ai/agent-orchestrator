package systemexec

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const agentShellOutputLimit = 64 * 1024

var (
	agentCommandName                          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	errShellOutputLimit                       = errors.New("agent shell output exceeded 64 KiB")
	_                   ports.AgentShellProbe = AgentShellProbe{}
)

// AgentShellProbe discovers commands using the user's supported login shell.
// Shell is optional and exists to permit isolated host and fixture selection.
type AgentShellProbe struct {
	Shell string
}

// ProbeAgentShell runs one bounded, non-interactive login-shell snapshot.
func (p AgentShellProbe) ProbeAgentShell(parent context.Context, names []string) (ports.AgentShellSnapshot, error) {
	if err := validateAgentCommandNames(names); err != nil {
		return ports.AgentShellSnapshot{}, err
	}
	if err := parent.Err(); err != nil {
		return ports.AgentShellSnapshot{}, err
	}
	marker, err := probeMarker()
	if err != nil {
		return ports.AgentShellSnapshot{}, fmt.Errorf("create shell probe marker: %w", err)
	}

	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	cmd, err := agentShellCommand(ctx, p.Shell, marker, names)
	if err != nil {
		return ports.AgentShellSnapshot{}, err
	}
	configureProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessTree(cmd) }
	cmd.WaitDelay = time.Second
	cmd.Stdin = strings.NewReader("")

	var output boundedCombinedOutput
	output.cancel = cancel
	cmd.Stdout = &output
	cmd.Stderr = &output
	runErr := cmd.Run()
	if runErr != nil {
		_ = killProcessTree(cmd)
	}
	if output.overflowed() {
		return ports.AgentShellSnapshot{}, errShellOutputLimit
	}
	if err := parent.Err(); err != nil {
		return ports.AgentShellSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return ports.AgentShellSnapshot{}, err
	}
	if runErr != nil {
		return ports.AgentShellSnapshot{}, fmt.Errorf("probe agent login shell: %w", runErr)
	}
	return parseAgentShellSnapshot(output.bytes(), marker, names)
}

func validateAgentCommandNames(names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !agentCommandName.MatchString(name) {
			return fmt.Errorf("invalid agent command name %q", name)
		}
		if _, exists := seen[name]; exists {
			return fmt.Errorf("duplicate agent command name %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func probeMarker() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "AO_AGENT_SHELL_" + hex.EncodeToString(value), nil
}

func parseAgentShellSnapshot(output []byte, marker string, names []string) (ports.AgentShellSnapshot, error) {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[name] = struct{}{}
	}
	snapshot := ports.AgentShellSnapshot{Paths: make(map[string]string)}
	seen := make(map[string]bool, len(names))
	pathSeen := false
	done := false
	prefix := marker + "\t"
	for _, line := range strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		fields := strings.Split(line, "\t")
		if done {
			return ports.AgentShellSnapshot{}, errors.New("agent shell record after completion")
		}
		switch {
		case len(fields) == 2 && fields[1] == "DONE":
			done = true
		case len(fields) == 3 && fields[1] == "PATH":
			if pathSeen {
				return ports.AgentShellSnapshot{}, errors.New("duplicate agent shell PATH record")
			}
			pathSeen = true
			snapshot.Path = fields[2]
		case len(fields) == 4 && fields[1] == "FOUND":
			name, path := fields[2], fields[3]
			if _, ok := wanted[name]; !ok {
				return ports.AgentShellSnapshot{}, fmt.Errorf("unexpected agent shell command %q", name)
			}
			if seen[name] {
				return ports.AgentShellSnapshot{}, fmt.Errorf("duplicate agent shell command %q", name)
			}
			seen[name] = true
			if path == "" {
				continue
			}
			if !filepath.IsAbs(path) {
				return ports.AgentShellSnapshot{}, fmt.Errorf("agent shell command %q resolved to non-executable shell value", name)
			}
			snapshot.Paths[name] = path
		default:
			return ports.AgentShellSnapshot{}, errors.New("malformed agent shell record")
		}
	}
	if !done || !pathSeen {
		return ports.AgentShellSnapshot{}, errors.New("incomplete agent shell response")
	}
	return snapshot, nil
}

type boundedCombinedOutput struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	cancel   context.CancelFunc
	overflow bool
}

func (w *boundedCombinedOutput) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	remaining := agentShellOutputLimit - w.buf.Len()
	if remaining < len(value) {
		if remaining > 0 {
			_, _ = w.buf.Write(value[:remaining])
		}
		w.overflow = true
		w.cancel()
		return len(value), nil
	}
	_, _ = w.buf.Write(value)
	return len(value), nil
}

func (w *boundedCombinedOutput) overflowed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.overflow
}

func (w *boundedCombinedOutput) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return bytes.Clone(w.buf.Bytes())
}
