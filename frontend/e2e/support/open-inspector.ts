import { expect, type Page } from "@playwright/test";

/** Opening a session leaves the inspector closed; open it for specs that exercise the rail. */
export async function openInspector(page: Page) {
	await page.getByRole("button", { name: "Open inspector panel" }).click();
	const inspector = page.locator("#inspector");
	await expect(inspector).toBeVisible();
	return inspector;
}
