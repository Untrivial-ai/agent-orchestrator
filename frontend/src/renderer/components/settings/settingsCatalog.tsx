import { BadgeCheck, Bot, CircleHelp, Cloud, Globe2, Keyboard, RefreshCw, Settings2, Smartphone, type LucideIcon } from "lucide-react";
import { lazy, type ReactNode } from "react";
import type { TFunction } from "i18next";
import type { GlobalSettingsSection } from "../../stores/ui-store";
import { BrowserDownloadsSection } from "./BrowserDownloadsSection";
import { BrowserProfilesSection } from "./BrowserProfilesSection";
import { CloudCredentialsSection } from "./CloudCredentialsSection";
import { CodexAccountsSection } from "./CodexAccountsSection";
import { ConnectMobileContent } from "./ConnectMobileContent";
import { GeneralSettingsSection } from "./GeneralSettingsSection";
import { HarnessSettingsSection } from "./HarnessSettingsSection";
import { KeyboardShortcutsContent } from "./KeyboardShortcutsContent";
import { MobileDevicesSection } from "./MobileDevicesSection";
import { ReportProblemContent } from "./ReportProblemContent";
import { SettingsSection } from "./SettingsSection";

const UpdatesSection = lazy(async () => {
	const module = await import("./UpdatesSection");
	return { default: module.UpdatesSection };
});

type CatalogContext = {
	cloudEnabled: boolean;
};

export type SettingsCatalogItem = {
	id: GlobalSettingsSection;
	icon: LucideIcon;
	label: (t: TFunction) => string;
	visible?: (context: CatalogContext) => boolean;
	render: (t: TFunction, titleHidden: boolean) => ReactNode;
};

function SettingsContentPanel({ children }: { children: ReactNode }) {
	return <div className="rounded-md bg-[var(--color-bg-settings-row)]">{children}</div>;
}

const globalSettingsCatalog: SettingsCatalogItem[] = [
	{
		id: "general",
		icon: Settings2,
		label: (t) => t("settings.general"),
		render: (_t, titleHidden) => <GeneralSettingsSection titleHidden={titleHidden} />,
	},
	{
		id: "harness",
		icon: Bot,
		label: (t) => t("settings.harness"),
		render: (_t, titleHidden) => <HarnessSettingsSection titleHidden={titleHidden} />,
	},
	{
		id: "agents",
		icon: BadgeCheck,
		label: (t) => t("settings.agents"),
		render: (_t, titleHidden) => <CodexAccountsSection titleHidden={titleHidden} />,
	},
	{
		id: "browserProfiles",
		icon: Globe2,
		label: (t) => t("settings.browserProfiles"),
		render: (_t, titleHidden) => (
			<>
				<BrowserProfilesSection titleHidden={titleHidden} />
				<div className="border-t border-border/60 pt-5">
					<BrowserDownloadsSection />
				</div>
			</>
		),
	},
	{
		id: "cloud",
		icon: Cloud,
		label: (t) => t("settings.cloud"),
		visible: ({ cloudEnabled }) => cloudEnabled,
		render: (_t, titleHidden) => <CloudCredentialsSection titleHidden={titleHidden} />,
	},
	{
		id: "mobile",
		icon: Smartphone,
		label: (t) => t("settings.mobile"),
		render: (t, titleHidden) => (
			<SettingsSection titleHidden={titleHidden} title={t("settings.mobile")}>
				<div className="rounded-md bg-[var(--color-bg-settings-row)] pb-4 pt-0">
					<ConnectMobileContent active />
					<MobileDevicesSection />
				</div>
			</SettingsSection>
		),
	},
	{
		id: "shortcuts",
		icon: Keyboard,
		label: (t) => t("settings.shortcuts"),
		render: (t, titleHidden) => (
			<SettingsSection titleHidden={titleHidden} title={t("settings.keyboardShortcuts")}>
				<SettingsContentPanel><KeyboardShortcutsContent active /></SettingsContentPanel>
			</SettingsSection>
		),
	},
	{
		id: "updates",
		icon: RefreshCw,
		label: (t) => t("settings.updates"),
		render: (_t, titleHidden) => <UpdatesSection titleHidden={titleHidden} />,
	},
	{
		id: "help",
		icon: CircleHelp,
		label: (t) => t("settings.help"),
		render: (t, titleHidden) => (
			<SettingsSection titleHidden={titleHidden} title={t("settings.reportProblem")}>
				<SettingsContentPanel><ReportProblemContent active /></SettingsContentPanel>
			</SettingsSection>
		),
	},
];

export function visibleGlobalSettings(context: CatalogContext): SettingsCatalogItem[] {
	return globalSettingsCatalog.filter((item) => item.visible?.(context) ?? true);
}

export function globalSettingsItem(section: GlobalSettingsSection, context: CatalogContext): SettingsCatalogItem {
	return visibleGlobalSettings(context).find((item) => item.id === section) ?? globalSettingsCatalog[0];
}

export function globalSettingsItemsFor(section: GlobalSettingsSection | "all", context: CatalogContext): SettingsCatalogItem[] {
	return section === "all" ? visibleGlobalSettings(context) : [globalSettingsItem(section, context)];
}
