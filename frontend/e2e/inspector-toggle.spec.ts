import { expect, test } from "@playwright/test";

// Regression for the dead inspector toggle. Open/close is store-driven Motion
// (same spring as the left sidebar). The native browser view and xterm fit
// must follow the sliding rail; this needs the real layout pipeline.
test("topbar button collapses and reopens the inspector rail", async ({ page }) => {
	// A worker session from the dev:web mock dataset (lib/mock-data.ts).
	await page.goto("/#/projects/ao-demo/sessions/demo-working");

	// Fresh profile: the rail must mount open, not get toggled shut by
	// mount-time layout events.
	const inspector = page.locator("#inspector");
	await expect(inspector).toBeVisible();

	await page.getByRole("button", { name: "Close inspector panel" }).click();
	await expect(inspector).toBeHidden();

	await page.getByRole("button", { name: "Open inspector panel" }).click();
	await expect(inspector).toBeVisible();
});
