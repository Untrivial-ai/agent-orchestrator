import { useState } from "react";
import { StyleSheet, Text, View } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useThemedStyles } from "../ThemeProvider";
import { SheetHeader } from "../ui";
import { ElicitationAction, ElicitationTextField } from "./elicitation-native-controls";
import { normalizeConversationTitle } from "./conversationMenuModel";

export function ConversationRenameSheet({
	initialTitle,
	onClose,
	onRename,
}: {
	initialTitle: string;
	onClose(): void;
	onRename(title: string): Promise<void> | void;
}) {
	const styles = useThemedStyles(makeStyles);
	const [title, setTitle] = useState(initialTitle);
	const [saving, setSaving] = useState(false);
	const [error, setError] = useState<string>();
	const normalizedTitle = normalizeConversationTitle(title);

	const save = async () => {
		if (!normalizedTitle || saving) return;
		setSaving(true);
		setError(undefined);
		try {
			await onRename(normalizedTitle);
			haptics.success();
			onClose();
		} catch (cause) {
			haptics.error();
			setError(cause instanceof Error ? cause.message : "Could not rename this conversation.");
			setSaving(false);
		}
	};

	return <View style={styles.screen}>
		<SheetHeader title="Rename conversation" subtitle="Use a short name that makes this worker easy to find." />
		<View style={styles.field}>
			<ElicitationTextField value={title} label="Conversation title" autoFocus maxLength={100} onChange={(value) => setTitle(String(value))} />
		</View>
		{error ? <Text accessibilityRole="alert" style={styles.error}>{error}</Text> : null}
		<View style={styles.actions}>
			<ElicitationAction label="Cancel" onPress={onClose} />
			<ElicitationAction label={saving ? "Saving…" : "Save"} primary disabled={!normalizedTitle || saving} width={82} onPress={() => void save()} />
		</View>
	</View>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgSurface, paddingHorizontal: 20, paddingTop: 22 },
	field: { marginTop: 22 },
	error: { color: t.red, fontSize: 12, lineHeight: 17, marginTop: 8 },
	actions: { flexDirection: "row", justifyContent: "flex-end", alignItems: "center", gap: 6, marginTop: 12 },
});
