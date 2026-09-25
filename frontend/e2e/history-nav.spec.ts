import { expect, test } from "@playwright/test";

// Repro for the titlebar history arrows: navigate home → project → back,
// then the forward arrow must be enabled and actually traverse forward.
test("titlebar back/forward arrows traverse history", async ({ page }) => {
	await page.goto("/");
	await expect(page.getByText("Projects", { exact: true })).toBeVisible();

	// Navigate: home → session view (in-app push).
	await page.getByRole("button", { name: "ao-demo /demo/ao-demo 3 hours ago" }).click();
	await expect(page).toHaveURL(/projects\/ao-demo/);
	await page.locator('[data-session-id="demo-working"]').click();
	await expect(page).toHaveURL(/sessions\/demo-working/);

	const back = page.getByRole("button", { name: "Go back" });
	const forward = page.getByRole("button", { name: "Go forward" });

	await expect(forward).toBeDisabled();
	await expect(back).toBeEnabled();

	await back.click();
	await expect(page).not.toHaveURL(/sessions\/demo-working/);

	await expect(forward).toBeEnabled();
	await forward.click();
	await expect(page).toHaveURL(/sessions\/demo-working/);
});
