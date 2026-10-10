import { Activity, BadgeCheck, Bot, CircleHelp, Globe2, Keyboard, RefreshCw, Server, Settings2, Smartphone, type LucideIcon } from "lucide-react";
import { lazy, type ReactNode } from "react";
import type { TFunction } from "i18next";
import type { GlobalSettingsSection } from "../../stores/ui-store";
import { BrowserDownloadsSection } from "./BrowserDownloadsSection";
import { BrowserProfilesSection } from "./BrowserProfilesSection";
import { Coder11xSection } from "./Coder11xSection";
import { ProviderAccountsSection } from "./ProviderAccountsSection";
import { ConnectMobileContent } from "./ConnectMobileContent";
import { GeneralSettingsSection } from "./GeneralSettingsSection";
import { HarnessSettingsSection } from "./HarnessSettingsSection";
import { KeyboardShortcutsContent } from "./KeyboardShortcutsContent";
import { MobileDevicesSection } from "./MobileDevicesSection";
import { ReportProblemContent } from "./ReportProblemContent";
import { RemoteHostsSettings } from "./RemoteHostsSettings";
import { SettingsSection } from "./SettingsSection";

/** The memory window's own blocks, loaded only when the page is opened: it
 * samples the machine every two seconds while it is on screen. */
const MemoryDiagnostics = lazy(async () => {
	const module = await import("../SessionMemoryPanel");
	return { default: module.MemoryDiagnostics };
});

const UpdatesSection = lazy(async () => {
	const module = await import("./UpdatesSection");
	return { default: module.UpdatesSection };
});

type CatalogContext = {
	cloudEnabled: boolean;
	developerMode: boolean;
	/** Developer mode with the Diagnostics toggle on — gates the memory and CPU page. */
	diagnostics: boolean;
	/** Signed-in user's email ends with @11x.ai — gates the bring-your-own-Coder page. */
	is11x: boolean;
	focusAgentId?: string;
	hostId?: string;
	harnessView?: "local" | "cloud";
	startLogin?: boolean;
};

export type SettingsCatalogItem = {
	id: GlobalSettingsSection;
	icon: LucideIcon;
	label: (t: TFunction) => string;
	visible?: (context: CatalogContext) => boolean;
	/** Left out of the single-page "all" view; it has its own page in the nav.
	 * Diagnostics is a live monitor, not a preference: rendering it inside the
	 * whole-settings page would sample the machine whenever settings opens. */
	pageOnly?: boolean;
	render: (t: TFunction, titleHidden: boolean, context: CatalogContext) => ReactNode;
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
		render: (_t, titleHidden, { focusAgentId, hostId, harnessView, startLogin }) => <HarnessSettingsSection focusAgentId={focusAgentId} {...(hostId ? { hostId } : {})} {...(harnessView ? { initialView: harnessView } : {})} {...(startLogin ? { startLogin } : {})} titleHidden={titleHidden} />,
	},
	{
		id: "accountManager",
		icon: BadgeCheck,
		label: (t) => t("providerAccounts.title"),
		render: (_t, titleHidden) => <ProviderAccountsSection titleHidden={titleHidden} />,
	},
	{
		id: "browserProfiles",
		icon: Globe2,
		label: (t) => t("settings.browserProfiles"),
		render: (_t, titleHidden) => (
			<>
				<BrowserProfilesSection titleHidden={titleHidden} />
				<BrowserDownloadsSection />
			</>
		),
	},
	{
		id: "coder11x",
		icon: Server,
		label: (t) => t("settings.coder11x.navLabel"),
		visible: ({ is11x }) => is11x,
		render: (_t, titleHidden) => <Coder11xSection titleHidden={titleHidden} />,
	},
	{
		id: "remoteHosts",
		icon: Server,
		label: (t) => t("settings.remoteHosts"),
		visible: ({ developerMode }) => developerMode,
		render: (_t, titleHidden) => <RemoteHostsSettings titleHidden={titleHidden} />,
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
		id: "diagnostics",
		icon: Activity,
		label: (t) => t("settings.diagnostics"),
		pageOnly: true,
		visible: ({ diagnostics }) => diagnostics,
		render: (t, titleHidden) => (
			<SettingsSection titleHidden={titleHidden} title={t("settings.diagnostics")}>
				<MemoryDiagnostics />
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
	return section === "all"
		? visibleGlobalSettings(context).filter((item) => !item.pageOnly)
		: [globalSettingsItem(section, context)];
}
