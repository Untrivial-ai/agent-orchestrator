import { useLocalSearchParams, useRouter } from "expo-router";
import { useEffect } from "react";
import { ConversationActionsSheet } from "../../lib/chat/ConversationActionsSheet";
import { readChatSheet, releaseChatSheet } from "../../lib/chat/chatSheetRegistry";

export default function ConversationActionsRoute() {
	const router = useRouter();
	const { sheetKey } = useLocalSearchParams<{ sheetKey?: string }>();
	const entry = readChatSheet(sheetKey);
	useEffect(() => () => releaseChatSheet(sheetKey), [sheetKey]);
	if (entry?.kind !== "conversation-actions") return null;
	const closeThen = (action: () => void) => {
		router.back();
		setTimeout(action, 220);
	};
	return <ConversationActionsSheet entry={entry} onAction={closeThen} />;
}

export { SheetErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
