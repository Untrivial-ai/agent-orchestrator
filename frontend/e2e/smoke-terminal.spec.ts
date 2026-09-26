import { expect, test } from "./support/test";

// #2483 TRM-001, RENDERER SLICE. Under dev:web there is no window.ao and no PTY,
// so TerminalPane renders its deterministic browser-preview transcript (the
// data-testid="session-terminal" surface) seeded from lib/mock-data.ts. This
// proves the renderer attaches the terminal surface and paints a stream; the real
// zellij/PTY attach is exercised only in the packaged-app pod gate (#2697), which
// today runs a boot-level smoke, NOT this case — per-case pod coverage is future
// work. Not the canonical T0/P0 gate.

test("renderer: terminal attaches on session detail and renders a stream @T0 @TRM", async ({ page }) => {
	await page.goto("/#/projects/ao-demo/sessions/demo-working");
	await expect(page.getByTestId("session-detail")).toBeVisible();

	const terminal = page.getByTestId("session-terminal");
	await expect(terminal).toBeVisible();
	// The streamed transcript for demo-working (mock-data.ts) is rendered.
	await expect(terminal).toContainText("PASS 18 tests passed");
});
