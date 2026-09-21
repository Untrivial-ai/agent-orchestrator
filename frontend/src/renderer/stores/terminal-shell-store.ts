import { create } from "zustand";
import {
	DEFAULT_TERMINAL_SHELL,
	coerceTerminalShell,
	type TerminalShellPreference,
} from "../../shared/ui-locale";
import { aoBridge } from "../lib/bridge";

type TerminalShellState = {
	preference: TerminalShellPreference;
	loaded: boolean;
	saving: boolean;
	saveError: boolean;
	load: () => Promise<void>;
	setPreference: (preference: TerminalShellPreference) => Promise<void>;
};

let settingRevision = 0;
let pendingLoad: Promise<void> | undefined;

export const useTerminalShellStore = create<TerminalShellState>((set, get) => ({
	preference: DEFAULT_TERMINAL_SHELL,
	loaded: false,
	saving: false,
	saveError: false,
	load: async () => {
		if (get().loaded) return;
		if (pendingLoad) return pendingLoad;
		const revisionAtStart = settingRevision;
		pendingLoad = (async () => {
			let preference = DEFAULT_TERMINAL_SHELL;
			try {
				const settings = await aoBridge.uiSettings.get();
				preference = coerceTerminalShell(settings.terminalShell);
			} catch {
				// A missing bridge or unreadable setting must not prevent terminals from opening.
			}
			if (revisionAtStart === settingRevision) set({ preference, loaded: true });
		})();
		try {
			await pendingLoad;
		} finally {
			pendingLoad = undefined;
		}
	},
	setPreference: async (candidate) => {
		const preference = coerceTerminalShell(candidate);
		const revision = ++settingRevision;
		set({ saving: true, saveError: false });
		try {
			await aoBridge.uiSettings.set({ terminalShell: preference });
			if (revision === settingRevision) set({ preference, loaded: true, saving: false });
		} catch {
			if (revision === settingRevision) set({ saving: false, saveError: true });
		}
	},
}));

export function terminalShellRequestValue(preference: TerminalShellPreference): string {
	if (preference.kind === "custom") return preference.path?.trim() || "auto";
	return preference.kind;
}
