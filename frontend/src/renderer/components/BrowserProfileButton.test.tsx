import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AoBridge } from "../../preload";
import type { BrowserProfileViewState } from "../../shared/browser-profiles";
import { useUiStore } from "../stores/ui-store";
import { BrowserProfileButton } from "./BrowserProfileButton";

describe("BrowserProfileButton", () => {
	let originalShowMenu: AoBridge["browser"]["showProfileMenu"];
	let originalOnManage: AoBridge["browser"]["onProfileManage"];

	afterEach(() => {
		if (window.ao) {
			window.ao.browser.showProfileMenu = originalShowMenu;
			window.ao.browser.onProfileManage = originalOnManage;
		}
		useUiStore.getState().closeSettings();
	});

	it("opens a native menu with the compact profile label and translated confirmation labels", async () => {
		const original = window.ao!;
		originalShowMenu = original.browser.showProfileMenu;
		originalOnManage = original.browser.onProfileManage;
		const showProfileMenu = vi.fn(async () => undefined);
		original.browser.showProfileMenu = showProfileMenu;
		const profileState: BrowserProfileViewState = {
			viewId: "1:worker-1",
			profileId: "11111111-1111-4111-8111-111111111111",
			profileName: "Work",
			temporary: false,
		};

		render(<BrowserProfileButton profileState={profileState} viewId="1:worker-1" />);
		const button = screen.getByRole("button", { name: "Browser profile: Work" });
		expect(button).toHaveClass("browser-profile-button");
		expect(button).toHaveAttribute("title", "Work");
		expect(screen.getByText("Work")).toHaveClass("browser-profile-button__label");
		await userEvent.click(button);

		expect(showProfileMenu).toHaveBeenCalledWith(
			expect.objectContaining({
				viewId: "1:worker-1",
				bounds: expect.objectContaining({ width: 0, height: 0 }),
				labels: expect.objectContaining({ temporary: "Temporary", cancel: "No", confirm: "Yes" }),
			}),
		);
	});

	it("opens Browser settings from the native menu's manage action", () => {
		const original = window.ao!;
		originalShowMenu = original.browser.showProfileMenu;
		originalOnManage = original.browser.onProfileManage;
		const listeners = new Set<(viewId: string) => void>();
		original.browser.onProfileManage = (listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		};

		render(
			<BrowserProfileButton
				profileState={{ viewId: "1:worker-1", profileId: null, temporary: true }}
				viewId="1:worker-1"
			/>,
		);
		act(() => listeners.forEach((listener) => listener("1:worker-1")));

		expect(useUiStore.getState().settingsModal).toEqual({ scope: "global", section: "browserProfiles" });
	});
});
