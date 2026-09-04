import { expect, test } from "@playwright/test";
import { resolve } from "node:path";
import { installFakeAgent } from "./support/fake-bridge";

const labels = ["Auto", "Manual", "Accept Edits", "Don't Ask", "Bypass Permissions"];

for (const harness of ["codex", "claude-code"] as const) {
	test(`${harness} uses the AO permission menu and preserves the Manual wire value @T0`, async ({ page }) => {
		await page.setViewportSize({ width: 1440, height: 960 });
		const projectId = "permission-fixture";
		const sessionId = `${harness}-permissions`;
		const claude = harness === "claude-code";
		let approvalMode = "accept-edits";
		let nativeMode = "acceptEdits";
		const options = () => [{
			id: "mode", name: "Permission mode", category: "mode", type: "select", currentValue: nativeMode,
			choices: [
				{ value: "auto", name: "Auto" },
				{ value: "default", name: "Manual" },
				{ value: "acceptEdits", name: "Accept Edits" },
				{ value: "dontAsk", name: "Don't Ask" },
				{ value: "bypassPermissions", name: "Bypass Permissions" },
			],
		}];
		await installFakeAgent(page, { projectId, projectName: "Permission menu fixture", workers: [{ id: sessionId, provider: harness, title: `${claude ? "Claude ACP" : "Codex"} permission fixture`, mode: "chat" }] });
		await page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
			const path = new URL(route.request().url()).pathname;
			if (path === "/api/v1/agents/readiness" || path === "/api/v1/agents/readiness/ensure") {
				await route.fulfill({ json: { agents: [] } });
			} else if (path === `/api/v1/projects/${projectId}`) {
				await route.fulfill({ json: { project: { id: projectId, agent: harness, config: { worker: { agent: harness } } } } });
			} else if (path.endsWith("/conversation")) {
				await route.fulfill({ json: { conversationId: "permissions", sessionId, harness, mode: "chat", controller: "ready", latestSequence: 0, oldestSequence: 0, hasMoreBefore: false, turns: [], messages: [], activities: [], settings: { approvalMode }, capabilities: claude ? ["config_options"] : [] } });
			} else if (path.endsWith("/conversation/models")) {
				await route.fulfill({ json: { models: [], selected: { approvalMode } } });
			} else if (path.endsWith("/conversation/settings")) {
				approvalMode = route.request().postDataJSON().approvalMode;
				await route.fulfill({ json: { approvalMode } });
			} else if (path.endsWith("/conversation/config-options/mode")) {
				nativeMode = route.request().postDataJSON().value;
				await route.fulfill({ json: { options: options() } });
			} else if (path.endsWith("/conversation/config-options")) {
				await route.fulfill({ json: { options: claude ? options() : [] } });
			} else if (path.endsWith("/conversation/skills")) {
				await route.fulfill({ json: { skills: [] } });
			} else if (path.endsWith("/workspace/files")) {
				await route.fulfill({ json: { files: [], truncated: false } });
			} else {
				await route.fulfill({ json: { status: "ok" } });
			}
		});
		await page.goto(`/#/projects/${projectId}/sessions/${sessionId}`);
		await expect(page.getByRole("region", { name: "Chat", exact: true })).toBeVisible({ timeout: 20_000 });
		const trigger = page.getByRole("button", { name: claude ? "Permission mode" : "Approval policy for the next turn" });
		await expect(trigger).toHaveText("Accept Edits");
		await trigger.click();
		const items = page.getByRole("menuitem");
		await expect(items).toHaveText(claude ? labels : [...labels, "Provider configuration"]);
		for (const label of labels) await expect(page.getByRole("menuitem", { name: label, exact: true })).toBeEnabled();
		if (process.env.AO_CAPTURE_PERMISSION_EVIDENCE === "1") {
			await page.screenshot({ path: resolve("../docs/screenshots/pr-4930", claude ? "ao-permissions-claude.png" : "ao-permissions.png"), animations: "disabled" });
		}
		const request = page.waitForRequest((request) => request.method() === "PATCH" && request.url().endsWith(claude ? "/conversation/config-options/mode" : "/conversation/settings"));
		await page.getByRole("menuitem", { name: "Manual", exact: true }).click();
		expect((await request).postDataJSON()).toEqual(claude ? { value: "default" } : { approvalMode: "manual" });
		await expect(trigger).toHaveText("Manual");
	});
}
