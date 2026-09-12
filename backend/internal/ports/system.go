package ports

import (
	"context"
	"io"
	"time"
)

// ExecutableFinder resolves command names against the host PATH. Core
// services depend on this port rather than importing os/exec directly.
type ExecutableFinder interface {
	LookPath(file string) (string, error)
}

// CommandRunner executes an already-resolved argv and streams output to the
// supplied writers. Callers are responsible for constraining argv to a safe
// allowlist before it reaches this boundary.
type CommandRunner interface {
	Run(ctx context.Context, argv []string, stdout, stderr io.Writer) error
}

// InstallCommand carries the extra execution policy required for unattended
// installers. Argv remains server-owned; Env contains only explicit overrides.
type InstallCommand struct {
	Argv []string
	Env  []string
}

// InstallCommandRunner executes an installer with closed stdin and controlled
// noninteractive environment overrides.
type InstallCommandRunner interface {
	RunInstall(ctx context.Context, command InstallCommand, stdout, stderr io.Writer) error
}

// InstallScriptCommand is a daemon-owned remote installer recipe. The caller
// selects the exact URL and interpreter; the adapter downloads the complete
// script before executing it.
type InstallScriptCommand struct {
	URL         string
	Interpreter []string
	Env         []string
}

// InstallScriptResult records display-safe evidence about a downloaded script.
type InstallScriptResult struct {
	SHA256 string
}

// InstallScriptRunner downloads and executes a fixed remote installer without
// exposing script authority to clients.
type InstallScriptRunner interface {
	RunInstallScript(ctx context.Context, command InstallScriptCommand, stdout, stderr io.Writer) (InstallScriptResult, error)
}

// NPMInstallCapabilities is one request-scoped npm/Node preflight snapshot.
// Err is manager-specific so an unavailable npm does not suppress viable
// Homebrew recipes in the same catalog response.
type NPMInstallCapabilities struct {
	NodeVersion    string
	NPMVersion     string
	GlobalPrefix   string
	PrefixWritable bool
	Err            error
}

// HomebrewInstallCapabilities is one request-scoped Homebrew preflight
// snapshot. Installed package maps come from successful full inventory reads;
// callers must treat Err as unavailable rather than guessing "not installed".
type HomebrewInstallCapabilities struct {
	Prefix         string
	PrefixWritable bool
	Formulae       map[string]bool
	Casks          map[string]bool
	Err            error
}

// InstallCapabilities contains all subprocess-backed package-manager facts
// used to resolve the server-owned recipe catalog for one request.
type InstallCapabilities struct {
	NPM      NPMInstallCapabilities
	Homebrew HomebrewInstallCapabilities
}

// InstallCapabilityProbe resolves package-manager state that PATH lookup alone
// cannot validate safely. Probe must honor ctx and returns one immutable
// snapshot so recipe resolution never reruns subprocesses per harness.
type InstallCapabilityProbe interface {
	Probe(ctx context.Context) (InstallCapabilities, error)
}

// AgentInstallJobRecord is the storage-bound representation of a daemon-owned
// harness installation job. Strings keep the persistence adapter independent
// of the systeminstall package's domain types.
type AgentInstallJobRecord struct {
	Target              string
	Status              string
	Method              string
	Command             string
	ExpectedDestination string
	Output              string
	Error               string
	StartedAt           time.Time
	FinishedAt          *time.Time
	UpdatedAt           time.Time
}

// AgentInstallJobStore persists the latest harness installation job per
// target so Settings can recover it after remounts and daemon restarts.
type AgentInstallJobStore interface {
	UpsertAgentInstallJob(ctx context.Context, job AgentInstallJobRecord) error
	GetAgentInstallJob(ctx context.Context, target string) (AgentInstallJobRecord, bool, error)
	ListAgentInstallJobs(ctx context.Context) ([]AgentInstallJobRecord, error)
	InterruptActiveAgentInstallJobs(ctx context.Context, interruptedAt time.Time) error
}
