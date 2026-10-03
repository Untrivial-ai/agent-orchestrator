import { expect, test, type Page } from "@playwright/test";
import { installFakeBridge } from "./support/fake-bridge";
import { openInspector } from "./support/open-inspector";

const sessionId = "demo-working";
const filePath = "backend/internal/session/lifecycle.go";
const patch = [
	`diff --git a/${filePath} b/${filePath}`,
	"index 1111111..2222222 100644",
	`--- a/${filePath}`,
	`+++ b/${filePath}`,
	"@@ -1,3 +1,6 @@",
	" package session",
	" ",
	"+func (s *lifecycleStore) UpdateSessionArtifactOutput() (bool, error) {",
	"+\treturn true, nil",
	"+}",
	" func other() {}",
	"",
].join("\n");

// The preview session has no workspace behind it, so serve one changed file
// with every header action (edit included) to fill the review row. The fake
// bridge gives the renderer a ready daemon on :8080 for these routes to answer.
async function stubWorkspaceFiles(page: Page) {
	await installFakeBridge(page);
	const file = { path: filePath, status: "modified", additions: 3, deletions: 0, size: 512, binary: false, editable: true, fileFingerprint: "fp-1" };
	await page.route(`http://127.0.0.1:8080/api/v1/sessions/${sessionId}/workspace/files*`, (route) =>
		route.fulfill({
			json: {
				sessionId,
				workspaceVersion: "workspace-1",
				files: [file],
				sections: { committed: [], staged: [], unstaged: [file], untracked: [] },
				commits: [],
				summary: { additions: 3, deletions: 0, files: 1 },
				truncated: false,
			},
		}),
	);
	await page.route(`http://127.0.0.1:8080/api/v1/sessions/${sessionId}/workspace/diffs*`, (route) =>
		route.fulfill({
			json: {
				sessionId,
				workspaceVersion: "workspace-1",
				groups: [{ repository: "", patch, truncated: false, includedPaths: [filePath], deferred: [] }],
			},
		}),
	);
}

function overlaps(a: { x: number; y: number; width: number; height: number }, b: { x: number; y: number; width: number; height: number }) {
	return a.x < b.x + b.width && b.x < a.x + a.width && a.y < b.y + b.height && b.y < a.y + a.height;
}

// Tab buttons can overflow their tab list's track, so measure each tab rather
// than the list's own box.
async function filterOverlapsATab(page: Page) {
	const filter = await page.locator("#inspector").getByRole("textbox", { name: "Filter files" }).boundingBox();
	if (!filter) return true;
	for (const tab of await page.locator("#inspector .session-inspector__tablist").getByRole("tab").all()) {
		const box = await tab.boundingBox();
		if (!box || overlaps(box, filter)) return true;
	}
	return false;
}

async function filterIsBelowTabs(page: Page) {
	const tabs = await page.locator("#inspector .session-inspector__tablist").boundingBox();
	const filter = await page.locator("#inspector").getByRole("textbox", { name: "Filter files" }).boundingBox();
	return Boolean(tabs && filter && filter.y >= tabs.y + tabs.height);
}

async function expectFileNameUntruncated(page: Page) {
	const name = page.getByRole("button", { name: `Collapse ${filePath}` }).locator("span").last();
	await expect(name).toHaveText("lifecycle.go");
	await expect.poll(() => name.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true);
}

async function expectNarrowLayout(page: Page) {
	const inspector = page.locator("#inspector");
	await expect.poll(() => filterIsBelowTabs(page)).toBe(true);
	expect(await filterOverlapsATab(page)).toBe(false);
	await expect(inspector.getByRole("button", { name: "Edit file" })).toBeHidden();
	await expect(inspector.getByRole("button", { name: "More actions" })).toBeVisible();
	await expectFileNameUntruncated(page);
}

test("@P0 files filter and review rows stay clear of each other in a narrow inspector", async ({ page }) => {
	await stubWorkspaceFiles(page);
	await page.goto(`/#/projects/ao-demo/sessions/${sessionId}`);
	const inspector = await openInspector(page);
	await inspector.getByRole("tab", { name: /Files?$/ }).click();
	await expect(page.getByRole("button", { name: `Collapse ${filePath}` })).toBeVisible();

	// Wide: the filter shares the tab row, and every row action is inline.
	expect((await inspector.boundingBox())!.width).toBeGreaterThan(440);
	await expect.poll(() => filterOverlapsATab(page)).toBe(false);
	expect(await filterIsBelowTabs(page)).toBe(false);
	await expect(inspector.getByRole("button", { name: "Edit file" })).toBeVisible();
	await expect(inspector.getByRole("button", { name: "More actions" })).toBeHidden();
	await expectFileNameUntruncated(page);

	// Narrow (zoom or a small window), both between the floor and the 440px
	// breakpoint and at the rail's 300px floor: like the browser's URL field,
	// the filter takes its own row, and the row's secondary actions fold into
	// one menu so the file name keeps its room.
	await page.setViewportSize({ width: 1200, height: 720 });
	await expect.poll(async () => (await inspector.boundingBox())!.width).toBeLessThan(400);
	expect((await inspector.boundingBox())!.width).toBeGreaterThan(300);
	await expectNarrowLayout(page);
	await page.setViewportSize({ width: 960, height: 720 });
	await expectNarrowLayout(page);

	await inspector.getByRole("button", { name: "More actions" }).click();
	await expect(page.getByRole("menuitem", { name: "Edit file" })).toBeVisible();
	await expect(page.getByRole("menuitem", { name: "Add feedback" })).toBeVisible();
});
