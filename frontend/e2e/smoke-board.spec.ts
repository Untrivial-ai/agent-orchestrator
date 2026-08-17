import { expect, test } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";

// BRD-* RENDERER SMOKE (issue #2483, renderer slice). dev:web + fake bridge —
// does NOT hit the real daemon/storage/API/preload/PTY/FS. Those boundaries are
// exercised only in the packaged-app pod gate (#2697), which today runs a
// boot-level smoke (app launches, daemon ready), NOT these cases — per-case pod
// coverage is future work. Drives the board off the fake-agent CDC SSE stream so
// column moves and live updates exercise the same SSE → invalidate → refetch
// path the real daemon uses (see fake-bridge.ts). IDs cross-reference #2483.

const columnCard = (column: string, id: string) =>
	`[data-testid="board-column"][data-column="${column}"] [data-session-id="${id}"]`;

// #2483 BRD-002.
test("renderer: card moves columns when its status changes @T0 @BRD", async ({ page }) => {
	await installFakeAgent(page, { workers: [{ id: "mover", title: "Wandering worker", status: "working" }] });
	await page.goto("/#/");
	await expect(page.getByTestId("board")).toBeVisible();
	// Starts in Working.
	await expect(page.locator(columnCard("working", "mover"))).toBeVisible();
	await expect(page.locator(columnCard("action", "mover"))).toHaveCount(0);

	// Fake agent hits waiting_input → the card must move to the "Needs you" column.
	await page.evaluate(() => window.__aoFakeAgent!.setStatus("mover", "needs_input", "waiting_input"));

	await expect(page.locator(columnCard("action", "mover"))).toBeVisible();
	await expect(page.locator(columnCard("working", "mover"))).toHaveCount(0);
	await expect(page.locator(columnCard("action", "mover"))).toContainText("Input needed");
});

// #2483 BRD-006.
test("renderer: SSE pushes card updates without a manual refresh @T0 @BRD", async ({ page }) => {
	await installFakeAgent(page, { workers: [{ id: "live", title: "Live worker", status: "working" }] });
	await page.goto("/#/");
	await expect(page.locator(columnCard("working", "live"))).toContainText("Working");

	// A single CDC frame (no page.reload) must repaint the card into "Ready to
	// merge" with its new badge.
	await page.evaluate(() => window.__aoFakeAgent!.setStatus("live", "mergeable", "idle"));

	await expect(page.locator(columnCard("merge", "live"))).toBeVisible();
	await expect(page.locator(columnCard("merge", "live"))).toContainText("Ready");
});

test("renderer: narrow card status truncates without overlapping metadata @BRD", async ({ page }) => {
	await installFakeAgent(page, {
		workers: [{ id: "review", title: "Review worker", status: "review_pending", activity: "idle" }],
	});
	await page.route("**/api/v1/usage/sessions**", (route) =>
		route.fulfill({
			contentType: "application/json",
			body: JSON.stringify({
				sessions: [{ sessionId: "review", totalTokens: 24_600_000, incomplete: false }],
			}),
		}),
	);
	await page.goto("/#/");

	const card = page.locator(columnCard("pending", "review"));
	const status = card.getByText("Review pending", { exact: true });
	const usage = card.getByText("24.6M tok", { exact: true });
	await expect(usage).toBeVisible();

	// Reproduce the effective card width reached at enlarged browser zoom.
	await card.evaluate((element) => {
		(element as HTMLElement).style.width = "12rem";
	});

	const statusMetrics = await status.evaluate((element) => ({
		clientWidth: element.clientWidth,
		scrollWidth: element.scrollWidth,
		overflow: getComputedStyle(element).overflow,
		textOverflow: getComputedStyle(element).textOverflow,
		whiteSpace: getComputedStyle(element).whiteSpace,
	}));
	const statusBox = await status.boundingBox();
	const usageBox = await usage.boundingBox();

	expect(statusMetrics.scrollWidth).toBeGreaterThan(statusMetrics.clientWidth);
	expect(statusMetrics).toMatchObject({ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" });
	expect(statusBox).not.toBeNull();
	expect(usageBox).not.toBeNull();
	expect(Math.abs(usageBox!.y - statusBox!.y)).toBeLessThan(2);
	expect(statusBox!.x + statusBox!.width).toBeLessThanOrEqual(usageBox!.x);
});
