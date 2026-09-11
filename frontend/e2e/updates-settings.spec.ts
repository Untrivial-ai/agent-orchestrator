import { expect, test } from "@playwright/test";
import { installFakeBridge } from "./support/fake-bridge";

test("downloaded update keeps the full version readable and actions aligned", async ({ page }) => {
	await page.setViewportSize({ width: 1010, height: 700 });
	await page.emulateMedia({ colorScheme: "dark" });
	await installFakeBridge(page, {
		version: "0.12.7-nightly.202608240525",
		updateSettings: { enabled: true, channel: "nightly", nightlyAck: true, feature: null },
		updateStatus: {
			state: "downloaded",
			version: "0.12.8-nightly.202608241447",
			checkedAt: new Date("2026-08-24T17:11:00.000Z").getTime(),
		},
	});

	await page.goto("/#/settings");
	await page.getByRole("button", { name: "Updates" }).click();

	await expect(page.getByTestId("update-status-line")).toContainText("Ready to install.");

	// The heading carries the base version; the full nightly stamp sits on its
	// own monospace line. As one heading it wrapped mid-token and swallowed the
	// row, and the primary action grew across it.
	const version = page.getByTestId("app-version");
	await expect(version).toHaveText("v0.12.7");
	await expect(version).toHaveAttribute("aria-label", "Current version: v0.12.7-nightly.202608240525");
	await expect(page.getByText("0.12.7-nightly.202608240525", { exact: true })).toBeVisible();

	await expect(page.getByRole("button", { name: "Install Update" })).toBeVisible();
	await expect(page.getByRole("button", { name: "Check for updates" })).toBeVisible();
	await expect(page.getByRole("switch", { name: "Automatic updates" })).toBeChecked();
	await expect(page.getByRole("button", { name: "Channel", exact: true })).toContainText("Nightly");
	await expect(page.locator(".nightly-warning")).toBeVisible();

	const lineCount = await version.evaluate((element) => element.getClientRects().length);
	expect(lineCount).toBe(1);

	const restartBox = await page.getByRole("button", { name: "Install Update" }).boundingBox();
	const checkBox = await page.getByRole("button", { name: "Check for updates" }).boundingBox();
	expect(restartBox).not.toBeNull();
	expect(checkBox).not.toBeNull();
	expect(Math.abs((restartBox?.height ?? 0) - (checkBox?.height ?? 0))).toBeLessThan(1);
	// The actions row must not overrun the version block.
	const versionBox = await version.boundingBox();
	expect(restartBox?.x ?? 0).toBeGreaterThan((versionBox?.x ?? 0) + (versionBox?.width ?? 0));
});

test("@P0 live update flow stays synchronized without reopening Settings", async ({ page }) => {
	await installFakeBridge(page, {
		version: "1.0.0",
		updateStatus: { state: "available", version: "2.0.0" },
		updateSettings: { enabled: false, channel: "latest", nightlyAck: false, feature: null },
	});
	await page.goto("/#/settings");
	await page.getByRole("button", { name: "Updates", exact: true }).click();
	await page.getByRole("button", { name: "Update to v2.0.0" }).click();
	await expect(page.getByTestId("sidebar-update-downloading")).toContainText("Starting download…");
	await expect(page.getByTestId("update-status-line")).toContainText("Starting download…");

	// A missed push is recovered by one local status read, without feed checks.
	await page.evaluate(() => {
		window.ao!.updates.getStatus = async () => ({ state: "downloading", version: "2.0.0", percent: 42, transferred: 42_000_000, total: 100_000_000 });
	});
	await expect(page.getByTestId("sidebar-update-downloading")).toContainText("42% downloaded");
	await expect(page.getByTestId("update-status-line")).toContainText("42.0 / 100.0 MB");
	await expect(page.getByTestId("app-version")).toHaveText("v1.0.0");
	await expect(page.getByText("Updating to v2.0.0")).toBeVisible();

	await page.evaluate(() => {
		window.ao!.updates.getStatus = async () => ({ state: "preparing", version: "2.0.0", percent: 100, staged: { version: "2.0.0", stagedAt: 10, escalated: false, ready: false } });
	});
	await expect(page.getByTestId("sidebar-update-downloading")).toContainText("Preparing update…");
	await expect(page.getByTestId("update-status-line")).toContainText("Preparing update…");
	await expect(page.getByRole("button", { name: "Install Update", exact: true })).toHaveCount(0);

	await page.evaluate(() => {
		window.ao!.updates.getStatus = async () => ({ state: "downloaded", version: "2.0.0" });
	});
	await expect(page.getByTestId("sidebar-update-ready")).toContainText("Install Update");
	await expect(page.getByTestId("sidebar-update-ready")).toHaveClass(/text-success/);
	await expect(page.getByRole("button", { name: "Install Update", exact: true })).toBeEnabled();
});
