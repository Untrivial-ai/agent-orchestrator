import { expect, test } from "@playwright/test";

// The Playwright web server runs `dev:web` (VITE_NO_ELECTRON=1), so
// useWorkspaceQuery serves the deterministic preview fixtures from
// lib/mock-data.ts instead of hitting a daemon. The tests run in Chromium
// (no window.ao), so the terminal shows its browser-preview surface.

test("renders the orchestrator-first workbench shell", async ({ page }) => {
	await page.goto("/");
	// The single pinned Orchestrator anchor + the Projects group + a name-only worker row.
	await expect(page.getByRole("button", { name: "Orchestrator", exact: true })).toBeVisible();
	await expect(page.getByText("Projects", { exact: true })).toBeVisible();
	await expect(page.getByRole("button", { name: "fix-webgl-fallback", exact: true })).toBeVisible();
	// Orchestrator side rail = the quiet Workers list.
	await expect(page.getByText("Workers", { exact: true })).toBeVisible();
});

test("deep-links into a worker session", async ({ page }) => {
	await page.goto("/#/workspaces/api-gateway/sessions/refactor-mux");
	// Worker view = three-pane with the Git review rail.
	await expect(page.getByText("Changed")).toBeVisible();
	await expect(page.getByRole("button", { name: /Commit & Push/ })).toBeVisible();
});

test("drilling into a worker opens its Git review rail", async ({ page }) => {
	await page.goto("/");
	await page.getByRole("button", { name: "refactor-mux", exact: true }).click();
	await expect(page.getByRole("button", { name: /Commit & Push/ })).toBeVisible();
	await expect(page.getByText("internal/mux/terminal_mux.go")).toBeVisible();
});
