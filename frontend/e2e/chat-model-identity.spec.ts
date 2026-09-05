import { expect, test } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";

for (const { emptyCatalog, model } of [{ emptyCatalog: true, model: "gpt-6-astra" }, { emptyCatalog: false, model: "gpt-6-astra" }, { emptyCatalog: false, model: undefined }]) {
	test(`${model ?? "native default"} stays truthful with ${emptyCatalog ? "empty" : "stale"} catalog @T0`, async ({ page }) => {
		const projectId = "model-identity";
		const sessionId = "astra-chat";
		await installFakeAgent(page, { projectId, projectName: projectId, workers: [{ id: sessionId, provider: "codex", title: "Astra", mode: "chat" }] });
		await page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
			const path = new URL(route.request().url()).pathname;
			if (path === "/api/v1/agents/readiness" || path === "/api/v1/agents/readiness/ensure") {
				await route.fulfill({ json: { agents: [] } });
			} else if (path === `/api/v1/projects/${projectId}`) {
				await route.fulfill({ json: { project: { id: projectId, agent: "codex", config: { worker: { agent: "codex" } } } } });
			} else if (path.endsWith("/conversation")) {
				await route.fulfill({ json: { conversationId: "astra", sessionId, harness: "codex", mode: "chat", controller: "ready", latestSequence: 0, oldestSequence: 0, hasMoreBefore: false, turns: [], messages: [], activities: [], settings: { model } } });
			} else if (path.endsWith("/conversation/models")) {
				await route.fulfill({ json: { models: emptyCatalog ? [] : [{ id: "terra", displayName: "Terra", default: true, efforts: ["high"], defaultEffort: "high" }], selected: { model } } });
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
		const trigger = page.getByRole("button", { name: "Model and reasoning effort for the next turn" });
		await expect(trigger).toHaveText(model ?? "Provider default");
		await trigger.click();
		await expect(page.getByRole("menuitem", { name: /^Effort/ })).toHaveCount(0);
		await page.getByRole("menuitem", { name: /^Model/ }).hover();
		if (model) await expect(page.getByRole("menuitem", { name: "gpt-6-astra (not in catalog)" })).toBeVisible();
	});
}
