package systeminstall

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var agentDocumentationURLs = map[Target]string{
	TargetOpencode: "https://github.com/anomalyco/opencode",
}

func (s requestPlanner) agentMethodPlans(target Target, operation AgentOperation) []Plan {
	var plans []Plan
	switch target {
	case TargetOpencode:
		switch s.goos {
		case "windows":
			plans = []Plan{s.planWinget(target, "SST.opencode")}
		case "darwin":
			plans = []Plan{s.planBrew(target, "anomalyco/tap/opencode"), s.planNPM(target, "opencode-ai@latest"), s.planShellInstaller(target, "https://opencode.ai/install", "bash")}
		case "linux":
			plans = []Plan{s.planNPM(target, "opencode-ai@latest"), s.planShellInstaller(target, "https://opencode.ai/install", "bash")}
		default:
			plans = []Plan{s.planNPM(target, "opencode-ai@latest")}
		}
	default:
		plans = []Plan{{Target: target, Unsupported: true, Method: "manual", Reason: "unknown install target"}}
	}
	for index := range plans {
		plans[index].DocsURL = agentDocumentationURLs[target]
		plans[index] = s.planForOperation(plans[index], operation)
	}
	return plans
}

func (s *Service) resolveAgentMethod(target Target, method string) (Plan, error) {
	planner, err := s.newRequestPlanner(context.Background())
	if err != nil {
		return Plan{}, err
	}
	return planner.resolveAgentMethod(target, method, AgentOperationInstall)
}

func (s requestPlanner) resolveAgentMethod(target Target, method string, operation AgentOperation) (Plan, error) {
	for _, plan := range s.agentMethodPlans(target, operation) {
		if plan.Method != method {
			continue
		}
		if plan.Unsupported {
			return Plan{}, fmt.Errorf("%w: install method %q is not available: %s", ErrInstallMethod, method, plan.Reason)
		}
		return plan, nil
	}
	return Plan{}, fmt.Errorf("%w: unknown install method %q for %s", ErrInstallMethod, method, target)
}

func (s requestPlanner) planForOperation(plan Plan, operation AgentOperation) Plan {
	if operation == AgentOperationInstall || plan.Unsupported {
		return plan
	}
	switch plan.Method {
	case "homebrew":
		// planHomebrew already chooses install when another manager owns the
		// harness and reinstall when the formula/cask itself is present.
	case "npm":
		plan.Command = append(plan.Command, "--force")
	case "winget":
		plan.Command = append(plan.Command, "--force")
	case "uv":
		pkg := plan.Command[len(plan.Command)-1]
		plan.Command = []string{"uv", "tool", "install", pkg, "--force", "--reinstall"}
	case "pipx":
		pkg := plan.Command[len(plan.Command)-1]
		plan.Command = []string{"pipx", "install", "--force", pkg}
	case "bun":
		plan.Command = append(plan.Command, "--force")
	case "official-installer":
		plan.Unsupported = true
		plan.Command = nil
		plan.Script = nil
		plan.Reason = "This vendor installer does not provide a verified headless reinstall operation."
	default:
		plan.Unsupported = true
		plan.Reason = "This installation method does not provide an explicit reinstall operation."
	}
	return plan
}

// planAgent preserves the legacy single-plan call sites while selecting from
// the same method registry used by the catalog and execution route.
func (s *Service) planAgent(target Target) Plan {
	planner, err := s.newRequestPlanner(context.Background())
	if err != nil {
		return Plan{Target: target, Unsupported: true, Method: "manual", Reason: "install capabilities could not be inspected", DocsURL: agentDocumentationURLs[target]}
	}
	plans := planner.agentMethodPlans(target, AgentOperationInstall)
	return plans[recommendedPlanIndex(plans)]
}

func (s *Service) planShellInstaller(target Target, url, shell string) Plan {
	resolved, err := s.executables.LookPath(shell)
	if err != nil {
		return Plan{Target: target, Unsupported: true, Method: "official-installer", Reason: fmt.Sprintf("%s was not found on PATH.", shell)}
	}
	return Plan{
		Target: target, Method: "official-installer",
		Script: &ports.InstallScriptCommand{URL: url, Interpreter: []string{resolved}},
	}
}
