package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/pkg/agentruntime"
)

// transcriptPath is the worker-auth control-plane route for delete/restore
// checkpoints. PUT pushes a checkpoint (204); GET returns this worker's captured
// checkpoint or 404 when none exists.
const transcriptPath = "/worker/transcript"

// maxTranscriptBytes caps a decoded checkpoint payload. A long conversation's
// base64 transcript comfortably exceeds the 1 MiB default response cap, so the
// transcript route uses its own generous ceiling while still bounding a
// misbehaving control plane.
const maxTranscriptBytes = 64 << 20

// transcriptCheckpoint is the wire body exchanged with the control plane's
// /worker/transcript route. The field names are the delete/restore contract; the
// control plane persists them verbatim and returns them on restore.
type transcriptCheckpoint struct {
	AgentSessionID  string `json:"agentSessionId"`
	Harness         string `json:"harness"`
	Transcript      string `json:"transcript"` // base64 of the transcript file bytes
	PreservedGitRef string `json:"preservedGitRef"`
}

// putTranscript pushes a checkpoint. It reuses the worker's authenticated client
// (Worker-token Authorization header, control-plane base URL) exactly like every
// other worker RPC.
func (c *client) putTranscript(ctx context.Context, body transcriptCheckpoint) error {
	return c.doMethod(ctx, http.MethodPut, transcriptPath, body, nil)
}

// getTranscript fetches this worker's captured checkpoint. ok=false with a nil
// error means the control plane has nothing captured (HTTP 404) — the normal
// case for a session that was never deleted. It handles its own response so a
// 404 is a clean "nothing captured" signal rather than an error, and so a large
// transcript is not truncated by the default response cap.
func (c *client) getTranscript(ctx context.Context) (transcriptCheckpoint, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+transcriptPath, nil)
	if err != nil {
		return transcriptCheckpoint{}, false, err
	}
	if token := c.currentToken(); token != "" {
		request.Header.Set("Authorization", "Worker "+token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return transcriptCheckpoint{}, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return transcriptCheckpoint{}, false, nil
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return transcriptCheckpoint{}, false, fmt.Errorf(
			"%s returned %d: %s", transcriptPath, response.StatusCode, strings.TrimSpace(string(snippet)),
		)
	}
	var out transcriptCheckpoint
	if err := json.NewDecoder(io.LimitReader(response.Body, maxTranscriptBytes)).Decode(&out); err != nil {
		return transcriptCheckpoint{}, false, fmt.Errorf("decode %s response: %w", transcriptPath, err)
	}
	return out, true, nil
}

// transcriptResolver locates a harness's resume transcript for capture and
// reconstructs the path it must land at for rehydrate. Claude Code is
// first-class; Codex is best-effort; other harnesses are extensible stubs.
type transcriptResolver struct {
	harness              string
	dataDir              string
	workspace            string
	launchAgentSessionID string
	aoSessionID          string
}

// locate finds the on-disk transcript to capture. It returns the native agent
// session id (the transcript file's stem) and its absolute path. ok=false means
// no transcript exists yet (the agent has not produced one this session).
func (r transcriptResolver) locate() (agentSessionID, path string, ok bool) {
	switch r.harness {
	case "claude-code":
		return r.locateClaude()
	case "codex":
		return r.locateCodex()
	default:
		// cursor and unknown harnesses: capture is not yet implemented. Returning
		// false keeps the checkpoint a silent no-op rather than a hard failure.
		return "", "", false
	}
}

// rehydratePath returns where a captured transcript must be written so the
// harness's --resume finds it on a fresh sandbox, creating no directories (the
// caller does). It mirrors the location locate would discover.
func (r transcriptResolver) rehydratePath(agentSessionID string) (string, error) {
	agentSessionID = strings.TrimSpace(agentSessionID)
	if agentSessionID == "" {
		return "", fmt.Errorf("agent session id is required to rehydrate a transcript")
	}
	switch r.harness {
	case "claude-code":
		configDir := r.claudeConfigDir()
		if configDir == "" {
			return "", fmt.Errorf("cannot resolve Claude config directory")
		}
		// Claude stores a cwd's transcripts under <config>/projects/<encoded>/ and
		// --resume reads from the directory matching the process cwd, so the file
		// must land under the workspace's encoded project directory.
		return filepath.Join(
			configDir, "projects", encodeClaudeProjectDir(r.workspace), agentSessionID+".jsonl",
		), nil
	case "codex":
		home := r.codexHome()
		if home == "" {
			return "", fmt.Errorf("cannot resolve Codex home")
		}
		// Codex resume walks the whole sessions tree matching the native id
		// suffix, so a flat rollout-<id>.jsonl under sessions/ is discoverable.
		return filepath.Join(home, "sessions", "rollout-"+agentSessionID+".jsonl"), nil
	default:
		return "", fmt.Errorf("rehydrate is unsupported for harness %q", r.harness)
	}
}

func (r transcriptResolver) locateClaude() (string, string, bool) {
	configDir := r.claudeConfigDir()
	if configDir == "" {
		return "", "", false
	}
	// Candidate identities mirror workerexec.interactiveRestoreIdentity: the
	// launch-provided native id first, then the deterministic id a fresh
	// --session-id launch uses. The first with a transcript on disk wins.
	var candidates []string
	if id := strings.TrimSpace(r.launchAgentSessionID); id != "" {
		candidates = append(candidates, id)
	}
	if r.aoSessionID != "" {
		candidates = append(candidates, agentruntime.ClaudeSessionID(r.aoSessionID))
	}
	for _, id := range candidates {
		matches, _ := filepath.Glob(filepath.Join(configDir, "projects", "*", id+".jsonl"))
		if len(matches) > 0 {
			return id, matches[0], true
		}
	}
	return "", "", false
}

func (r transcriptResolver) locateCodex() (string, string, bool) {
	home := r.codexHome()
	if home == "" {
		return "", "", false
	}
	sessionsDir := filepath.Join(home, "sessions")
	want := strings.TrimSpace(r.launchAgentSessionID)
	var newestPath, newestID string
	var newestMod time.Time
	_ = filepath.WalkDir(sessionsDir, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		id := codexRolloutID(name)
		if id == "" {
			return nil
		}
		if want != "" {
			if id == want {
				newestPath, newestID = p, id
				return fs.SkipAll
			}
			return nil
		}
		// No launch-provided id (Codex mints it at first turn): fall back to the
		// most recently written rollout, which is this session's active one.
		if info, err := entry.Info(); err == nil && info.ModTime().After(newestMod) {
			newestMod, newestPath, newestID = info.ModTime(), p, id
		}
		return nil
	})
	if newestPath == "" {
		return "", "", false
	}
	return newestID, newestPath, true
}

func (r transcriptResolver) claudeConfigDir() string {
	if v := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); v != "" {
		return v
	}
	if r.dataDir != "" {
		return filepath.Join(r.dataDir, "claude")
	}
	return ""
}

func (r transcriptResolver) codexHome() string {
	if v := strings.TrimSpace(os.Getenv("CODEX_HOME")); v != "" {
		return v
	}
	if r.dataDir != "" {
		return filepath.Join(r.dataDir, "codex")
	}
	return ""
}

var (
	nonProjectChar   = regexp.MustCompile(`[^a-zA-Z0-9]`)
	codexRolloutUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// encodeClaudeProjectDir reproduces Claude Code's project-directory naming: the
// absolute workspace path with every non-alphanumeric character replaced by a
// hyphen (for example /home/ao/work becomes -home-ao-work). A rehydrated
// transcript must land under this directory so --resume, which reads the project
// directory matching the process cwd, finds the conversation.
func encodeClaudeProjectDir(workspace string) string {
	return nonProjectChar.ReplaceAllString(workspace, "-")
}

// codexRolloutID extracts the native session UUID from a Codex rollout file name
// of the form rollout-<timestamp>-<uuid>.jsonl. It returns "" when the trailing
// segment is not a UUID.
func codexRolloutID(name string) string {
	base := strings.TrimSuffix(name, ".jsonl")
	if len(base) < 37 || base[len(base)-37] != '-' {
		return ""
	}
	id := base[len(base)-36:]
	if !codexRolloutUUID.MatchString(id) {
		return ""
	}
	return id
}
