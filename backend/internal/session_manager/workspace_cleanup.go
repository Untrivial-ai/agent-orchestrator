package sessionmanager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// ErrCleanupScript marks a script failure that preserves the worktree for retry.
var ErrCleanupScript = errors.New("workspace cleanup script failed")

// runPreRemove is called only for permanent AO-owned workspace retirement,
// after the session's processes have stopped and before worktree removal.
func (m *Manager) runPreRemove(ctx context.Context, projectID domain.ProjectID, workspacePath string) error {
	if workspacePath == "" {
		return nil
	}
	project, err := m.loadProject(ctx, projectID)
	if err != nil {
		return err
	}
	if project.Kind.WithDefault() == domain.ProjectKindScratch || len(project.Config.PreRemove) == 0 {
		return nil
	}
	if _, err := os.Stat(workspacePath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect workspace for cleanup: %w", err)
	}
	managedRoot, err := filepath.EvalSymlinks(filepath.Join(m.dataDir, "worktrees"))
	if err != nil {
		return fmt.Errorf("resolve managed workspace root: %w", err)
	}
	physicalPath, err := filepath.EvalSymlinks(workspacePath)
	if err != nil {
		return fmt.Errorf("resolve workspace for cleanup: %w", err)
	}
	rel, err := filepath.Rel(managedRoot, physicalPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return errors.New("cleanup path is outside managed workspaces")
	}
	for index, command := range project.Config.PreRemove {
		if strings.TrimSpace(command) == "" {
			continue
		}
		stepCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = aoprocess.CommandContext(stepCtx, "cmd", "/c", command)
		} else {
			cmd = aoprocess.CommandContext(stepCtx, "sh", "-c", command)
		}
		cmd.Dir = workspacePath
		cmd.Env = os.Environ()
		for key, value := range project.Config.Env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		cmd.Env = append(cmd.Env, "AO_SOURCE_TREE_PATH="+project.Path, "AO_WORKTREE_PATH="+workspacePath)
		out := &cleanupOutput{}
		cmd.Stdout, cmd.Stderr = out, out
		err := cmd.Run()
		cancel()
		if err != nil {
			message := strings.TrimSpace(string(out.tail))
			for _, value := range project.Config.Env {
				if value != "" {
					message = strings.ReplaceAll(message, value, "[REDACTED]")
				}
			}
			return fmt.Errorf("%w: step %d: %w: %s", ErrCleanupScript, index+1, err, message)
		}
	}
	return nil
}

// cleanupOutput holds the last 4 KiB of output from a cleanup step.
type cleanupOutput struct{ tail []byte }

func (o *cleanupOutput) Write(p []byte) (int, error) {
	n := len(p)
	o.tail = append(o.tail, p...)
	if len(o.tail) > 4096 {
		o.tail = append([]byte(nil), o.tail[len(o.tail)-4096:]...)
	}
	return n, nil
}
