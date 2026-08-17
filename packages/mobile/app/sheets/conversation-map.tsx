import { useLocalSearchParams, useRouter } from "expo-router";
import { useEffect } from "react";
import { ConversationMapSheet } from "../../lib/chat/ConversationMapSheet";
import { readChatSheet, releaseChatSheet } from "../../lib/chat/chatSheetRegistry";

export default function ConversationMapRoute() {
	const router = useRouter(); const { sheetKey } = useLocalSearchParams<{ sheetKey?: string }>(); const entry = readChatSheet(sheetKey);
	useEffect(() => () => releaseChatSheet(sheetKey), [sheetKey]);
	if (entry?.kind !== "conversation-map") return null;
	return <ConversationMapSheet markers={entry.markers} onSelect={(sequence) => { router.back(); entry.onSelect(sequence); }} />;
}
