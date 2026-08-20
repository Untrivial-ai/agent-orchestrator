import { beforeEach, describe, expect, it, vi } from "vitest";

const getSettings = vi.fn();
const setSettings = vi.fn();

vi.mock("../lib/bridge", () => ({
	aoBridge: {
		rpc: {
			getSettings: (...args: unknown[]) => getSettings(...args),
			setSettings: (...args: unknown[]) => setSettings(...args),
			getStatus: async () => ({ state: "disconnected" }),
			onStatus: () => () => undefined,
		},
	},
}));

import { useRpcStore } from "./rpc-store";

describe("rpc-store", () => {
	beforeEach(() => {
		getSettings.mockReset();
		setSettings.mockReset();
		getSettings.mockResolvedValue({ enabled: false });
		setSettings.mockImplementation(async (settings: { enabled: boolean }) => settings);
		useRpcStore.setState({ enabled: false, loaded: false, saving: false, saveError: false });
	});

	it("defaults to disabled before load", () => {
		expect(useRpcStore.getState().enabled).toBe(false);
		expect(useRpcStore.getState().loaded).toBe(false);
	});

	it("loads persisted enabled=true from the main process", async () => {
		getSettings.mockResolvedValue({ enabled: true });
		await useRpcStore.getState().load();
		expect(useRpcStore.getState().enabled).toBe(true);
		expect(useRpcStore.getState().loaded).toBe(true);
	});

	it("loads persisted enabled=false from the main process", async () => {
		getSettings.mockResolvedValue({ enabled: false });
		await useRpcStore.getState().load();
		expect(useRpcStore.getState().enabled).toBe(false);
		expect(useRpcStore.getState().loaded).toBe(true);
	});

	it("does not reload after the first successful load", async () => {
		await useRpcStore.getState().load();
		await useRpcStore.getState().load();
		expect(getSettings).toHaveBeenCalledTimes(1);
	});

	it("shares one persisted read across concurrent startup callers", async () => {
		let resolveGet: ((settings: { enabled: true }) => void) | undefined;
		getSettings.mockReturnValue(
			new Promise<{ enabled: true }>((resolve) => {
				resolveGet = resolve;
			}),
		);

		const first = useRpcStore.getState().load();
		const second = useRpcStore.getState().load();
		expect(getSettings).toHaveBeenCalledTimes(1);
		resolveGet?.({ enabled: true });
		await Promise.all([first, second]);

		expect(useRpcStore.getState()).toMatchObject({ enabled: true, loaded: true });
	});

	it("keeps disabled fallback when persisted settings cannot be read", async () => {
		getSettings.mockRejectedValue(new Error("IPC unavailable"));
		await expect(useRpcStore.getState().load()).resolves.toBeUndefined();
		expect(useRpcStore.getState().enabled).toBe(false);
		expect(useRpcStore.getState().loaded).toBe(true);
	});

	it("persists enabled changes and updates store", async () => {
		await useRpcStore.getState().setEnabled(true);
		expect(setSettings).toHaveBeenCalledWith({ enabled: true });
		expect(useRpcStore.getState().enabled).toBe(true);
		expect(useRpcStore.getState().loaded).toBe(true);
	});

	it("exposes saveError when persistence fails", async () => {
		setSettings.mockRejectedValue(new Error("disk full"));
		await expect(useRpcStore.getState().setEnabled(true)).resolves.toBeUndefined();
		expect(useRpcStore.getState()).toMatchObject({
			enabled: false,
			saving: false,
			saveError: true,
		});
	});

	it("does not let a late persisted read overwrite a newer user selection", async () => {
		let resolveGet: ((settings: { enabled: false }) => void) | undefined;
		getSettings.mockReturnValue(
			new Promise<{ enabled: false }>((resolve) => {
				resolveGet = resolve;
			}),
		);

		const loading = useRpcStore.getState().load();
		await useRpcStore.getState().setEnabled(true);
		resolveGet?.({ enabled: false });
		await loading;

		expect(useRpcStore.getState().enabled).toBe(true);
	});
});
