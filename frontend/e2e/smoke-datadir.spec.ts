import { expect, test } from "@playwright/test";
import { installFakeAgent, installFakeBridge } from "./support/fake-bridge";

// Data-directory invariant (issue #2483, RENDERER SLICE). dev:web + fake
// bridge — this asserts only the renderer's readiness reflection, NOT the on-disk
// ~/.ao layout or the real daemon/storage. The on-disk boundary is exercised only
// in the packaged-app pod gate (#2697), which today runs a boot-level smoke (app
// launches, daemon ready), NOT the full ~/.ao layout assertion — that stays in
// the pod data-dir script as future per-case work.
//
// The on-disk checks (Electron userData resolves under ~/.ao/electron, daemon
// data under ~/.ao, no OS-default app-data writes, AO_DATA_DIR override) run in
// the pod against a packaged build — they are not observable from the renderer.
// What the renderer CAN attest is the daemon-readiness contract that those
// invariants gate: a daemon only advertises its REST port once its data dir +
// config skeleton are initialized, and the renderer reflects that as a ready
// status and a hydrated board. This locks the renderer side of the invariant;
// the ~/.ao filesystem assertions stay in the pod data-dir script.

test("renderer: reflects daemon data-dir readiness @P0 @DATADIR", async ({ page }) => {
	// Use installFakeAgent so the board card is served through the
	// window.__aoFakeAgent.snapshot() workspace seam — the daemon-backed source —
	// not the static mockWorkspaces fallback. Otherwise the card assertion below
	// would pass even if daemon/data-dir-backed hydration were broken (false green).
	await installFakeAgent(page, {
		daemonPort: 8080,
		workers: [{ id: "datadir", title: "Persisted worker", status: "working" }],
	});
	await page.goto("/#/projects/fake-proj");

	// A ready daemon on a port ⇒ its data dir + config skeleton initialized.
	await expect(page.getByTestId("daemon-status")).toHaveAttribute("data-state", "ready");
	// State backed by that data dir hydrates the board.
	await expect(page.getByTestId("board")).toBeVisible();
	await expect(page.getByTestId("board-session-card").first()).toBeVisible();
});

test("renderer: surfaces a not-ready data dir without crashing @P0 @DATADIR", async ({ page }) => {
	// If the data dir is not ready the daemon never advertises a port; the
	// renderer must degrade to a non-ready status, not crash.
	await installFakeBridge(page, { daemonState: "starting" });
	await page.goto("/#/projects/ao-demo");
	await expect(page.getByTestId("daemon-status")).not.toHaveAttribute("data-state", "ready");
	await expect(page.getByTestId("board")).toBeVisible();
});
