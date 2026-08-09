import { existsSync } from "node:fs";
import path from "node:path";
import { app, Menu, nativeImage, Tray, type MenuItemConstructorOptions } from "electron";
import { catalogFor, type MessageKey, type PluralMessageKey } from "../renderer/i18n/messages";
import type { AppLocale, ThemePreference } from "../shared/ui-locale";
import type { TrayAttentionState, TrayAttentionZone, TrayOpenSessionTarget, TraySessionEntry } from "../shared/tray";
import type { UpdateSettings } from "./update-settings";

const MAX_MENU_SESSIONS = 8;

const zoneRank: Record<TrayAttentionZone, number> = { merge: 0, action: 1 };

const zoneLabelKey: Record<TrayAttentionZone, MessageKey> = { merge: "zone.merge", action: "zone.action" };

function format(template: string, vars: Record<string, string | number>): string {
	return template.replace(/\{\{(\w+)\}\}/g, (match, key: string) => String(vars[key] ?? match));
}

function trayIconPath(): string | undefined {
	const candidate = app.isPackaged
		? path.join(process.resourcesPath, "trayIconTemplate.png")
		: path.join(__dirname, "../../assets/trayIconTemplate.png");
	return existsSync(candidate) ? candidate : undefined;
}

export type TrayController = {
	setState(state: TrayAttentionState): void;
	setLocale(locale: AppLocale): void;
	setThemePreference(preference: ThemePreference): void;
	setUpdateSettings(settings: UpdateSettings): void;
	clear(): void;
	dispose(): void;
};

export type TrayControllerOptions = {
	focusWindow: () => void;
	openSession: (target: TrayOpenSessionTarget) => void;
	openSettings: () => void;
	locale: AppLocale;
	themePreference: ThemePreference;
	onThemeSelect: (preference: ThemePreference) => void;
	updateSettings: UpdateSettings;
	onUpdateChannelSelect: (channel: UpdateSettings["channel"]) => void;
	onUpdateEnabledToggle: (enabled: boolean) => void;
	onCheckForUpdates: () => void;
};

export function createTrayController(options: TrayControllerOptions): TrayController | null {
	const iconPath = trayIconPath();
	if (!iconPath) return null;
	const icon = nativeImage.createFromPath(iconPath);
	if (icon.isEmpty()) return null;
	icon.setTemplateImage(true);

	const tray = new Tray(icon);
	let sessions: TraySessionEntry[] = [];
	let catalog = catalogFor(options.locale);
	let themePreference = options.themePreference;
	let updateSettings = options.updateSettings;

	const t = (key: MessageKey, vars?: Record<string, string | number>): string => {
		const raw = catalog[key] ?? key;
		return vars ? format(raw, vars) : raw;
	};

	const tPlural = (base: PluralMessageKey, count: number): string => {
		const key = (count === 1 ? `${base}_one` : `${base}_other`) as MessageKey;
		return t(key, { count });
	};

	const sessionItem = (entry: TraySessionEntry): MenuItemConstructorOptions => {
		const title = entry.title || t("tray.untitledSession");
		return {
			label: entry.projectName ? `${title}  ·  ${entry.projectName}` : title,
			click: () => options.openSession({ projectId: entry.projectId, sessionId: entry.sessionId }),
		};
	};

	const themeMenu = (): MenuItemConstructorOptions => ({
		label: "Theme",
		submenu: [
			{ label: "System", type: "radio", checked: themePreference === "system", click: () => options.onThemeSelect("system") },
			{ label: "Light", type: "radio", checked: themePreference === "light", click: () => options.onThemeSelect("light") },
			{ label: "Dark", type: "radio", checked: themePreference === "dark", click: () => options.onThemeSelect("dark") },
		],
	});

	const updatesMenu = (): MenuItemConstructorOptions => ({
		label: "Updates",
		submenu: [
			{
				label: "Stable Channel",
				type: "radio",
				checked: updateSettings.channel === "latest",
				click: () => options.onUpdateChannelSelect("latest"),
			},
			{
				label: "Nightly Channel",
				type: "radio",
				checked: updateSettings.channel === "nightly",
				click: () => options.onUpdateChannelSelect("nightly"),
			},
			{ type: "separator" },
			{
				label: "Automatically Check for Updates",
				type: "checkbox",
				checked: updateSettings.enabled,
				click: () => options.onUpdateEnabledToggle(!updateSettings.enabled),
			},
			{ label: "Check for Updates Now", click: () => options.onCheckForUpdates() },
		],
	});

	const render = () => {
		const count = sessions.length;
		tray.setToolTip(count > 0 ? tPlural("tray.attentionTooltip", count) : "Agent Orchestrator");

		const items: MenuItemConstructorOptions[] = [];
		if (count === 0) {
			items.push({ label: t("tray.empty"), enabled: false });
		} else {
			const ordered = [...sessions].sort(
				(a, b) => zoneRank[a.zone] - zoneRank[b.zone] || a.title.localeCompare(b.title),
			);
			const visible = ordered.slice(0, MAX_MENU_SESSIONS);
			const overflow = ordered.slice(MAX_MENU_SESSIONS);

			let lastZone: TrayAttentionZone | null = null;
			for (const entry of visible) {
				if (entry.zone !== lastZone) {
					if (lastZone !== null) items.push({ type: "separator" });
					items.push({ label: t(zoneLabelKey[entry.zone]), enabled: false });
					lastZone = entry.zone;
				}
				items.push(sessionItem(entry));
			}
			if (overflow.length > 0) {
				items.push({ type: "separator" });
				items.push({ label: `${t("tray.more")} (${overflow.length})`, submenu: overflow.map(sessionItem) });
			}
		}
		items.push({ type: "separator" });
		items.push(themeMenu());
		items.push(updatesMenu());
		items.push({ label: "Settings…", click: () => options.openSettings() });
		items.push({ type: "separator" });
		items.push({ label: t("tray.show"), click: () => options.focusWindow() });
		items.push({ label: t("tray.quit"), role: "quit" });
		tray.setContextMenu(Menu.buildFromTemplate(items));
	};

	render();

	return {
		setState(state) {
			sessions = state.sessions ?? [];
			render();
		},
		setLocale(locale) {
			catalog = catalogFor(locale);
			render();
		},
		setThemePreference(preference) {
			themePreference = preference;
			render();
		},
		setUpdateSettings(settings) {
			updateSettings = settings;
			render();
		},
		clear() {
			sessions = [];
			render();
		},
		dispose() {
			tray.destroy();
		},
	};
}
