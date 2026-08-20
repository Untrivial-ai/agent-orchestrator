import { create } from "zustand";
import { aoBridge } from "../lib/bridge";
import type { RpcStatus } from "../../shared/rpc";

type RpcState = {
	enabled: boolean;
	status: RpcStatus;
	loaded: boolean;
	saving: boolean;
	saveError: boolean;
	load: () => Promise<void>;
	setEnabled: (enabled: boolean) => Promise<void>;
	setStatus: (status: RpcStatus) => void;
};

let rpcRevision = 0;
let pendingLoad: Promise<void> | undefined;

export const useRpcStore = create<RpcState>((set, get) => ({
	enabled: false,
	status: { state: "disconnected" },
	loaded: false,
	saving: false,
	saveError: false,
	load: async () => {
		if (get().loaded) return;
		if (pendingLoad) return pendingLoad;
		const revisionAtStart = rpcRevision;
		pendingLoad = (async () => {
			let enabled = false;
			try {
				const settings = await aoBridge.rpc.getSettings();
				enabled = settings.enabled;
			} catch {
				// A missing bridge or unreadable setting must not prevent the UI from starting.
			}
			if (revisionAtStart !== rpcRevision) return;
			if (revisionAtStart === rpcRevision) set({ enabled, loaded: true });
		})();
		try {
			await pendingLoad;
		} finally {
			pendingLoad = undefined;
		}
	},
	setEnabled: async (candidate) => {
		const revision = ++rpcRevision;
		set({ saving: true, saveError: false });
		try {
			const settings = await aoBridge.rpc.setSettings({ enabled: candidate });
			if (revision === rpcRevision) set({ enabled: settings.enabled, loaded: true, saving: false });
		} catch {
			if (revision === rpcRevision) set({ saving: false, saveError: true });
		}
	},
	setStatus: (status) => {
		set({ status });
	},
}));

export function useRpcEnabled(): boolean {
	return useRpcStore((state) => state.enabled);
}

export function useRpcConnectionState(): RpcStatus {
	return useRpcStore((state) => state.status);
}
