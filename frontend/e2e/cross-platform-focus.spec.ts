import { expect, test, type Page } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";
import { installFakeTerminalMux } from "./support/fake-terminal-mux";

// The focus fixes are renderer-only and Electron ships the same Chromium on
// every desktop platform, but two things branch on the platform: the command
// palette gives a focused terminal the plain Ctrl+K readline binding off macOS,
// and the terminal host styles itself per platform. Cover the branches here by
// faking the platform the way terminal-viewport-retention.spec.ts does, so the
// macOS-only development machine is not the only place these are exercised.

// isMacPlatform() checks navigator.userAgent as well as navigator.platform, so
// a fake that sets only the platform still takes the macOS branch on a macOS
// development machine. All three have to be overridden together.
const PLATFORMS = [
	{
		name: "macOS",
		navigator: "MacIntel",
		userAgentData: "macOS",
		userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36",
	},
	{
		name: "Windows",
		navigator: "Win32",
		userAgentData: "Windows",
		userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36",
	},
	{
		name: "Linux",
		navigator: "Linux x86_64",
		userAgentData: "Linux",
		userAgent: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36",
	},
] as const;

const sessionA = { id: "xplat-a", title: "Xplat A", activity: "active" as const };
const sessionB = { id: "xplat-b", title: "Xplat B", activity: "active" as const };
const handleA = `${sessionA.id}/terminal_0`;
const handleB = `${sessionB.id}/terminal_0`;

async function fakePlatform(page: Page, platform: (typeof PLATFORMS)[number]) {
	await page.addInitScript(
		({ nav, uad, ua }) => {
			Object.defineProperty(window.navigator, "platform", { configurable: true, value: nav });
			Object.defineProperty(window.navigator, "userAgent", { configurable: true, value: ua });
			Object.defineProperty(window.navigator, "userAgentData", {
				configurable: true,
				value: { platform: uad },
			});
		},
		{ nav: platform.navigator, uad: platform.userAgentData, ua: platform.userAgent },
	);
}

function visibleTerminal(page: Page) {
	return page.getByTestId("session-terminal-slot").locator("[data-terminal-activation-phase='visible']");
}

function caretIsInTheVisibleTerminal(page: Page) {
	return page.evaluate(() => {
		const active = document.activeElement;
		const host = active?.closest("[data-terminal-activation-phase]");
		return (
			Boolean(active?.classList.contains("xterm-helper-textarea")) &&
			host?.getAttribute("data-terminal-activation-phase") === "visible"
		);
	});
}

async function terminalHarness(page: Page, platform: (typeof PLATFORMS)[number]) {
	await page.setViewportSize({ width: 1280, height: 800 });
	await fakePlatform(page, platform);
	await installFakeAgent(page, { workers: [sessionA, sessionB] });
	await installFakeTerminalMux(page, { [handleA]: "A ready", [handleB]: "B ready" });
	await page.goto(`/#/projects/fake-proj/sessions/${sessionA.id}`);
	await expect(visibleTerminal(page)).toHaveCount(1);
}

for (const platform of PLATFORMS) {
	test(`renderer: switching TUI sessions moves the caret on ${platform.name} @T0 @TRM`, async ({ page }) => {
		await terminalHarness(page, platform);
		await expect.poll(() => caretIsInTheVisibleTerminal(page)).toBe(true);

		await page.getByRole("button", { name: `Open ${sessionB.title}`, exact: true }).click();
		await expect(visibleTerminal(page)).toHaveCount(1);
		await expect.poll(() => caretIsInTheVisibleTerminal(page)).toBe(true);
		await page.keyboard.type("bbb");

		const inputs = await page.evaluate(() => window.__aoFakeTerminalMux?.stats().inputs ?? {});
		expect(inputs[handleB]?.join("")).toBe("bbb");
	});

	test(`renderer: New task from the project menu focuses the prompt on ${platform.name} @T0`, async ({ page }) => {
		await fakePlatform(page, platform);
		await installFakeAgent(page, { projectId: "fake-proj", projectName: "fake-proj" });
		await page.goto("/#/projects/fake-proj");
		await page
			.getByRole("button", { name: /Project actions for fake-proj/ })
			.first()
			.click({ force: true });
		await page.getByRole("menuitem", { name: /New session/ }).click();
		const prompt = page.getByRole("dialog").getByLabel("Task");
		await expect(prompt).toBeVisible();
		await page.keyboard.type("caret is here");
		await expect(prompt).toHaveValue("caret is here");
	});
}

// Documents a deliberate consequence of focusing the terminal on open. Off
// macOS the palette hands plain Ctrl+K to a focused terminal so readline's
// kill-to-end-of-line keeps working, which now applies as soon as a session
// opens rather than only after the user clicks into the terminal.
test("renderer: a focused terminal keeps plain Ctrl+K off macOS @T0", async ({ page }) => {
	await terminalHarness(page, PLATFORMS[1]);
	await expect.poll(() => caretIsInTheVisibleTerminal(page)).toBe(true);

	await page.keyboard.press("Control+k");
	await expect(page.getByRole("dialog", { name: "Command palette" })).toHaveCount(0);

	// The palette is still reachable once focus is out of the terminal.
	await page.getByRole("button", { name: `Open ${sessionB.title}`, exact: true }).focus();
	await page.keyboard.press("Control+k");
	await expect(page.getByRole("dialog", { name: "Command palette" })).toHaveCount(1);
});

test("renderer: macOS keeps Cmd+K working from a focused terminal @T0", async ({ page }) => {
	await terminalHarness(page, PLATFORMS[0]);
	await expect.poll(() => caretIsInTheVisibleTerminal(page)).toBe(true);

	await page.keyboard.press("Meta+k");
	await expect(page.getByRole("dialog", { name: "Command palette" })).toHaveCount(1);
});
