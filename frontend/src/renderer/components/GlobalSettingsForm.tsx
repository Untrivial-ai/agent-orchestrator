import { Fragment, Suspense } from "react";
import { useTranslation } from "react-i18next";
import { type GlobalSettingsSection as GlobalSettingsPage, useUiStore } from "../stores/ui-store";
import { globalSettingsItemsFor } from "./settings/settingsCatalog";

export type GlobalSettingsSection = GlobalSettingsPage | "all";

export function GlobalSettingsForm({
	cloudEnabled = true,
	is11x = false,
	focusAgentId,
	hostId,
	harnessView,
	section = "all",
}: {
	cloudEnabled?: boolean;
	is11x?: boolean;
	focusAgentId?: string;
	hostId?: string;
	harnessView?: "local" | "cloud";
	section?: GlobalSettingsSection;
}) {
	const { t } = useTranslation();
	const developerMode = useUiStore((state) => state.developerMode);
	const all = section === "all";
	const context = { cloudEnabled, developerMode, is11x, focusAgentId, hostId, harnessView };
	// One section per page means the dialog header already names it, so a
	// leading in-page heading would just repeat that title.
	const titleHidden = !all;

	return (
		<div
			aria-label={t("settings.title")}
			className="flex w-full flex-col gap-(--size-settings-section-gap)"
			data-testid="settings-page"
		>
			{globalSettingsItemsFor(section, context).map((item) => (
				<Fragment key={item.id}>
					<Suspense fallback={null}>{item.render(t, titleHidden, context)}</Suspense>
				</Fragment>
			))}
		</div>
	);
}
