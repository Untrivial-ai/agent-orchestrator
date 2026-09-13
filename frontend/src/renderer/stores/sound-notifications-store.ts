import { create } from "zustand";
import { aoBridge } from "../lib/bridge";
import type { NotificationSoundImportErrorCode } from "../../shared/notification-sound";

type SoundNotificationsState = {
	enabled: boolean;
	/** Local path of the imported custom sound; `null` means the system beep. */
	soundPath: string | null;
	loaded: boolean;
	saving: boolean;
	saveError: boolean;
	/** Why the last file pick was rejected; cleared by the next successful action. */
	soundError: NotificationSoundImportErrorCode | null;
	load: () => Promise<void>;
	setEnabled: (enabled: boolean) => Promise<void>;
	/** Open the OS file picker and import the chosen audio file as the notification sound. */
	chooseSound: () => Promise<void>;
	/** Drop the custom sound and go back to the system beep. */
	clearSound: () => Promise<void>;
	/** Play whatever a real notification would play right now. */
	previewSound: () => Promise<void>;
};

const DEFAULT_ENABLED = true;

let settingRevision = 0;
let pendingLoad: Promise<void> | undefined;

export const useSoundNotificationsStore = create<SoundNotificationsState>((set, get) => ({
	enabled: DEFAULT_ENABLED,
	soundPath: null,
	loaded: false,
	saving: false,
	saveError: false,
	soundError: null,
	load: async () => {
		if (get().loaded) return;
		if (pendingLoad) return pendingLoad;
		const revisionAtStart = settingRevision;
		pendingLoad = (async () => {
			let enabled = DEFAULT_ENABLED;
			let soundPath: string | null = null;
			try {
				const settings = await aoBridge.uiSettings.get();
				enabled = settings.soundNotificationsEnabled;
				soundPath = settings.notificationSoundPath ?? null;
			} catch {
				// A missing bridge or unreadable setting must not prevent the UI from starting.
			}
			if (revisionAtStart === settingRevision) set({ enabled, soundPath, loaded: true });
		})();
		try {
			await pendingLoad;
		} finally {
			pendingLoad = undefined;
		}
	},
	setEnabled: async (enabled) => {
		const revision = ++settingRevision;
		set({ saving: true, saveError: false });
		try {
			await aoBridge.uiSettings.set({ soundNotificationsEnabled: enabled });
			if (revision === settingRevision) set({ enabled, loaded: true, saving: false });
		} catch {
			if (revision === settingRevision) set({ saving: false, saveError: true });
		}
	},
	chooseSound: async () => {
		const revision = ++settingRevision;
		set({ saving: true, saveError: false, soundError: null });
		try {
			const result = await aoBridge.notificationSound.choose();
			if (revision !== settingRevision) return;
			if (result.error) {
				set({ saving: false, soundError: result.error });
				return;
			}
			// A cancelled picker returns neither settings nor an error: keep what we had.
			set(result.settings ? { saving: false, soundPath: result.settings.notificationSoundPath, loaded: true } : { saving: false });
		} catch {
			if (revision === settingRevision) set({ saving: false, saveError: true });
		}
	},
	clearSound: async () => {
		const revision = ++settingRevision;
		set({ saving: true, saveError: false, soundError: null });
		try {
			const settings = await aoBridge.notificationSound.clear();
			if (revision === settingRevision) set({ saving: false, soundPath: settings.notificationSoundPath, loaded: true });
		} catch {
			if (revision === settingRevision) set({ saving: false, saveError: true });
		}
	},
	previewSound: async () => {
		try {
			await aoBridge.notificationSound.preview();
		} catch {
			// Preview is best-effort; a failed test play is not worth an error state.
		}
	},
}));
