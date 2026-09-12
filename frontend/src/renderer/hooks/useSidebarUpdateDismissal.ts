import { useCallback, useEffect, useState } from "react";

export const SIDEBAR_UPDATE_DISMISSAL_STORAGE_KEY = "ao.sidebar.dismissed-update";
export const SIDEBAR_UPDATE_DISMISSAL_MS = 24 * 60 * 60 * 1000;

type DismissedSidebarUpdate = {
	version: string;
	dismissedUntil: number;
};

function removeDismissal(): void {
	try {
		localStorage.removeItem(SIDEBAR_UPDATE_DISMISSAL_STORAGE_KEY);
	} catch {
		// Storage failures must fail open so an update remains visible.
	}
}

function readDismissal(): DismissedSidebarUpdate | null {
	try {
		const raw = localStorage.getItem(SIDEBAR_UPDATE_DISMISSAL_STORAGE_KEY);
		if (!raw) return null;
		const value = JSON.parse(raw) as Partial<DismissedSidebarUpdate>;
		if (
			typeof value.version !== "string" ||
			value.version.length === 0 ||
			typeof value.dismissedUntil !== "number" ||
			!Number.isFinite(value.dismissedUntil)
		) {
			removeDismissal();
			return null;
		}
		if (value.dismissedUntil <= Date.now()) {
			removeDismissal();
			return null;
		}
		return { version: value.version, dismissedUntil: value.dismissedUntil };
	} catch {
		removeDismissal();
		return null;
	}
}

export function useSidebarUpdateDismissal(version: string | undefined) {
	const [dismissal, setDismissal] = useState(readDismissal);
	const dismissed =
		version !== undefined &&
		dismissal?.version === version &&
		dismissal.dismissedUntil > Date.now();

	useEffect(() => {
		if (!dismissed || !dismissal) return;
		const timer = window.setTimeout(() => {
			removeDismissal();
			setDismissal(null);
		}, dismissal.dismissedUntil - Date.now());
		return () => window.clearTimeout(timer);
	}, [dismissal, dismissed]);

	const dismiss = useCallback(() => {
		if (!version) return;
		const next = {
			version,
			dismissedUntil: Date.now() + SIDEBAR_UPDATE_DISMISSAL_MS,
		};
		try {
			localStorage.setItem(SIDEBAR_UPDATE_DISMISSAL_STORAGE_KEY, JSON.stringify(next));
			setDismissal(next);
		} catch {
			setDismissal(null);
		}
	}, [version]);

	return { dismissed, dismiss };
}
