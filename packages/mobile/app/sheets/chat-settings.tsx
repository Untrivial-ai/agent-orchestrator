import { useLocalSearchParams, useRouter } from "expo-router";
import { useEffect, useState } from "react";
import { ChatSettingsSheet } from "../../lib/chat/ChatSettingsModal";
import { readChatSheet, releaseChatSheet } from "../../lib/chat/chatSheetRegistry";
import { runSheetMutation } from "../../lib/chat/sheetMutation";

export default function ChatSettingsRoute() {
	const router = useRouter();
	const { sheetKey } = useLocalSearchParams<{ sheetKey?: string }>();
	const entry = readChatSheet(sheetKey);
	useEffect(() => () => releaseChatSheet(sheetKey), [sheetKey]);
	const [snapshot, setSnapshot] = useState(entry?.kind === "turn-settings" ? entry.snapshot : undefined);
	const [models, setModels] = useState(entry?.kind === "turn-settings" ? entry.models : []);
	const [options, setOptions] = useState(entry?.kind === "turn-settings" ? entry.options : []);
	const [error, setError] = useState(entry?.kind === "turn-settings" ? entry.error : undefined);
	const [saving, setSaving] = useState(false);
	const [refreshing, setRefreshing] = useState(false);
	if (entry?.kind !== "turn-settings" || !snapshot) return <Unavailable onClose={() => router.back()} />;
	const changeSettings = async (settings: typeof snapshot.settings) => {
		if (saving || refreshing) return;
		setSaving(true);
		setError(undefined);
		await runSheetMutation(
			() => entry.onSettings(settings),
			() => setSnapshot((current) => current ? { ...current, settings } : current),
			setError,
		);
		setSaving(false);
	};
	const changeOption = async (id: string, value: { value: string } | { enabled: boolean }) => {
		if (saving || refreshing) return;
		setSaving(true);
		setError(undefined);
		await runSheetMutation(() => entry.onOption(id, value), setOptions, setError);
		setSaving(false);
	};
	const refresh = async () => {
		if (saving || refreshing) return;
		setRefreshing(true);
		setError(undefined);
		await runSheetMutation(entry.onRefresh, (catalog) => {
			setModels(catalog.models);
			setOptions(catalog.configOptions);
		}, setError);
		setRefreshing(false);
	};
	return <ChatSettingsSheet snapshot={snapshot} models={models} options={options} disabled={entry.disabled || saving || refreshing} refreshing={refreshing} error={error} onRefresh={() => { void refresh(); }} onSettings={(settings) => { void changeSettings(settings); }} onOption={(id, value) => { void changeOption(id, value); }} />;
}

function Unavailable({ onClose }: { onClose(): void }) {
	useEffect(() => { const timer = setTimeout(onClose, 0); return () => clearTimeout(timer); }, [onClose]);
	return null;
}
