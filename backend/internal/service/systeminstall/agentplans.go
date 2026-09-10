package systeminstall

import (
	"context"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var agentDocumentationURLs = map[Target]string{
	TargetClaudeCode: "https://code.claude.com/docs/en/installation",
	TargetCodex:      "https://github.com/openai/codex",
	TargetCursor:     "https://docs.cursor.com/en/cli/installation",
	TargetOpencode:   "https://github.com/anomalyco/opencode",
	TargetAider:      "https://aider.chat/docs/install.html",
	TargetCopilot:    "https://docs.github.com/en/copilot/how-tos/copilot-cli/set-up-copilot-cli/install-copilot-cli",
	TargetGrok:       "https://docs.x.ai/build/overview",
	TargetKimi:       "https://moonshotai.github.io/kimi-code/en/",
	TargetPi:         "https://github.com/earendil-works/pi",
	TargetAmp:        "https://ampcode.com/manual",
	TargetAuggie:     "https://docs.augmentcode.com/cli/overview",
	TargetDroid:      "https://docs.factory.ai/droid-cli/cli-reference",
	TargetCrush:      "https://github.com/charmbracelet/crush",
	TargetCline:      "https://github.com/cline/cline",
	TargetGoose:      "https://block.github.io/goose/index.html",
	TargetQwen:       "https://qwenlm.github.io/qwen-code-docs/en/users/quickstart/",
	TargetContinue:   "https://docs.continue.dev/cli/quickstart",
	TargetDevin:      "https://docs.devin.ai/get-started/devin-intro",
	TargetKiro:       "https://kiro.dev/docs/getting-started/installation/",
	TargetKilocode:   "https://kilo.ai/docs/code-with-ai/platforms/cli",
	TargetVibe:       "https://github.com/mistralai/mistral-vibe",
	TargetMuse:       "https://ai.meta.com/llama/",
	TargetAgy:        "https://github.com/google-antigravity/antigravity-cli",
	TargetAutohand:   "https://docs.autohand.ai/working-with-autohand-code/cli",
	TargetKimchi:     "https://docs.kimchi.dev/docs/coding-getting-started",
	TargetPrimeAgent: "https://github.com/PrimeIntellect-ai/prime-agent/blob/main/packages/coding-agent/docs/quickstart.md",
	TargetOMP:        "https://github.com/can1357/oh-my-pi",
}

func (s requestPlanner) agentMethodPlans(target Target, operation AgentOperation) []Plan {
	var plans []Plan
	switch target {
	case TargetClaudeCode:
		official := s.officialByOS(target, "https://claude.ai/install.sh", "bash", "https://claude.ai/install.ps1", agentDocumentationURLs[target])
		if s.goos == "darwin" {
			plans = []Plan{s.planBrewCask(target, "claude-code"), s.planNPM(target, "@anthropic-ai/claude-code"), official}
		} else {
			plans = []Plan{s.planNPM(target, "@anthropic-ai/claude-code"), official}
		}
	case TargetCodex:
		official := s.officialByOS(target, "https://chatgpt.com/codex/install.sh", "sh", "https://chatgpt.com/codex/install.ps1", agentDocumentationURLs[target])
		if s.goos == "darwin" {
			plans = []Plan{s.planBrewCask(target, "codex"), s.planNPM(target, "@openai/codex"), official}
		} else {
			plans = []Plan{s.planNPM(target, "@openai/codex"), official}
		}
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
	case TargetCopilot:
		switch s.goos {
		case "windows":
			plans = []Plan{s.planWinget(target, "GitHub.Copilot"), s.planNPM(target, "@github/copilot")}
		case "darwin":
			plans = []Plan{s.planBrewCask(target, "copilot-cli"), s.planNPM(target, "@github/copilot")}
		default:
			plans = []Plan{s.planNPM(target, "@github/copilot")}
		}
	case TargetCursor:
		plans = []Plan{s.officialByOS(target, "https://cursor.com/install", "bash", "https://cursor.com/install?win32=true", agentDocumentationURLs[target])}
	case TargetAider:
		plans = []Plan{s.officialByOS(target, "https://aider.chat/install.sh", "sh", "https://aider.chat/install.ps1", agentDocumentationURLs[target])}
	case TargetGrok:
		plans = []Plan{s.officialByOS(target, "https://x.ai/cli/install.sh", "bash", "https://x.ai/cli/install.ps1", agentDocumentationURLs[target])}
	case TargetKimi:
		plans = []Plan{s.officialByOS(target, "https://code.kimi.com/kimi-code/install.sh", "bash", "https://code.kimi.com/kimi-code/install.ps1", agentDocumentationURLs[target])}
	case TargetPi:
		plans = []Plan{s.planNPM(target, "@earendil-works/pi-coding-agent")}
		if s.goos == "darwin" || s.goos == "linux" {
			plans = append(plans, s.planPiOfficialInstaller())
		}
	case TargetAmp:
		official := s.officialByOS(target, "https://ampcode.com/install.sh", "bash", "https://ampcode.com/install.ps1", agentDocumentationURLs[target])
		if s.goos == "darwin" {
			plans = []Plan{s.planBrew(target, "ampcode/tap/ampcode"), s.planNPM(target, "@ampcode/cli"), official}
		} else {
			plans = []Plan{s.planNPM(target, "@ampcode/cli"), official}
		}
	case TargetDroid:
		official := s.officialByOS(target, "https://app.factory.ai/cli", "sh", "https://app.factory.ai/cli/windows", agentDocumentationURLs[target])
		if s.goos == "darwin" {
			plans = []Plan{s.planBrewCask(target, "droid"), s.planNPM(target, "droid"), official}
		} else {
			plans = []Plan{s.planNPM(target, "droid"), official}
		}
	case TargetAuggie:
		plans = []Plan{s.planNPM(target, "@augmentcode/auggie")}
	case TargetCrush:
		if s.goos == "darwin" {
			plans = []Plan{s.planBrew(target, "charmbracelet/tap/crush"), s.planNPM(target, "@charmland/crush")}
		} else {
			plans = []Plan{s.planNPM(target, "@charmland/crush")}
		}
	case TargetCline:
		plans = []Plan{s.planNPM(target, "cline@latest")}
	case TargetGoose:
		switch s.goos {
		case "windows":
			plans = []Plan{manualPlan(target, "Goose does not publish a native Windows CLI installer; use WSL or the desktop download.", agentDocumentationURLs[target])}
		case "darwin", "linux":
			plans = []Plan{s.planShellInstaller(target, "https://github.com/aaif-goose/goose/releases/download/stable/download_cli.sh", "bash")}
		default:
			plans = []Plan{manualPlan(target, "Goose publishes this installer for macOS and Linux only.", agentDocumentationURLs[target])}
		}
	case TargetQwen:
		official := s.officialByOS(target, "https://qwen-code-assets.oss-cn-hangzhou.aliyuncs.com/installation/install-qwen-standalone.sh", "bash", "https://qwen-code-assets.oss-cn-hangzhou.aliyuncs.com/installation/install-qwen-standalone.ps1", agentDocumentationURLs[target])
		if s.goos == "darwin" {
			plans = []Plan{s.planBrew(target, "qwen-code"), s.planNPM(target, "@qwen-code/qwen-code@latest"), official}
		} else {
			plans = []Plan{s.planNPM(target, "@qwen-code/qwen-code@latest"), official}
		}
	case TargetContinue:
		plans = []Plan{s.planNPM(target, "@continuedev/cli")}
	case TargetDevin:
		switch s.goos {
		case "windows":
			plans = []Plan{manualPlan(target, "Devin for Terminal documents installation through WSL on Windows.", agentDocumentationURLs[target])}
		case "darwin", "linux":
			plans = []Plan{s.planShellInstaller(target, "https://cli.devin.ai/install.sh", "bash")}
		default:
			plans = []Plan{manualPlan(target, "Devin for Terminal publishes this installer for macOS and Linux only.", agentDocumentationURLs[target])}
		}
	case TargetAutohand:
		official := s.officialByOS(target, "https://autohand.ai/install.sh", "sh", "https://autohand.ai/install.ps1", agentDocumentationURLs[target])
		if s.goos == "darwin" {
			plans = []Plan{s.planBrew(target, "autohandai/code/autohand-code"), s.planNPM(target, "autohand-cli"), official}
		} else {
			plans = []Plan{s.planNPM(target, "autohand-cli"), official}
		}
	case TargetKimchi:
		official := s.officialByOS(target,
			"https://github.com/getkimchi/kimchi/releases/latest/download/install.sh", "sh",
			"https://github.com/getkimchi/kimchi/releases/latest/download/install.ps1",
			agentDocumentationURLs[target])
		if s.goos == "darwin" {
			plans = []Plan{s.planBrew(target, "getkimchi/tap/kimchi"), official}
		} else {
			plans = []Plan{official}
		}
	case TargetVibe:
		plans = []Plan{s.planUV(target, "mistral-vibe"), s.planPipx(target, "mistral-vibe")}
	case TargetKiro:
		plans = []Plan{s.planKiroInstall()}
	case TargetKilocode:
		plans = []Plan{s.planNPM(target, "@kilocode/cli")}
	case TargetMuse:
		switch s.goos {
		case "windows":
			plans = []Plan{manualPlan(target, "Muse Code does not currently publish a native Windows installer.", agentDocumentationURLs[target])}
		case "darwin", "linux":
			plans = []Plan{s.planShellInstaller(target, "https://dev.meta.ai/install.sh", "bash")}
		default:
			plans = []Plan{manualPlan(target, "Muse Code publishes this installer for macOS and Linux only.", agentDocumentationURLs[target])}
		}
	case TargetAgy:
		plans = []Plan{s.officialByOS(target, "https://antigravity.google/cli/install.sh", "bash", "https://antigravity.google/cli/install.ps1", agentDocumentationURLs[target])}
	case TargetPrimeAgent:
		switch s.goos {
		case "windows":
			plans = []Plan{manualPlan(target, "Prime Agent currently documents macOS and Linux; use WSL on Windows.", agentDocumentationURLs[target])}
		case "darwin", "linux":
			plans = []Plan{s.planShellInstaller(target, "https://app.primeintellect.ai/prime-agent/install.sh", "sh")}
		default:
			plans = []Plan{manualPlan(target, "Prime Agent publishes this installer for macOS and Linux only.", agentDocumentationURLs[target])}
		}
	case TargetOMP:
		official := s.officialByOS(target, "https://omp.sh/install", "sh", "https://omp.sh/install.ps1", agentDocumentationURLs[target])
		if s.goos == "darwin" {
			plans = []Plan{s.planBrew(target, "can1357/tap/omp"), s.planBun(target), official}
		} else {
			plans = []Plan{s.planBun(target), official}
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

func (s requestPlanner) planPiOfficialInstaller() Plan {
	plan := s.planShellInstaller(TargetPi, "https://pi.dev/install.sh", "sh")
	if plan.Unsupported {
		return plan
	}
	if s.capabilities == nil {
		plan.Unsupported = true
		plan.Reason = "Node.js 22.19+ and npm could not be inspected; Pi's installer cannot repair them without a terminal."
		return plan
	}
	nodeVersion, nodeOK := parseToolVersion(s.capabilities.NPM.NodeVersion)
	if !nodeOK || !versionAtLeast(nodeVersion, minimumNodeVersionForTarget(TargetPi)) {
		plan.Unsupported = true
		plan.Reason = "Node.js 22.19+ is required before Pi's installer can run headlessly."
		return plan
	}
	if _, npmOK := parseToolVersion(s.capabilities.NPM.NPMVersion); !npmOK {
		plan.Unsupported = true
		plan.Reason = "npm is required before Pi's installer can run headlessly."
	}
	return plan
}

func (s requestPlanner) planKiroInstall() Plan {
	if s.goos == "darwin" {
		return manualPlan(
			TargetKiro,
			"Kiro's official macOS installer writes an app bundle to /Applications and must be run interactively. Follow Kiro's installation guide, then refresh harness status.",
			agentDocumentationURLs[TargetKiro],
		)
	}
	return s.officialByOS(TargetKiro,
		"https://cli.kiro.dev/install", "bash",
		"https://cli.kiro.dev/install.ps1", agentDocumentationURLs[TargetKiro])
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

func (s *Service) officialByOS(target Target, unixURL, unixShell, windowsURL, docsURL string) Plan {
	switch s.goos {
	case "windows":
		return withDocs(s.planPowerShellInstaller(target, windowsURL), docsURL)
	case "darwin", "linux":
		return withDocs(s.planShellInstaller(target, unixURL, unixShell), docsURL)
	default:
		return manualPlan(target, fmt.Sprintf("The official installer does not support %s.", s.goos), docsURL)
	}
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

func (s *Service) planPowerShellInstaller(target Target, url string) Plan {
	for _, shell := range []string{"pwsh.exe", "powershell.exe", "pwsh", "powershell"} {
		resolved, err := s.executables.LookPath(shell)
		if err != nil {
			continue
		}
		return Plan{
			Target: target, Method: "official-installer",
			Script: &ports.InstallScriptCommand{
				URL:         url,
				Interpreter: []string{resolved, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File"},
			},
		}
	}
	return Plan{Target: target, Unsupported: true, Method: "official-installer", Reason: "PowerShell was not found on PATH."}
}

func (s *Service) planUV(target Target, pkg string) Plan {
	if _, err := s.executables.LookPath("uv"); err != nil {
		return Plan{Target: target, Unsupported: true, Method: "uv", Reason: "uv was not found on PATH. Install uv, then retry."}
	}
	return Plan{Target: target, Method: "uv", Command: []string{"uv", "tool", "install", pkg}}
}

func (s *Service) planBun(target Target) Plan {
	const pkg = "@oh-my-pi/pi-coding-agent"
	if _, err := s.executables.LookPath("bun"); err != nil {
		return Plan{Target: target, Unsupported: true, Method: "bun", Reason: "Bun was not found on PATH."}
	}
	return Plan{Target: target, Method: "bun", Command: []string{"bun", "install", "-g", pkg}}
}

func (s *Service) planPipx(target Target, pkg string) Plan {
	if _, err := s.executables.LookPath("pipx"); err != nil {
		return Plan{Target: target, Unsupported: true, Method: "pipx", Reason: "pipx was not found on PATH. Install pipx, then retry."}
	}
	return Plan{Target: target, Method: "pipx", Command: []string{"pipx", "install", pkg}}
}

func manualPlan(target Target, reason, docsURL string) Plan {
	return Plan{Target: target, Unsupported: true, Method: "manual", Reason: reason, DocsURL: docsURL}
}
