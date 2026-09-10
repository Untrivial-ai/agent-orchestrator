/** UI locales supported across the Electron main, preload, and renderer boundaries. */
export const APP_LOCALES = ["en", "zh-CN", "ja", "ko", "es", "fr", "de", "pt-BR"] as const;

export type AppLocale = (typeof APP_LOCALES)[number];

export const DEFAULT_LOCALE: AppLocale = "en";

export const TERMINAL_SHELL_KINDS = ["auto", "git-bash", "pwsh", "powershell", "cmd", "custom"] as const;

export type TerminalShellKind = (typeof TERMINAL_SHELL_KINDS)[number];

export interface TerminalShellPreference {
	kind: TerminalShellKind;
	path?: string;
}

export interface UiSettings {
	locale: AppLocale;
	/** Whether attention-worthy notifications (needs input, ready to merge) also play a sound. */
	soundNotificationsEnabled: boolean;
	/** Windows shell used for new standalone terminal panes. */
	terminalShell: TerminalShellPreference;
}

export const DEFAULT_TERMINAL_SHELL: TerminalShellPreference = { kind: "auto" };

export const DEFAULT_UI_SETTINGS: UiSettings = {
	locale: DEFAULT_LOCALE,
	soundNotificationsEnabled: true,
	terminalShell: DEFAULT_TERMINAL_SHELL,
};

/** Normalize an unknown value to a supported UI locale. */
export function coerceLocale(raw: unknown): AppLocale {
	if (typeof raw === "string" && (APP_LOCALES as readonly string[]).includes(raw)) {
		return raw as AppLocale;
	}
	return DEFAULT_LOCALE;
}

export function coerceTerminalShell(raw: unknown): TerminalShellPreference {
	if (typeof raw !== "object" || raw === null) return { ...DEFAULT_TERMINAL_SHELL };
	const record = raw as Record<string, unknown>;
	if (typeof record.kind !== "string" || !(TERMINAL_SHELL_KINDS as readonly string[]).includes(record.kind)) {
		return { ...DEFAULT_TERMINAL_SHELL };
	}
	const kind = record.kind as TerminalShellKind;
	if (kind !== "custom") return { kind };
	const customPath = typeof record.path === "string" ? record.path.trim() : "";
	return customPath ? { kind, path: customPath } : { kind };
}

/** Normalize unknown persisted or IPC data to the supported UI-settings schema. */
export function coerceUiSettings(raw: unknown): UiSettings {
	if (typeof raw !== "object" || raw === null) return { ...DEFAULT_UI_SETTINGS };
	const record = raw as Record<string, unknown>;
	const soundNotificationsEnabled =
		typeof record.soundNotificationsEnabled === "boolean"
			? record.soundNotificationsEnabled
			: DEFAULT_UI_SETTINGS.soundNotificationsEnabled;
	return {
		locale: coerceLocale(record.locale),
		soundNotificationsEnabled,
		terminalShell: coerceTerminalShell(record.terminalShell),
	};
}
