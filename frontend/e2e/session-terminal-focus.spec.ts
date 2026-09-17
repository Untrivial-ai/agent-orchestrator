import { expect, test, type Page } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";
import { installFakeTerminalMux } from "./support/fake-terminal-mux";

// A worker terminal used to be auto-focused only mid agent-switch, so opening a
// TUI session or switching between two of them left focus on the sidebar button
// that was clicked. Keystrokes went nowhere until the user clicked the terminal.

const sessionA = { id: "tui-focus-a", title: "Tui A", activity: "active" as const };
const sessionB = { id: "tui-focus-b", title: "Tui B", activity: "active" as const };
const handleA = `${sessionA.id}/terminal_0`;
const handleB = `${sessionB.id}/terminal_0`;

function visibleTerminal(page: Page) {
	return page.getByTestId("session-terminal-slot").locator("[data-terminal-activation-phase='visible']");
}

function caretIsInTheVisibleTerminal(page: Page) {
	return page.evaluate(() => {
		const active = document.activeElement;
		if (!active) return false;
		const host = active.closest("[data-terminal-activation-phase]");
		return active.classList.contains("xterm-helper-textarea") && host?.getAttribute("data-terminal-activation-phase") === "visible";
	});
}

async function openSession(page: Page, title: string) {
	await page.getByRole("button", { name: `Open ${title}`, exact: true }).click();
	await expect(visibleTerminal(page)).toHaveCount(1);
}

async function harness(page: Page) {
	await page.setViewportSize({ width: 1280, height: 800 });
	await installFakeAgent(page, { workers: [sessionA, sessionB] });
	await installFakeTerminalMux(page, { [handleA]: "A ready", [handleB]: "B ready" });
	await page.goto(`/#/projects/fake-proj/sessions/${sessionA.id}`);
	await expect(visibleTerminal(page)).toHaveCount(1);
}

test("renderer: a TUI session terminal holds the caret when it opens @T0 @TRM", async ({ page }) => {
	await harness(page);
	await expect.poll(() => caretIsInTheVisibleTerminal(page)).toBe(true);

	await page.keyboard.type("hi");
	await expect
		.poll(async () => (await page.evaluate(() => window.__aoFakeTerminalMux?.stats().inputs ?? {}))[handleA]?.join(""))
		.toBe("hi");
});

test("renderer: switching TUI sessions moves the caret to the session on screen @T0 @TRM", async ({ page }) => {
	await harness(page);
	await expect.poll(() => caretIsInTheVisibleTerminal(page)).toBe(true);

	await openSession(page, sessionB.title);
	await expect.poll(() => caretIsInTheVisibleTerminal(page)).toBe(true);
	await page.keyboard.type("bbb");

	await openSession(page, sessionA.title);
	await expect.poll(() => caretIsInTheVisibleTerminal(page)).toBe(true);
	await page.keyboard.type("aaa");

	// Each session received only its own keystrokes, so the caret followed the
	// terminal that was actually on screen.
	const inputs = await page.evaluate(() => window.__aoFakeTerminalMux?.stats().inputs ?? {});
	expect(inputs[handleB]?.join("")).toBe("bbb");
	expect(inputs[handleA]?.join("")).toBe("aaa");
});
