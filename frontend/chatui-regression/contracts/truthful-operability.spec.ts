import type { Locator } from "@playwright/test";

import { expect, message, test, turn } from "../support/test";

async function exposesSelectedState(locator: Locator): Promise<boolean> {
	return locator.evaluate((element) => {
		if (element instanceof HTMLInputElement && (element.type === "radio" || element.type === "checkbox")) {
			return element.checked;
		}
		return element.getAttribute("aria-checked") === "true" || element.getAttribute("aria-pressed") === "true";
	});
}

test.describe("ChatUI truthful controls and operability", () => {
	test.describe("MQA-01 provider Plan mode disclosure", () => {
		test.use({ chatUIOptions: { provider: "claude-code", sessionId: "chatui-plan-mode" } });

		test("qualifies provider copy that falsely promises no tool execution", async ({ chatUI, page }) => {
			chatUI.configOptions = [
				{
					id: "mode",
					name: "Mode",
					description: "Provider execution mode",
					category: "mode",
					type: "select",
					currentValue: "plan",
					choices: [
						{
							value: "plan",
							name: "Plan",
							description: "Planning mode, no actual tool execution.",
						},
						{ value: "default", name: "Default", description: "Normal provider behavior." },
					],
				},
			];
			await chatUI.open();

			await page.getByRole("button", { name: "Claude Code permission mode" }).click();
			const menu = page.getByRole("menu");
			await expect(menu).toBeVisible();
			await expect.soft(menu).not.toContainText("no actual tool execution");
			await expect.soft(menu).toContainText(
				"Claude Code Plan mode may inspect files, use tools, and write provider-owned plan artifacts outside the workspace. AO does not enforce a no-tool or no-write boundary.",
			);
		});
	});

	test.describe("MQA-03 deterministic agent-switch copy companion", () => {
		test.use({ chatUIOptions: { provider: "codex", sessionId: "chatui-agent-switch" } });

		test("states that a switch can start fresh without claiming semantic handoff", async ({ chatUI, page }) => {
			await chatUI.open();
			await page.getByRole("button", { name: "Switch agent", exact: true }).click();

			const dialog = page.getByRole("dialog", { name: "Switch agent" });
			await expect(dialog).toBeVisible();
			await expect.soft(dialog).toContainText("The target starts a fresh native session.");
			await expect.soft(dialog).toContainText("No semantic handoff is available for this switch.");
			await expect.soft(dialog).not.toContainText(
				"AO will preserve the current native session and hand off the work.",
			);
		});
	});

	test.describe("MQA-11 selection semantics", () => {
		test.use({
			chatUIOptions: {
				activity: "active",
				sessionId: "chatui-accessibility",
				status: "working",
			},
		});

		test("exposes delivery and settings selection to assistive technology", async ({ chatUI, page }) => {
			chatUI.conversation = {
				...chatUI.conversation,
				controller: "busy",
				latestSequence: 1,
				oldestSequence: 1,
				turns: [turn("turn-running", "running")],
				messages: [message("message-running", "turn-running", 1, "user", "Keep working")],
			};
			await chatUI.open();

			const delivery = page.getByRole("group", {
				name: "Where this message goes while the agent is working",
			});
			await expect(delivery).toBeVisible();
			const queue = delivery
				.getByRole("radio", { name: "Queue" })
				.or(delivery.getByRole("button", { name: "Queue" }));
			const steer = delivery
				.getByRole("radio", { name: "Steer" })
				.or(delivery.getByRole("button", { name: "Steer" }));
			await expect(queue).toHaveCount(1);
			await expect(steer).toHaveCount(1);
			await expect.poll(() => exposesSelectedState(queue)).toBe(true);
			await expect.poll(() => exposesSelectedState(steer)).toBe(false);

			await page.keyboard.down("Control");
			try {
				await expect.poll(() => exposesSelectedState(queue)).toBe(false);
				await expect.poll(() => exposesSelectedState(steer)).toBe(true);
			} finally {
				await page.keyboard.up("Control");
			}

			await page.getByRole("button", { name: "What the agent may do without asking" }).click();
			const choices = page.getByRole("menuitemradio");
			await expect(choices).toHaveCount(4);
			await expect.poll(() => exposesSelectedState(choices.first())).toBe(true);
			await expect.poll(() => exposesSelectedState(choices.last())).toBe(false);
			await choices.last().click();

			await page.getByRole("button", { name: "What the agent may do without asking" }).click();
			const updatedChoices = page.getByRole("menuitemradio");
			await expect(updatedChoices).toHaveCount(4);
			await expect.poll(() => exposesSelectedState(updatedChoices.first())).toBe(false);
			await expect.poll(() => exposesSelectedState(updatedChoices.last())).toBe(true);
		});
	});

	test.describe("GAP-01 implemented-but-unmounted signals", () => {
		test.use({ chatUIOptions: { sessionId: "chatui-operability" } });

		test("mounts context and quota telemetry where users can act on it", async ({ chatUI, page }) => {
			chatUI.conversation = {
				...chatUI.conversation,
				usage: {
					contextUsed: 180_000,
					contextWindow: 200_000,
					inputTokens: 175_000,
					outputTokens: 5_000,
					cachedTokens: 0,
					totalTokens: 180_000,
				},
				rateLimits: {
					primaryUsedPercent: 94,
					secondaryUsedPercent: 40,
					primaryResetsInSeconds: 3_600,
					planLabel: "Pro",
				},
			};
			await chatUI.open();

			await expect(page.getByRole("progressbar", { name: "Context window used" })).toHaveAttribute(
				"aria-valuenow",
				"90",
			);
			await expect(page.getByLabel(/94% of Pro rate limit used/i)).toBeVisible();
		});

		test("shows actionable credit-exhaustion detail instead of only reconnect status", async ({ chatUI, page }) => {
			chatUI.conversation = {
				...chatUI.conversation,
				latestSequence: 1,
				oldestSequence: 1,
				activities: [
					{
						id: "provider-error-credit",
						sequence: 1,
						revision: 0,
						activityKind: "error",
						status: "failed",
						summary: "Reconnecting... [1/5]",
						detail: {
							message: "Reconnecting... [1/5]",
							error: "You have no credits remaining. Add credits to continue using the API.",
							actionUrl: "https://platform.openai.com/settings/organization/billing",
						},
						createdAt: "2026-08-25T09:00:00.000Z",
					},
				],
			};
			await chatUI.open();

			await expect(page.getByText(/You have no credits remaining/i)).toBeVisible();
			await expect(page.getByRole("link", { name: /Add credits/i })).toHaveAttribute(
				"href",
				"https://platform.openai.com/settings/organization/billing",
			);
		});
	});

	test.describe("MQA-12 renderer error gate", () => {
		test.use({ chatUIOptions: { mode: "tui", sessionId: "chatui-runtime-errors" } });

		test("survives Terminal, shell-tab, Chat, close, and resize lifecycles without uncaught errors", async ({ chatUI, page }) => {
			await chatUI.open();
			const closeShellButtons = page.getByRole("button", { name: /^Close terminal / });
			const initialShellCount = await closeShellButtons.count();
			const muxStats = () => page.evaluate(() => window.__aoFakeTerminalMux?.stats());
			const closeLastShell = async () => {
				const close = closeShellButtons.last();
				await close.locator("..").hover();
				await expect(close).toBeVisible();
				await close.click();
			};

			await expect(page.getByTestId("terminal-interaction-surface")).toBeVisible();
			await expect
				.poll(async () => (await muxStats())?.opens[`${chatUI.sessionId}/terminal_0`] ?? 0)
				.toBeGreaterThan(0);
			await page.setViewportSize({ width: 1_080, height: 720 });

			await page.getByRole("button", { name: "New terminal" }).click();
			await expect(closeShellButtons).toHaveCount(initialShellCount + 1);
			await expect
				.poll(async () =>
					Object.keys((await muxStats())?.opens ?? {}).filter((handle) => handle.startsWith("shellterm-preview-")).length,
				)
				.toBe(1);
			await page.setViewportSize({ width: 1_440, height: 900 });

			const workerTab = page.getByRole("tab", { name: "ChatUI regression worker" });
			await workerTab.click();
			await expect(workerTab).toHaveAttribute("aria-selected", "true");
			await closeLastShell();
			await expect(closeShellButtons).toHaveCount(initialShellCount);

			await chatUI.setMode("chat");
			await expect(page.getByRole("region", { name: "Chat" })).toBeVisible();
			await page.getByRole("button", { name: "New terminal" }).click();
			await expect(closeShellButtons).toHaveCount(initialShellCount + 1);
			await expect(page.getByTestId("chat-shell-terminal")).toBeVisible();
			await expect
				.poll(async () =>
					Object.keys((await muxStats())?.opens ?? {}).filter((handle) => handle.startsWith("shellterm-preview-")).length,
				)
				.toBe(2);
			await page.setViewportSize({ width: 1_120, height: 760 });

			const chatTab = page.getByRole("tab", { name: "ChatUI regression worker" });
			await chatTab.press("Enter");
			await expect(chatTab).toHaveAttribute("aria-selected", "true");
			await closeLastShell();
			await expect(closeShellButtons).toHaveCount(initialShellCount);

			await chatUI.setMode("tui");
			await expect(page.getByRole("region", { name: "Chat" })).toHaveCount(0);
			await expect(page.getByTestId("terminal-interaction-surface")).toBeVisible();
			await page.setViewportSize({ width: 1_360, height: 840 });

			expect.soft(chatUI.pageErrors, "uncaught page errors").toEqual([]);
			expect.soft(chatUI.consoleErrors, "error-level console events").toEqual([]);
		});

		test("strict fixture rejects a deliberately injected page error", async ({ chatUI, page }) => {
			test.fail(true, "The synthetic error must be rejected by the fixture's strict page-error gate.");
			void chatUI;
			const marker = "MQA-12-SYNTHETIC-PAGE-ERROR";
			const observedPageError = page.waitForEvent("pageerror");
			await page.evaluate((message) => {
				window.setTimeout(() => {
					throw new Error(message);
				}, 0);
			}, marker);
			expect((await observedPageError).message).toContain(marker);
		});
	});
});
