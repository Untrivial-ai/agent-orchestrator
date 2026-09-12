import { expect, type Locator, type Page, test } from "@playwright/test";
import { agentReadiness } from "../src/renderer/test/agent-readiness-fixtures";
import { installFakeAgent } from "./support/fake-bridge";
import { openSwitchAgentDialog } from "./support/open-switch-agent-menu";

const projectId = "switch-agent-dialog";

async function setupSwitchAgentDialogTest(page: Page): Promise<{
	dialog: Locator;
	terminalPanel: Locator;
}> {
	await page.emulateMedia({ reducedMotion: "reduce" });
	await installFakeAgent(page, {
		projectId,
		projectName: projectId,
		workers: [{ id: "switch-worker", provider: "claude-code", title: "Switch worker" }],
	});
	await page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
		const pathname = new URL(route.request().url()).pathname;
		if (pathname === "/api/v1/agents/readiness" || pathname === "/api/v1/agents/readiness/ensure") {
			await route.fulfill({
				json: {
					agents: [agentReadiness("claude-code", "Claude Code"), agentReadiness("codex", "Codex")],
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
	const primaryTerminalTab = page.locator('[data-terminal-role="primary"]');
	const primaryTabBox = await primaryTerminalTab.boundingBox();
	const terminalRegionBox = await page.getByTestId("session-terminal-region").boundingBox();
	expect(primaryTabBox).not.toBeNull();
	expect(terminalRegionBox).not.toBeNull();
	expect(primaryTabBox!.x + primaryTabBox!.width).toBeLessThanOrEqual(
		terminalRegionBox!.x + terminalRegionBox!.width,
	);
	const dialog = await openSwitchAgentDialog(page);
	return {
		dialog,
		terminalPanel: page.getByRole("tabpanel", { name: "Switch worker terminal" }),
	};
}

test("renderer: switch-agent selector remains compact inside a wide terminal @T0", async ({ page }) => {
	const { dialog, terminalPanel } = await setupSwitchAgentDialogTest(page);
	await expect(dialog).toHaveCSS("width", "420px");
	await expect
		.poll(async () => (await terminalPanel.boundingBox())?.width ?? 0)
		.toBeGreaterThan(420);
});

test("renderer: switch-agent selector stays inside a narrow terminal @T0", async ({ page }) => {
	await page.setViewportSize({ width: 960, height: 720 });
	const { dialog, terminalPanel } = await setupSwitchAgentDialogTest(page);
	const dialogBox = await dialog.boundingBox();
	const terminalBox = await terminalPanel.boundingBox();

	expect(dialogBox).not.toBeNull();
	expect(terminalBox).not.toBeNull();
	expect(dialogBox!.x).toBeGreaterThanOrEqual(terminalBox!.x + 16);
	expect(dialogBox!.x + dialogBox!.width).toBeLessThanOrEqual(
		terminalBox!.x + terminalBox!.width - 16,
	);
});
