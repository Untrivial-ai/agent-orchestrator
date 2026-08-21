import { ChevronDown, UserRound } from "lucide-react";
import { useEffect, useRef } from "react";
import { useTranslation } from "react-i18next";
import type { BrowserProfileViewState } from "../../shared/browser-profiles";
import { useUiStore } from "../stores/ui-store";
import { Button } from "./ui/button";

export function BrowserProfileButton({
	viewId,
	profileState,
}: {
	viewId: string;
	profileState: BrowserProfileViewState;
}) {
	const { t } = useTranslation();
	const buttonRef = useRef<HTMLButtonElement>(null);
	const openGlobalSettings = useUiStore((state) => state.openGlobalSettings);
	const label = profileState.profileName ?? t("browser.profile.temporary");

	useEffect(() => {
		return window.ao?.browser.onProfileManage((managedViewId) => {
			if (managedViewId === viewId) openGlobalSettings("browserProfiles");
		});
	}, [openGlobalSettings, viewId]);

	const openMenu = () => {
		if (!viewId || !window.ao?.browser) return;
		const rect = buttonRef.current?.getBoundingClientRect();
		if (!rect) return;
		void window.ao.browser
			.showProfileMenu({
				viewId,
				bounds: { x: rect.left, y: rect.top, width: rect.width, height: rect.height },
				labels: {
					temporary: t("browser.profile.temporary"),
					manage: t("browser.profile.manage"),
					switchTitle: t("browser.profile.switchTitle"),
					switchMessage: t("browser.profile.switchMessage"),
					switchDetail: t("browser.profile.switchDetail"),
					cancel: t("common.no"),
					confirm: t("common.yes"),
				},
			})
			.catch(() => undefined);
	};

	return (
		<Button
			aria-haspopup="menu"
			aria-label={t("browser.profile.select", { profile: label })}
			className="browser-profile-button max-w-36 min-w-0 gap-1 px-1.5 text-xs"
			onClick={openMenu}
			ref={buttonRef}
			size="sm"
			title={label}
			type="button"
			variant="ghost"
		>
			<UserRound aria-hidden="true" className="size-3.5 shrink-0" />
			<span className="browser-profile-button__label truncate">{label}</span>
			<ChevronDown aria-hidden="true" className="browser-profile-button__chevron size-3 shrink-0 opacity-60" />
		</Button>
	);
}
