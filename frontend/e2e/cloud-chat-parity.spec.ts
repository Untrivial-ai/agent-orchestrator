import { test, expect } from "@playwright/test";

test("Cloud messages preserve Markdown boundaries and automation cards @T0", async ({ page }, testInfo) => {
	await page.goto("/e2e/performance/cloud-chat-parity.html");
	await expect(page.getByRole("heading", { name: "Comparison" })).toBeVisible();
	await expect(page.getByText("Checking listings.", { exact: true })).toBeVisible();
	const automation = page.locator(".cursor-chat-origin-message");
	await expect(automation).toContainText("automation");
	await expect(automation).toContainText("Worker report: the listing comparison is complete.");
	await expect(page.locator("[data-chat-scroll-anchor]")).toHaveCount(2);
	await page.reload();
	await expect(page.locator(".cursor-chat-origin-message")).toBeVisible();
	await expect(page.getByRole("heading", { name: "Comparison" })).toBeVisible();
	await page.screenshot({ path: testInfo.outputPath("cloud-chat-parity.png"), fullPage: true });
});
