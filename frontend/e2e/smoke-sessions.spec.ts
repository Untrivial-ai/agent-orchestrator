import { expect, test } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";

// SES-* RENDERER SMOKE (issue #2483, renderer slice).
//
// Scope: this runs under `dev:web` (VITE_NO_ELECTRON=1) with an injected
// `window.ao` + a fake CDC/SSE stream + an in-page workspace snapshot. It
// exercises the renderer's SSE → invalidate → refetch path only — NOT the real
// daemon, storage, API, preload, PTY, or filesystem. Those boundaries are
// exercised only in the packaged-app pod gate (#2697), which today runs a
// boot-level smoke (app launches, daemon ready), NOT these cases — per-case pod
// coverage is future work. The case IDs cross-reference the #2483 catalog; they
// are not a claim of full-boundary coverage, and this suite is not the canonical
// T0/P0 gate.

const card = (id: string) => `[data-testid="board-session-card"][data-session-id="${id}"]`;
const columnCard = (column: string, id: string) =>
	`[data-testid="board-column"][data-column="${column}"] [data-session-id="${id}"]`;

// #2483 SES-002.
test("renderer: new session card appears in the spawning/working state @T0 @SES", async ({ page }) => {
	// Renderer note: there is no distinct "spawning" badge — a freshly spawned
	// session enters the Working column (badge "Working"); the daemon's
	// spawning→working transition lands here. The card must not exist until the
	// fake agent creates it.
	await installFakeAgent(page);
	await page.goto("/#/projects/fake-proj");
	await expect(page.getByTestId("board")).toBeVisible();
	await expect(page.locator(card("fake-spawn"))).toHaveCount(0);

	await page.evaluate(() =>
		window.__aoFakeAgent!.createWorker({ id: "fake-spawn", title: "Spawning worker", activity: "exited" }),
	);

	await expect(page.locator(columnCard("building", "fake-spawn"))).toBeVisible();
	await expect(page.locator(card("fake-spawn"))).toContainText("Working");
});
