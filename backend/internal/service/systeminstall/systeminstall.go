// Package systeminstall executes real install commands from a fixed allowlist
// of system prerequisites and agent harnesses. This is
// the core security invariant of the package — a caller can only select
// which fixed Target value to install; the actual argv run on
// the machine is always built from hardcoded command shapes, never from
// caller-supplied strings. Runs are tracked as async Jobs so an HTTP handler
// never blocks on an installer that can take minutes.
package systeminstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	// ErrHarnessActive prevents installers from replacing a binary used by a live session.
	ErrHarnessActive = errors.New("systeminstall: harness has an active session")
	// ErrInstallMethod reports an unknown or currently unavailable install method.
	ErrInstallMethod = errors.New("systeminstall: invalid install method")
	// ErrInstallActive prevents a second operation from being silently discarded.
	ErrInstallActive = errors.New("systeminstall: harness install operation already active")
)

// Target is one of the fixed install targets AO knows how to install.
type Target string

// The exhaustive set of installable targets. No other value is ever accepted.
const (
	TargetTmux       Target = "tmux"
	TargetGH         Target = "gh"
	TargetClaude     Target = "claude"
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
	// TargetCloudflared is the optional connector that makes a paired phone
	// reachable from outside the local network.
	TargetCloudflared Target = "cloudflared"
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

// systemTargetSet is the stable contract of the legacy /system/install route.
// Agent-only targets use /agents/{agent}/install instead.
var systemTargetSet = map[Target]bool{
	TargetTmux: true, TargetGH: true, TargetClaude: true, TargetCloudflared: true,
	TargetCodex: true, TargetOpencode: true, TargetCopilot: true,
}

// knownTargets is the exhaustive allowlist backing Valid.
var knownTargets = func() map[Target]bool {
	out := make(map[Target]bool, len(systemTargetSet)+len(agentTargets))
	for target := range systemTargetSet {
		out[target] = true
	}
	for _, target := range agentTargets {
		out[target] = true
	}
	return out
}()

// IsAgentTarget reports whether target is a user-facing harness id.
func IsAgentTarget(target Target) bool { return agentTargetSet[target] }

// IsSystemTarget reports whether target belongs to the legacy system-install
// route documented by InstallTargetParam.
func IsSystemTarget(target Target) bool { return systemTargetSet[target] }

// Valid reports whether target is a known prerequisite or agent install target.
func Valid(target Target) bool {
	return knownTargets[target]
}

// Resolve reports how target would be installed on goos, without running
// anything and without constructing a Service.
//
// This is the package's answer to "how is this installed here?" Keeping one
// resolver lets the daemon expose a plan preview and lets the bootstrap CLI
// print the same manual tmux remedy without either caller executing arbitrary
// input. The caller supplies the PATH lookup boundary explicitly.
func Resolve(goos string, lookPath func(string) (string, error), target Target) Plan {
	if !Valid(target) {
		return Plan{Target: target, Unsupported: true, Reason: "unknown install target"}
	}
	return (&Service{goos: goos, executables: executableFinderFunc(lookPath)}).resolvePlan(target)
}

type executableFinderFunc func(string) (string, error)

func (f executableFinderFunc) LookPath(file string) (string, error) { return f(file) }

// Plan is the resolved install command for a Target on the current platform.
//
// Command is populated whenever an install command could be resolved at all,
// including when Unsupported is true. Those two are independent on purpose:
// "we know exactly how to install this" and "this daemon is allowed to run it"
// are different questions. On Linux every package manager needs root, so the
// daemon refuses (Unsupported) while still reporting the argv for the desktop
// to display as a manual command. Callers that intend to execute a Plan must
// branch on Unsupported; callers that only need to know whether a route exists
// should test len(Command).
type Plan struct {
	Target              Target
	Command             []string // argv, e.g. ["brew", "install", "tmux"]
	Script              *ports.InstallScriptCommand
	Manager             string // resolving package manager ("brew", "apt-get", ...), empty when none applies
	NeedsRoot           bool   // Command must run as root; the caller supplies the privilege
	Unsupported         bool
	Reason              string // set when Unsupported, or as extra context otherwise
	Method              string
	DocsURL             string
	ExpectedDestination string
}

// AgentPlan is the display-safe plan returned to the settings page. Command is
// a preview of fixed server-owned argv and is never accepted from the client.
type AgentPlan struct {
	AgentID             string               `json:"agentId"`
	Available           bool                 `json:"available"`
	Automatic           bool                 `json:"automatic"`
	Method              string               `json:"method"`
	Command             string               `json:"command,omitempty"`
	Reason              string               `json:"reason,omitempty"`
	DocumentationURL    string               `json:"documentationUrl"`
	ExpectedDestination string               `json:"expectedDestination,omitempty"`
	Methods             []AgentInstallMethod `json:"methods"`
}

// AgentInstallMethod is one server-owned installation recipe and its current
// viability. The renderer may select ID but never submits argv.
type AgentInstallMethod struct {
	ID                  string `json:"id"`
	Label               string `json:"label"`
	Available           bool   `json:"available"`
	Recommended         bool   `json:"recommended"`
	Command             string `json:"command,omitempty"`
	Reason              string `json:"reason,omitempty"`
	ExpectedDestination string `json:"expectedDestination,omitempty"`
	ReinstallAvailable  bool   `json:"reinstallAvailable"`
	ReinstallCommand    string `json:"reinstallCommand,omitempty"`
	ReinstallReason     string `json:"reinstallReason,omitempty"`
}

// AgentOperation distinguishes a first installation from an explicit rebuild
// of an existing harness environment.
type AgentOperation string

const (
	// AgentOperationInstall requests a first-time installation.
	AgentOperationInstall AgentOperation = "install"
	// AgentOperationReinstall requests an explicit rebuild of an existing installation.
	AgentOperationReinstall AgentOperation = "reinstall"
)

func (operation AgentOperation) valid() bool {
	return operation == AgentOperationInstall || operation == AgentOperationReinstall
}

// Status is the lifecycle state of an install Job.
type Status string

// The full set of Job lifecycle states.
const (
	StatusIdle        Status = "idle"
	StatusRunning     Status = "running"
	StatusInstalling  Status = "installing"
	StatusVerifying   Status = "verifying"
	StatusSucceeded   Status = "succeeded"
	StatusFailed      Status = "failed"
	StatusUnsupported Status = "unsupported"
	StatusInterrupted Status = "interrupted"
)

// maxOutputBytes bounds Job.Output so a chatty installer can't grow memory
// unbounded — only the last ~4000 bytes are kept.
const maxOutputBytes = 4000

// defaultInstallTimeout bounds how long a single install run may take. A
// stalled installer (a network hang on curl, a held brew/apt lock, winget
// waiting on a prompt it'll never get) would otherwise pin its target in
// StatusRunning forever: no retry path, and the caller polls an indefinite
// progress bar. Real installs (npm global, brew, curl-piped scripts) normally
// finish in well under a minute; 15 minutes is generous headroom, not a
// realistic expected duration.
const defaultInstallTimeout = 15 * time.Minute

// defaultPersistenceTimeout keeps best-effort worker state writes from
// consuming the daemon's entire shutdown drain budget behind a blocked DB.
const defaultPersistenceTimeout = 2 * time.Second

// Job is the tracked state of one install run for a Target.
type Job struct {
	Target              Target `json:"target" enum:"tmux,gh,claude,claude-code,codex,cursor,opencode,aider,copilot,grok,kimi,pi,amp,auggie,droid,crush,cline,goose,qwen,continue,devin,kiro,kilocode,vibe,muse,agy,autohand,kimchi,prime-agent,omp,cloudflared" description:"Fixed install target this job ran (or is running) for."`
	Status              Status `json:"status" enum:"idle,running,installing,verifying,succeeded,failed,unsupported,interrupted" description:"Current lifecycle state of the job."`
	Method              string `json:"method,omitempty" description:"Server-owned installation method selected for this harness job."`
	Command             string `json:"command,omitempty" description:"Human-readable install command, e.g. \"brew install tmux\", for display even before/without output."`
	ExpectedDestination string `json:"expectedDestination,omitempty" description:"Expected or adapter-resolved executable destination."`
	Output              string `json:"output,omitempty" description:"Combined stdout+stderr from the install command, tail-capped to the last ~4000 bytes."`
	Error               string `json:"error,omitempty" description:"Set on failure or when the target is unsupported on this machine: the exec error, the Unsupported reason, or a timeout message."`
	// Pointers, not time.Time: omitempty has no effect on a struct, so a bare
	// time.Time always serializes (as the zero value's "0001-01-01..."
	// timestamp) even when nothing has happened yet. A nil pointer actually
	// omits the field, matching FinishedAt's documented "zero until the job
	// finishes" contract.
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty" description:"Absent until the job finishes."`
	UpdatedAt  *time.Time `json:"updatedAt,omitempty"`
}

// HarnessVerifier performs post-install binary verification without probing
// authentication.
type HarnessVerifier interface {
	Verify(ctx context.Context, target Target) (VerifyResult, error)
}

// SessionLister exposes durable lifecycle facts used to keep the Droid vendor
// installer away from active Droid processes.
type SessionLister interface {
	ListAllSessions(ctx context.Context) ([]domain.SessionRecord, error)
}

// Deps are the durable and adapter-backed dependencies used for harness jobs.
type Deps struct {
	JobStore ports.AgentInstallJobStore
	Verifier HarnessVerifier
	Sessions SessionLister
}

// Service runs real install commands for the fixed Target allowlist.
type Service struct {
	mu                sync.Mutex
	jobs              map[Target]*Job
	backgroundContext context.Context
	stop              context.CancelFunc
	stopping          bool
	workers           sync.WaitGroup
	droidGate         sync.RWMutex

	executables         ports.ExecutableFinder
	commands            ports.CommandRunner
	installCommands     ports.InstallCommandRunner
	installScripts      ports.InstallScriptRunner
	installCapabilities ports.InstallCapabilityProbe
	jobStore            ports.AgentInstallJobStore
	verifier            HarnessVerifier
	sessions            SessionLister
	// goos selects the platform branch in planFor. Real use is always
	// runtime.GOOS; tests override it to exercise every OS branch from one
	// machine, the same seam lookPath provides for PATH probing.
	goos string
	// installTimeout bounds each run — see defaultInstallTimeout. Tests
	// override it with a short duration to exercise the timeout path without
	// a real multi-minute wait.
	installTimeout time.Duration
	// persistenceTimeout bounds worker-owned transition and terminal writes.
	persistenceTimeout time.Duration
	onSucceeded        func(Target)
}

// requestPlanner carries one immutable capability snapshot through all recipe
// resolution performed for a single HTTP/service request.
type requestPlanner struct {
	*Service
	capabilities *ports.InstallCapabilities
	ctx          context.Context
}

func (s *Service) newRequestPlanner(ctx context.Context) (requestPlanner, error) {
	planner := requestPlanner{Service: s, ctx: ctx}
	if s.installCapabilities == nil {
		return planner, nil
	}
	capabilities, err := s.installCapabilities.Probe(ctx)
	if err != nil {
		return requestPlanner{}, err
	}
	planner.capabilities = &capabilities
	return planner, nil
}

// SetOnSucceeded registers the daemon callback invoked after a verified
// install. It is called outside the job mutex.
func (s *Service) SetOnSucceeded(callback func(Target)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSucceeded = callback
}

// New returns a Service backed by explicit host-operation ports. The daemon
// supplies their concrete adapter; core service code never invokes os/exec.
func New(executables ports.ExecutableFinder, commands ports.CommandRunner) *Service {
	return NewWithDeps(executables, commands, Deps{})
}

// NewWithDeps returns a Service with durable harness-job and verification
// dependencies supplied by the daemon.
func NewWithDeps(executables ports.ExecutableFinder, commands ports.CommandRunner, deps Deps) *Service {
	installCommands, _ := commands.(ports.InstallCommandRunner)
	installScripts, _ := commands.(ports.InstallScriptRunner)
	installCapabilities, _ := executables.(ports.InstallCapabilityProbe)
	backgroundContext, stop := context.WithCancel(context.Background())
	return &Service{
		jobs:                make(map[Target]*Job),
		executables:         executables,
		commands:            commands,
		installCommands:     installCommands,
		installScripts:      installScripts,
		installCapabilities: installCapabilities,
		jobStore:            deps.JobStore,
		verifier:            deps.Verifier,
		sessions:            deps.Sessions,
		goos:                runtime.GOOS,
		installTimeout:      defaultInstallTimeout,
		persistenceTimeout:  defaultPersistenceTimeout,
		stop:                stop,
		backgroundContext:   backgroundContext,
	}
}

// AgentPlans resolves one installation plan for every supported harness
// without executing anything.
func (s *Service) AgentPlans(ctx context.Context) ([]AgentPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	planner, err := s.newRequestPlanner(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AgentPlan, 0, len(agentTargets))
	for _, target := range agentTargets {
		plans := planner.agentMethodPlans(target, AgentOperationInstall)
		reinstallPlans := planner.agentMethodPlans(target, AgentOperationReinstall)
		reinstallByMethod := make(map[string]Plan, len(reinstallPlans))
		for _, reinstallPlan := range reinstallPlans {
			reinstallByMethod[reinstallPlan.Method] = reinstallPlan
		}
		recommended := recommendedPlanIndex(plans)
		plan := plans[recommended]
		methods := make([]AgentInstallMethod, 0, len(plans))
		for index, methodPlan := range plans {
			reinstallPlan := reinstallByMethod[methodPlan.Method]
			methods = append(methods, AgentInstallMethod{
				ID: methodPlan.Method, Label: installMethodLabel(methodPlan.Method),
				Available: !methodPlan.Unsupported, Recommended: index == recommended,
				Command: displayCommand(methodPlan), Reason: methodPlan.Reason,
				ExpectedDestination: methodPlan.ExpectedDestination,
				ReinstallAvailable:  !reinstallPlan.Unsupported,
				ReinstallCommand:    displayCommand(reinstallPlan), ReinstallReason: reinstallPlan.Reason,
			})
		}
		out = append(out, AgentPlan{
			AgentID: string(target), Available: !plan.Unsupported,
			Automatic: !plan.Unsupported, Method: plan.Method,
			Command: displayCommand(plan), Reason: plan.Reason,
			DocumentationURL:    plan.DocsURL,
			ExpectedDestination: plan.ExpectedDestination,
			Methods:             methods,
		})
	}
	return out, nil
}

func recommendedPlanIndex(plans []Plan) int {
	for index, plan := range plans {
		if plan.Method == "official-installer" && !plan.Unsupported {
			return index
		}
	}
	for index, plan := range plans {
		if !plan.Unsupported {
			return index
		}
	}
	return len(plans) - 1
}

func installMethodLabel(method string) string {
	switch method {
	case "homebrew":
		return "Homebrew"
	case "npm":
		return "npm"
	case "winget":
		return "winget"
	case "uv":
		return "uv tool"
	case "pipx":
		return "pipx"
	case "bun":
		return "Bun"
	case "official-installer":
		return "Official installer"
	default:
		return "Manual installation"
	}
}

// Start begins the install for target, or returns the already-running Job if
// one is in flight (idempotent — it never starts a second concurrent run of
// the same target). target must be one of the fixed known values; anything else
// is a caller bug and returns an error the controller turns into a 400.
func (s *Service) Start(ctx context.Context, target Target) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	if !Valid(target) {
		return Job{}, fmt.Errorf("systeminstall: unknown target %q", target)
	}
	if IsAgentTarget(target) {
		return s.StartAgent(ctx, target, "")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if job, ok := s.jobs[target]; ok && job.Status == StatusRunning {
		return *job, nil
	}

	plan := s.resolvePlan(target)
	command := displayCommand(plan)
	now := time.Now()
	if plan.Unsupported {
		job := &Job{
			Target:     target,
			Status:     StatusUnsupported,
			Command:    command,
			Error:      plan.Reason,
			StartedAt:  &now,
			FinishedAt: &now,
		}
		s.jobs[target] = job
		return *job, nil
	}

	job := &Job{
		Target:    target,
		Status:    StatusRunning,
		Command:   command,
		StartedAt: &now,
	}
	s.jobs[target] = job
	if s.stopping {
		job.Status = StatusInterrupted
		job.Error = "daemon shutdown interrupted the install"
		job.FinishedAt = &now
		return *job, nil
	}
	s.workers.Add(1)

	go func() { //nolint:gosec // bounded daemon-owned worker intentionally outlives the request.
		defer s.workers.Done()
		s.run(s.backgroundContext, plan.Command, job)
	}()

	return *job, nil
}

// StartAgent begins a harness install using one server-owned method ID. An
// empty method chooses the first currently viable method for compatibility
// with older clients.
func (s *Service) StartAgent(ctx context.Context, target Target, method string) (Job, error) {
	return s.StartAgentOperation(ctx, target, method, AgentOperationInstall)
}

// StartAgentOperation begins a harness install or reinstall using one
// server-owned method ID and operation.
func (s *Service) StartAgentOperation(ctx context.Context, target Target, method string, operation AgentOperation) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	if !operation.valid() {
		return Job{}, fmt.Errorf("%w: unknown install operation %q", ErrInstallMethod, operation)
	}
	if !IsAgentTarget(target) {
		return Job{}, fmt.Errorf("systeminstall: unknown harness target %q", target)
	}
	var releaseDroid func()
	if target == TargetDroid {
		s.mu.Lock()
		if current, ok := s.jobs[target]; ok && activeStatus(current.Status) {
			s.mu.Unlock()
			return Job{}, ErrInstallActive
		}
		s.mu.Unlock()
		if !s.droidGate.TryLock() {
			return Job{}, fmt.Errorf("%w: a Droid session is starting", ErrHarnessActive)
		}
		releaseDroid = s.droidGate.Unlock
		defer func() {
			if releaseDroid != nil {
				releaseDroid()
			}
		}()
		if s.sessions != nil {
			sessions, err := s.sessions.ListAllSessions(ctx)
			if err != nil {
				return Job{}, fmt.Errorf("systeminstall: list sessions before droid install: %w", err)
			}
			for _, session := range sessions {
				if session.Harness == domain.HarnessDroid && !session.IsTerminated {
					return Job{}, fmt.Errorf("%w: end Droid session %s before installing or reinstalling Droid", ErrHarnessActive, session.ID)
				}
			}
		}
	}

	var plan Plan
	var err error
	planner, plannerErr := s.newRequestPlanner(ctx)
	if plannerErr != nil {
		return Job{}, plannerErr
	}
	if method == "" {
		plans := planner.agentMethodPlans(target, operation)
		plan = plans[recommendedPlanIndex(plans)]
	} else {
		plan, err = planner.resolveAgentMethod(target, method, operation)
		if err != nil {
			return Job{}, err
		}
	}

	s.mu.Lock()
	if current, ok := s.jobs[target]; ok && activeStatus(current.Status) {
		s.mu.Unlock()
		return Job{}, ErrInstallActive
	}
	previous, hadPrevious := s.jobs[target]
	now := time.Now().UTC()
	status := StatusInstalling
	finishedAt := (*time.Time)(nil)
	if plan.Unsupported {
		status = StatusUnsupported
		finishedAt = &now
	}
	job := &Job{
		Target: target, Status: status, Method: plan.Method,
		Command: displayCommand(plan), ExpectedDestination: plan.ExpectedDestination,
		Error: plan.Reason, StartedAt: &now, FinishedAt: finishedAt, UpdatedAt: &now,
	}
	s.jobs[target] = job
	s.mu.Unlock()

	if err := s.persistJob(ctx, *job); err != nil {
		s.mu.Lock()
		if s.jobs[target] == job {
			if hadPrevious {
				s.jobs[target] = previous
			} else {
				delete(s.jobs, target)
			}
		}
		s.mu.Unlock()
		return Job{}, err
	}
	if status == StatusUnsupported {
		return *job, nil
	}

	initial := *job
	if !s.beginWorker() {
		s.finishAgentJob(job, StatusInterrupted, "", "daemon shutdown interrupted the install", "")
		return initial, nil
	}
	workerRelease := releaseDroid
	releaseDroid = nil
	go func() { //nolint:gosec // bounded daemon-owned worker intentionally outlives the request.
		defer s.workers.Done()
		if workerRelease != nil {
			defer workerRelease()
		}
		s.runAgentInstall(s.backgroundContext, plan, job)
	}()
	return initial, nil
}

// TryBeginHarnessUse prevents a Droid session launch from racing replacement
// of the Droid executable. The returned release must be called after launch.
func (s *Service) TryBeginHarnessUse(harness domain.AgentHarness) (func(), bool) {
	if harness != domain.HarnessDroid {
		return func() {}, true
	}
	if !s.droidGate.TryRLock() {
		return nil, false
	}
	return s.droidGate.RUnlock, true
}

// Status returns the current or last known Job for target. A target that has
// never been started returns a plan preview: Idle when the daemon can run it,
// or Unsupported with a manual command/reason when it cannot. An error is
// returned only when target is not a known install target.
func (s *Service) Status(ctx context.Context, target Target) (Job, error) {
	if err := ctx.Err(); err != nil {
		return Job{}, err
	}
	if !Valid(target) {
		return Job{}, fmt.Errorf("systeminstall: unknown target %q", target)
	}
	s.mu.Lock()
	inMemory, ok := s.jobs[target]
	if ok {
		snapshot := *inMemory
		s.mu.Unlock()
		return snapshot, nil
	}
	s.mu.Unlock()
	if IsAgentTarget(target) && s.jobStore != nil {
		record, ok, err := s.jobStore.GetAgentInstallJob(ctx, string(target))
		if err != nil {
			return Job{}, err
		}
		if ok {
			return jobFromRecord(record), nil
		}
	}

	var plan Plan
	if IsAgentTarget(target) {
		planner, err := s.newRequestPlanner(ctx)
		if err != nil {
			return Job{}, err
		}
		plans := planner.agentMethodPlans(target, AgentOperationInstall)
		plan = plans[recommendedPlanIndex(plans)]
	} else {
		plan = s.resolvePlan(target)
	}
	status := StatusIdle
	if plan.Unsupported {
		status = StatusUnsupported
	}
	return Job{
		Target:  target,
		Status:  status,
		Command: displayCommand(plan),
		Error:   plan.Reason,
	}, nil
}

// AgentJobs returns the latest durable job for every harness that has one.
func (s *Service) AgentJobs(ctx context.Context) ([]Job, error) {
	if s.jobStore != nil {
		records, err := s.jobStore.ListAgentInstallJobs(ctx)
		if err != nil {
			return nil, err
		}
		jobs := make([]Job, 0, len(records))
		indexes := make(map[Target]int, len(records))
		for _, record := range records {
			job := jobFromRecord(record)
			indexes[job.Target] = len(jobs)
			jobs = append(jobs, job)
		}
		s.mu.Lock()
		for target, job := range s.jobs {
			if IsAgentTarget(target) {
				if index, ok := indexes[target]; ok {
					jobs[index] = *job
				} else {
					indexes[target] = len(jobs)
					jobs = append(jobs, *job)
				}
			}
		}
		s.mu.Unlock()
		return jobs, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := make([]Job, 0, len(s.jobs))
	for target, job := range s.jobs {
		if IsAgentTarget(target) {
			jobs = append(jobs, *job)
		}
	}
	return jobs, nil
}

// Recover marks unfinished durable jobs interrupted and hydrates the in-memory
// coordination map before the HTTP server starts accepting requests.
func (s *Service) Recover(ctx context.Context) error {
	if s.jobStore == nil {
		return nil
	}
	if err := s.jobStore.InterruptActiveAgentInstallJobs(ctx, time.Now().UTC()); err != nil {
		return err
	}
	records, err := s.jobStore.ListAgentInstallJobs(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range records {
		job := jobFromRecord(record)
		s.jobs[job.Target] = &job
	}
	return nil
}

// Verify starts adapter-backed verification without rerunning an installer.
func (s *Service) Verify(ctx context.Context, target Target) (Job, error) {
	if !IsAgentTarget(target) {
		return Job{}, fmt.Errorf("systeminstall: unknown harness target %q", target)
	}
	s.mu.Lock()
	if current, ok := s.jobs[target]; ok && activeStatus(current.Status) {
		s.mu.Unlock()
		return Job{}, ErrInstallActive
	}
	previous, hadPrevious := s.jobs[target]
	now := time.Now().UTC()
	job := &Job{Target: target, Status: StatusVerifying, StartedAt: &now, UpdatedAt: &now}
	if current, ok := s.jobs[target]; ok {
		job.Method = current.Method
		job.Command = current.Command
		job.ExpectedDestination = current.ExpectedDestination
		job.Output = current.Output
	}
	s.jobs[target] = job
	s.mu.Unlock()
	if err := s.persistJob(ctx, *job); err != nil {
		s.mu.Lock()
		if s.jobs[target] == job {
			if hadPrevious {
				s.jobs[target] = previous
			} else {
				delete(s.jobs, target)
			}
		}
		s.mu.Unlock()
		return Job{}, err
	}
	initial := *job
	if !s.beginWorker() {
		s.finishAgentJob(job, StatusInterrupted, "", "daemon shutdown interrupted verification", "")
		return initial, nil
	}
	go func() { //nolint:gosec // bounded daemon-owned worker intentionally outlives the request.
		defer s.workers.Done()
		s.runAgentVerification(s.backgroundContext, job)
	}()
	return initial, nil
}

func (s *Service) beginWorker() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return false
	}
	s.workers.Add(1)
	return true
}

// Close cancels and drains daemon-owned harness installation work.
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	if !s.stopping {
		s.stopping = true
		s.stop()
	}
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("systeminstall: drain workers: %w", ctx.Err())
	}
}

// run executes argv in the background and records the outcome onto job. job
// is only ever mutated here and read back through a copy under s.mu, so
// concurrent Start/Status calls never race with this goroutine's writes.
// The run is bounded by installTimeout so a stalled installer eventually
// surfaces as a failure instead of pinning the target in StatusRunning.
func (s *Service) run(parent context.Context, argv []string, job *Job) {
	ctx, cancel := context.WithTimeout(parent, s.installTimeout)
	defer cancel()

	out := &capturedOutput{max: maxOutputBytes}
	var runErr error
	if s.installCommands != nil {
		runErr = s.installCommands.RunInstall(ctx, ports.InstallCommand{
			Argv: argv,
			Env: []string{
				"CI=1",
				"NONINTERACTIVE=1",
				"HOMEBREW_NO_AUTO_UPDATE=1",
				"NPM_CONFIG_AUDIT=false",
				"NPM_CONFIG_FUND=false",
			},
		}, out, out)
	} else {
		runErr = s.commands.Run(ctx, argv, out, out)
	}
	now := time.Now()

	s.mu.Lock()
	job.Output = out.String()
	job.FinishedAt = &now
	if ctx.Err() == context.DeadlineExceeded {
		job.Status = StatusFailed
		job.Error = fmt.Sprintf("install timed out after %s", s.installTimeout)
		s.mu.Unlock()
		return
	}
	if ctx.Err() == context.Canceled {
		job.Status = StatusInterrupted
		job.Error = "daemon shutdown interrupted the install"
		s.mu.Unlock()
		return
	}
	if runErr != nil {
		job.Status = StatusFailed
		job.Error = runErr.Error()
		s.mu.Unlock()
		return
	}
	if !IsAgentTarget(job.Target) {
		if path, err := s.executables.LookPath(string(job.Target)); err != nil || path == "" {
			job.Status = StatusFailed
			job.Error = fmt.Sprintf("install command finished but %s is still not in PATH", job.Target)
			s.mu.Unlock()
			return
		}
	}
	job.Status = StatusSucceeded
	callback := s.onSucceeded
	target := job.Target
	s.mu.Unlock()
	if callback != nil {
		callback(target)
	}
}

func (s *Service) runAgentInstall(parent context.Context, plan Plan, job *Job) {
	ctx, cancel := context.WithTimeout(parent, s.installTimeout)
	defer cancel()
	out := &capturedOutput{max: maxOutputBytes}
	env := []string{
		"CI=1", "NONINTERACTIVE=1", "HOMEBREW_NO_AUTO_UPDATE=1",
		"NPM_CONFIG_AUDIT=false", "NPM_CONFIG_FUND=false",
	}
	var runErr error
	if plan.Script != nil {
		if s.installScripts == nil {
			runErr = errors.New("remote installer runner is not configured")
		} else {
			command := *plan.Script
			command.Env = append([]string(nil), env...)
			var result ports.InstallScriptResult
			result, runErr = s.installScripts.RunInstallScript(ctx, command, out, out)
			if result.SHA256 != "" {
				_, _ = fmt.Fprintf(out, "\nsource: %s\nsha256: %s\n", command.URL, result.SHA256)
			}
		}
	} else if s.installCommands != nil {
		runErr = s.installCommands.RunInstall(ctx, ports.InstallCommand{
			Argv: plan.Command,
			Env:  env,
		}, out, out)
	} else {
		runErr = s.commands.Run(ctx, plan.Command, out, out)
	}
	if ctx.Err() == context.DeadlineExceeded {
		s.finishAgentJob(job, StatusFailed, out.String(), fmt.Sprintf("install timed out after %s", s.installTimeout), "")
		return
	}
	if ctx.Err() == context.Canceled {
		s.finishAgentJob(job, StatusInterrupted, out.String(), "daemon shutdown interrupted the install", "")
		return
	}
	if runErr != nil {
		s.finishAgentJob(job, StatusFailed, out.String(), runErr.Error(), "")
		return
	}

	if err := s.transitionAgentJob(job, StatusVerifying, out.String(), "", ""); err != nil {
		s.finishAgentJob(job, StatusFailed, "", fmt.Sprintf("persist verifying state: %v", err), "")
		return
	}
	s.runAgentVerification(s.backgroundContext, job)
}

func (s *Service) runAgentVerification(ctx context.Context, job *Job) {
	if s.verifier == nil {
		s.finishAgentJob(job, StatusFailed, "", "adapter-backed install verifier is not configured", "")
		return
	}
	result, err := s.verifier.Verify(ctx, job.Target)
	if ctx.Err() == context.Canceled {
		s.finishAgentJob(job, StatusInterrupted, result.Output, "daemon shutdown interrupted verification", result.ResolvedPath)
		return
	}
	if err != nil {
		s.finishAgentJob(job, StatusFailed, result.Output, err.Error(), result.ResolvedPath)
		return
	}
	s.finishAgentJob(job, StatusSucceeded, result.Output, "", result.ResolvedPath)
}

func (s *Service) transitionAgentJob(job *Job, status Status, output, errorMessage, resolvedPath string) error {
	now := time.Now().UTC()
	s.mu.Lock()
	job.Status = status
	job.Output = combineOutput(job.Output, output)
	job.Error = errorMessage
	if resolvedPath != "" {
		job.ExpectedDestination = resolvedPath
	}
	job.UpdatedAt = &now
	snapshot := *job
	s.mu.Unlock()
	return s.persistJobBestEffort(snapshot)
}

func (s *Service) finishAgentJob(job *Job, status Status, output, errorMessage, resolvedPath string) {
	now := time.Now().UTC()
	s.mu.Lock()
	job.Status = status
	job.Output = combineOutput(job.Output, output)
	job.Error = errorMessage
	if resolvedPath != "" {
		job.ExpectedDestination = resolvedPath
	}
	job.FinishedAt = &now
	job.UpdatedAt = &now
	snapshot := *job
	callback := s.onSucceeded
	target := job.Target
	s.mu.Unlock()
	if err := s.persistJobBestEffort(snapshot); err != nil {
		s.mu.Lock()
		job.Status = StatusFailed
		job.Error = fmt.Sprintf("persist terminal install state: %v", err)
		s.mu.Unlock()
		return
	}
	if status == StatusSucceeded && callback != nil {
		callback(target)
	}
}

func (s *Service) persistJobBestEffort(job Job) error {
	timeout := s.persistenceTimeout
	if timeout <= 0 {
		timeout = defaultPersistenceTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.persistJob(ctx, job)
}

func (s *Service) persistJob(ctx context.Context, job Job) error {
	if s.jobStore == nil {
		return nil
	}
	if job.StartedAt == nil {
		return fmt.Errorf("systeminstall: persist job without startedAt")
	}
	updatedAt := *job.StartedAt
	if job.UpdatedAt != nil {
		updatedAt = *job.UpdatedAt
	}
	return s.jobStore.UpsertAgentInstallJob(ctx, ports.AgentInstallJobRecord{
		Target: string(job.Target), Status: string(job.Status), Method: job.Method,
		Command: job.Command, ExpectedDestination: job.ExpectedDestination,
		Output: job.Output, Error: job.Error, StartedAt: *job.StartedAt,
		FinishedAt: job.FinishedAt, UpdatedAt: updatedAt,
	})
}

func jobFromRecord(record ports.AgentInstallJobRecord) Job {
	startedAt := record.StartedAt
	updatedAt := record.UpdatedAt
	return Job{
		Target: Target(record.Target), Status: Status(record.Status), Method: record.Method,
		Command: record.Command, ExpectedDestination: record.ExpectedDestination,
		Output: record.Output, Error: record.Error, StartedAt: &startedAt,
		FinishedAt: record.FinishedAt, UpdatedAt: &updatedAt,
	}
}

func activeStatus(status Status) bool {
	return status == StatusRunning || status == StatusInstalling || status == StatusVerifying
}

func combineOutput(existing, next string) string {
	out := &capturedOutput{max: maxOutputBytes}
	_, _ = out.Write([]byte(existing))
	if existing != "" && next != "" && !strings.HasSuffix(existing, "\n") {
		_, _ = out.Write([]byte("\n"))
	}
	_, _ = out.Write([]byte(next))
	return out.String()
}

func (s *Service) resolvePlan(target Target) Plan {
	if IsAgentTarget(target) {
		return s.planAgent(target)
	}
	return s.planFor(target)
}

func displayCommand(plan Plan) string {
	if plan.Script != nil {
		return fmt.Sprintf("%s <downloaded from %s>", strings.Join(plan.Script.Interpreter, " "), plan.Script.URL)
	}
	argv := plan.Command
	if plan.NeedsRoot && len(argv) > 0 {
		argv = append([]string{"sudo"}, argv...)
	}
	return strings.Join(argv, " ")
}

// capturedOutput is an io.Writer that keeps only the last max bytes written,
// trimming from the front.
type capturedOutput struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (c *capturedOutput) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf.Write(p)
	if c.buf.Len() > c.max {
		tail := c.buf.String()[c.buf.Len()-c.max:]
		c.buf.Reset()
		c.buf.WriteString(tail)
	}
	return len(p), nil
}

func (c *capturedOutput) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// planFor resolves the install Plan for target on the current platform,
// probing PATH via s.executables so tests can inject deterministic results.
func (s *Service) planFor(target Target) Plan {
	switch target {
	case TargetTmux:
		return s.planTmux()
	case TargetGH:
		return s.planGH()
	case TargetClaude:
		return s.planNPM(TargetClaude, "@anthropic-ai/claude-code")
	case TargetCodex:
		return s.planNPM(TargetCodex, "@openai/codex")
	case TargetCopilot:
		return s.planNPM(TargetCopilot, "@github/copilot")
	case TargetOpencode:
		return s.planOpencode()
	case TargetCloudflared:
		return s.planCloudflared()
	default:
		return Plan{Target: target, Unsupported: true, Reason: "unknown install target"}
	}
}

func (s *Service) planTmux() Plan {
	switch s.goos {
	case "windows":
		return Plan{
			Target: TargetTmux, Unsupported: true,
			Reason: "tmux is not required on Windows; AO uses the built-in ConPTY terminal runtime instead.",
		}
	case "darwin":
		return s.planBrew(TargetTmux, "tmux")
	case "linux":
		return s.planLinuxPackage(TargetTmux, func(string) string { return "tmux" })
	default:
		return Plan{Target: TargetTmux, Unsupported: true, Reason: "tmux installation is not supported on this platform."}
	}
}

func (s *Service) planGH() Plan {
	switch s.goos {
	case "windows":
		return s.planWinget(TargetGH, "GitHub.cli")
	case "darwin":
		return s.planBrew(TargetGH, "gh")
	case "linux":
		return s.planLinuxPackage(TargetGH, func(mgr string) string {
			if mgr == "pacman" {
				return "github-cli"
			}
			return "gh"
		})
	default:
		return Plan{Target: TargetGH, Unsupported: true, Reason: "gh installation is not supported on this platform."}
	}
}

// planCloudflared mirrors planGH: Cloudflare publishes cloudflared through
// Homebrew and winget, and Linux distributions package it too.
//
// Deliberately not a downloaded binary. Fetching a release archive ourselves
// would mean pinning per-platform URLs, verifying checksums and clearing
// macOS quarantine before executing it — a fourth install shape this package
// does not have, for a tool the three it does have already cover. The one
// curl-piped target here is opencode, and only because that is the vendor's
// own documented installer.
func (s *Service) planCloudflared() Plan {
	switch s.goos {
	case "windows":
		return s.planWinget(TargetCloudflared, "Cloudflare.cloudflared")
	case "darwin":
		return s.planBrew(TargetCloudflared, "cloudflared")
	case "linux":
		return s.planLinuxPackage(TargetCloudflared, func(string) string { return "cloudflared" })
	default:
		return Plan{
			Target: TargetCloudflared, Unsupported: true,
			Reason: "cloudflared installation is not supported on this platform.",
		}
	}
}

func (s *Service) planNPM(target Target, pkg string) Plan {
	if !IsAgentTarget(target) {
		return (requestPlanner{Service: s}).planNPM(target, pkg)
	}
	planner, err := s.newRequestPlanner(context.Background())
	if err != nil {
		return Plan{Target: target, Unsupported: true, Method: "npm", Reason: "npm and Node.js capabilities could not be inspected."}
	}
	return planner.planNPM(target, pkg)
}

func (p requestPlanner) planNPM(target Target, pkg string) Plan {
	s := p.Service
	if _, err := s.executables.LookPath("npm"); err != nil {
		return Plan{
			Target: target, Unsupported: true,
			Method: "npm", Reason: "npm was not found on PATH. Install Node.js from https://nodejs.org first, then retry.",
		}
	}
	plan := Plan{Target: target, Command: []string{"npm", "install", "-g", pkg}, Method: "npm"}
	if IsAgentTarget(target) {
		if p.capabilities == nil || p.capabilities.NPM.Err != nil {
			plan.Unsupported = true
			plan.Reason = "npm and Node.js capabilities could not be inspected."
			return plan
		}
		npm := p.capabilities.NPM
		nodeVersion, nodeOK := parseToolVersion(npm.NodeVersion)
		_, npmOK := parseToolVersion(npm.NPMVersion)
		if !nodeOK || !npmOK {
			plan.Unsupported = true
			plan.Reason = "npm and Node.js versions could not be validated."
			return plan
		}
		minimumNode := minimumNodeVersionForTarget(target)
		if !versionAtLeast(nodeVersion, minimumNode) {
			plan.Unsupported = true
			plan.Reason = fmt.Sprintf("Node.js %s+ is required for %s's npm recipe; found %s.", formatToolVersion(minimumNode), target, npm.NodeVersion)
			return plan
		}
		prefix := npm.GlobalPrefix
		if prefix == "" {
			plan.Unsupported = true
			plan.Reason = "npm's global install prefix could not be resolved."
			return plan
		}
		if !npm.PrefixWritable {
			plan.Unsupported = true
			plan.Reason = fmt.Sprintf("npm's global prefix %s is not writable by the current user. Configure a user-owned prefix; AO will not use sudo.", prefix)
			return plan
		}
		if s.goos == "windows" {
			plan.ExpectedDestination = prefix
		} else {
			plan.ExpectedDestination = filepath.Join(prefix, "bin")
		}
	}
	return plan
}

func minimumNodeVersionForTarget(target Target) [3]int {
	switch target {
	case TargetAuggie, TargetDroid:
		return [3]int{20, 0, 0}
	case TargetClaudeCode, TargetQwen, TargetAutohand:
		return [3]int{22, 0, 0}
	case TargetPi:
		return [3]int{22, 19, 0}
	default:
		return [3]int{16, 0, 0}
	}
}

func formatToolVersion(version [3]int) string {
	if version[2] != 0 {
		return fmt.Sprintf("%d.%d.%d", version[0], version[1], version[2])
	}
	if version[1] != 0 {
		return fmt.Sprintf("%d.%d", version[0], version[1])
	}
	return strconv.Itoa(version[0])
}

func parseToolVersion(value string) ([3]int, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	fields := strings.Split(value, ".")
	if len(fields) == 0 || len(fields) > 3 {
		return [3]int{}, false
	}
	var parsed [3]int
	for index, field := range fields {
		part, err := strconv.Atoi(field)
		if err != nil || part < 0 {
			return [3]int{}, false
		}
		parsed[index] = part
	}
	return parsed, true
}

func versionAtLeast(got, minimum [3]int) bool {
	for index := range got {
		if got[index] != minimum[index] {
			return got[index] > minimum[index]
		}
	}
	return true
}

func (s *Service) planOpencode() Plan {
	if s.goos == "windows" {
		return s.planWinget(TargetOpencode, "SST.opencode")
	}
	return Plan{
		Target: TargetOpencode, Unsupported: true, Method: "manual",
		Reason: "AO does not automatically execute opencode's mutable remote installer script.",
	}
}

func (s *Service) planBrew(target Target, pkg string) Plan {
	if !IsAgentTarget(target) {
		return (requestPlanner{Service: s}).planBrew(target, pkg)
	}
	planner, err := s.newRequestPlanner(context.Background())
	if err != nil {
		return Plan{Target: target, Unsupported: true, Method: "homebrew", Reason: "Homebrew packages could not be inspected."}
	}
	return planner.planBrew(target, pkg)
}

func (s *Service) planBrewCask(target Target, pkg string) Plan {
	if !IsAgentTarget(target) {
		return (requestPlanner{Service: s}).planBrewCask(target, pkg)
	}
	planner, err := s.newRequestPlanner(context.Background())
	if err != nil {
		return Plan{Target: target, Unsupported: true, Method: "homebrew", Reason: "Homebrew packages could not be inspected."}
	}
	return planner.planBrewCask(target, pkg)
}

func (p requestPlanner) planBrew(target Target, pkg string) Plan {
	return p.planHomebrew(target, pkg, false)
}

func (p requestPlanner) planBrewCask(target Target, pkg string) Plan {
	return p.planHomebrew(target, pkg, true)
}

func (p requestPlanner) planHomebrew(target Target, pkg string, cask bool) Plan {
	s := p.Service
	if _, err := s.executables.LookPath("brew"); err != nil {
		return Plan{
			Target: target, Unsupported: true,
			Method: "homebrew", Reason: "Homebrew was not found on PATH. Install it from https://brew.sh first, then retry.",
		}
	}
	var installed bool
	if IsAgentTarget(target) {
		if p.capabilities == nil || p.capabilities.Homebrew.Err != nil {
			return Plan{Target: target, Unsupported: true, Method: "homebrew", Reason: "Homebrew packages could not be inspected."}
		}
		homebrew := p.capabilities.Homebrew
		prefix := homebrew.Prefix
		if prefix == "" {
			return Plan{Target: target, Unsupported: true, Method: "homebrew", Reason: "Homebrew's installation prefix could not be resolved."}
		}
		if !homebrew.PrefixWritable {
			return Plan{Target: target, Unsupported: true, Method: "homebrew", Reason: fmt.Sprintf("Homebrew's prefix %s is not writable by the current user. Repair Homebrew ownership; AO will not use sudo.", prefix)}
		}
		if cask {
			installed = homebrewPackageInstalled(homebrew.Casks, pkg)
		} else {
			installed = homebrewPackageInstalled(homebrew.Formulae, pkg)
		}
	}
	verb := "install"
	if installed {
		verb = "reinstall"
	}
	command := []string{"brew", verb}
	if cask {
		command = append(command, "--cask")
	}
	command = append(command, pkg)
	return Plan{Target: target, Command: command, Method: "homebrew"}
}

func homebrewPackageInstalled(inventory map[string]bool, pkg string) bool {
	if inventory[pkg] {
		return true
	}
	if slash := strings.LastIndexByte(pkg, '/'); slash >= 0 {
		return inventory[pkg[slash+1:]]
	}
	return false
}

func (s *Service) planWinget(target Target, id string) Plan {
	if _, err := s.executables.LookPath("winget"); err != nil {
		plan := Plan{Target: target, Unsupported: true, Reason: "winget was not found on PATH."}
		if IsAgentTarget(target) {
			plan.Method = "winget"
		}
		return plan
	}
	command := []string{"winget", "install", "-e", "--id", id}
	if IsAgentTarget(target) {
		command = append(command, "--silent", "--accept-package-agreements", "--accept-source-agreements", "--disable-interactivity")
		return Plan{Target: target, Command: command, Method: "winget"}
	}
	return Plan{Target: target, Command: command}
}

func withDocs(plan Plan, docsURL string) Plan {
	plan.DocsURL = docsURL
	return plan
}

// linuxPackageManagers is probed in this fixed order; the first one found on
// PATH is used.
var linuxPackageManagers = []string{"apt-get", "dnf", "pacman", "zypper", "apk"}

// planLinuxPackage resolves a Linux install command for target via the first
// available package manager. pkgFor lets a target use a different package
// name on a given manager (e.g. gh is "github-cli" on pacman).
//
// AO deliberately never elevates privileges on the user's behalf (no auto
// sudo, no pkexec): every one of apt-get/dnf/pacman/zypper install requires
// root, so running the resolved command as the desktop user is guaranteed to
// fail with a permission error. Rather than expose a button that always
// fails, this always resolves as Unsupported on Linux. Command carries the
// exact argv and displayCommand adds sudo for the user-facing manual remedy.
func (s *Service) planLinuxPackage(target Target, pkgFor func(mgr string) string) Plan {
	for _, mgr := range linuxPackageManagers {
		if _, err := s.executables.LookPath(mgr); err != nil {
			continue
		}
		argv := linuxInstallArgv(mgr, pkgFor(mgr))
		return Plan{
			Target: target, Command: argv, Manager: mgr, NeedsRoot: true, Unsupported: true,
			Reason: "AO cannot ask for your administrator password. Run the command below in a terminal.",
		}
	}
	return Plan{
		Target: target, Unsupported: true,
		Reason: fmt.Sprintf(
			"No supported Linux package manager (%s) was found.",
			strings.Join(linuxPackageManagers, ", "),
		),
	}
}

func linuxInstallArgv(mgr, pkg string) []string {
	switch mgr {
	case "apt-get":
		return []string{"apt-get", "install", "-y", pkg}
	case "dnf":
		return []string{"dnf", "install", "-y", pkg}
	case "pacman":
		return []string{"pacman", "-S", "--noconfirm", pkg}
	case "zypper":
		return []string{"zypper", "install", "-y", pkg}
	case "apk":
		return []string{"apk", "add", pkg}
	default:
		return nil
	}
}
