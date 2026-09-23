import { beforeEach, describe, expect, it, vi } from "vitest";

const getUiSettings = vi.fn();
const setUiSettings = vi.fn();

vi.mock("../lib/bridge", () => ({
	aoBridge: {
		uiSettings: {
			get: (...args: unknown[]) => getUiSettings(...args),
			set: (...args: unknown[]) => setUiSettings(...args),
		},
	},
}));

import { useSoundNotificationsStore } from "./sound-notifications-store";

describe("sound-notifications-store", () => {
	beforeEach(() => {
		getUiSettings.mockReset();
		setUiSettings.mockReset();
		getUiSettings.mockResolvedValue({ locale: "en", soundNotificationsEnabled: true });
		setUiSettings.mockImplementation(async (settings: { soundNotificationsEnabled: boolean }) => settings);
		useSoundNotificationsStore.setState({ enabled: true, loaded: false, saving: false, saveError: false });
	});

	it("defaults to enabled before load", () => {
		expect(useSoundNotificationsStore.getState().enabled).toBe(true);
	});

	it("loads the persisted setting from the main process", async () => {
		getUiSettings.mockResolvedValue({ locale: "en", soundNotificationsEnabled: false });
		await useSoundNotificationsStore.getState().load();
		expect(useSoundNotificationsStore.getState()).toMatchObject({ enabled: false, loaded: true });
	});

	it("persists changes", async () => {
		await useSoundNotificationsStore.getState().setEnabled(false);
		expect(setUiSettings).toHaveBeenCalledWith({ soundNotificationsEnabled: false });
		expect(useSoundNotificationsStore.getState()).toMatchObject({ enabled: false, saving: false, saveError: false });
	});

	it("does not reload after the first successful load", async () => {
		await useSoundNotificationsStore.getState().load();
		await useSoundNotificationsStore.getState().load();
		expect(getUiSettings).toHaveBeenCalledTimes(1);
	});

	it("shares one persisted read across concurrent startup callers", async () => {
		let resolveGet: ((settings: { soundNotificationsEnabled: boolean }) => void) | undefined;
		getUiSettings.mockReturnValue(
			new Promise<{ soundNotificationsEnabled: boolean }>((resolve) => {
				resolveGet = resolve;
			}),
		);

		const first = useSoundNotificationsStore.getState().load();
		const second = useSoundNotificationsStore.getState().load();
		expect(getUiSettings).toHaveBeenCalledTimes(1);
		resolveGet?.({ soundNotificationsEnabled: false });
		await Promise.all([first, second]);

		expect(useSoundNotificationsStore.getState()).toMatchObject({ enabled: false, loaded: true });
	});

	it("does not let a late persisted read overwrite a newer user selection", async () => {
		let resolveGet: ((settings: { soundNotificationsEnabled: boolean }) => void) | undefined;
		getUiSettings.mockReturnValue(
			new Promise<{ soundNotificationsEnabled: boolean }>((resolve) => {
				resolveGet = resolve;
			}),
		);

		const loading = useSoundNotificationsStore.getState().load();
		await useSoundNotificationsStore.getState().setEnabled(false);
		resolveGet?.({ soundNotificationsEnabled: true });
		await loading;

		expect(useSoundNotificationsStore.getState().enabled).toBe(false);
	});

	it("keeps the default usable when persisted settings cannot be read", async () => {
		getUiSettings.mockRejectedValue(new Error("IPC unavailable"));
		await expect(useSoundNotificationsStore.getState().load()).resolves.toBeUndefined();
		expect(useSoundNotificationsStore.getState()).toMatchObject({ enabled: true, loaded: true });
	});

	it("keeps the current value and exposes an error when persistence fails", async () => {
		setUiSettings.mockRejectedValue(new Error("disk full"));
		await expect(useSoundNotificationsStore.getState().setEnabled(false)).resolves.toBeUndefined();
		expect(useSoundNotificationsStore.getState()).toMatchObject({
			enabled: true,
			saving: false,
			saveError: true,
		});
	});
});
