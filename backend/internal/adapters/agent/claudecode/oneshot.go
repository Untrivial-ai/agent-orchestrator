package claudecode

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// oneShotTimeout bounds an internal question. A classification prompt is a few
// hundred tokens; anything slower than this is a stall, and the caller degrades
// rather than making the user wait.
const oneShotTimeout = 90 * time.Second

var _ ports.AgentOneShot = (*Plugin)(nil)

// RunOneShot answers a single prompt with `claude --print`, using the
// credentials the user already authorized.
//
// It runs in workDir because print mode still records a transcript under
// ~/.claude/projects, keyed by the working directory. Pointing that at an
// AO-owned directory keeps AO's own questions out of the conversation history
// AO reads, which would otherwise grow a junk entry on every run. Overriding
// CLAUDE_CONFIG_DIR to isolate the transcript instead is not an option: the
// credentials live in that same directory, so isolating it logs the CLI out.
//
// No tools are allowed. The question is about text AO already holds, so the
// agent has no reason to touch the machine.
func (p *Plugin) RunOneShot(ctx context.Context, workDir, prompt string) (string, error) {
	binary, err := p.claudeBinary(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("claude-code: one-shot prompt is empty")
	}

	runCtx, cancel := context.WithTimeout(ctx, oneShotTimeout)
	defer cancel()

	cmd := aoprocess.CommandContext(runCtx, binary,
		"--print",
		"--disallowed-tools", "Bash,Edit,Write,Read,Grep,Glob,WebFetch,WebSearch",
		"--disable-slash-commands",
		prompt,
	)
	cmd.Dir = workDir

	out, err := cmd.Output()
	if runCtx.Err() != nil {
		return "", runCtx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("claude-code: one-shot: %w", err)
	}
	return string(out), nil
}
