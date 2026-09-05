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

	await expect(page.getByTestId("update-status-line")).toContainText("Downloaded. Restart to finish updating.");

	// The heading carries the base version; the full nightly stamp sits on its
	// own monospace line. As one heading it wrapped mid-token and swallowed the
	// row, and the primary action grew across it.
	const version = page.getByTestId("app-version");
	await expect(version).toHaveText("v0.12.7");
	await expect(version).toHaveAttribute("aria-label", "Current version - v0.12.7-nightly.202608240525");
	await expect(page.getByText("0.12.7-nightly.202608240525", { exact: true })).toBeVisible();

	await expect(page.getByRole("button", { name: "Restart & install" })).toBeVisible();
	await expect(page.getByRole("button", { name: "Check for updates" })).toBeVisible();
	await expect(page.getByRole("switch", { name: "Automatic Updates" })).toBeChecked();
	await expect(page.getByRole("button", { name: "Updates channel" })).toContainText("Nightly");
	await expect(page.locator(".nightly-warning")).toBeVisible();

	const lineCount = await version.evaluate((element) => element.getClientRects().length);
	expect(lineCount).toBe(1);

	const restartBox = await page.getByRole("button", { name: "Restart & install" }).boundingBox();
	const checkBox = await page.getByRole("button", { name: "Check for updates" }).boundingBox();
	expect(restartBox).not.toBeNull();
	expect(checkBox).not.toBeNull();
	expect(Math.abs((restartBox?.height ?? 0) - (checkBox?.height ?? 0))).toBeLessThan(1);
	// The actions row must not overrun the version block.
	const versionBox = await version.boundingBox();
	expect(restartBox?.x ?? 0).toBeGreaterThan((versionBox?.x ?? 0) + (versionBox?.width ?? 0));
});

test("replacement progress can restart below A completion without losing either identity", async ({ page }) => {
	await installFakeBridge(page, {
		updateStatus: { state: "downloaded", version: "1.0.0", stagedAt: 100 },
	});
	await page.goto("/#/settings");
	await page.getByRole("button", { name: "Updates" }).click();
	await expect(page.getByRole("button", { name: "Restart & install" })).toBeVisible();

	await page.evaluate(() => {
		(window as typeof window & { __aoEmitUpdateStatus: (status: unknown) => void }).__aoEmitUpdateStatus({
			state: "replacing",
			version: "2.0.0",
			percent: 7,
			stagedCandidate: { version: "1.0.0", channel: "latest", operationId: "a" },
			replacementCandidate: { version: "2.0.0", channel: "nightly", operationId: "b" },
			replacementPhase: "differential",
			installDisabledReason: "Replacement 2.0.0 is incomplete",
		});
	});

	await expect(page.locator("#update-status-line")).toContainText("Downloading 2.0.0 to replace staged update 1.0.0");
	await expect(page.locator("#update-status-line")).toContainText("Quitting now may install 1.0.0");
	await expect(page.getByRole("progressbar", { name: "Download progress for 2.0.0" })).toHaveAttribute("aria-valuenow", "7");
	await expect(page.getByRole("button", { name: "Restart & install" })).toBeHidden();
});

test("replacement discovery exposes Download while preserving the staged warning", async ({ page }) => {
	await installFakeBridge(page, {
		updateSettings: { enabled: false, channel: "latest", nightlyAck: false, feature: null },
		updateStatus: {
			state: "replacing",
			version: "2.0.0",
			stagedCandidate: { version: "1.0.0", channel: "latest", operationId: "a" },
			replacementCandidate: { version: "2.0.0", channel: "latest", operationId: "b" },
			replacementPhase: "checking",
		},
	});
	await page.goto("/#/settings");
	await page.getByRole("button", { name: "Updates", exact: true }).click();
	await expect(page.getByRole("button", { name: /Update to.*2.0.0/i }).last()).toBeEnabled();
	await expect(page.locator("#update-status-line")).toContainText("Quitting now may install 1.0.0");
	await expect(page.getByRole("button", { name: "Restart & install" })).toBeHidden();
});
