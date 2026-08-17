import { beforeEach, describe, expect, it, vi } from "vitest";

const store = new Map<string, string>();

vi.mock("@react-native-async-storage/async-storage", () => ({
	default: {
		getItem: vi.fn(async (k: string) => store.get(k) ?? null),
		setItem: vi.fn(async (k: string, v: string) => void store.set(k, v)),
	},
}));

describe("installId", () => {
	beforeEach(() => {
		store.clear();
		vi.resetModules();
	});

	it("returns the same id across repeated reads", async () => {
		const { getInstallId } = await import("./installId");
		const first = await getInstallId();
		const second = await getInstallId();
		expect(first).toBeTruthy();
		expect(second).toBe(first);
	});

	it("persists the id so a later module load reuses it", async () => {
		const { getInstallId } = await import("./installId");
		const first = await getInstallId();

		vi.resetModules();
		const reloaded = await import("./installId");
		expect(await reloaded.getInstallId()).toBe(first);
	});

	it("exposes nothing synchronously until primed", async () => {
		const { cachedInstallId, primeInstallId, getInstallId } = await import("./installId");
		expect(cachedInstallId()).toBeNull();
		await primeInstallId();
		expect(cachedInstallId()).toBe(await getInstallId());
	});
});
