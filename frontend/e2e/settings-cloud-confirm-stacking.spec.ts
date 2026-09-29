import { expect, test } from "@playwright/test";
import { installFakeBridge } from "./support/fake-bridge";

// REGRESSION: settings-dialog stacking vs ConfirmDialog (#5873, then #5944).
//
// #5873 raised the settings dialog content to z-overlay+1 to keep it above its
// own blurred scrim; #5944 kept that value while deduping the class list. Every
// ConfirmDialog lives on the z-overlay layer and portals AFTER the settings
// dialog, so any confirm opened from inside Settings painted UNDER the settings
// surface: present in the DOM, visually buried, unusable. The visible break was
// the Developer-mode Cloud toggle, whose enable path is confirm-gated (disable
// is not), so the switch looked dead in one direction only.
//
// toBeVisible() and plain clicks cannot catch this: Radix marks the covered
// background pointer-events:none, so an occluded confirm still has size and
// still receives synthetic clicks. The check that fails on the bug is the
// stacking rule itself: the confirm's computed z-index must beat the settings
// dialog's, or tie it and portal after it — which is exactly how CSS decides
// paint order for two body-level portals.

const port = Number(process.env.AO_E2E_PORT ?? 5173);

const settingsBody = (cloudOffering: boolean) => ({
	defaultSessionMode: "tui",
	chatHarnesses: ["claude-code"],
	client: "",
	localEnabled: true,
	cloudOffering,
	cloudEnabled: cloudOffering,
	cloudControlPlaneUrl: "https://cloud.test",
});

test("settings: confirm opened from Settings stacks above it and enables Cloud", async ({ page }) => {
	await installFakeBridge(page, { daemonPort: port });
	await page.route("**/api/v1/settings*", async (route) => {
		await route.fulfill({ json: settingsBody(false) });
	});

	await page.goto("/#/settings");
	await expect(page.getByTestId("settings-page")).toBeVisible();

	// The Cloud row only exists with Developer mode on; enabling it is the
	// confirm-gated direction this regression broke.
	await page.getByRole("switch", { name: "Developer mode" }).click();
	const cloudSwitch = page.getByRole("switch", { name: "Cloud", exact: true });
	await cloudSwitch.click();

	const confirm = page.getByRole("dialog", { name: "Enable Cloud?" });
	await expect(confirm).toBeVisible();

	// Stacking check: the confirm must paint above the settings dialog.
	const stacking = await page.evaluate(() => {
		const dialogs = [...document.querySelectorAll<HTMLElement>('[role="dialog"]')];
		const settings = dialogs.find((d) => d.textContent?.includes("Developer mode"));
		const confirmDialog = dialogs.find((d) => d.textContent?.includes("Enable Cloud?"));
		const settingsOverlay = document.querySelector<HTMLElement>('[data-testid="settings-dialog-overlay"]');
		if (!settings || !confirmDialog || !settingsOverlay) return null;
		const confirmAfter = Boolean(
			settings.compareDocumentPosition(confirmDialog) & Node.DOCUMENT_POSITION_FOLLOWING,
		);
		return {
			overlayZ: getComputedStyle(settingsOverlay).zIndex,
			settingsZ: getComputedStyle(settings).zIndex,
			confirmZ: getComputedStyle(confirmDialog).zIndex,
			confirmAfter,
		};
	});
	expect(stacking).not.toBeNull();
	expect(Number(stacking!.overlayZ), JSON.stringify(stacking)).toBeLessThan(Number(stacking!.settingsZ));
	const confirmPaintsAbove =
		Number(stacking!.confirmZ) > Number(stacking!.settingsZ) ||
		(stacking!.confirmZ === stacking!.settingsZ && stacking!.confirmAfter);
	expect(confirmPaintsAbove, JSON.stringify(stacking)).toBe(true);

	// Confirming runs the enable path in the renderer: the dialog closes via
	// onConfirm (the cancel path would leave it open until dismissed). The
	// PATCH/refetch wiring is React Query + apiClient, untouched by this fix
	// and already exercised by the SettingsDialog unit tests.
	await confirm.getByRole("button", { name: "Enable Cloud" }).click();
	await expect(confirm).not.toBeVisible({ timeout: 5_000 });
	await expect(cloudSwitch).toBeEnabled();
});
