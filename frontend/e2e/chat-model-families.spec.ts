import { expect, test } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";

for (const family of ["Astra", "Opus", "Fable"]) {
 const emptyCatalog=false, model=undefined;
 const harness=family === "Astra" ? "codex" : "claude-code";
 const options=[{id:"model",name:"Model",category:"model",type:"select",currentValue:"default",choices:[{value:"default",name:"Default"}]}];
 const id=family === "Astra" ? "gpt-6-astra" : family === "Opus" ? "claude-opus-4-8" : "claude-fable-5-1[1m]";
 const label=family === "Astra" ? "GPT-6 Astra" : family === "Opus" ? "Opus 4.8" : "Fable 5.1";
	test(`${family} family selects exact advertised version @T0`, async ({ page }) => {
		options[0].choices = family === "Opus"
			? [{ value: "default", name: "Default" }, { value: "opus[1m]", name: "Opus 5" }, { value: id, name: label }]
			: [{ value: "default", name: "Default" }, { value: id, name: label }];
 let chosen: unknown;
 const projectId = "model-identity";
		const sessionId = "astra-chat";
		await installFakeAgent(page, { projectId, projectName: projectId, workers: [{ id: sessionId, provider: harness, title: `${family} versions`, mode: "chat" }] });
		await page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
			const path = new URL(route.request().url()).pathname;
			if (path.endsWith("/conversation/config-options")) {await route.fulfill({json:{options}});
 } else if (path.endsWith("/conversation/settings") || path.endsWith("/config-options/model")) { chosen=route.request().postDataJSON(); await route.fulfill({json:{settings:chosen}});
 } else if (path === "/api/v1/agents/readiness" || path === "/api/v1/agents/readiness/ensure") {
				await route.fulfill({ json: { agents: [] } });
			} else if (path === `/api/v1/projects/${projectId}`) {
				await route.fulfill({ json: { project: { id: projectId, agent: "codex", config: { worker: { agent: "codex" } } } } });
			} else if (path.endsWith("/conversation")) {
				await route.fulfill({ json: { conversationId: "astra", sessionId, harness, capabilities:harness === "claude-code" ? ["config_options"] : [], mode: "chat", controller: "ready", latestSequence: 0, oldestSequence: 0, hasMoreBefore: false, turns: [], messages: [], activities: [], settings: { model } } });
			} else if (path.endsWith("/conversation/models")) {
				await route.fulfill({ json: { models: emptyCatalog ? [] : [{ id, displayName: label, efforts: ["high"] }], selected: { model } } });
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
		const trigger = page.getByRole("button", { name: harness === "codex" ? "Model and reasoning effort for the next turn" : "Model", exact:true });
		await expect(trigger).toHaveText(harness === "codex" ? "Provider default" : "Default");
		await trigger.click();
		await expect(page.getByRole("menuitem", { name: /^Effort/ })).toHaveCount(0);
		if (harness === "codex") await page.getByRole("menuitem", { name: /^Model/ }).hover();
		if (family === "Opus") await page.getByRole("menuitem", { name: family, exact: true }).hover();
		const choice = page.getByRole("menuitem", { name: new RegExp("^" + label.replaceAll(".", "\\.")) });
  await expect(choice).toBeVisible();
  await page.screenshot({path:`../docs/screenshots/pr-4923/${family.toLowerCase()}-versions.png`});
  await choice.click();
  await expect.poll(()=>chosen).toMatchObject(harness === "codex" ? {model:id} : {value:id});
	});
}
