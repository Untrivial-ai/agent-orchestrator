import { describe, expect, it } from "vitest";
import { createProjectConfig } from "../routes/_shell";

describe("createProjectConfig", () => {
	it("persists independently selected profile paths under role configs", () => {
		expect(createProjectConfig({ workerAgent: "claude-code", workerClaudeConfigDir: "/home/test/.claude-team", orchestratorAgent: "claude-code", orchestratorClaudeConfigDir: "" })).toEqual({
			worker: { agent: "claude-code", agentConfig: { claudeConfigDir: "/home/test/.claude-team" } },
			orchestrator: { agent: "claude-code", agentConfig: { claudeConfigDir: "" } },
		});
	});

	it("persists selected worker and orchestrator agents without tracker intake by default", () => {
		expect(
			createProjectConfig({
				workerAgent: "codex",
				orchestratorAgent: "claude-code",
			}),
		).toEqual({
			worker: { agent: "codex" },
			orchestrator: { agent: "claude-code" },
		});
	});

	it("preserves tracker intake alongside selected agent defaults", () => {
		expect(
			createProjectConfig({
				workerAgent: "cursor",
				orchestratorAgent: "opencode",
				trackerIntake: { enabled: true, provider: "github", assignee: "octocat" },
			}),
		).toEqual({
			worker: { agent: "cursor" },
			orchestrator: { agent: "opencode" },
			trackerIntake: { enabled: true, provider: "github", assignee: "octocat" },
		});
	});
});
