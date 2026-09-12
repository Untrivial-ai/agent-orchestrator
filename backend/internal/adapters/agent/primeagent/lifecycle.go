package primeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	primeCommandTimeout   = 10 * time.Second
	maxPrimeCommandOutput = 4 << 10
)

type primeCommandRunner func(ctx context.Context, binary, workingDir string, env map[string]string, args ...string) ([]byte, error)

type primeListResponse struct {
	Sessions []primeSessionSummary `json:"sessions"`
}

type primeSessionSummary struct {
	SessionID       string `json:"sessionId"`
	ActiveSessionID string `json:"activeSessionId"`
}

func runPrimeCommand(ctx context.Context, binary, workingDir string, env map[string]string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...) //nolint:gosec // binary is adapter-resolved; args are adapter-owned
	if strings.TrimSpace(workingDir) != "" {
		cmd.Dir = workingDir
	}
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd.CombinedOutput()
}

func (p *Plugin) executePrimeCommand(ctx context.Context, dataDir string, args ...string) ([]byte, error) {
	binary, err := p.primeAgentBinary(ctx)
	if err != nil {
		return nil, err
	}
	dir, err := primeDataDir(dataDir)
	if err != nil {
		return nil, err
	}
	env := map[string]string{primeAgentCodingAgentDirEnv: dir}
	runner := p.runCommand
	if runner == nil {
		runner = runPrimeCommand
	}
	runCtx, cancel := context.WithTimeout(ctx, primeCommandTimeout)
	defer cancel()
	return runner(runCtx, binary, dir, env, args...)
}

func activeSessionForTranscript(output []byte, nativeID string) (string, bool, error) {
	var response primeListResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return "", false, fmt.Errorf("decode session list: %w", err)
	}
	var matches []string
	for _, session := range response.Sessions {
		if strings.TrimSpace(session.SessionID) == nativeID && strings.TrimSpace(session.ActiveSessionID) != "" {
			matches = append(matches, strings.TrimSpace(session.ActiveSessionID))
		}
	}
	if len(matches) == 0 {
		return "", false, nil
	}
	if len(matches) != 1 {
		return "", false, fmt.Errorf("multiple live Prime sessions match transcript %q", nativeID)
	}
	return matches[0], true, nil
}

func boundedPrimeOutput(output []byte) string {
	if len(output) > maxPrimeCommandOutput {
		output = output[:maxPrimeCommandOutput]
	}
	return strings.TrimSpace(string(output))
}

// TerminateNativeSession stops the live Prime worker that owns session's
// transcript. An inactive transcript is already safe and remains resumable.
func (p *Plugin) TerminateNativeSession(ctx context.Context, session ports.SessionRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	nativeID := strings.TrimSpace(session.Metadata[ports.MetadataKeyAgentSessionID])
	if nativeID == "" {
		return nil
	}
	output, err := p.executePrimeCommand(ctx, session.DataDir, "list", "--json")
	if err != nil {
		return fmt.Errorf("prime-agent: list live sessions: %w: %s", err, boundedPrimeOutput(output))
	}
	activeID, ok, err := activeSessionForTranscript(output, nativeID)
	if err != nil || !ok {
		return err
	}
	output, err = p.executePrimeCommand(ctx, session.DataDir, "stop", activeID, "--json")
	if err != nil {
		return fmt.Errorf("prime-agent: stop active session %q: %w: %s", activeID, err, boundedPrimeOutput(output))
	}
	return nil
}
