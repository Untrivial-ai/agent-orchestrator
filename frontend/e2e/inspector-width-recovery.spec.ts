import { expect, test, type Locator, type Page } from "@playwright/test";
import { openInspector } from "./support/open-inspector";

// Zoom or a narrow window caps the inspector at its 300px floor, which wraps
// the top-bar field (Browser URL, Files filter) to a second row. Switching tabs
// there re-runs the width restore against that cap; widening again must grow
// the rail back and unwrap the field instead of leaving both pinned.

async function inspectorWidth(page: Page) {
	return (await page.locator("#inspector").boundingBox())?.width ?? 0;
}

// The rail springs between widths; wait until two reads agree.
async function settledInspectorWidth(page: Page) {
	let previous = -1;
	await expect
		.poll(async () => {
			const current = await inspectorWidth(page);
			const settled = Math.abs(current - previous) < 0.5;
			previous = current;
			return settled;
		}, { intervals: [250] })
		.toBe(true);
	return previous;
}

async function isBelowTabs(page: Page, field: Locator) {
	const tabs = await page.locator("#inspector .session-inspector__tablist").boundingBox();
	const box = await field.boundingBox();
	return Boolean(tabs && box && box.y >= tabs.y + tabs.height);
}

// Starts from a 1440px window, the width the spec widens back to.
async function narrowSwitchAndWiden(page: Page, inspector: Locator, tab: string, field: Locator) {
	await expect.poll(() => isBelowTabs(page, field)).toBe(false);
	const wide = await settledInspectorWidth(page);
	expect(wide).toBeGreaterThan(440);

	await page.setViewportSize({ width: 960, height: 720 });
	await expect.poll(() => inspectorWidth(page)).toBeLessThan(301);
	await expect.poll(() => isBelowTabs(page, field)).toBe(true);
	await inspector.getByRole("tab", { name: "Summary" }).click();
	await inspector.getByRole("tab", { name: tab }).click();
	await expect.poll(() => isBelowTabs(page, field)).toBe(true);
	return wide;
}

test("@P0 Browser grows back and unwraps its URL field after a narrow-window tab switch", async ({ page }) => {
	await page.setViewportSize({ width: 1440, height: 720 });
	await page.goto("/#/projects/ao-demo/sessions/demo-working");
	const inspector = await openInspector(page);
	await inspector.getByRole("tab", { name: "Browser" }).click();
	const address = page.getByTestId("browser-address-bar");
	await expect(address).toBeVisible();

	const wide = await narrowSwitchAndWiden(page, inspector, "Browser", address);
	// Pressing the resize handle without moving must not pin the narrow width either.
	const handle = await page.getByTestId("inspector-resize-handle").boundingBox();
	if (!handle) throw new Error("inspector resize handle not visible");
	await page.mouse.move(handle.x + handle.width / 2, handle.y + handle.height / 2);
	await page.mouse.down();
	await page.mouse.up();

	await page.setViewportSize({ width: 1440, height: 720 });
	await expect.poll(() => isBelowTabs(page, address)).toBe(false);
	expect(Math.abs((await settledInspectorWidth(page)) - wide)).toBeLessThanOrEqual(8);
	expect(await page.evaluate(() => window.localStorage.getItem("ao.workspace.browser.canvasWidthPx"))).toBeNull();
});

test("@P0 Files grows back and unwraps its filter after a narrow-window tab switch", async ({ page }) => {
	await page.setViewportSize({ width: 1440, height: 720 });
	await page.goto("/#/projects/ao-demo/sessions/demo-working");
	const inspector = await openInspector(page);
	await inspector.getByRole("tab", { name: /Files?$/ }).click();
	const filter = inspector.getByRole("textbox", { name: "Filter files" });
	await expect(filter).toBeVisible();

	const wide = await narrowSwitchAndWiden(page, inspector, "Files", filter);

	await page.setViewportSize({ width: 1440, height: 720 });
	await expect.poll(() => isBelowTabs(page, filter)).toBe(false);
	expect(Math.abs((await settledInspectorWidth(page)) - wide)).toBeLessThanOrEqual(8);
});

test("@P0 Browser grows back when the sidebar collapses after the window narrowed", async ({ page }) => {
	await page.setViewportSize({ width: 1440, height: 720 });
	await page.goto("/#/projects/ao-demo/sessions/demo-working");
	const inspector = await openInspector(page);
	await inspector.getByRole("tab", { name: "Browser" }).click();
	const address = page.getByTestId("browser-address-bar");
	await expect(address).toBeVisible();
	await settledInspectorWidth(page);

	// Narrowing must not write the 300px cap into the width: collapsing the
	// sidebar widens the split without a window resize, and the rail has to
	// grow into that room on its own.
	await page.setViewportSize({ width: 960, height: 720 });
	await expect.poll(() => inspectorWidth(page)).toBeLessThan(301);
	await expect.poll(() => isBelowTabs(page, address)).toBe(true);
	await page.keyboard.press("ControlOrMeta+b");
	await expect(page.locator('[data-slot="sidebar"]')).toHaveAttribute("data-state", "collapsed");
	expect(await settledInspectorWidth(page)).toBeGreaterThan(440);
	await expect.poll(() => isBelowTabs(page, address)).toBe(false);
});

test("@P0 toggling the sidebar leaves the Browser rail's width alone", async ({ page }) => {
	await page.goto("/#/projects/ao-demo/sessions/demo-working");
	const inspector = await openInspector(page);
	await inspector.getByRole("tab", { name: "Browser" }).click();
	await expect(page.getByTestId("browser-address-bar")).toBeVisible();
	const before = await settledInspectorWidth(page);

	await page.keyboard.press("ControlOrMeta+b");
	await expect(page.locator('[data-slot="sidebar"]')).toHaveAttribute("data-state", "collapsed");
	expect(Math.abs((await settledInspectorWidth(page)) - before)).toBeLessThanOrEqual(1);
});
