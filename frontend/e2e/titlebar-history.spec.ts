import { expect, test } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";

for (const platform of ["MacIntel", "Linux x86_64"]) {
	test.describe(platform, () => {
		test.use({ userAgent: platform === "MacIntel"
			? "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36"
			: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36" });

		for (const [open, storedWidth] of [[true, 200], [true, 420], [false, 420]] as const) {
			test(`history stays visible and navigates with sidebar open=${open}, stored width=${storedWidth} @T0`, async ({ page }) => {
				await page.emulateMedia({ reducedMotion: "reduce" });
				await installFakeAgent(page, { platform, workers: [{ id: "nav-worker", title: "Navigation worker", mode: "tui" }] });
				await page.addInitScript((width) => window.localStorage.setItem("ao-sidebar-w", String(width)), storedWidth);
				await page.goto("/#/");
				const nav = page.locator('[data-slot="titlebar-nav"]');
				if (!open) await nav.getByRole("button", { name: "Collapse sidebar", exact: true }).click();
				const brand = nav.locator("[data-sidebar-brand]");
				if (open) await expect(brand).toBeVisible();
				else await expect(brand).toHaveCount(0);
				const back = nav.getByRole("button", { name: "Go back", exact: true });
				const forward = nav.getByRole("button", { name: "Go forward", exact: true });
				await expect(back).toBeVisible();
				await expect(back).toBeDisabled();
				await expect(forward).toBeVisible();
				await expect(forward).toBeDisabled();
				// Use an app link so the router, rather than a document load, owns history.
				await page.getByRole("button", { name: /^fake-proj \/repos\/fake-proj / }).click();
				await expect(page).toHaveURL(/#\/projects\/fake-proj$/);
				await expect(back).toBeEnabled();
				await page.locator('[data-testid="board-session-card"][data-session-id="nav-worker"]').click();
				await expect(page).toHaveURL(/#\/projects\/fake-proj\/sessions\/nav-worker$/);
				await back.click();
				await expect(page).toHaveURL(/#\/projects\/fake-proj$/);
				await expect(forward).toBeEnabled();
				await forward.click();
				await expect(page).toHaveURL(/#\/projects\/fake-proj\/sessions\/nav-worker$/);
				await expect(forward).toBeDisabled();
				if (open) {
					for (const width of [1280, 800]) {
						await page.setViewportSize({ width, height: 800 });
						await expect(brand).toBeVisible();
						await expect(back).toBeVisible();
						await expect(forward).toBeVisible();
						// History updates the URL before the session tab finishes mounting.
						await expect(page.getByRole("tab").first()).toBeVisible();
						await expect.poll(() => nav.evaluate((el) => {
							const right = el.querySelectorAll("button")[2].getBoundingClientRect().right;
							const brand = el.querySelector("[data-sidebar-brand]")!.getBoundingClientRect();
							const sidebar = document.querySelector('[data-slot="sidebar-container"]')!.getBoundingClientRect();
							const tab = document.querySelector('[role="tab"]')!.getBoundingClientRect();
							return right <= brand.left && brand.right <= sidebar.right && brand.right <= tab.left;
						})).toBe(true);
						if (width === 1280) {
							await expect.poll(() => brand.locator("[data-brand-label]").evaluate((el) => el.scrollWidth <= el.clientWidth + 1)).toBe(true);
						}
					}
				}
			});
		}
	});
}
