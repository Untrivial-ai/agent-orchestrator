// Package systeminstall installs agent harnesses from a fixed, code-owned
// allowlist. Callers select only a target id; command shapes and URLs never
// come from the request.
package systeminstall

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Target identifies one installable AO agent harness.
type Target string

// Supported harness install targets.
const (
	TargetClaudeCode Target = "claude-code"
	TargetCodex      Target = "codex"
	TargetCursor     Target = "cursor"
	TargetOpencode   Target = "opencode"
	TargetAider      Target = "aider"
	TargetCopilot    Target = "copilot"
	TargetGrok       Target = "grok"
	TargetKimi       Target = "kimi"
	TargetPi         Target = "pi"
	TargetAmp        Target = "amp"
	TargetAuggie     Target = "auggie"
	TargetDroid      Target = "droid"
	TargetCrush      Target = "crush"
	TargetCline      Target = "cline"
	TargetGoose      Target = "goose"
	TargetQwen       Target = "qwen"
	TargetContinue   Target = "continue"
	TargetDevin      Target = "devin"
	TargetKiro       Target = "kiro"
	TargetKilocode   Target = "kilocode"
	TargetVibe       Target = "vibe"
	TargetMuse       Target = "muse"
	TargetAgy        Target = "agy"
	TargetAutohand   Target = "autohand"
	TargetKimchi     Target = "kimchi"
	TargetPrimeAgent Target = "prime-agent"
	TargetOMP        Target = "omp"
)

// agentTargets is the stable settings-page order.
var agentTargets = []Target{
	TargetClaudeCode, TargetCodex, TargetCursor, TargetOpencode, TargetAider,
	TargetCopilot, TargetGrok, TargetKimi, TargetPi, TargetAmp, TargetAuggie,
	TargetDroid, TargetCrush, TargetCline, TargetGoose, TargetQwen,
	TargetContinue, TargetDevin, TargetKiro, TargetKilocode, TargetVibe,
	TargetMuse, TargetAgy, TargetAutohand, TargetKimchi, TargetPrimeAgent,
	TargetOMP,
}

var agentTargetSet = func() map[Target]bool {
	out := make(map[Target]bool, len(agentTargets))
	for _, target := range agentTargets {
		out[target] = true
	}
	return out
}()

// IsAgentTarget reports whether target is a user-facing harness id.
func IsAgentTarget(target Target) bool { return agentTargetSet[target] }

// Plan is a resolved, fixed installation command.
type Plan struct {
	Target      Target
	Command     []string
	Unsupported bool
	Reason      string
	Method      string
	DocsURL     string
}

// AgentPlan is the display-safe plan returned to the settings page. Command is
// a preview of fixed server-owned argv and is never accepted from the client.
type AgentPlan struct {
	AgentID          string `json:"agentId"`
	Available        bool   `json:"available"`
	Automatic        bool   `json:"automatic"`
	Method           string `json:"method"`
	Command          string `json:"command,omitempty"`
	Reason           string `json:"reason,omitempty"`
	DocumentationURL string `json:"documentationUrl"`
}

// Status is the lifecycle state of an install job.
type Status string

// Install job lifecycle states.
const (
	StatusIdle        Status = "idle"
	StatusRunning     Status = "running"
	StatusSucceeded   Status = "succeeded"
	StatusFailed      Status = "failed"
	StatusUnsupported Status = "unsupported"
)

const (
	maxOutputBytes        = 4000
	defaultInstallTimeout = 15 * time.Minute
)

// Job is the tracked state of one asynchronous install.
type Job struct {
	Target     Target     `json:"target" description:"Fixed agent harness this job ran (or is running) for."`
	Status     Status     `json:"status" enum:"idle,running,succeeded,failed,unsupported" description:"Current lifecycle state of the job."`
	Command    string     `json:"command,omitempty" description:"Human-readable fixed install command."`
	Output     string     `json:"output,omitempty" description:"Combined stdout and stderr, tail-capped to about 4000 bytes."`
	Error      string     `json:"error,omitempty" description:"Failure or unsupported reason."`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty" description:"Absent until the job finishes."`
}

// Service resolves and runs fixed harness installers.
type Service struct {
	mu   sync.Mutex
	jobs map[Target]*Job

	lookPath       func(string) (string, error)
	commandFunc    func(context.Context, []string) *exec.Cmd
	goos           string
	installTimeout time.Duration
}

// New returns a service backed by the host PATH and real commands.
func New() *Service {
	return &Service{
		jobs:           make(map[Target]*Job),
		lookPath:       exec.LookPath,
		goos:           runtime.GOOS,
		installTimeout: defaultInstallTimeout,
		commandFunc: func(ctx context.Context, argv []string) *exec.Cmd {
			return exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // fixed allowlist; argv is never caller-derived
		},
	}
}

// AgentPlans resolves one installation plan for every supported harness
// without executing anything.
func (s *Service) AgentPlans(ctx context.Context) ([]AgentPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]AgentPlan, 0, len(agentTargets))
	for _, target := range agentTargets {
		plan := s.planAgent(target)
		out = append(out, AgentPlan{
			AgentID: string(target), Available: !plan.Unsupported,
			Automatic: !plan.Unsupported, Method: plan.Method,
			Command: strings.Join(plan.Command, " "), Reason: plan.Reason,
			DocumentationURL: plan.DocsURL,
		})
	}
	return out, nil
}

// Start begins an install or returns the already-running job for the same
// target. Unsupported plans become terminal jobs rather than executing.
func (s *Service) Start(ctx context.Context, target Target) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	if !IsAgentTarget(target) {
		return Job{}, fmt.Errorf("systeminstall: unknown target %q", target)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.jobs[target]; ok && job.Status == StatusRunning {
		return *job, nil
	}

	plan := s.planAgent(target)
	now := time.Now()
	job := &Job{
		Target: target, Command: strings.Join(plan.Command, " "),
		StartedAt: &now,
	}
	if plan.Unsupported {
		job.Status = StatusUnsupported
		job.Error = plan.Reason
		job.FinishedAt = &now
		s.jobs[target] = job
		return *job, nil
	}

	job.Status = StatusRunning
	s.jobs[target] = job
	go s.run(plan.Command, job) //nolint:gosec // run owns a bounded background context
	return *job, nil
}

// Status returns the current or last job. A target not started yet is idle.
func (s *Service) Status(target Target) (Job, error) {
	if !IsAgentTarget(target) {
		return Job{}, fmt.Errorf("systeminstall: unknown target %q", target)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.jobs[target]; ok {
		return *job, nil
	}
	return Job{Target: target, Status: StatusIdle}, nil
}

func (s *Service) run(argv []string, job *Job) {
	ctx, cancel := context.WithTimeout(context.Background(), s.installTimeout)
	defer cancel()

	cmd := s.commandFunc(ctx, argv)
	out := &capturedOutput{max: maxOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = out
	runErr := cmd.Run()
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	job.Output = out.String()
	job.FinishedAt = &now
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		job.Status = StatusFailed
		job.Error = fmt.Sprintf("install timed out after %s", s.installTimeout)
	case runErr != nil:
		job.Status = StatusFailed
		job.Error = runErr.Error()
	default:
		job.Status = StatusSucceeded
	}
}

type capturedOutput struct {
	buf bytes.Buffer
	max int
}

func (c *capturedOutput) Write(p []byte) (int, error) {
	c.buf.Write(p)
	if c.buf.Len() > c.max {
		tail := c.buf.String()[c.buf.Len()-c.max:]
		c.buf.Reset()
		c.buf.WriteString(tail)
	}
	return len(p), nil
}

func (c *capturedOutput) String() string { return c.buf.String() }

func withDocs(plan Plan, docsURL string) Plan {
	plan.DocsURL = docsURL
	return plan
}

func (s *Service) planNPM(target Target, pkg string) Plan {
	if _, err := s.lookPath("npm"); err != nil {
		return Plan{Target: target, Unsupported: true, Method: "npm", Reason: "npm was not found on PATH. Install Node.js, then retry."}
	}
	return Plan{Target: target, Command: []string{"npm", "install", "-g", pkg}, Method: "npm"}
}

func (s *Service) planOpencode() Plan {
	if s.goos == "windows" {
		return s.planWinget(TargetOpencode, "SST.opencode")
	}
	if _, err := s.lookPath("curl"); err != nil {
		return Plan{Target: TargetOpencode, Unsupported: true, Method: "official-installer", Reason: "curl was not found on PATH."}
	}
	if _, err := s.lookPath("bash"); err != nil {
		return Plan{Target: TargetOpencode, Unsupported: true, Method: "official-installer", Reason: "bash was not found on PATH."}
	}
	return Plan{Target: TargetOpencode, Command: []string{"bash", "-c", "curl -fsSL https://opencode.ai/install | bash"}, Method: "official-installer"}
}

func (s *Service) planBrew(target Target, pkg string) Plan {
	if _, err := s.lookPath("brew"); err != nil {
		return Plan{Target: target, Unsupported: true, Method: "homebrew", Reason: "Homebrew was not found on PATH."}
	}
	return Plan{Target: target, Command: []string{"brew", "install", pkg}, Method: "homebrew"}
}

func (s *Service) planBrewCask(target Target, pkg string) Plan {
	if _, err := s.lookPath("brew"); err != nil {
		return Plan{Target: target, Unsupported: true, Method: "homebrew", Reason: "Homebrew was not found on PATH."}
	}
	return Plan{Target: target, Command: []string{"brew", "install", "--cask", pkg}, Method: "homebrew"}
}

func (s *Service) planWinget(target Target, id string) Plan {
	if _, err := s.lookPath("winget"); err != nil {
		return Plan{Target: target, Unsupported: true, Method: "winget", Reason: "winget was not found on PATH."}
	}
	return Plan{Target: target, Command: []string{"winget", "install", "-e", "--id", id}, Method: "winget"}
}
