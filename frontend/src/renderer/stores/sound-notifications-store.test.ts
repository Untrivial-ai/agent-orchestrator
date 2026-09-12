import { beforeEach, describe, expect, it, vi } from "vitest";

const getUiSettings = vi.fn();
const setUiSettings = vi.fn();
const chooseSound = vi.fn();
const clearSound = vi.fn();
const previewSound = vi.fn();

vi.mock("../lib/bridge", () => ({
	aoBridge: {
		uiSettings: {
			get: (...args: unknown[]) => getUiSettings(...args),
			set: (...args: unknown[]) => setUiSettings(...args),
		},
		notificationSound: {
			choose: () => chooseSound(),
			clear: () => clearSound(),
			preview: () => previewSound(),
		},
	},
}));

import { useSoundNotificationsStore } from "./sound-notifications-store";

describe("sound-notifications-store", () => {
	beforeEach(() => {
		getUiSettings.mockReset();
		setUiSettings.mockReset();
		chooseSound.mockReset();
		clearSound.mockReset();
		previewSound.mockReset();
		previewSound.mockResolvedValue(undefined);
		getUiSettings.mockResolvedValue({ locale: "en", soundNotificationsEnabled: true });
		setUiSettings.mockImplementation(async (settings: { soundNotificationsEnabled: boolean }) => settings);
		useSoundNotificationsStore.setState({
			enabled: true,
			soundPath: null,
			loaded: false,
			saving: false,
			saveError: false,
			soundError: null,
		});
	});

	it("loads the custom sound path alongside the toggle", async () => {
		getUiSettings.mockResolvedValue({
			locale: "en",
			soundNotificationsEnabled: true,
			notificationSoundPath: "/state/notification-sound/ding.wav",
		});
		await useSoundNotificationsStore.getState().load();
		expect(useSoundNotificationsStore.getState().soundPath).toBe("/state/notification-sound/ding.wav");
	});

	it("adopts the imported sound returned by the picker", async () => {
		chooseSound.mockResolvedValue({
			settings: { locale: "en", soundNotificationsEnabled: true, notificationSoundPath: "/state/x.mp3" },
			error: null,
		});
		await useSoundNotificationsStore.getState().chooseSound();
		expect(useSoundNotificationsStore.getState()).toMatchObject({
			soundPath: "/state/x.mp3",
			saving: false,
			soundError: null,
		});
	});

	it("keeps the current sound when the picker is cancelled", async () => {
		useSoundNotificationsStore.setState({ soundPath: "/state/keep.mp3" });
		chooseSound.mockResolvedValue({ settings: null, error: null });
		await useSoundNotificationsStore.getState().chooseSound();
		expect(useSoundNotificationsStore.getState()).toMatchObject({ soundPath: "/state/keep.mp3", saving: false });
	});

	it("surfaces an import rejection without changing the sound", async () => {
		chooseSound.mockResolvedValue({ settings: null, error: "unsupported_type" });
		await useSoundNotificationsStore.getState().chooseSound();
		expect(useSoundNotificationsStore.getState()).toMatchObject({
			soundPath: null,
			saving: false,
			soundError: "unsupported_type",
		});
	});

	it("clears the custom sound back to the system default", async () => {
		useSoundNotificationsStore.setState({ soundPath: "/state/x.mp3", soundError: "too_large" });
		clearSound.mockResolvedValue({ locale: "en", soundNotificationsEnabled: true, notificationSoundPath: null });
		await useSoundNotificationsStore.getState().clearSound();
		expect(useSoundNotificationsStore.getState()).toMatchObject({ soundPath: null, saving: false, soundError: null });
	});

	it("asks the main process to play a preview", async () => {
		await useSoundNotificationsStore.getState().previewSound();
		expect(previewSound).toHaveBeenCalledTimes(1);
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
