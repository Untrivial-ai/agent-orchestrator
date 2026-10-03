import { expect, test, type Page } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";

test.use({ userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36" });

async function geometry(page: Page) {
	return page.evaluate(() => {
		const rect = (selector: string) => {
			const box = document.querySelector(selector)!.getBoundingClientRect();
			return { x: box.x, y: box.y, width: box.width, height: box.height, center: box.y + box.height / 2 };
		};
		return {
			nav: rect('[data-slot="titlebar-nav"]'),
			header: rect('[data-testid="session-topbar-host"]'),
			region: rect('[data-testid="session-terminal-region"]'),
		};
	});
}

for (const mode of ["chat", "tui"] as const) {
	for (const fullScreen of [false, true]) {
		test(`${mode} navigation stays level when sidebar toggles, fullscreen=${fullScreen} @T0`, async ({ page }) => {
			await page.emulateMedia({ reducedMotion: "reduce" });
			await installFakeAgent(page, {
				platform: "MacIntel",
				workers: [{ id: "chrome-worker", provider: "codex", title: "Chrome worker", mode, status: "idle", activity: "idle" }],
			});
			await page.route("**/api/v1/sessions/chrome-worker/conversation{,?*}", (route) => route.fulfill({ json: {
				conversationId: "chrome-conversation", sessionId: "chrome-worker", harness: "codex", mode: "chat", controller: "ready",
				latestSequence: 0, oldestSequence: 0, hasMoreBefore: false, turns: [], messages: [], activities: [], settings: {},
			} }));
			await page.route("**/api/v1/sessions/chrome-worker/conversation/models", (route) => route.fulfill({ json: { models: [], selected: {} } }));
			await page.route("**/api/v1/sessions/chrome-worker/conversation/skills", (route) => route.fulfill({ json: { skills: [] } }));
			await page.addInitScript((fullscreen) => {
				window.ao!.window.isFullScreen = async () => fullscreen;
			}, fullScreen);
			await page.goto("/");
			await page.getByRole("button", { name: "Toggle fake-proj sessions", exact: true }).click();
			await page.getByRole("button", { name: "Open Chrome worker", exact: true }).click();
			await expect(page.locator('[data-testid="session-topbar-host"]')).toBeVisible();
			await expect(page.locator('[data-testid="session-terminal-region"]')).toBeVisible();
			const nav = page.locator('[data-slot="titlebar-nav"]');
			if (await nav.getByRole("button", { name: "Expand sidebar", exact: true }).count()) {
				await nav.getByRole("button", { name: "Expand sidebar", exact: true }).click();
			}
			await expect.poll(async () => {
				const boxes = await geometry(page);
				return Math.abs(boxes.nav.center - boxes.header.center);
			}).toBeLessThanOrEqual(1);
			if (mode === "chat" && !fullScreen) {
				await page.setViewportSize({ width: 700, height: 800 });
				await expect(page.locator('[data-slot="sidebar-gap"]')).toHaveCount(0);
				await page.setViewportSize({ width: 1400, height: 800 });
				await expect.poll(() => page.evaluate(() => Number.parseFloat(document.documentElement.style.getPropertyValue("--ao-sidebar-layout-width")))).toBeGreaterThan(100);
			}
			const expanded = await geometry(page);
			const expandedTab = await page.getByRole("tab").first().boundingBox();
			expect(Math.abs(expandedTab!.x - expanded.region.x)).toBeLessThanOrEqual(1);
			await nav.getByRole("button", { name: "Collapse sidebar", exact: true }).click();
			await expect.poll(async () => (await geometry(page)).region.x).toBeLessThan(expanded.region.x);
			const collapsed = await geometry(page);
			expect(collapsed.nav.y).toBe(expanded.nav.y);
			expect(collapsed.nav.height).toBe(expanded.nav.height);
			expect(Math.abs(collapsed.nav.center - collapsed.header.center)).toBeLessThanOrEqual(1);
			const firstTab = await page.getByRole("tab").first().boundingBox();
			expect(firstTab!.x).toBeGreaterThanOrEqual(collapsed.nav.x + collapsed.nav.width);
			if (!fullScreen) {
				const strip = await page.locator('[data-slot="titlebar-drag-region"]').boundingBox();
				expect(strip!.x + strip!.width).toBeLessThanOrEqual(firstTab!.x);
			}
		});
	}
}

for (const mode of ["chat", "tui"] as const) {
	test(`${mode} title moves continuously left during sidebar collapse @T0`, async ({ page }) => {
		await page.emulateMedia({ reducedMotion: "no-preference" });
		await installFakeAgent(page, { platform: "MacIntel", workers: [{ id: "motion-worker", title: "Motion worker", mode }] });
		await page.route("**/api/v1/sessions/motion-worker/conversation{,?*}", (route) => route.fulfill({ json: {
			conversationId: "motion-conversation", sessionId: "motion-worker", harness: "codex", mode: "chat", controller: "ready",
			latestSequence: 0, oldestSequence: 0, hasMoreBefore: false, turns: [], messages: [], activities: [], settings: {},
		} }));
		await page.goto("/#/projects/fake-proj/sessions/motion-worker");
		await expect(page.getByRole("tab").first()).toBeVisible();
		const nav = page.locator('[data-slot="titlebar-nav"]');
		if (await nav.getByRole("button", { name: "Expand sidebar", exact: true }).count()) {
			await nav.getByRole("button", { name: "Expand sidebar", exact: true }).click();
		}
		await expect.poll(() => page.evaluate(() => document.documentElement.style.getPropertyValue("--ao-sidebar-collapse-progress"))).toBe("0");
		const radius = await page.locator(".center-panel-surface").evaluate((el) => parseFloat(getComputedStyle(el).borderTopLeftRadius));
		expect(radius).toBeGreaterThan(0);
		const positions = await page.evaluate(async () => {
			const tab = document.querySelector<HTMLElement>('[role="tab"]')!;
			const frames = [tab.getBoundingClientRect().x];
			const gap = document.querySelector('[data-slot="sidebar-gap"]')!;
			// Layout observers run before paint. Sample after the chrome observer,
			// rather than forcing layout mid-frame between Motion and its observer.
			const observer = new ResizeObserver(() => frames.push(tab.getBoundingClientRect().x));
			observer.observe(gap);
			document.querySelector<HTMLButtonElement>('[data-slot="titlebar-nav"] button')!.click();
			for (let i = 0; i < 45; i++) await new Promise(requestAnimationFrame);
			observer.disconnect();
			frames.push(tab.getBoundingClientRect().x);
			return frames;
		});
		expect(positions.at(-1)!).toBeLessThan(positions[0] - 20);
		for (let i = 1; i < positions.length; i++) expect(positions[i]).toBeLessThanOrEqual(positions[i - 1] + 1);
	});
}
