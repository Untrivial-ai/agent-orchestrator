import { expect, test } from "@playwright/test";

// dev:web serves lib/mock-data.ts. The review-stack session carries two open
// PRs and one draft PR.

test("the inspector rail stacks every PR a session owns, actionable-first", async ({ page }) => {
	await page.goto("/#/projects/ao-demo/sessions/demo-review-stack");
	await expect(page).toHaveURL(/sessions\/demo-review-stack/);

	const inspector = page.locator("#inspector");
	await expect(inspector).toBeVisible();

	// Plural heading reflects the stack size.
	await expect(inspector.getByText("Pull requests (3)")).toBeVisible();

	// One card per PR, ordered open, then draft. The exact accessible name
	// excludes similarly named links in the activity timeline.
	const cards = inspector.getByRole("link", { name: /^Open PR #\d+$/ });
	await expect(cards).toHaveText(["PR #319", "PR #320", "PR #321"]);
});
