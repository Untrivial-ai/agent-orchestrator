import { expect, test } from "@playwright/test";
import { agentReadiness } from "../src/renderer/test/agent-readiness-fixtures";
import { installFakeAgent } from "./support/fake-bridge";

const projectId = "model-scroll-proj";
const models = Array.from({ length: 30 }, (_, index) => ({
	id: `opencode-go/model-${index + 1}`,
	label: `opencode-go/model-${index + 1}`,
	provider: "opencode-go",
	isDefault: index === 0,
}));

test("renderer: hidden model-menu scrollbar keeps wheel scrolling functional @T0", async ({ page }) => {
	await installFakeAgent(page, { projectId, projectName: projectId });
	await page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
		const request = route.request();
		const pathname = new URL(request.url()).pathname;
		if (pathname === "/api/v1/agents/readiness" || pathname === "/api/v1/agents/readiness/ensure") {
			await route.fulfill({
				json: {
					agents: [agentReadiness("opencode", "OpenCode")],
				},
			});
			return;
		}
		if (pathname === `/api/v1/projects/${projectId}`) {
			await route.fulfill({
				json: {
					status: "ok",
					project: {
						id: projectId,
						agent: "opencode",
						config: { worker: { agent: "opencode" } },
					},
				},
			});
			return;
		}
		if (pathname === "/api/v1/agents/opencode/models") {
			await route.fulfill({
				json: {
					agent: "opencode",
					selectionMode: "catalog",
					models,
					allowCustom: true,
					refreshRecommended: false,
				},
			});
			return;
		}
		await route.fulfill({ json: { status: "ok" } });
	});

	await page.goto(`/#/projects/${projectId}`);
	await page.getByRole("button", { name: "New task" }).first().click();
	const dialog = page.getByRole("dialog", { name: "New task" });
	await expect(dialog).toBeVisible();
	await dialog.getByRole("button", { name: "Model" }).click();

	const scroller = page.locator(".model-menu-scroll");
	await expect(scroller).toBeVisible();
	const geometry = await scroller.evaluate((element) => ({
		clientHeight: element.clientHeight,
		scrollHeight: element.scrollHeight,
	}));
	expect(geometry.scrollHeight).toBeGreaterThan(geometry.clientHeight);
	await scroller.hover();
	await page.mouse.wheel(0, 400);

	await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
});
