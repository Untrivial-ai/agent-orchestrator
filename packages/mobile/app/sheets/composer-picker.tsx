import { useLocalSearchParams, useRouter } from "expo-router";
import { useEffect } from "react";
import { ComposerPickerSheet } from "../../lib/chat/ComposerPickerSheet";
import { readChatSheet, releaseChatSheet } from "../../lib/chat/chatSheetRegistry";
import { useSheetEntryPresent } from "../../lib/chat/useSheetEntryPresent";
import { backOr } from "../../lib/backNavigation";

export default function ComposerPickerRoute() {
	const router = useRouter(); const { sheetKey } = useLocalSearchParams<{ sheetKey?: string }>(); const entry = readChatSheet(sheetKey);
	useEffect(() => () => releaseChatSheet(sheetKey), [sheetKey]);
	// Dismiss rather than draw an empty sheet when the hand-off is gone.
	useSheetEntryPresent(entry?.kind === "composer-picker");
	if (entry?.kind !== "composer-picker") return null;
	return <ComposerPickerSheet catalog={entry.catalog} initialQuery={entry.initialQuery} truncated={entry.truncated} onSelect={(value) => { backOr(router); entry.onSelect(value); }} />;
}

export { SheetErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
