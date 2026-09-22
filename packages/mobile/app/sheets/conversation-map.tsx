import { useLocalSearchParams, useRouter } from "expo-router";
import { useEffect } from "react";
import { ConversationMapSheet } from "../../lib/chat/ConversationMapSheet";
import { readChatSheet, releaseChatSheet } from "../../lib/chat/chatSheetRegistry";
import { useSheetEntryPresent } from "../../lib/chat/useSheetEntryPresent";
import { backOr } from "../../lib/backNavigation";

export default function ConversationMapRoute() {
	const router = useRouter(); const { sheetKey } = useLocalSearchParams<{ sheetKey?: string }>(); const entry = readChatSheet(sheetKey);
	useEffect(() => () => releaseChatSheet(sheetKey), [sheetKey]);
	// Dismiss rather than draw an empty sheet when the hand-off is gone.
	useSheetEntryPresent(entry?.kind === "conversation-map");
	if (entry?.kind !== "conversation-map") return null;
	return <ConversationMapSheet markers={entry.markers} onSelect={(sequence) => { backOr(router); entry.onSelect(sequence); }} />;
}

export { SheetErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
