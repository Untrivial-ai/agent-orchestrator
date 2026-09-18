package cursor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// NativeConversationID bridges Cursor's terminal resume id and ACP
// conversation id. A TUI source must have reported its native id through the
// AO-owned hook metadata; Chat supplies the id returned by Cursor ACP.
func (p *Plugin) NativeConversationID(
	ctx context.Context,
	session ports.SessionRef,
	currentMode domain.SessionMode,
	providerConversationID string,
) (string, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if currentMode == domain.SessionModeChat {
		id := strings.TrimSpace(providerConversationID)
		return id, id != "", nil
	}
	id := strings.TrimSpace(session.Metadata[ports.MetadataKeyAgentSessionID])
	return id, id != "", nil
}

// NativeConversationExists reports whether Cursor has persisted exactly one
// non-empty transcript for id beneath the AO-owned CURSOR_DATA_DIR. It does not
// parse or project provider messages.
func (p *Plugin) NativeConversationExists(
	ctx context.Context,
	_ ports.SessionRef,
	nativeConversationID string,
	env map[string]string,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	id := strings.TrimSpace(nativeConversationID)
	if !validCursorConversationID(id) {
		return false, nil
	}
	dataDir := strings.TrimSpace(env[cursorDataDirEnv])
	if dataDir == "" {
		return false, nil
	}
	ok, err := cursorRealDirectory(dataDir)
	if err != nil {
		return false, cursorFilesystemError("inspect data root", err)
	}
	if !ok {
		return false, nil
	}

	projectsDir := filepath.Join(dataDir, "projects")
	ok, err = cursorRealDirectory(projectsDir)
	if err != nil {
		return false, cursorFilesystemError("inspect transcript root", err)
	}
	if !ok {
		return false, nil
	}
	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return false, cursorFilesystemError("read transcript root", err)
	}

	matches := 0
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if project.Type()&os.ModeSymlink != 0 {
			continue
		}
		projectDir := filepath.Join(projectsDir, project.Name())
		ok, err = cursorRealDirectory(projectDir)
		if err != nil {
			return false, cursorFilesystemError("inspect project transcript directory", err)
		}
		if !ok {
			continue
		}
		transcriptsDir := filepath.Join(projectDir, "agent-transcripts")
		ok, err := cursorRealDirectory(transcriptsDir)
		if err != nil {
			return false, cursorFilesystemError("inspect transcript directory", err)
		}
		if !ok {
			continue
		}
		conversationDir := filepath.Join(transcriptsDir, id)
		ok, err = cursorRealDirectory(conversationDir)
		if err != nil {
			return false, cursorFilesystemError("inspect conversation directory", err)
		}
		if !ok {
			continue
		}
		info, err := os.Lstat(filepath.Join(conversationDir, id+".jsonl"))
		switch {
		case err == nil && info.Mode().IsRegular() && info.Size() > 0:
			matches++
			if matches > 1 {
				return false, errors.New("cursor: multiple native transcripts found")
			}
		case err == nil, os.IsNotExist(err):
			continue
		default:
			return false, cursorFilesystemError("inspect transcript", err)
		}
	}
	return matches == 1, nil
}

func validCursorConversationID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, `/\\*?[`)
}

func cursorRealDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false, err
	}
	expected := filepath.Clean(path)
	if tempDir := filepath.Clean(os.TempDir()); cursorPathWithin(tempDir, expected) {
		resolvedTemp, err := filepath.EvalSymlinks(tempDir)
		if err != nil {
			return false, err
		}
		rel, err := filepath.Rel(tempDir, expected)
		if err != nil {
			return false, err
		}
		expected = filepath.Join(resolvedTemp, rel)
	}
	return filepath.Clean(resolved) == expected, nil
}

func cursorPathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func cursorFilesystemError(operation string, err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		err = pathErr.Err
	}
	return fmt.Errorf("cursor: %s: %w", operation, err)
}
