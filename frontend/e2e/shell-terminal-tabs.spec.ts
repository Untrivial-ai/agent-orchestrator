import { expect, test } from "@playwright/test";

// Standalone shell terminals (#2822): shells the user opens by hand, with no
// agent session behind them. They render as tabs beside the session's own pane.
// The real pane needs a daemon-spawned PTY, so the preview build stands in with
// an in-memory shell list — enough to cover the parts that live in the renderer:
// which tab is current, and that opening/closing updates the strip.
test("opens, selects, and closes standalone shell terminals from the tab strip", async ({ page }) => {
	await page.goto("/#/projects/ao-demo/sessions/demo-working");
	await expect(page.getByRole("button", { name: "New terminal" })).toBeVisible();

	const closeButtons = page.getByRole("button", { name: /^Close terminal / });
	const initialCount = await closeButtons.count();

	// The button at the end of the tab strip opens a shell and makes it the
	// active pane. It creates a terminal directly — no menu, no session picker.
	await page.getByRole("button", { name: "New terminal" }).click();
	await expect(page.getByRole("menu")).toHaveCount(0);
	await expect(closeButtons).toHaveCount(initialCount + 1);

	// Selecting the session tab hands the pane back to the agent. Matched by the
	// session's own title: the tab's accessible name is that title, and its
	// title attribute falls back to the label once the strip truncates it.
	// Scoped to the terminal panel: the sidebar carries the same session name.
	const sessionTab = page
		.getByTestId("terminal")
		.getByRole("tab", { name: /^Build screenshot-ready dashboard data/ });
	await sessionTab.click();
	await expect(sessionTab).toHaveAttribute("aria-selected", "true");

	// Closing a shell removes exactly its own tab.
	await closeButtons.last().click();
	await expect(closeButtons).toHaveCount(initialCount);
});

// Regression: the open request used to be consumed by the session view, which
// only mounts on a session route — so on the board (or any project with no
// sessions yet) the topbar button and Ctrl+` raised the signal and nothing was
// listening. Both silently did nothing. The shell layout owns it now, and
// routes to the standalone terminals view when there is no session on screen.
test("opens a terminal from the board, where no session view is mounted", async ({ page }) => {
	await page.goto("/#/projects/ao-demo");
	await expect(page.getByRole("button", { name: "New terminal" })).toBeVisible();

	await page.getByRole("button", { name: "New terminal" }).click();

	await expect(page).toHaveURL(/#\/terminals$/);
	await expect(page.getByRole("button", { name: /^Close terminal / })).not.toHaveCount(0);
});

test("shows an empty state once every standalone terminal is closed", async ({ page }) => {
	await page.goto("/#/terminals");

	// Wait for the strip to render before counting — a count taken mid-mount
	// reads 0 and would skip the loop entirely, leaving the terminals open.
	const closeButtons = page.getByRole("button", { name: /^Close terminal / });
	await expect(closeButtons).not.toHaveCount(0);

	// Close one at a time, asserting the strip shrank before the next click: the
	// close is async, so clicking on a stale count would race the re-render.
	for (let remaining = await closeButtons.count(); remaining > 0; remaining--) {
		await closeButtons.first().click();
		await expect(closeButtons).toHaveCount(remaining - 1);
	}

	await expect(page.getByText("No terminals open")).toBeVisible();
});
