import { useLocalSearchParams, useRouter } from "expo-router";
import { useEffect } from "react";
import { ConversationRenameSheet } from "../../lib/chat/ConversationRenameSheet";
import { readChatSheet, releaseChatSheet } from "../../lib/chat/chatSheetRegistry";
import { useSheetEntryPresent } from "../../lib/chat/useSheetEntryPresent";
import { backOr } from "../../lib/backNavigation";

export default function ConversationRenameRoute() {
	const router = useRouter();
	const { sheetKey } = useLocalSearchParams<{ sheetKey?: string }>();
	const entry = readChatSheet(sheetKey);
	useEffect(() => () => releaseChatSheet(sheetKey), [sheetKey]);
	// Dismiss rather than draw an empty sheet when the hand-off is gone.
	useSheetEntryPresent(entry?.kind === "conversation-rename");
	if (entry?.kind !== "conversation-rename") return null;
	return <ConversationRenameSheet initialTitle={entry.initialTitle} onRename={entry.onRename} onClose={() => backOr(router)} />;
}

export { SheetErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
