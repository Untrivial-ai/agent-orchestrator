import { useLocalSearchParams, useRouter } from "expo-router";
import { useEffect } from "react";
import { ComposerPickerSheet } from "../../lib/chat/ComposerPickerSheet";
import { readChatSheet, releaseChatSheet } from "../../lib/chat/chatSheetRegistry";

export default function ComposerPickerRoute() {
	const router = useRouter(); const { sheetKey } = useLocalSearchParams<{ sheetKey?: string }>(); const entry = readChatSheet(sheetKey);
	useEffect(() => () => releaseChatSheet(sheetKey), [sheetKey]);
	if (entry?.kind !== "composer-picker") return null;
	return <ComposerPickerSheet catalog={entry.catalog} initialQuery={entry.initialQuery} truncated={entry.truncated} onSelect={(value) => { router.back(); entry.onSelect(value); }} />;
}
