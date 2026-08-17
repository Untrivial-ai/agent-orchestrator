import { expect, type Page, test } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";

const projectId = "switch-agent-dialog";

async function openSwitchAgentDialog(page: Page) {
	await page.emulateMedia({ reducedMotion: "reduce" });
	await installFakeAgent(page, {
		projectId,
		projectName: projectId,
		workers: [{ id: "switch-worker", provider: "claude-code", title: "Switch worker" }],
	});
	await page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
		const pathname = new URL(route.request().url()).pathname;
		if (pathname === `/api/v1/projects/${projectId}`) {
			await route.fulfill({
				json: {
					status: "ok",
					project: {
						id: projectId,
						agent: "claude-code",
						config: { worker: { agent: "claude-code" } },
					},
				},
			});
			return;
		}
		if (pathname === "/api/v1/agents/codex/models") {
			await route.fulfill({
				json: {
					agentId: "codex",
					allowCustom: false,
					fetchedAt: "2026-08-15T00:00:00Z",
					models: [{ id: "gpt-5.4", label: "GPT-5.4", isDefault: true }],
					selectionMode: "catalog",
					source: "test",
					stale: false,
				},
			});
			return;
		}
		await route.fulfill({ json: { status: "ok" } });
	});

	await page.goto(`/#/projects/${projectId}/sessions/switch-worker`);
	await page.getByRole("button", { name: "Switch agent", exact: true }).click();
	const dialog = page.getByRole("dialog", { name: "Switch agent" });
	await expect(dialog).toBeVisible();
	return {
		dialog,
		terminalPanel: page.getByRole("tabpanel", { name: "Switch worker terminal" }),
	};
}

test("renderer: switch-agent selector remains compact inside a wide terminal @T0", async ({ page }) => {
	const { dialog, terminalPanel } = await openSwitchAgentDialog(page);
	await expect(dialog).toHaveCSS("width", "420px");
	await expect
		.poll(async () => (await terminalPanel.boundingBox())?.width ?? 0)
		.toBeGreaterThan(420);
});

test("renderer: switch-agent selector stays inside a narrow terminal @T0", async ({ page }) => {
	await page.setViewportSize({ width: 960, height: 720 });
	const { dialog, terminalPanel } = await openSwitchAgentDialog(page);
	const dialogBox = await dialog.boundingBox();
	const terminalBox = await terminalPanel.boundingBox();

	expect(dialogBox).not.toBeNull();
	expect(terminalBox).not.toBeNull();
	expect(dialogBox!.x).toBeGreaterThanOrEqual(terminalBox!.x + 16);
	expect(dialogBox!.x + dialogBox!.width).toBeLessThanOrEqual(
		terminalBox!.x + terminalBox!.width - 16,
	);
});
