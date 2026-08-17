import { useTranslation } from "react-i18next";
import type { ThemePreference, ThemeStyle } from "../../lib/theme";
import type { AppLocale } from "../../i18n";
import { useLocaleStore } from "../../stores/locale-store";
import { useUiStore } from "../../stores/ui-store";
import { SettingsOptionMenu, type SettingsOption } from "./SettingsOptionMenu";
import { SettingsLinkRow, SettingsRow } from "./SettingsRow";
import { SettingsSection } from "./SettingsSection";
import { cn } from "../../lib/utils";
import { Switch } from "../ui/switch";
import { useSettings, useUpdateSessionInterface } from "../../hooks/useSettings";
import type { SessionMode } from "../../types/workspace";

/**
 * The default interface for new sessions.
 *
 * Daemon-owned, so `ao spawn` and mobile resolve the same value. Two things this
 * control must be honest about: it only affects sessions created afterwards —
 * a session's interface is fixed when it is born — and chat is limited to the
 * agents that have a structured driver today.
 */
function SessionInterfaceRow() {
	const { t } = useTranslation();
	const { settings, isLoading, error } = useSettings();
	const { update, saving, error: saveError } = useUpdateSessionInterface();
	const interfaceOptions = [
		{
			value: "tui",
			label: t("settings.sessionInterface.terminal"),
		},
		{
			value: "chat",
			label: t("settings.sessionInterface.chat"),
		},
	] satisfies SettingsOption<SessionMode>[];

	const chatAvailable = (settings?.chatHarnesses.length ?? 0) > 0;
	const help = !chatAvailable
		? t("settings.sessionInterface.unavailable")
		: t("settings.sessionInterface.available", { harnesses: settings?.chatHarnesses.join(", ") });

	const note = saveError ?? error ?? help;

	return (
		<div className="flex w-full flex-col">
			<SettingsRow className="rounded-none" label={t("settings.sessionInterface.label")}>
				<SettingsOptionMenu
					aria-label={t("settings.sessionInterface.label")}
					value={settings?.defaultSessionMode ?? "tui"}
					options={interfaceOptions}
					onChange={(mode) => update(mode)}
					disabled={isLoading || saving || !chatAvailable}
				/>
			</SettingsRow>
			{/* Stated rather than implied: this changes what NEW sessions get. An
			    existing session's interface is fixed when it is created, so nothing
			    here can move a session that already exists. */}
			<p
				className={cn(
					"px-3 pt-0 pb-4 text-xs leading-relaxed",
					saveError || error ? "text-destructive" : "text-muted-foreground",
				)}
			>
				{note}
			</p>
		</div>
	);
}

const COLOR_THEME_OPTIONS = [
	{ value: "orchestrate", label: "Orchestrate" },
	{ value: "github", label: "GitHub" },
	{ value: "catppuccin", label: "Catppuccin" },
	{ value: "dracula", label: "Dracula" },
	{ value: "tokyo-night", label: "Tokyo Night" },
	{ value: "rose-pine", label: "Rosé Pine" },
	{ value: "nord", label: "Nord" },
	{ value: "gruvbox", label: "Gruvbox" },
	{ value: "solarized", label: "Solarized" },
] satisfies SettingsOption<ThemeStyle>[];

export function GeneralSettingsSection({
	onConnectMobile,
	titleHidden,
}: {
	onConnectMobile: () => void;
	titleHidden?: boolean;
}) {
	const { t } = useTranslation();
	const themePreference = useUiStore((state) => state.themePreference);
	const setThemePreference = useUiStore((state) => state.setThemePreference);
	const themeStyle = useUiStore((state) => state.themeStyle);
	const setThemeStyle = useUiStore((state) => state.setThemeStyle);
	const locale = useLocaleStore((state) => state.locale);
	const setLocale = useLocaleStore((state) => state.setLocale);
	const localeSaving = useLocaleStore((state) => state.saving);
	const localeSaveError = useLocaleStore((state) => state.saveError);
	const developerMode = useUiStore((state) => state.developerMode);
	const setDeveloperMode = useUiStore((state) => state.setDeveloperMode);

	const themeOptions = [
		{ value: "light", label: t("settings.theme.light") },
		{ value: "dark", label: t("settings.theme.dark") },
		{
			value: "system",
			label: t("settings.theme.system"),
		},
	] satisfies SettingsOption<ThemePreference>[];

	const languageOptions = [
		{ value: "en", label: t("settings.language.en") },
		{ value: "zh-CN", label: t("settings.language.zhCN") },
		{ value: "ja", label: t("settings.language.ja") },
		{ value: "ko", label: t("settings.language.ko") },
		{ value: "es", label: t("settings.language.es") },
		{ value: "fr", label: t("settings.language.fr") },
		{ value: "de", label: t("settings.language.de") },
		{ value: "pt-BR", label: t("settings.language.ptBR") },
	] satisfies SettingsOption<AppLocale>[];

	return (
		<SettingsSection title={t("settings.general")} titleHidden={titleHidden} grouped>
			<SettingsRow label={t("settings.colorTheme")}>
				<SettingsOptionMenu
					aria-label={t("settings.colorTheme")}
					value={themeStyle}
					options={COLOR_THEME_OPTIONS}
					onChange={setThemeStyle}
				/>
			</SettingsRow>
			<SettingsRow label={t("settings.theme")}>
				<SettingsOptionMenu
					aria-label={t("settings.theme")}
					value={themePreference}
					options={themeOptions}
					onChange={setThemePreference}
				/>
			</SettingsRow>
			<SettingsRow label={t("settings.language")}>
				<SettingsOptionMenu
					aria-label={t("settings.language")}
					disabled={localeSaving}
					value={locale}
					options={languageOptions}
					onChange={(next) => {
						void setLocale(next);
					}}
				/>
			</SettingsRow>
			{localeSaveError ? (
				<p role="alert" className="px-3 text-caption leading-4 text-error">
					{t("settings.language.saveFailed")}
				</p>
			) : null}
			<SessionInterfaceRow />
			<SettingsRow label={t("settings.developerMode")}>
				<Switch
					aria-label={t("settings.developerMode")}
					checked={developerMode}
					onCheckedChange={setDeveloperMode}
				/>
			</SettingsRow>
			<SettingsLinkRow label={t("settings.connectMobile")} onClick={onConnectMobile} />
		</SettingsSection>
	);
}
