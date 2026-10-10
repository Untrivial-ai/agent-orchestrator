package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	processutil "github.com/aoagents/agent-orchestrator/backend/internal/process"
	"github.com/aoagents/agent-orchestrator/backend/internal/skillassets"
)

func prepareTarget(ctx context.Context, frontend, commit, root string) (err error) {
	dir := filepath.Join(root, "setup")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"prepare.py", "prepare.cjs", "bootstrap.cjs"} {
		data, err := skillassets.TestingScript(name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return err
		}
	}
	lease, err := os.OpenFile(filepath.Join(filepath.Dir(frontend), ".ao-testing-active"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("reserve preparation checkout: %w", err)
	}
	shutdownVerified := true
	defer func() {
		if shutdownVerified {
			err = errors.Join(err, releaseIntakeLease(lease))
		} else {
			err = errors.Join(err, lease.Close())
		}
	}()
	if _, err := lease.WriteString(root); err != nil {
		return err
	}
	cmd := processutil.CommandContext(ctx, "python3", filepath.Join(dir, "prepare.py"), "--repository", filepath.Dir(frontend), "--commit", commit, "--reservation-owner", root)
	cmd.Env = strippedEnv(os.Environ())
	output, shutdownVerified, err := runSetupCommand(ctx, cmd)
	var groupErr *setupGroupError
	if errors.As(err, &groupErr) {
		var statErr error
		groupErr.leaseInfo, statErr = lease.Stat()
		err = errors.Join(err, statErr)
	}
	if err != nil {
		return fmt.Errorf("prepare target: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func buildOwnedDaemon(ctx context.Context, frontend, executable string) error {
	if err := os.Mkdir(filepath.Dir(executable), 0o700); err != nil {
		return err
	}
	cmd := processutil.CommandContext(ctx, "go", "build", "-p", "2", "-o", executable, "./cmd/ao")
	cmd.Dir, cmd.Env = filepath.Join(filepath.Dir(frontend), "backend"), strippedEnv(os.Environ())
	output, _, err := runSetupCommand(ctx, cmd)
	if err != nil {
		return fmt.Errorf("build owned daemon: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// Commands inherit one private process group, including npm, Git and compiler
// children. Keep the checkout reservation until that group has stopped.
type setupGroupError struct {
	group     int
	cause     error
	leaseInfo os.FileInfo
}

func (e *setupGroupError) Error() string { return e.cause.Error() }
func (e *setupGroupError) Unwrap() error { return e.cause }

func runSetupCommand(ctx context.Context, cmd *exec.Cmd) ([]byte, bool, error) {
	if runtime.GOOS == "windows" {
		return nil, true, errors.New("unsupported_revision: local desktop preparation requires POSIX process groups")
	}
	processutil.ConfigureTreeCancellation(cmd)
	output, err := cmd.CombinedOutput()
	if cmd.Process == nil {
		return output, true, errors.Join(err, ctx.Err())
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	cleanupErr := waitSetupGroup(cleanupCtx, cmd.Process.Pid)
	if cleanupErr != nil {
		cleanupErr = &setupGroupError{group: cmd.Process.Pid, cause: cleanupErr}
	}
	return output, cleanupErr == nil, errors.Join(err, ctx.Err(), cleanupErr)
}

func waitSetupGroup(ctx context.Context, group int) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		cmd := processutil.CommandContext(ctx, "/bin/ps", "-axo", "pgid=,stat=")
		output, err := cmd.Output()
		if err != nil {
			return fmt.Errorf("verify preparation process group %d: %w", group, err)
		}
		alive := false
		for line := range strings.SplitSeq(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if len(fields) != 2 {
				return errors.New("invalid preparation process inventory")
			}
			pidGroup, err := strconv.Atoi(fields[0])
			if err != nil {
				return fmt.Errorf("invalid preparation process group: %w", err)
			}
			if pidGroup == group && !strings.HasPrefix(fields[1], "Z") {
				alive = true
			}
		}
		if !alive {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("preparation process group %d remains alive: %w", group, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (a *Adapter) prepareOwnedDaemon(ctx context.Context, s *launch) error {
	data, err := os.ReadFile(filepath.Join(s.frontend, ".vite", "testing-target.json"))
	if err != nil {
		return err
	}
	var manifest struct {
		Preflight json.RawMessage `json:"preflight"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	if len(manifest.Preflight) == 0 || string(manifest.Preflight) == "null" {
		return fmt.Errorf("unsupported_revision: prepared checkout has no checked runtime facts")
	}
	s.preflight = manifest.Preflight
	return a.ops.build(ctx, s.frontend, s.daemon)
}

func daemonCommand(executable string) string {
	return "exec '" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "' daemon"
}
