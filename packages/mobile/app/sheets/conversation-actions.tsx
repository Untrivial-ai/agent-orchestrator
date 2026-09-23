import { useLocalSearchParams, useRouter } from "expo-router";
import { useEffect } from "react";
import { ConversationActionsSheet } from "../../lib/chat/ConversationActionsSheet";
import { readChatSheet, releaseChatSheet } from "../../lib/chat/chatSheetRegistry";
import { useSheetEntryPresent } from "../../lib/chat/useSheetEntryPresent";
import { backOr } from "../../lib/backNavigation";

export default function ConversationActionsRoute() {
	const router = useRouter();
	const { sheetKey } = useLocalSearchParams<{ sheetKey?: string }>();
	const entry = readChatSheet(sheetKey);
	useEffect(() => () => releaseChatSheet(sheetKey), [sheetKey]);
	// Dismiss rather than draw an empty sheet when the hand-off is gone.
	useSheetEntryPresent(entry?.kind === "conversation-actions");
	if (entry?.kind !== "conversation-actions") return null;
	const closeThen = (action: () => void) => {
		backOr(router);
		setTimeout(action, 220);
	};
	return <ConversationActionsSheet entry={entry} onAction={closeThen} />;
}

export { SheetErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
