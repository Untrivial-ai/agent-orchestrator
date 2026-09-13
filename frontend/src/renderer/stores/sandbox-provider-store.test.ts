import { beforeEach, describe, expect, it } from "vitest";
import { readSelectedSandboxProvider, useSandboxProviderStore } from "./sandbox-provider-store";

const storageKey = "ao.cloud.sandboxProvider";

describe("sandbox-provider-store", () => {
	beforeEach(() => {
		window.localStorage.clear();
		useSandboxProviderStore.setState({ selectedProvider: null });
	});

	it("defaults to null (follow the control plane default)", () => {
		expect(useSandboxProviderStore.getState().selectedProvider).toBeNull();
		expect(readSelectedSandboxProvider()).toBeNull();
	});

	it("persists a chosen provider to localStorage and state", () => {
		useSandboxProviderStore.getState().setSelectedProvider("coder");
		expect(useSandboxProviderStore.getState().selectedProvider).toBe("coder");
		expect(window.localStorage.getItem(storageKey)).toBe("coder");
		// Hook-free readers see the same value.
		expect(readSelectedSandboxProvider()).toBe("coder");
	});

	it("clears the preference when set back to null", () => {
		useSandboxProviderStore.getState().setSelectedProvider("nodeops");
		useSandboxProviderStore.getState().setSelectedProvider(null);
		expect(useSandboxProviderStore.getState().selectedProvider).toBeNull();
		expect(window.localStorage.getItem(storageKey)).toBeNull();
		expect(readSelectedSandboxProvider()).toBeNull();
	});

	it("reads a previously persisted value", () => {
		window.localStorage.setItem(storageKey, "docker");
		expect(readSelectedSandboxProvider()).toBe("docker");
	});
});
