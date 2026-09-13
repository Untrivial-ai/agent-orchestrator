import { create } from "zustand";

// The cloud sandbox provider a user picks for their new cloud sessions, for a
// control plane that offers more than one. Persisted to localStorage (a
// per-machine client preference, not synced to the control plane): it is only a
// default the session-create request carries, and the control plane validates
// it against what it actually offers. null means "follow the control plane
// default".
const storageKey = "ao.cloud.sandboxProvider";

function getLocalStorage(): Storage | null {
	if (typeof window === "undefined" || !window.localStorage) return null;
	return window.localStorage;
}

/**
 * Reads the persisted provider without a React subscription, for hook-free
 * callers (e.g. the orchestrator launcher). Returns null when unset or
 * unreadable, which the caller treats as "use the control plane default".
 */
export function readSelectedSandboxProvider(): string | null {
	try {
		const value = getLocalStorage()?.getItem(storageKey);
		return value && value !== "" ? value : null;
	} catch {
		return null;
	}
}

function persistSelectedSandboxProvider(provider: string | null): void {
	try {
		const storage = getLocalStorage();
		if (!storage) return;
		if (provider === null) storage.removeItem(storageKey);
		else storage.setItem(storageKey, provider);
	} catch {
		// A blocked or unavailable localStorage must not break provider selection;
		// the choice simply will not survive a reload.
	}
}

type SandboxProviderState = {
	selectedProvider: string | null;
	setSelectedProvider: (provider: string | null) => void;
};

export const useSandboxProviderStore = create<SandboxProviderState>((set) => ({
	selectedProvider: readSelectedSandboxProvider(),
	setSelectedProvider: (provider) => {
		persistSelectedSandboxProvider(provider);
		set({ selectedProvider: provider });
	},
}));
