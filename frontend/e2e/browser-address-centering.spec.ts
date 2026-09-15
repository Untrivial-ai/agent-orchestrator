import { expect, test, type Page } from "@playwright/test";

async function expectAddressCentered(page: Page) {
	await expect
		.poll(async () => {
			const inspectorTopbar = await page.locator("#inspector .session-inspector__topbar").boundingBox();
			const address = await page.getByTestId("browser-address-bar").boundingBox();
			if (!inspectorTopbar || !address) return Number.POSITIVE_INFINITY;
			return Math.abs(address.x + address.width / 2 - (inspectorTopbar.x + inspectorTopbar.width / 2));
		})
		.toBeLessThanOrEqual(1);
}

async function expectAddressShiftedRight(page: Page) {
	await expect
		.poll(async () => {
			const inspectorTopbar = await page.locator("#inspector .session-inspector__topbar").boundingBox();
			const address = await page.getByTestId("browser-address-bar").boundingBox();
			if (!inspectorTopbar || !address) return false;
			const offset = address.x + address.width / 2 - (inspectorTopbar.x + inspectorTopbar.width / 2);
			return offset >= 32 && offset <= 48;
		})
		.toBe(true);
}

async function expectAddressWidth(page: Page, width: number) {
	await expect
		.poll(async () => {
			const measuredWidth = (await page.getByTestId("browser-address-bar").boundingBox())?.width;
			return measuredWidth !== undefined && Math.abs(measuredWidth - width) <= 0.1;
		})
		.toBe(true);
}

async function expectAddressBelowInspectorTabs(page: Page) {
	await expect
		.poll(async () => {
			const tabs = await page.locator("#inspector .session-inspector__tablist").boundingBox();
			const address = await page.getByTestId("browser-address-bar").boundingBox();
			if (!tabs || !address) return false;
			return address.y >= tabs.y + tabs.height;
		})
		.toBe(true);
}

test("@P0 browser address shifts clear of wide tabs and remains centered in the compact inspector", async ({ page }) => {
	await page.goto("/#/projects/ao-demo/sessions/demo-working");
	await page.locator("#inspector").getByRole("tab", { name: "Browser" }).click();
	await expect(page.getByTestId("browser-address-bar")).toBeVisible();

	await expectAddressShiftedRight(page);
	await expectAddressWidth(page, 240);
	await page.setViewportSize({ width: 1100, height: 720 });
	await expectAddressCentered(page);
	await expectAddressWidth(page, 180);
	await expectAddressBelowInspectorTabs(page);
	await page.setViewportSize({ width: 960, height: 720 });
	await expectAddressCentered(page);
	await expectAddressWidth(page, 180);
	await expectAddressBelowInspectorTabs(page);
});
