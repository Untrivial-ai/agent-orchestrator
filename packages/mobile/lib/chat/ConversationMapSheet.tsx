import { Feather } from "@expo/vector-icons";
import { FlatList, Pressable, StyleSheet, Text, View } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { SheetHeader } from "../ui";
import { conversationMarkerPresentation } from "./chatPresentation";
import type { ConversationMarker } from "./timelineModel";

export function ConversationMapSheet({ markers, onSelect }: { markers: ConversationMarker[]; onSelect(sequence: number): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <FlatList
		style={styles.list}
		contentContainerStyle={styles.content}
		data={markers}
		keyExtractor={(item) => item.key}
		ListHeaderComponent={<>
			<View style={styles.header}><SheetHeader title="Conversation history" subtitle={markers.length ? `${markers.length} ${markers.length === 1 ? "exchange" : "exchanges"}` : "Jump between the important moments in this conversation."} /></View>
			{markers.length ? <View style={styles.sectionHeader}><Text style={styles.sectionTitle}>History</Text><View style={styles.sectionRule} /></View> : null}
		</>}
		ListEmptyComponent={<View style={styles.empty}><View style={styles.emptyIcon}><Feather name="message-circle" size={20} color={t.textTertiary} /></View><Text style={styles.emptyTitle}>No exchanges yet</Text><Text style={styles.emptyCopy}>Messages and completed work will appear here as the conversation grows.</Text></View>}
		renderItem={({ item }) => {
			const presentation = conversationMarkerPresentation(item.state);
			const color = presentation.tone === "working" ? t.orange : presentation.tone === "success" ? t.green : presentation.tone === "danger" ? t.red : presentation.tone === "accent" ? t.blue : t.textTertiary;
			return <Pressable
				accessibilityRole="button"
				accessibilityLabel={`Jump to ${item.title}, ${presentation.label}`}
				onPress={() => { haptics.select(); onSelect(item.sequence); }}
				style={({ pressed }) => [styles.row, pressed && styles.pressed]}
			>
				<View style={styles.eyebrow}>
					<Feather name={presentation.icon} size={12} color={color} />
					<Text style={[styles.state, { color }]}>{presentation.label}</Text>
					<View style={styles.spacer} />
					<Feather name="chevron-right" size={16} color={t.textFaint} />
				</View>
				<Text numberOfLines={2} style={styles.title}>{item.title}</Text>
				{item.detail ? <Text numberOfLines={2} style={styles.detail}>{item.detail}</Text> : null}
			</Pressable>;
		}}
	/>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	list: { flex: 1, backgroundColor: t.bgBase },
	content: { paddingTop: 22, paddingBottom: 24 },
	header: { paddingHorizontal: 20 },
	sectionHeader: { flexDirection: "row", alignItems: "center", gap: 12, paddingHorizontal: 20, paddingTop: 22, paddingBottom: 5 },
	sectionTitle: { color: t.textTertiary, fontSize: 12, lineHeight: 16, fontWeight: "600" },
	sectionRule: { flex: 1, height: StyleSheet.hairlineWidth, backgroundColor: t.borderSubtle },
	row: { minHeight: 76, paddingHorizontal: 20, paddingVertical: 10, gap: 3, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: t.borderSubtle },
	pressed: { backgroundColor: t.bgSubtle },
	eyebrow: { minHeight: 17, flexDirection: "row", alignItems: "center", gap: 6 },
	spacer: { flex: 1 },
	state: { fontSize: 12, lineHeight: 16, fontWeight: "500" },
	title: { color: t.textPrimary, fontSize: 16, fontWeight: "600", lineHeight: 21, letterSpacing: -0.15 },
	detail: { color: t.textTertiary, fontSize: 12, lineHeight: 16 },
	empty: { alignItems: "center", paddingHorizontal: 24, paddingVertical: 70 },
	emptyIcon: { width: 44, height: 44, borderRadius: 14, backgroundColor: t.bgSubtle, alignItems: "center", justifyContent: "center", marginBottom: 14 },
	emptyTitle: { color: t.textPrimary, fontSize: 16, fontWeight: "700" },
	emptyCopy: { maxWidth: 270, color: t.textTertiary, fontSize: 13, lineHeight: 18, textAlign: "center", marginTop: 6 },
});
