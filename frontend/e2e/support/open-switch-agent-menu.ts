import { expect, type Page } from "@playwright/test";

export async function openSwitchAgentDialog(page: Page) {
	await page.getByRole("button", { name: "Session actions", exact: true }).click();
	const switchAgent = page.getByRole("menuitem", { name: "Switch agent", exact: true });
	await expect(switchAgent).toBeVisible();
	await switchAgent.click();
	const dialog = page.getByRole("dialog", { name: "Switch agent" });
	await expect(dialog).toBeVisible();
	return dialog;
}
