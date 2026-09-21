import { expect, test } from "@playwright/test";

// dev:web (VITE_NO_ELECTRON=1) serves lib/mock-data.ts. Use stable session URLs
// so fixture title changes do not prevent these tests from reaching Reviews.

test("the Reviews tab renders the reviewer panel for a session that owns PRs", async ({ page }) => {
	await page.goto("/#/projects/ao-demo/sessions/demo-ready");

	const inspector = page.locator("#inspector");
	await expect(inspector).toBeVisible();

	await inspector.getByRole("tab", { name: "Reviews" }).click();

	// The reviewer card surfaces the harness, its approved verdict, and the rerun
	// action — never the empty state, since this session owns a PR.
	await expect(inspector.getByText("No pull request opened yet.")).toHaveCount(0);
	const reviewRow = inspector.getByTestId("review-pr-row");
	await expect(reviewRow).toHaveAttribute("aria-expanded", "false");
	await expect(inspector.getByText("codex", { exact: true })).toHaveCount(0);
	await reviewRow.click();
	await expect(reviewRow).toHaveAttribute("aria-expanded", "true");
	await expect(inspector.getByText("codex", { exact: true })).toBeVisible();
	await expect(inspector.getByText("Approved", { exact: true }).first()).toBeVisible();
	await expect(inspector.getByRole("button", { name: "Re-run review" })).toBeVisible();
	await expect(inspector.getByRole("button", { name: "Open terminal" })).toHaveCount(0);
});

test("the Reviews tab stays hidden for a session with no reviewable PRs", async ({ page }) => {
	await page.goto("/#/projects/ao-demo/sessions/demo-working");

	const inspector = page.locator("#inspector");
	await expect(inspector).toBeVisible();
	await expect(inspector.getByRole("tab", { name: "Reviews" })).toHaveCount(0);
	await expect(inspector.getByRole("tab", { name: "Summary" })).toHaveAttribute("aria-selected", "true");
});

test("review controls stay aligned and collapse the run action at minimum width", async ({ page }) => {
	await page.addInitScript(() => {
		window.localStorage.setItem("ao.inspector.widthPx", "350");
	});
	await page.setViewportSize({ height: 900, width: 1100 });
	await page.goto("/#/projects/ao-demo/sessions/demo-needs-input");

	const inspector = page.locator("#inspector");
	await inspector.getByRole("tab", { name: "Reviews" }).click();
	await expect(inspector.getByText("Review controls")).toBeVisible();

	const reviewerLabel = inspector.getByText("Select reviewer agent", { exact: true });
	const reviewerSelect = inspector.getByRole("button", { name: "Select reviewer agent" });
	const [reviewerLabelBox, reviewerSelectBox] = await Promise.all([
		reviewerLabel.boundingBox(),
		reviewerSelect.boundingBox(),
	]);
	if (!reviewerLabelBox || !reviewerSelectBox) throw new Error("reviewer controls are not visible");
	const reviewerLabelCenterY = reviewerLabelBox.y + reviewerLabelBox.height / 2;
	const reviewerSelectCenterY = reviewerSelectBox.y + reviewerSelectBox.height / 2;
	expect(Math.abs(reviewerLabelCenterY - reviewerSelectCenterY)).toBeLessThanOrEqual(2);

	const controls = inspector.locator(".review-run-controls-container");
	const controlsBox = await controls.boundingBox();
	if (!controlsBox) throw new Error("review controls are not visible");
	expect(reviewerSelectBox.width).toBeLessThan(controlsBox.width / 2);

	const triggerLabel = inspector.getByText("Trigger review", { exact: true });
	const runButton = inspector.getByRole("button", { name: "Re-run review" });
	const [triggerLabelBox, runButtonBox] = await Promise.all([
		triggerLabel.boundingBox(),
		runButton.boundingBox(),
	]);
	if (!triggerLabelBox || !runButtonBox) throw new Error("review trigger controls are not visible");
	const triggerLabelCenterY = triggerLabelBox.y + triggerLabelBox.height / 2;
	const runButtonCenterY = runButtonBox.y + runButtonBox.height / 2;
	expect(Math.abs(triggerLabelCenterY - runButtonCenterY)).toBeLessThanOrEqual(2);
	await expect(runButton.locator(".review-run-action-label")).toBeHidden();
	await runButton.hover();
	await expect(page.getByRole("tooltip", { name: "Re-run review" })).toBeVisible();

	const prRow = inspector.getByTestId("review-pr-row");
	await expect(prRow).toHaveAttribute("aria-expanded", "false");
	await prRow.click();
	await expect(prRow).toHaveAttribute("aria-expanded", "true");
	await expect(inspector.getByText("External reviews")).toBeVisible();
	const [prTitleBox, prVerdictBox] = await Promise.all([
		prRow.getByText("Terminal polish feedback", { exact: true }).boundingBox(),
		prRow.getByText("Changes requested", { exact: true }).first().boundingBox(),
	]);
	if (!prTitleBox || !prVerdictBox) throw new Error("review PR header is not visible");
	expect(prVerdictBox.y).toBeGreaterThanOrEqual(prTitleBox.y + prTitleBox.height);

	const agentSummary = inspector.getByTestId("review-run-summary").first().locator("xpath=ancestor::article[1]");
	const [agentActorBox, agentActionBox, agentVerdictBox] = await Promise.all([
		agentSummary.getByText("codex", { exact: true }).boundingBox(),
		agentSummary.getByRole("button", { name: "Review actions" }).boundingBox(),
		agentSummary.getByText("Changes requested", { exact: true }).boundingBox(),
	]);
	if (!agentActorBox || !agentActionBox || !agentVerdictBox) throw new Error("agent review header is not visible");
	const actorCenterY = agentActorBox.y + agentActorBox.height / 2;
	const verdictCenterY = agentVerdictBox.y + agentVerdictBox.height / 2;
	expect(Math.abs(actorCenterY - verdictCenterY)).toBeLessThanOrEqual(2);

	const overflows = await inspector.evaluate((root) => {
		const targets = [
			root.querySelector<HTMLElement>('[role="tabpanel"]'),
			root.querySelector<HTMLElement>(".review-run-controls-container"),
			...root.querySelectorAll<HTMLElement>('[data-testid="review-pr-row"]'),
			...Array.from(root.querySelectorAll<HTMLElement>('[data-testid="review-run-summary"]'))
				.map((summary) => summary.closest<HTMLElement>("article")),
			...root.querySelectorAll<HTMLElement>('[data-testid="external-review-header"]'),
		].filter((element): element is HTMLElement => Boolean(element));

		return targets.flatMap((element) => {
			const bounds = element.getBoundingClientRect();
			const culprits = Array.from(element.querySelectorAll<HTMLElement>("*"))
				.filter((child) => {
					const childBounds = child.getBoundingClientRect();
					if (childBounds.width === 0 || childBounds.height === 0) return false;
					return childBounds.left < bounds.left - 1 || childBounds.right > bounds.right + 1;
				})
				.slice(0, 3)
				.map((child) => child.getAttribute("aria-label") || child.className || child.tagName);
			return culprits.length > 0
				? [{ culprits, target: element.dataset.testid || element.className || element.getAttribute("role") }]
				: [];
		});
	});

	expect(overflows).toEqual([]);
});
