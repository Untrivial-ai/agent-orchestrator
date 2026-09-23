import { useRouter } from "expo-router";
import { useEffect } from "react";

/**
 * Chat sheets are opened through the sheet registry rather than by URL, so a
 * route that resolves without an entry — a deep link, or a hand-off that was
 * already released — has nothing to draw.
 *
 * Rendering nothing there left a blank sheet covering the screen with no way out
 * but a swipe, so these routes dismiss themselves instead. Kept out of
 * `chatSheetRegistry` so that module stays free of navigation imports.
 */
export function useSheetEntryPresent(isPresent: boolean): boolean {
	const router = useRouter();
	useEffect(() => {
		if (isPresent) return;
		const timer = setTimeout(() => {
			if (router.canGoBack()) router.back();
			else router.replace("/");
		}, 0);
		return () => clearTimeout(timer);
	}, [isPresent, router]);
	return isPresent;
}
