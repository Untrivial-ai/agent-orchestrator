import { expect, test } from "@playwright/test";

// dev:web (VITE_NO_ELECTRON=1) serves lib/mock-data.ts. The api-gateway
// workspace owns a "stacked-auth" session ("auth stack") carrying three PRs:
// #41 open, #42 draft, #40 merged — the multi-PR-per-session case this suite
// guards across the inspector rail.

test("the inspector rail stacks every PR a session owns, actionable-first", async ({ page }) => {
	await page.goto("/");
	await page.getByRole("button", { name: "Open auth stack" }).click();
	await expect(page).toHaveURL(/sessions\/stacked-auth/);

	const inspector = page.locator("#inspector");
	await expect(inspector).toBeVisible();

	// Plural heading reflects the stack size.
	await expect(inspector.getByText("Pull requests (3)")).toBeVisible();

	// One card per PR, ordered open → draft → merged (the merged base sinks).
	// Scope to the PR section: the Activity timeline also renders "Opened PR #n".
	const prSection = inspector.locator("section.inspector-section", { hasText: "Pull requests (3)" });
	const cards = prSection.locator("text=/^PR #\\d+$/");
	await expect(cards).toHaveText(["PR #41", "PR #42", "PR #40"]);
});
