import { expect, test } from "@playwright/test";

// The Playwright web server runs `dev:web` (VITE_NO_ELECTRON=1), so
// useWorkspaceQuery serves the deterministic preview fixtures from
// lib/mock-data.ts instead of hitting a daemon. The tests run in Chromium
// (no window.ao), so the terminal shows its browser-preview surface.

test("renders the project-first workbench shell", async ({ page }) => {
	await page.goto("/");
	await expect(page.getByText("Projects", { exact: true })).toBeVisible();
	await expect(page.getByRole("button", { name: "Open ao-demo orchestrator" })).toBeVisible();
	await expect(page.getByRole("heading", { name: "Recent projects" })).toBeVisible();
});

test("deep-links into a worker session", async ({ page }) => {
	await page.goto("/#/projects/ao-demo/sessions/demo-needs-input");
	await expect(page.getByRole("tabpanel", { name: /Resolve reviewer feedback on terminal polish terminal/ })).toBeVisible();
	await expect(page.getByRole("complementary", { name: "Session inspector" })).toBeVisible();
	await expect(page.getByRole("tab", { name: /Files/ })).toBeVisible();
});

test("drilling into a worker opens its session inspector", async ({ page }) => {
	await page.goto("/");
	await page.getByRole("button", { name: "Toggle ao-demo sessions" }).click();
	await page.getByRole("button", { name: "Open Resolve reviewer feedback on terminal polish" }).click();
	await expect(page).toHaveURL(/sessions\/demo-needs-input/);
	await expect(page.getByRole("complementary", { name: "Session inspector" })).toBeVisible();
});
