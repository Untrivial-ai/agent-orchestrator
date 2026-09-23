import { expect, test } from "@playwright/test";
import type { AoBridge } from "../src/preload";
import { agentReadiness } from "../src/renderer/test/agent-readiness-fixtures";
import { installFakeAgent } from "./support/fake-bridge";

for (const platform of ["macOS", "Windows", "Linux"] as const) {
test(`renderer: workspace import preserves the root branch and explains unresolved startup on ${platform} @T0`, async ({ page }) => {
	test.setTimeout(120_000);
	await installFakeAgent(page, { projectId: "local-root", workers: [] });
	await page.addInitScript((platform) => {
		// AO checks all three signals; changing only navigator.platform on a
		// Mac still takes the macOS path through navigator.userAgent.
		Object.defineProperty(navigator, "platform", { configurable: true, value: platform });
		Object.defineProperty(navigator, "userAgent", { configurable: true, value: `Mozilla/5.0 (${platform === "macOS" ? "Macintosh" : platform})` });
		Object.defineProperty(navigator, "userAgentData", { configurable: true, value: { platform } });
	}, platform);
	let created = false;
	let started = false;
	await page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
		const pathname = new URL(route.request().url()).pathname;
		if (pathname.startsWith("/api/v1/agents/readiness")) {
			await route.fulfill({ json: { agents: [agentReadiness("codex", "Codex")] } });
			return;
		}
		if (pathname === "/api/v1/imports/validate" && route.request().method() === "POST") {
			await route.fulfill({ json: {
				importKind: "workspace", isValid: true, blockingErrors: [], nextStep: "continue",
				root: { repoPath: "/repos/local-root", isRepo: false, hasCommit: false, hasOrigin: false, isEmptyFolder: false, needsGitInit: false, requiredActions: [], blockingErrors: [] },
				childRepos: [{ repoPath: "/repos/local-root/api", isRepo: true, hasCommit: true, hasOrigin: true, isEmptyFolder: false, needsGitInit: false, requiredActions: [], blockingErrors: [] }],
			} });
			return;
		}
		if (pathname === "/api/v1/projects" && route.request().method() === "POST") {
			expect(route.request().postDataJSON()).toMatchObject({
				path: "/repos/local-root", asWorkspace: true, config: { defaultBranch: "trunk" },
			});
			created = true;
			await route.fulfill({ json: { project: {
				id: "local-root", name: "Local workspace", kind: "workspace", path: "/repos/local-root", config: {},
			} } });
			return;
		}
		if (pathname === "/api/v1/sessions" && route.request().method() === "POST") {
			started = true;
			// A separate runtime failure must retain its real cause in the board.
			await route.fulfill({ status: 422, json: {
				error: "invalid", code: "DEFAULT_BRANCH_UNRESOLVED",
				message: 'resolve workspace repo "__root__" base: remote did not advertise a symbolic HEAD (DEFAULT_BRANCH_UNRESOLVED)',
			} });
			return;
		}
		await route.fulfill({ json: {} });
	});
	await page.goto("/#/");
	await page.evaluate(() => {
		// The default fixture has a running orchestrator. This test represents a
		// failed first spawn, so every later CDC/query refresh must also have none.
		// Otherwise SessionsBoard correctly clears the startup error on refresh.
		window.__aoFakeAgent!.removeWorker("local-root-orchestrator");
		const bridge = (window as unknown as { ao: AoBridge }).ao;
		bridge.app.chooseDirectory = async () => "/repos/local-root";
		bridge.app.checkAncestorRepo = async () => undefined;
		bridge.app.getRepositoryBranch = async () => "trunk";
		bridge.app.scanImportFolder = async () => ({ path: "/repos/local-root", repos: [
			{
				name: "api", path: "/repos/local-root/api", relativePath: "api", branch: "dev",
				hasRemote: true, isRepo: true, hasCommit: true,
				remote: "https://github.com/example/api.git", status: "ok",
			},
			{
				name: "docs", path: "/repos/local-root/docs", relativePath: "docs", branch: "",
				hasRemote: false, isRepo: false, hasCommit: false, remote: "", status: "ok", needsGitInit: true,
			},
		] });
	});
	await page.getByRole("button", { name: "New project", exact: true }).first().click();
	await page.getByRole("button", { name: "Import a workspace folder", exact: true }).click();
	await expect(page.getByText("api", { exact: true })).toBeVisible();
	await expect(page.getByText("docs", { exact: true })).not.toBeVisible();
	await expect(page.getByRole("button", { name: /Set up|Hide setup/ })).not.toBeVisible();
	await page.getByRole("button", { name: "Continue", exact: true }).click();
	await page.getByRole("button", { name: "Create workspace and start", exact: true }).click();
	await expect(page).toHaveURL(/projects\/local-root/);
	await expect(page.getByText(/AO could not determine the default branch for the workspace root repository/)).toBeVisible();
	await expect(page.getByText(/Details:.*remote did not advertise a symbolic HEAD/)).toBeVisible();
	expect(created).toBe(true);
	expect(started).toBe(true);
	await page.screenshot({ path: `test-results/workspace-root-startup-${platform}.png` });
});
}
