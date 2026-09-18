import { useLocalSearchParams, useRouter } from "expo-router";
import { useEffect } from "react";
import { ConversationRenameSheet } from "../../lib/chat/ConversationRenameSheet";
import { readChatSheet, releaseChatSheet } from "../../lib/chat/chatSheetRegistry";

export default function ConversationRenameRoute() {
	const router = useRouter();
	const { sheetKey } = useLocalSearchParams<{ sheetKey?: string }>();
	const entry = readChatSheet(sheetKey);
	useEffect(() => () => releaseChatSheet(sheetKey), [sheetKey]);
	if (entry?.kind !== "conversation-rename") return null;
	return <ConversationRenameSheet initialTitle={entry.initialTitle} onRename={entry.onRename} onClose={() => router.back()} />;
}

export { SheetErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
