import { useEffect } from "react";
import { useTranslation } from "react-i18next";
import { percentLeft, useProviderAccounts } from "../hooks/useProviderAccounts";

// Tells the user a thing once: the stamp names the limit window, or the move, it was about.
function tellOnce(key: string, stamp: string, title: string, body: string) {
	try {
		if (localStorage.getItem(key) === stamp) return;
		localStorage.setItem(key, stamp);
		new Notification(title, { body });
	} catch {
		// Nowhere to remember it or to show it.
	}
}
// Watches the accounts that asked for it: one notice when little is left, one when a reached limit moved the sessions on.
export function AccountAlertsRuntime() {
	const { t } = useTranslation();
	const catalogue = useProviderAccounts(true, false).data?.accounts;
	const watched = Boolean(catalogue?.some((account) => account.warnAt || account.onLimit));
	const accounts = useProviderAccounts(watched, true).data?.accounts;
	useEffect(() => {
		for (const { id, displayName: name, warnAt, moved, usage } of watched ? accounts ?? [] : []) {
			const general = (usage?.status === "available" ? usage.windows ?? [] : []).filter((window) => !window.scope);
			const lowest = general.sort((first, second) => first.remainingFraction - second.remainingFraction)[0];
			const left = percentLeft(lowest?.remainingFraction ?? 1);
			if (warnAt && lowest && left <= warnAt) {
				tellOnce(`ao.account-low.${id}`, lowest.resetTime ?? "now", t("providerAccounts.lowTitle", { name }), t("providerAccounts.lowBody", { percent: left }));
			}
			if (moved) {
				const to = accounts?.find((other) => other.id === moved.to)?.displayName ?? "";
				tellOnce(`ao.account-moved.${id}`, moved.at, t("providerAccounts.movedTitle", { name }), t("providerAccounts.sessionsMoved", { count: moved.sessions, name: to }));
			}
		}
	}, [accounts, watched, t]);
	return null;
}
