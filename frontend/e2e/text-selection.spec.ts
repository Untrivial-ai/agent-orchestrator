import { expect, test } from "@playwright/test";

// Regression for #4268. `body { user-select: none }` (added by PR #3508 to stop
// drags from painting a highlight over sidebar/board chrome) inherits into
// every descendant, so almost nothing outside inputs/textarea/contenteditable
// and the terminal could be selected or copied. The fix does NOT touch that
// `body` rule (a prior PR that did was rejected by the maintainer — see the
// issue) — it opts specific read-only content surfaces back in with the
// `select-text` utility class. These cases pin both halves of the contract:
// `body` stays unselectable, and the named content surfaces select and copy.
//
// jsdom does not enforce CSS `user-select` on its Selection API, so this
// contract can only be verified in a real browser — this is also how the
// original bug was diagnosed (a computed-style read against the live app).

const card = (id: string) => `[data-testid="board-session-card"][data-session-id="${id}"]`;

async function dragSelect(page: import("@playwright/test").Page, locator: import("@playwright/test").Locator) {
	await locator.scrollIntoViewIfNeeded();
	const box = await locator.boundingBox();
	if (!box) throw new Error("target has no layout box");
	await page.mouse.move(box.x + 2, box.y + box.height / 2);
	await page.mouse.down();
	await page.mouse.move(box.x + box.width - 2, box.y + box.height / 2, { steps: 12 });
	await page.mouse.up();
}

function selectedText(page: import("@playwright/test").Page) {
	return page.evaluate(() => window.getSelection()?.toString() ?? "");
}

test("body stays unselectable — the fix does not flip the global default", async ({ page }) => {
	await page.goto("/#/");
	expect(
		await page.evaluate(() => getComputedStyle(document.body).userSelect),
	).toBe("none");
});

test("a board session card stays unselectable on a drag-select", async ({ page }) => {
	await page.goto("/#/projects/ao-demo");
	const target = page.locator(card("demo-ready"));
	await expect(target).toBeVisible();

	expect(
		await target.evaluate((element) => getComputedStyle(element).userSelect),
	).toBe("none");

	await dragSelect(page, target);
	expect(await selectedText(page)).toBe("");
});

test("a board session card's click-to-open still works", async ({ page }) => {
	await page.goto("/#/projects/ao-demo");
	const target = page.locator(card("demo-ready"));
	await expect(target).toBeVisible();
	await target.click();
	await expect(page).toHaveURL(/sessions\/demo-ready/);
});

test("the inspector PR title is selectable and copyable", async ({ page }) => {
	await page.goto("/#/projects/ao-demo/sessions/demo-ready");
	const inspector = page.locator("#inspector");
	await expect(inspector).toBeVisible();

	const prCard = inspector.getByRole("article").filter({ hasText: "Merge README screenshot asset update" });
	const prNumber = prCard.getByText("PR #323", { exact: true });
	const prTitle = prCard.getByText("Merge README screenshot asset update", { exact: true });
	await expect(prTitle).toBeVisible();
	await expect(prNumber).not.toHaveAttribute("href");
	await expect(prTitle).not.toHaveAttribute("href");
	expect(
		await prTitle.evaluate((element) => getComputedStyle(element).userSelect),
	).toBe("text");

	await dragSelect(page, prTitle);
	const selection = await selectedText(page);
	expect(selection.trim()).toContain("README screenshot asset update");

	await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
	await page.keyboard.press(process.platform === "darwin" ? "Meta+C" : "Control+C");
	const clipboard = await page.evaluate(() => navigator.clipboard.readText().catch(() => ""));
	expect(clipboard.trim()).toBe(selection.trim());
});
