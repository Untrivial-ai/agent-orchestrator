import { expect, test } from "@playwright/test";

test("CI auto-injection policy is visible before a PR exists", async ({ page }) => {
	await page.goto("/#/projects/ao-demo/sessions/demo-working");

	const inspector = page.locator("#inspector");
	const toggle = inspector.getByRole("switch", { name: "Automatically send CI failures" });
	await expect(toggle).toBeVisible();
	await expect(toggle).toBeChecked();
	await expect(inspector.getByText("No pull request opened yet.")).toBeVisible();

	await toggle.click();
	await expect(toggle).not.toBeChecked();
});

test("a failing PR captured with injection disabled keeps its checks visible", async ({ page }) => {
	await page.goto("/#/projects/ao-demo/sessions/demo-ci-failed");

	const inspector = page.locator("#inspector");
	await expect(inspector.getByRole("switch", { name: "Automatically send CI failures" })).not.toBeChecked();
	await expect(inspector.getByText("CI failures not injected")).toBeVisible();
	await expect(inspector.getByText("renderer smoke", { exact: true })).toBeVisible();
});
