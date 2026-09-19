package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
// reconstructs the path it must land at for rehydrate. opencode is the only
// harness AO supports today; its transcript capture is not yet implemented, so
// checks are silent no-ops rather than hard failures.
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
	// opencode transcript capture is not yet implemented. Returning false keeps
	// the checkpoint a silent no-op rather than a hard failure.
	return "", "", false
}

// rehydratePath returns where a captured transcript must be written so the
// harness's --resume finds it on a fresh sandbox, creating no directories (the
// caller does). It mirrors the location locate would discover.
func (r transcriptResolver) rehydratePath(agentSessionID string) (string, error) {
	agentSessionID = strings.TrimSpace(agentSessionID)
	if agentSessionID == "" {
		return "", fmt.Errorf("agent session id is required to rehydrate a transcript")
	}
	return "", fmt.Errorf("rehydrate is unsupported for harness %q", r.harness)
}