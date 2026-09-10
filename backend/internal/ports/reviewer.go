package ports

import (
	"context"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Reviewer is the contract a code-review adapter satisfies. It is deliberately
// separate from Agent: a reviewer is invoked once over a checkout to review a
// PR, and need not be a prompt-fed interactive agent. A prompt-driven reviewer
// (claude-code) builds its own prompt internally; a one-shot CLI (greptile)
// returns its own argv with no prompt at all.
type Reviewer interface {
	// ReviewCommand builds the command (and any extra env) AO should run to
	// spawn a fresh reviewer over the worker's checkout for a PR.
	ReviewCommand(ctx context.Context, inv ReviewInvocation) (ReviewCommandSpec, error)
	// ReviewMessage builds the text AO injects into an already-running reviewer
	// pane to ask it to review a new commit. It must be self-contained (carry
	// the ids the reviewer needs to submit) since AO passes no environment.
	ReviewMessage(ctx context.Context, inv ReviewInvocation) (string, error)
}

// ReviewCancelMode names how AO should stop a running reviewer.
type ReviewCancelMode string

const (
	// ReviewCancelInterrupt sends the terminal interrupt key sequence to the
	// reviewer process while preserving the terminal pane.
	ReviewCancelInterrupt ReviewCancelMode = "interrupt"
	// ReviewCancelMessage sends an in-band message to the reviewer process. Use
	// this for harnesses where Ctrl-C exits the TUI instead of cancelling only
	// the active turn.
	ReviewCancelMessage ReviewCancelMode = "message"
	// ReviewCancelInput sends raw terminal input to the reviewer process without
	// appending Enter. Use this for TUI keybindings such as Escape.
	ReviewCancelInput ReviewCancelMode = "input"
)

// ReviewCancelSpec is the adapter-selected cancellation behavior for a running
// reviewer.
type ReviewCancelSpec struct {
	Mode       ReviewCancelMode
	Interrupts int
	Message    string
	Input      string
	Inputs     []string
	InputDelay time.Duration
}

// ReviewerCanceller is implemented by reviewer adapters that explicitly define
// how their running CLI should be cancelled.
type ReviewerCanceller interface {
	ReviewCancel(ctx context.Context) (ReviewCancelSpec, error)
}

// ReviewerRestorer is implemented by prompt-driven reviewers that can resume a
// native agent conversation after AO recreates the terminal pane.
type ReviewerRestorer interface {
	ReviewRestoreCommand(ctx context.Context, inv ReviewInvocation) (cmd ReviewCommandSpec, ok bool, err error)
}

// ReviewerReusePolicy is implemented by interactive reviewer adapters that
// need a fresh TUI for each task because request-scoped context is fixed at
// process launch. Returning false forces a fresh launch for every pass.
type ReviewerReusePolicy interface {
	ReviewProcessReusable() bool
}

// ReviewerPromptReadinessProvider lets an interactive reviewer describe when
// its terminal prompt is ready for InitialMessage injection. Reviewers without
// this capability receive a conservative startup delay from the launcher.
type ReviewerPromptReadinessProvider interface {
	ReviewPromptReadinessHints(ctx context.Context) (PromptReadinessHints, error)
}

// ReviewInvocation describes one review pass for a reviewer to act on. All ids
// the reviewer needs are passed explicitly here (and embedded in the prompt /
// message), never through environment variables.
type ReviewInvocation struct {
	// ReviewerID is a stable id for the reviewer's runtime instance (pane,
	// native session id), derived from the worker session.
	ReviewerID string
	// RunID is the review_run this pass completes; the reviewer passes it to
	// `ao review submit`.
	RunID string
	// WorkerSessionID is the worker whose PR is under review.
	WorkerSessionID domain.SessionID
	// AgentSessionID is the reviewer's native agent conversation id, used only
	// to resume a destroyed/recreated reviewer terminal.
	AgentSessionID string
	// PRURL is the pull request to review.
	PRURL string
	// TargetSHA is the PR head commit under review.
	TargetSHA string
	// ReviewQueue lists all review tasks created by the same trigger so a shared
	// reviewer pane can review multiple PRs and submit the results together.
	ReviewQueue []ReviewTask
	// ReviewIndex is this invocation's zero-based position in ReviewQueue.
	ReviewIndex int
	// Config carries the reviewer's resolved agent configuration override.
	Config domain.AgentConfig
	// WorkspacePath is the worker's checkout the reviewer reads.
	WorkspacePath string
	// DataDir is AO's owned state root. Reviewer prelaunch hooks may use it for
	// profile installation but must not write outside AO/workspace boundaries.
	DataDir string
	// RunFilePath is the daemon run-file reviewer-local AO CLI calls must use to
	// find this daemon. Some harnesses filter shell command environments, so
	// adapters may need to pass this value through their own command config.
	RunFilePath string
	// Prompt and SystemPrompt are the review instructions AO authored centrally,
	// mirroring the worker's LaunchConfig.Prompt / SystemPrompt split: SystemPrompt
	// carries the standing reviewer role, Prompt the per-pass task. A prompt-driven
	// adapter (claude-code) feeds them to the agent; a one-shot CLI reviewer may
	// ignore them.
	Prompt       string
	SystemPrompt string
	// SystemPromptFile is the AO-owned file form of SystemPrompt. Reviewer
	// launchers use it to keep standing instructions out of the shared terminal
	// stream while preserving their system-level role in agent harnesses that
	// support prompt files.
	SystemPromptFile string
	// TaskPromptFile is the AO-owned file containing the full per-pass task.
	// Prompt carries only a short reference to this file so the instructions do
	// not enter the shared terminal stream.
	TaskPromptFile string
	// TaskPromptRoot is the stable AO-owned directory containing task prompt
	// files for this reviewer. Adapters use it when a long-lived reviewer needs
	// permission to read request-scoped task files created after launch.
	TaskPromptRoot string
}

// ReviewTask is one PR/run in a multi-PR review trigger queue.
type ReviewTask struct {
	RunID     string
	PRURL     string
	TargetSHA string
}

// ReviewCommandSpec is how to launch a reviewer: the argv, any extra env, and
// any launch-time native session id the adapter can determine. AO supplies the
// workspace and review-tracking env around it.
type ReviewCommandSpec struct {
	Argv           []string
	Env            map[string]string
	AgentSessionID string
	// InitialMessage is injected after the process starts. Interactive-only
	// reviewers use this instead of placing a task on the command line.
	InitialMessage string
	// WorkingDirectory overrides the worker checkout as the process working
	// directory. Secure interactive reviewers use an AO-owned neutral directory;
	// this routing field is not itself a process sandbox.
	WorkingDirectory string
}

// ReviewerResolver maps a reviewer harness onto its adapter. ok=false means no
// adapter is registered for that harness.
type ReviewerResolver interface {
	Reviewer(harness domain.ReviewerHarness) (Reviewer, bool)
}
