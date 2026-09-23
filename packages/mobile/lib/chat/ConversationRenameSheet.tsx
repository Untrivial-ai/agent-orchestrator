import { useState } from "react";
import { StyleSheet, Text, View } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useThemedStyles } from "../ThemeProvider";
import { SheetHeader } from "../ui";
import { ElicitationAction, ElicitationTextField } from "./elicitation-native-controls";
import { normalizeConversationTitle } from "./conversationMenuModel";
import { space, type } from "../tokens";

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
			setError(cause instanceof Error ? cause.message : "Couldn't rename this conversation.");
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
	screen: { flex: 1, backgroundColor: t.bgSurface, paddingHorizontal: space.xl, paddingTop: space.xl },
	field: { marginTop: space.xl },
	error: { fontFamily: "Geist_400Regular", color: t.red, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, marginTop: space.sm },
	actions: { flexDirection: "row", justifyContent: "flex-end", alignItems: "center", gap: space.xs, marginTop: space.md },
});
