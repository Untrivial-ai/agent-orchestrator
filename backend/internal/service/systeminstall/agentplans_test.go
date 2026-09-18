package systeminstall

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type installCapabilitiesStub struct {
	prefix            string
	prefixErr         error
	nodeVersion       string
	npmVersion        string
	homebrewPrefix    string
	homebrewErr       error
	homebrewInstalled bool
	writable          bool
	calls             *int
	probe             func(context.Context) error
}

func (s installCapabilitiesStub) Probe(ctx context.Context) (ports.InstallCapabilities, error) {
	if s.calls != nil {
		(*s.calls)++
	}
	if s.probe != nil {
		if err := s.probe(ctx); err != nil {
			return ports.InstallCapabilities{}, err
		}
	}
	nodeVersion := s.nodeVersion
	if nodeVersion == "" {
		nodeVersion = "v22.19.0"
	}
	npmVersion := s.npmVersion
	if npmVersion == "" {
		npmVersion = "10.0.0"
	}
	homebrewPrefix := s.homebrewPrefix
	if s.homebrewPrefix == "" && s.homebrewErr == nil {
		homebrewPrefix = "/opt/homebrew"
	}
	formulae := map[string]bool{}
	casks := map[string]bool{}
	if s.homebrewInstalled {
		formulae["opencode"] = true
	}
	return ports.InstallCapabilities{
		NPM: ports.NPMInstallCapabilities{
			NodeVersion: nodeVersion, NPMVersion: npmVersion,
			GlobalPrefix: s.prefix, PrefixWritable: s.writable, Err: s.prefixErr,
		},
		Homebrew: ports.HomebrewInstallCapabilities{
			Prefix: homebrewPrefix, PrefixWritable: s.writable,
			Formulae: formulae, Casks: casks, Err: s.homebrewErr,
		},
	}, nil
}

func TestAgentPlansSnapshotsCapabilitiesOnce(t *testing.T) {
	calls := 0
	s := newTestService("darwin", "npm", "brew")
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm", writable: true, calls: &calls}
	if _, err := s.AgentPlans(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("capability probes = %d, want one snapshot for the request", calls)
	}
}

func TestAgentPlansCancelsCapabilitySnapshotWithRequest(t *testing.T) {
	started := make(chan struct{})
	s := newTestService("darwin", "npm", "brew")
	s.installCapabilities = installCapabilitiesStub{probe: func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.AgentPlans(ctx)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("AgentPlans error = %v, want context canceled", err)
	}
}

func TestAgentPlansCoverEveryHarnessOnce(t *testing.T) {
	s := newTestService("darwin", "npm", "brew", "curl", "bash", "sh", "bun", "uv", "python3")
	plans, err := s.AgentPlans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 {
		t.Fatalf("got %d plans, want 1 (opencode)", len(plans))
	}
	if plans[0].AgentID != string(TargetOpencode) {
		t.Fatalf("plan agent = %q, want opencode", plans[0].AgentID)
	}
	seen := make(map[string]bool, len(plans))
	for _, plan := range plans {
		if seen[plan.AgentID] {
			t.Fatalf("duplicate plan for %q", plan.AgentID)
		}
		seen[plan.AgentID] = true
		if plan.DocumentationURL == "" {
			t.Fatalf("plan %q has no documentation URL", plan.AgentID)
		}
		if plan.Available && (!plan.Automatic || plan.Command == "" || plan.Method == "") {
			t.Fatalf("available plan %q is incomplete: %+v", plan.AgentID, plan)
		}
	}
}

func TestAgentPlanSelectsAvailableFallback(t *testing.T) {
	tests := []struct {
		name        string
		goos        string
		target      Target
		found       []string
		wantMethod  string
		wantCommand string
	}{
		{"opencode brew", "darwin", TargetOpencode, []string{"brew"}, "homebrew", "brew install anomalyco/tap/opencode"},
		{"opencode npm", "linux", TargetOpencode, []string{"npm"}, "npm", "npm install -g opencode-ai@latest"},
		{"opencode winget", "windows", TargetOpencode, []string{"winget", "npm"}, "winget", "winget install -e --id SST.opencode --silent --accept-package-agreements --accept-source-agreements --disable-interactivity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := newTestService(tt.goos, tt.found...).planAgent(tt.target)
			if plan.Unsupported || plan.Method != tt.wantMethod || strings.Join(plan.Command, " ") != tt.wantCommand {
				t.Fatalf("plan = %+v, want method %q command %q", plan, tt.wantMethod, tt.wantCommand)
			}
		})
	}
}

func TestOfficialInstallerPlansAreAutomaticAndServerOwned(t *testing.T) {
	tests := []struct {
		goos        string
		target      Target
		found       []string
		wantURL     string
		wantProgram string
	}{
		{"darwin", TargetOpencode, []string{"bash"}, "https://opencode.ai/install", "bash"},
		{"linux", TargetOpencode, []string{"bash"}, "https://opencode.ai/install", "bash"},
	}
	for _, tt := range tests {
		t.Run(string(tt.target)+"/"+tt.goos, func(t *testing.T) {
			plan := newTestService(tt.goos, tt.found...).planAgent(tt.target)
			if plan.Unsupported || plan.Method != "official-installer" || plan.Script == nil {
				t.Fatalf("plan = %+v", plan)
			}
			if plan.Script.URL != tt.wantURL || plan.Script.Interpreter[0] != "/usr/bin/"+tt.wantProgram {
				t.Fatalf("script = %+v", plan.Script)
			}
			if len(plan.Command) != 0 {
				t.Fatalf("remote plan exposed executable argv: %v", plan.Command)
			}
		})
	}
}

func TestAgentInstallPlansNeverUseShellEvaluationOrSudo(t *testing.T) {
	found := []string{"brew", "npm", "pnpm", "bun", "uv", "pipx", "winget", "bash", "sh", "pwsh.exe", "powershell.exe"}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		s := newTestService(goos, found...)
		planner, err := s.newRequestPlanner(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range agentTargets {
			for _, plan := range planner.agentMethodPlans(target, AgentOperationInstall) {
				argv := append([]string(nil), plan.Command...)
				if plan.Script != nil {
					argv = append(argv, plan.Script.Interpreter...)
				}
				for _, arg := range argv {
					if arg == "sudo" || arg == "-c" || arg == "-Command" || strings.Contains(arg, "|") {
						t.Fatalf("%s/%s/%s contains shell-evaluated argument %q: %+v", goos, target, plan.Method, arg, plan)
					}
				}
			}
		}
	}
}

func TestOfficialInstallerIsPreferredOverPackageManagers(t *testing.T) {
	s := newTestService("darwin", "brew", "npm", "sh", "bash")
	s.installCapabilities = installCapabilitiesStub{
		prefix: "/Users/test/.npm", homebrewPrefix: "/opt/homebrew", writable: true,
	}
	plans, err := s.AgentPlans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, plan := range plans {
		if plan.AgentID != string(TargetOpencode) {
			continue
		}
		if plan.Method != "official-installer" {
			t.Fatalf("recommended method = %q, want official-installer", plan.Method)
		}
		if len(plan.Methods) != 3 {
			t.Fatalf("methods = %+v", plan.Methods)
		}
		want := []string{"homebrew", "npm", "official-installer"}
		for i, method := range plan.Methods {
			if method.ID != want[i] || method.Recommended != (i == 2) {
				t.Fatalf("method[%d] = %+v", i, method)
			}
		}
		return
	}
	t.Fatal("opencode plan not found")
}

func TestReinstallUsesPackageManagerReinstallCommands(t *testing.T) {
	s := newTestService("darwin", "npm", "brew", "bash")
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm", writable: true}
	planner, err := s.newRequestPlanner(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	plans := planner.agentMethodPlans(TargetOpencode, AgentOperationReinstall)
	want := map[string]string{
		"npm":   "npm install -g opencode-ai@latest --force",
		"homebrew": "brew install anomalyco/tap/opencode",
	}
	for _, plan := range plans {
		if plan.Method == "official-installer" {
			if !plan.Unsupported || !strings.Contains(plan.Reason, "verified headless reinstall") {
				t.Fatalf("official-installer reinstall plan = %+v, want instructions-only", plan)
			}
			continue
		}
		if got := strings.Join(plan.Command, " "); got != want[plan.Method] {
			t.Errorf("%s reinstall command = %q, want %q", plan.Method, got, want[plan.Method])
		}
	}
}

func TestHomebrewReinstallRepairsThroughInstallWhenPackageIsNotOwned(t *testing.T) {
	for _, tt := range []struct {
		name      string
		installed bool
		want      string
	}{
		{name: "package absent", want: "brew install anomalyco/tap/opencode"},
		{name: "package present", installed: true, want: "brew reinstall anomalyco/tap/opencode"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestService("darwin", "brew")
			s.installCapabilities = installCapabilitiesStub{homebrewInstalled: tt.installed, writable: true}
			planner, err := s.newRequestPlanner(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			plan, err := planner.resolveAgentMethod(TargetOpencode, "homebrew", AgentOperationReinstall)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(plan.Command, " "); got != tt.want {
				t.Fatalf("Homebrew repair command = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNPMPlanUsesTargetNodeFloor(t *testing.T) {
	for _, tt := range []struct {
		name        string
		target      Target
		nodeVersion string
		wantAllowed bool
		wantReason  string
	}{
		{name: "opencode accepts node 16", target: TargetOpencode, nodeVersion: "v16.0.0", wantAllowed: true},
		{name: "opencode rejects node 15", target: TargetOpencode, nodeVersion: "v15.19.0", wantReason: "Node.js 16+"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestService("darwin", "npm")
			s.installCapabilities = installCapabilitiesStub{
				prefix: "/Users/test/.npm", writable: true,
				nodeVersion: tt.nodeVersion, npmVersion: "9.0.0",
			}
			plan := s.planNPM(tt.target, "opencode-ai@latest")
			if tt.wantAllowed && plan.Unsupported {
				t.Fatalf("plan = %+v, want available", plan)
			}
			if !tt.wantAllowed && (!plan.Unsupported || !strings.Contains(plan.Reason, tt.wantReason)) {
				t.Fatalf("plan = %+v, want unavailable reason containing %q", plan, tt.wantReason)
			}
		})
	}
}

func TestNPMPlanRequiresWritableGlobalPrefix(t *testing.T) {
	tests := []struct {
		name       string
		caps       installCapabilitiesStub
		wantReason string
	}{
		{name: "prefix lookup fails", caps: installCapabilitiesStub{prefixErr: errors.New("npm failed")}, wantReason: "could not be inspected"},
		{name: "prefix not writable", caps: installCapabilitiesStub{prefix: "/usr/local", writable: false}, wantReason: "not writable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestService("darwin", "npm")
			s.installCapabilities = tt.caps
			plan := s.planNPM(TargetOpencode, "opencode-ai@latest")
			if !plan.Unsupported || !strings.Contains(plan.Reason, tt.wantReason) {
				t.Fatalf("plan = %+v, want unavailable reason containing %q", plan, tt.wantReason)
			}
		})
	}

	s := newTestService("darwin", "npm")
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm", writable: true}
	plan := s.planNPM(TargetOpencode, "opencode-ai@latest")
	if plan.Unsupported || plan.ExpectedDestination != "/Users/test/.npm/bin" {
		t.Fatalf("plan = %+v, want writable npm destination", plan)
	}
}

func TestNPMPlanRequiresParseableNodeAndNPMVersions(t *testing.T) {
	tests := []struct {
		name        string
		nodeVersion string
		npmVersion  string
		wantReason  string
	}{
		{name: "unparseable node", nodeVersion: "unknown", npmVersion: "10.8.0", wantReason: "could not be validated"},
		{name: "unparseable npm", nodeVersion: "v22.19.0", npmVersion: "unknown", wantReason: "could not be validated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestService("darwin", "npm")
			s.installCapabilities = installCapabilitiesStub{
				prefix: "/Users/test/.npm", writable: true,
				nodeVersion: tt.nodeVersion, npmVersion: tt.npmVersion,
			}
			plan := s.planNPM(TargetOpencode, "opencode-ai@latest")
			if !plan.Unsupported || !strings.Contains(plan.Reason, tt.wantReason) {
				t.Fatalf("plan = %+v, want unavailable reason containing %q", plan, tt.wantReason)
			}
		})
	}
}

func TestHomebrewPlanRequiresWritablePrefix(t *testing.T) {
	s := newTestService("darwin", "brew")
	s.installCapabilities = installCapabilitiesStub{homebrewPrefix: "/opt/homebrew", writable: false}
	plan := s.planBrew(TargetOpencode, "anomalyco/tap/opencode")
	if !plan.Unsupported || !strings.Contains(plan.Reason, "not writable") {
		t.Fatalf("plan = %+v, want unavailable Homebrew writability reason", plan)
	}
}

func TestHomebrewPlanReinstallsAnExistingPackage(t *testing.T) {
	s := newTestService("darwin", "brew")
	s.installCapabilities = installCapabilitiesStub{homebrewPrefix: "/opt/homebrew", homebrewInstalled: true, writable: true}
	plan := s.planBrew(TargetOpencode, "anomalyco/tap/opencode")
	if got := strings.Join(plan.Command, " "); got != "brew reinstall anomalyco/tap/opencode" {
		t.Fatalf("command = %q, want an actual formula reinstall", got)
	}
}

func TestHomebrewPlanFailsClosedWhenInstalledPackageProbeFails(t *testing.T) {
	s := newTestService("darwin", "brew")
	s.installCapabilities = installCapabilitiesStub{
		homebrewPrefix: "/opt/homebrew", homebrewErr: errors.New("brew list timed out"), writable: true,
	}
	plan := s.planBrew(TargetOpencode, "anomalyco/tap/opencode")
	if !plan.Unsupported || !strings.Contains(plan.Reason, "could not be inspected") {
		t.Fatalf("plan = %+v, want failed-closed Homebrew inspection error", plan)
	}
}

func TestAgentPlansExposeEveryViableServerOwnedMethod(t *testing.T) {
	s := newTestService("darwin", "brew", "npm")
	s.installCapabilities = installCapabilitiesStub{prefix: "/Users/test/.npm", writable: true}
	plans, err := s.AgentPlans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var opencode AgentPlan
	for _, plan := range plans {
		if plan.AgentID == string(TargetOpencode) {
			opencode = plan
			break
		}
	}
	if len(opencode.Methods) != 3 {
		t.Fatalf("opencode methods = %+v, want homebrew, npm, and official installer", opencode.Methods)
	}
	if opencode.Methods[0].ID != "homebrew" || !opencode.Methods[0].Recommended || !opencode.Methods[0].Available {
		t.Fatalf("first method = %+v, want recommended viable homebrew", opencode.Methods[0])
	}
	if opencode.Methods[1].ID != "npm" || opencode.Methods[1].Recommended || !opencode.Methods[1].Available {
		t.Fatalf("second method = %+v, want alternate viable npm", opencode.Methods[1])
	}
	if opencode.Methods[2].ID != "official-installer" || opencode.Methods[2].Recommended || opencode.Methods[2].Available {
		t.Fatalf("third method = %+v, want unavailable official installer without bash", opencode.Methods[2])
	}
	if strings.Contains(opencode.Methods[0].Command, "curl") || strings.Contains(opencode.Methods[1].Command, "curl") {
		t.Fatalf("opencode methods include remote script execution: %+v", opencode.Methods)
	}
}

func TestResolveAgentMethodRejectsUnknownOrUnavailableMethod(t *testing.T) {
	s := newTestService("darwin", "brew")
	if _, err := s.resolveAgentMethod(TargetOpencode, "npm"); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("resolve npm error = %v, want unavailable", err)
	}
	if _, err := s.resolveAgentMethod(TargetOpencode, "made-up"); err == nil || !strings.Contains(err.Error(), "unknown install method") {
		t.Fatalf("resolve made-up error = %v, want unknown method", err)
	}
	plan, err := s.resolveAgentMethod(TargetOpencode, "homebrew")
	if err != nil || plan.Method != "homebrew" {
		t.Fatalf("resolve homebrew = %+v, %v", plan, err)
	}
}

func TestAgentTargetsAreValidButPrerequisitesAreNotHarnessRows(t *testing.T) {
	for _, target := range agentTargets {
		if !Valid(target) || !IsAgentTarget(target) {
			t.Fatalf("agent target %q is not accepted by both allowlists", target)
		}
	}
	for _, target := range []Target{TargetTmux, TargetGH, TargetCloudflared} {
		if !Valid(target) || IsAgentTarget(target) {
			t.Fatalf("prerequisite target %q was classified incorrectly", target)
		}
	}
}