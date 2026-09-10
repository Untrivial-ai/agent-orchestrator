import { expect, test, type Page } from "@playwright/test";

// Dragging a panel edge must clamp at the panel's minimum width — never
// auto-collapse. Collapse belongs to the explicit controls only (⌘B / topbar
// buttons). The clamp lives in useResizable (sidebar + inspector).

async function dragPointer(page: Page, from: { x: number; y: number }, to: { x: number; y: number }) {
	await page.mouse.move(from.x, from.y);
	await page.mouse.down();
	// Multiple steps so rrp/useResizable see a realistic pointermove stream.
	await page.mouse.move(to.x, to.y, { steps: 8 });
	await page.mouse.up();
}

test("sidebar drag stops at its minimum width instead of collapsing", async ({ page }) => {
	await page.goto("/");

	const sidebar = page.locator('[data-slot="sidebar"]');
	await expect(sidebar).toHaveAttribute("data-state", "expanded");

	const handle = page.getByTestId("resize-handle");
	const box = await handle.boundingBox();
	if (!box) throw new Error("sidebar resize handle not visible");

	// Drag far past the 200px floor, all the way to the window edge.
	await dragPointer(
		page,
		{ x: box.x + box.width / 2, y: box.y + box.height / 2 },
		{ x: 0, y: box.y + box.height / 2 },
	);

	await expect(sidebar).toHaveAttribute("data-state", "expanded");
	const width = await page.evaluate(() =>
		document.documentElement.style.getPropertyValue("--ao-sidebar-w"),
	);
	expect(width).toBe("200px");

	// The explicit toggle still collapses (and the drag-clamped width persisted).
	await page.keyboard.press("ControlOrMeta+b");
	await expect(sidebar).toHaveAttribute("data-state", "collapsed");
	expect(await page.evaluate(() => window.localStorage.getItem("ao-sidebar-w"))).toBe("200");
});

test("inspector drag stops at minSize instead of collapsing; buttons still toggle", async ({ page }) => {
	// A worker session from the dev:web mock dataset (lib/mock-data.ts).
	await page.goto("/#/projects/ao-demo/sessions/demo-working");

	const inspector = page.locator("#inspector");
	await expect(inspector).toBeVisible();

	const handle = page.getByTestId("inspector-resize-handle");
	const handleBox = await handle.boundingBox();
	const groupBox = await page.locator("#session-workspace").boundingBox();
	if (!handleBox || !groupBox) throw new Error("session split not visible");
	const y = handleBox.y + handleBox.height / 2;

	// Drag the handle all the way to the right edge: the rail must clamp at
	// its minimum width, not snap away.
	await dragPointer(
		page,
		{ x: handleBox.x + handleBox.width / 2, y },
		{ x: groupBox.x + groupBox.width - 2, y },
	);

	await expect(inspector).toBeVisible();
	const inspectorBox = await inspector.boundingBox();
	if (!inspectorBox) throw new Error("inspector hidden after drag");
	expect(inspectorBox.width).toBeGreaterThanOrEqual(350);

	// The explicit control still collapses…
	await page.getByRole("button", { name: "Close inspector panel" }).click();
	await expect(inspector).toBeHidden();

	// …and dragging the collapsed rail back out reopens it.
	const collapsedRail = page.getByTestId("inspector-collapsed-rail");
	const collapsedRailBox = await collapsedRail.boundingBox();
	if (!collapsedRailBox) throw new Error("collapsed rail not visible");
	await dragPointer(
		page,
		{ x: collapsedRailBox.x + collapsedRailBox.width / 2, y },
		{ x: groupBox.x + groupBox.width * 0.7, y },
	);
	await expect(inspector).toBeVisible();

	// Reopened rail must again refuse to drag-collapse.
	const reopenedHandleBox = await handle.boundingBox();
	if (!reopenedHandleBox) throw new Error("handle not visible after reopen");
	await dragPointer(
		page,
		{ x: reopenedHandleBox.x + reopenedHandleBox.width / 2, y },
		{ x: groupBox.x + groupBox.width - 2, y },
	);
	await expect(inspector).toBeVisible();
});
