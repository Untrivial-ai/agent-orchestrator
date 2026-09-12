import { useLocalSearchParams, useRouter } from "expo-router";
import { useEffect, useRef } from "react";
import { releaseSheetResult, takeSheetResult } from "../../lib/sheetResult";
import { StoreUpdateSheet } from "../../lib/StoreUpdateSheet";

// The store-update nudge, as a native form sheet.
//
// Both outcomes dismiss before reporting, unlike the theme sheet: taking the
// update leaves the app, and `onClose` is `router.back()`. A swipe counts as
// "Not now" — the sheet shows a grabber, so without that the snooze is only ever
// written by people who tap the button and the nudge returns every launch.
export default function StoreUpdateSheetRoute() {
	const router = useRouter();
	const { resultKey, version, storeConfirmed } = useLocalSearchParams<{ resultKey?: string; version?: string; storeConfirmed?: string }>();

	const decided = useRef(false);

	useEffect(
		() => () => {
			if (!decided.current) takeSheetResult<"update" | "dismiss">(resultKey)?.("dismiss");
			releaseSheetResult(resultKey);
		},
		[resultKey],
	);

	function finish(action: "update" | "dismiss") {
		decided.current = true;
		const handler = takeSheetResult<"update" | "dismiss">(resultKey);
		router.back();
		handler?.(action);
	}

	return (
		<StoreUpdateSheet
			version={version}
			storeConfirmed={storeConfirmed === "1"}
			onUpdate={() => finish("update")}
			onDismiss={() => finish("dismiss")}
		/>
	);
}
