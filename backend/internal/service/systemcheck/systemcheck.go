// Package systemcheck reports lightweight executable prerequisites the desktop
// app checks before showing the board: git, tmux (macOS/Linux only), one agent
// executable, and the advisory GitHub CLI. It also supports a deeper,
// user-triggered agent-harness inventory check, which is intentionally
// excluded from first-render startup.
package systemcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
	"github.com/aoagents/agent-orchestrator/backend/internal/tmuxbin"
)

// Requirement is one named startup gate check.
type Requirement struct {
	ID        string `json:"id" enum:"git,tmux,harness,gh,github-auth" description:"Stable requirement identifier."`
	Label     string `json:"label" description:"Human-readable requirement name."`
	Satisfied bool   `json:"satisfied" description:"Whether this requirement is currently met."`
	Required  bool   `json:"required" description:"Whether this requirement blocks the overall Ready state."`
	Detail    string `json:"detail,omitempty" description:"Extra context: the resolved path when satisfied, or why it is not."`
}

// Report is a requirements result suitable for either the lightweight startup
// preflight or a deeper, user-triggered environment check.
type Report struct {
	Ready        bool          `json:"ready" description:"True iff every requirement with Required=true is satisfied. Requirements with Required=false (e.g. gh) are advisory and never block readiness."`
	Requirements []Requirement `json:"requirements" description:"Individual checks in stable order for the selected probe."`
}

// HarnessCatalog is the subset of agent.Service the harness requirement needs.
// The implementation delegates freshness to the shared readiness coordinator.
type HarnessCatalog interface {
	RefreshFresh(ctx context.Context) (agentsvc.Inventory, error)
	FindInstalledBinary(ctx context.Context) (agentsvc.Info, bool)
}

// GitHubAuthTerminalOpener starts the reviewed GitHub CLI login command in a
// daemon-owned PTY. The renderer never supplies command arguments.
type GitHubAuthTerminalOpener interface {
	OpenCommandTerminal(context.Context, shellterm.OpenCommandTerminalInput) (shellterm.ShellTerminal, error)
}

// Service runs the startup requirements gate.
type Service struct {
	harnesses   HarnessCatalog
	executables ports.ExecutableFinder
	commands    ports.CommandRunner
	terminals   GitHubAuthTerminalOpener
}

// SetGitHubAuthTerminalOpener late-binds the shell terminal service, which is
// created after startup checks during daemon assembly.
func (s *Service) SetGitHubAuthTerminalOpener(terminals GitHubAuthTerminalOpener) {
	s.terminals = terminals
}

// New returns a Service backed by the supplied host executable adapter and
// harness catalog (an *agent.Service in production).
func New(harnesses HarnessCatalog, executables ports.ExecutableFinder) *Service {
	return &Service{harnesses: harnesses, executables: executables}
}

// NewWithCommandRunner returns a Service that can also verify GitHub CLI
// credential configuration. The probe never captures token output.
func NewWithCommandRunner(harnesses HarnessCatalog, executables ports.ExecutableFinder, commands ports.CommandRunner) *Service {
	return &Service{harnesses: harnesses, executables: executables, commands: commands}
}

type executableFinderFunc func(string) (string, error)

func (f executableFinderFunc) LookPath(file string) (string, error) { return f(file) }

// NewWithLookPath returns a Service with an injected lookPath, for tests that
// need deterministic binary-resolution results without touching the real PATH.
func NewWithLookPath(harnesses HarnessCatalog, lookPath func(string) (string, error)) *Service {
	return New(harnesses, executableFinderFunc(lookPath))
}

// CheckStartup runs only the inexpensive prerequisite probes needed before AO
// presents its primary session UI. It deliberately excludes authentication
// probes because they can invoke CLIs or credential stores and must not delay
// first render.
func (s *Service) CheckStartup(ctx context.Context) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	return reportFor([]Requirement{
		s.checkGit(),
		s.checkTmux(),
		s.checkStartupHarness(ctx),
		s.checkGH(),
	}), nil
}

// CheckGitHubAuth runs the bounded, advisory GitHub credential probe separately
// from the startup gate so a slow credential store cannot delay first render.
func (s *Service) CheckGitHubAuth(ctx context.Context) (Requirement, error) {
	if err := ctx.Err(); err != nil {
		return Requirement{}, err
	}
	return s.checkGitHubAuth(ctx), nil
}

// OpenGitHubAuthTerminal starts the fixed GitHub CLI login flow. Executable
// resolution remains daemon-owned so callers cannot choose an arbitrary
// command or binary.
func (s *Service) OpenGitHubAuthTerminal(ctx context.Context) (shellterm.ShellTerminal, error) {
	if err := ctx.Err(); err != nil {
		return shellterm.ShellTerminal{}, err
	}
	path, err := s.executables.LookPath("gh")
	if err != nil || path == "" {
		return shellterm.ShellTerminal{}, apierr.Invalid("GITHUB_CLI_UNAVAILABLE", "GitHub CLI was not found on PATH.", nil)
	}
	if s.terminals == nil {
		return shellterm.ShellTerminal{}, apierr.Internal("GITHUB_AUTH_TERMINAL_UNAVAILABLE", "GitHub authentication terminal service is unavailable.")
	}
	return s.terminals.OpenCommandTerminal(ctx, shellterm.OpenCommandTerminalInput{
		// Keep the native interactive flow so users can choose GitHub.com or
		// Enterprise, HTTPS or SSH, and any authentication mode supported by their
		// installed gh version. The renderer forwards the terminal protocol replies
		// these prompts require.
		Argv:  []string{path, "auth", "login"},
		Title: "Connect GitHub",
	})
}

// Check runs the complete, user-triggered requirements probe, including a
// fresh agent inventory. Startup callers should use CheckStartup instead.
func (s *Service) Check(ctx context.Context) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}

	requirements := []Requirement{
		s.checkGit(),
		s.checkTmux(),
		s.checkHarness(ctx),
		s.checkGH(),
		s.checkGitHubAuth(ctx),
	}

	return reportFor(requirements), nil
}

func reportFor(requirements []Requirement) Report {
	ready := true
	for _, req := range requirements {
		if req.Required && !req.Satisfied {
			ready = false
			break
		}
	}
	return Report{Ready: ready, Requirements: requirements}
}

func (s *Service) checkGit() Requirement {
	path, err := s.executables.LookPath("git")
	if err != nil || path == "" {
		return Requirement{ID: "git", Label: "git", Required: true, Detail: "git was not found on PATH."}
	}
	return Requirement{ID: "git", Label: "git", Satisfied: true, Required: true, Detail: path}
}

func (s *Service) checkTmux() Requirement {
	if runtime.GOOS == "windows" {
		// tmux is a macOS/Linux-only requirement: AO uses the built-in ConPTY
		// terminal runtime on Windows instead, so this always passes there.
		return Requirement{
			ID: "tmux", Label: "tmux", Satisfied: true, Required: true,
			Detail: "Not required on Windows — AO uses the built-in ConPTY terminal runtime instead of tmux.",
		}
	}
	configured := strings.TrimSpace(os.Getenv("AO_TMUX_BINARY"))
	resolution, err := tmuxbin.ResolveWith(configured, os.Executable, s.executables.LookPath)
	if err != nil || resolution.Path == "" {
		detail := "tmux was not found on PATH; it is required on macOS/Linux to start sessions."
		if configured != "" {
			detail = "AO's bundled tmux is missing or not executable: " + configured
		}
		return Requirement{
			ID: "tmux", Label: "tmux", Required: true,
			Detail: detail,
		}
	}
	return Requirement{ID: "tmux", Label: "tmux", Satisfied: true, Required: true, Detail: resolution.Path}
}

func (s *Service) checkHarness(ctx context.Context) Requirement {
	const label = "agent harness"
	inv, err := s.harnesses.RefreshFresh(ctx)
	if err != nil {
		return Requirement{ID: "harness", Label: label, Required: true, Detail: err.Error()}
	}
	if len(inv.Installed) == 0 {
		return Requirement{
			ID: "harness", Label: label, Required: true,
			Detail: "No agent CLI (Claude Code, Codex, etc.) was found on PATH.",
		}
	}
	labels := make([]string, 0, len(inv.Installed))
	for _, info := range inv.Installed {
		labels = append(labels, info.Label)
	}
	return Requirement{ID: "harness", Label: label, Satisfied: true, Required: true, Detail: strings.Join(labels, ", ")}
}

// checkStartupHarness verifies only that one supported agent executable can
// be resolved. Authentication is intentionally deferred: many agent CLIs
// determine it by starting a process, which must not delay first render.
func (s *Service) checkStartupHarness(ctx context.Context) Requirement {
	const label = "agent harness"
	info, ok := s.harnesses.FindInstalledBinary(ctx)
	if !ok {
		return Requirement{
			ID: "harness", Label: label, Required: true,
			Detail: "No agent CLI (Claude Code, Codex, etc.) was found on PATH or in a supported install location.",
		}
	}
	return Requirement{ID: "harness", Label: label, Satisfied: true, Required: true, Detail: info.Label}
}

// checkGH probes for the GitHub CLI. It is advisory only (Required: false):
// agent sessions use it to open pull requests and read issues, but AO itself
// never depends on it, so its absence must never block Ready.
func (s *Service) checkGH() Requirement {
	path, err := s.executables.LookPath("gh")
	if err != nil || path == "" {
		return Requirement{
			ID: "gh", Label: "gh",
			Detail: "gh was not found on PATH. It lets agent sessions open pull requests and read issues, but AO runs fine without it.",
		}
	}
	return Requirement{ID: "gh", Label: "gh", Satisfied: true, Detail: path}
}

// checkGitHubAuth is advisory: local work remains available when GitHub is
// not configured, but onboarding should surface the missing capability before
// an agent first tries to open a pull request. Token bytes are never captured.
func (s *Service) checkGitHubAuth(ctx context.Context) Requirement {
	const detail = "Sign in with `gh auth login` so agent sessions can open pull requests and read issues."
	path, err := s.executables.LookPath("gh")
	if err != nil || path == "" {
		return Requirement{ID: "github-auth", Label: "GitHub access", Detail: detail}
	}
	if s.commands == nil {
		// An unavailable probe is not proof of authentication. Keep this advisory
		// unsatisfied so a wiring error cannot silently disable onboarding.
		return Requirement{ID: "github-auth", Label: "GitHub access", Detail: detail}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	err = s.commands.Run(probeCtx, []string{path, "auth", "status", "--active", "--json", "hosts"}, &stdout, io.Discard)
	var status struct {
		Hosts map[string][]struct {
			Active bool   `json:"active"`
			State  string `json:"state"`
		} `json:"hosts"`
	}
	if err == nil && json.Unmarshal(stdout.Bytes(), &status) == nil {
		// An empty host map is a definitive sign-out, not a validation failure.
		if len(status.Hosts) == 0 {
			return Requirement{ID: "github-auth", Label: "GitHub access", Detail: detail}
		}
		for _, accounts := range status.Hosts {
			for _, account := range accounts {
				if account.Active && account.State == "success" {
					return Requirement{ID: "github-auth", Label: "GitHub access", Satisfied: true, Detail: "GitHub CLI is signed in."}
				}
			}
		}
	}
	// Unsupported/malformed JSON or failed validation falls back to the local
	// credential check so an offline user is not prompted to sign in again.
	if s.hasGitHubAuth(ctx, path) {
		return Requirement{ID: "github-auth", Label: "GitHub access", Satisfied: true, Detail: "GitHub CLI is signed in."}
	}
	return Requirement{ID: "github-auth", Label: "GitHub access", Detail: detail}
}

func (s *Service) hasGitHubAuth(ctx context.Context, path string) bool {
	// Give the local credential probe its own budget, even when status timed out.
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.commands.Run(probeCtx, []string{path, "auth", "token"}, io.Discard, io.Discard) == nil
}
