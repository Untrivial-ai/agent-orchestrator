package codex

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// oneShotTimeout bounds an internal question, matching the other adapters.
const oneShotTimeout = 90 * time.Second

var _ ports.AgentOneShot = (*Plugin)(nil)

// RunOneShot answers a single prompt with `codex exec`, using the credentials
// the user already authorized.
//
// --ephemeral keeps the run out of the user's thread history, so AO's own
// questions never come back as importable conversations. --skip-git-repo-check
// is required because the AO-owned working directory is not a repository, and
// codex otherwise refuses to start there.
func (p *Plugin) RunOneShot(ctx context.Context, workDir, prompt string) (string, error) {
	binary, err := p.codexBinary(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("codex: one-shot prompt is empty")
	}

	runCtx, cancel := context.WithTimeout(ctx, oneShotTimeout)
	defer cancel()

	cmd := aoprocess.CommandContext(runCtx, binary,
		"exec",
		"--ephemeral",
		"--skip-git-repo-check",
		prompt,
	)
	cmd.Dir = workDir

	out, err := cmd.Output()
	if runCtx.Err() != nil {
		return "", runCtx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("codex: one-shot: %w", err)
	}
	return string(out), nil
}
