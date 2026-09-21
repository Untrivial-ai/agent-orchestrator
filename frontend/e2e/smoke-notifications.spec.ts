import { expect, test } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";

// NTF-003 RENDERER SMOKE (issue #2483, renderer slice). dev:web + fake bridge —
// not the canonical T0/P0 gate. The real transport boundary is exercised only in
// the packaged-app pod gate (#2697), which today runs a boot-level smoke (app
// launches, daemon ready), NOT this case — per-case pod coverage is future work.
// The notification bell (NotificationCenter) is
// platform-gated to Linux, so we override navigator.platform. The daemon's
// unread list is REST-routed; a needs-input notification then arrives over the
// fake notifications SSE stream (the session-driven part) and the unread badge
// count must track both — exactly the real transport's fetch + merge path.

// #2483 NTF-003.
test("renderer: notification center shows the correct unread count @T0 @NTF", async ({ page }) => {
	await installFakeAgent(page, { platform: "Linux x86_64" });

	// The daemon's initial unread list (two, one of them a needs-input alert).
	await page.route(/\/api\/v1\/notifications\?/, (route) =>
		route.fulfill({
			json: {
				notifications: [
					{
						id: "n1",
						type: "needs_input",
						title: "Worker needs your input",
						body: "",
						status: "unread",
						sessionId: "fake-a",
						projectId: "fake-proj",
						target: { kind: "session", sessionId: "fake-a" },
						createdAt: new Date().toISOString(),
					},
					{
						id: "n2",
						type: "ready_to_merge",
						title: "PR ready to merge",
						body: "",
						status: "unread",
						sessionId: "fake-b",
						projectId: "fake-proj",
						target: { kind: "session", sessionId: "fake-b" },
						createdAt: new Date().toISOString(),
					},
				],
			},
		}),
	);

	await page.goto("/#/projects/ao-demo");
	// The global board on Linux renders the bell in its subhead actions.
	const bell = page.getByRole("button", { name: "2 unread notifications" });
	await expect(bell).toBeVisible();

	// A session-driven needs-input notification arrives over SSE (no refetch); the
	// count climbs to 3 through the transport's merge path.
	await page.evaluate(() =>
		window.__aoFakeAgent!.notify({
			id: "n3",
			type: "needs_input",
			title: "Second worker needs input",
			sessionId: "fake-c",
		}),
	);
	await expect(page.getByRole("button", { name: "3 unread notifications" })).toBeVisible();
});
