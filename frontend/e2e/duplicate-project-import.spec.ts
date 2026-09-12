import { expect, test } from "@playwright/test";
import type { AoBridge } from "../src/preload";
import { agentReadiness } from "../src/renderer/test/agent-readiness-fixtures";
import { installFakeAgent } from "./support/fake-bridge";

// Renderer integration: real project-add API client and import dialogs, with
// deterministic daemon responses and a fake native picker/workspace snapshot.
test("renderer: importing an alias opens the registered project without starting another orchestrator @T0", async ({ page }) => {
	const projectId = "already-registered";
	const selectedPath = "/alias/registered-project";
	await installFakeAgent(page, { projectId, projectName: projectId, workers: [] });
	let creates = 0;
	let orchestratorStarts = 0;
	await page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
		const pathname = new URL(route.request().url()).pathname;
		if (pathname === "/api/v1/agents/readiness" || pathname === "/api/v1/agents/readiness/ensure") {
			await route.fulfill({ json: { agents: [agentReadiness("codex", "Codex")] } });
			return;
		}
		if (pathname === "/api/v1/imports/validate") {
			await route.fulfill({ json: {
				importKind: "project", isValid: true, blockingErrors: [], nextStep: "continue", childRepos: [],
				root: { repoPath: selectedPath, isRepo: true, hasCommit: true, hasOrigin: true, isEmptyFolder: false, needsGitInit: false, requiredActions: [], blockingErrors: [] },
			} });
			return;
		}
		if (pathname === "/api/v1/projects" && route.request().method() === "POST") {
			creates++;
			expect(route.request().postDataJSON().path).toBe(selectedPath);
			await route.fulfill({ status: 409, json: {
				error: "conflict", code: "PATH_ALREADY_REGISTERED", message: "A project at this path is already registered",
				details: { existingProjectId: projectId }, requestId: "duplicate-import-test",
			} });
			return;
		}
		if (pathname.startsWith("/api/v1/orchestrators") && route.request().method() === "POST") orchestratorStarts++;
		await route.fulfill({ json: { status: "ok", project: { id: projectId, config: {} } } });
	});
	await page.goto("/#/");
	await page.evaluate((path) => {
		const bridge = (window as unknown as { ao: AoBridge }).ao;
		bridge.app.chooseDirectory = async () => path;
		bridge.app.getRepositoryBranch = async () => "main";
	}, selectedPath);
	await page.getByRole("button", { name: "New project", exact: true }).first().click();
	await page.getByRole("button", { name: "Import an existing project", exact: true }).click();
	await page.getByRole("button", { name: "Create and start", exact: true }).click();
	await expect(page).toHaveURL(new RegExp(`projects/${projectId}`));
	await expect(page.getByText("Opened the registered project for this folder.")).toBeVisible();
	await expect(page.getByRole("dialog")).toHaveCount(0);
	expect(creates).toBe(1);
	expect(orchestratorStarts).toBe(0);
});
