import { Feather } from "../icons";
import { FlatList, Pressable, StyleSheet, Text, View } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { SheetHeader } from "../ui";
import { conversationMarkerPresentation } from "./chatPresentation";
import type { ConversationMarker } from "./timelineModel";
import { iconSize, space, type } from "../tokens";

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
		ListEmptyComponent={<View style={styles.empty}><View style={styles.emptyIcon}><Feather name="message-circle" size={iconSize.lg} color={t.textTertiary} /></View><Text style={styles.emptyTitle}>No exchanges yet</Text><Text style={styles.emptyCopy}>Messages and completed work will appear here as the conversation grows.</Text></View>}
		renderItem={({ item }) => {
			const presentation = conversationMarkerPresentation(item.state);
			const color = presentation.tone === "working" ? t.orange : presentation.tone === "success" ? t.green : presentation.tone === "danger" ? t.red : presentation.tone === "accent" ? t.accent : t.textTertiary;
			return <Pressable
				accessibilityRole="button"
				accessibilityLabel={`Jump to ${item.title}, ${presentation.label}`}
				onPress={() => { haptics.select(); onSelect(item.sequence); }}
				style={({ pressed }) => [styles.row, pressed && styles.pressed]}
			>
				<View style={styles.eyebrow}>
					<Feather name={presentation.icon} size={iconSize.xs} color={color} />
					<Text style={[styles.state, { color }]}>{presentation.label}</Text>
					<View style={styles.spacer} />
					<Feather name="chevron-right" size={iconSize.sm} color={t.textFaint} />
				</View>
				<Text numberOfLines={2} style={styles.title}>{item.title}</Text>
				{item.detail ? <Text numberOfLines={2} style={styles.detail}>{item.detail}</Text> : null}
			</Pressable>;
		}}
	/>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	list: { flex: 1, backgroundColor: t.bgBase },
	content: { paddingTop: space.xl, paddingBottom: space.xxl },
	header: { paddingHorizontal: space.xl },
	sectionHeader: { flexDirection: "row", alignItems: "center", gap: space.md, paddingHorizontal: space.xl, paddingTop: space.xl, paddingBottom: space.xxs },
	sectionTitle: { fontFamily: "Geist_600SemiBold", color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "600" },
	sectionRule: { flex: 1, height: StyleSheet.hairlineWidth, backgroundColor: t.borderSubtle },
	row: { minHeight: 76, paddingHorizontal: space.xl, paddingVertical: space.sm, gap: space.hair, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: t.borderSubtle },
	pressed: { backgroundColor: t.bgSubtle },
	eyebrow: { minHeight: 17, flexDirection: "row", alignItems: "center", gap: space.xs },
	spacer: { flex: 1 },
	state: { fontFamily: "Geist_500Medium", fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "500" },
	title: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.callout.fontSize, fontWeight: "600", lineHeight: type.callout.lineHeight, letterSpacing: -0.15 },
	detail: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight },
	empty: { alignItems: "center", paddingHorizontal: space.xxl, paddingVertical: 70 },
	emptyIcon: { width: 44, height: 44, borderRadius: 12, borderCurve: "continuous", backgroundColor: t.bgSubtle, alignItems: "center", justifyContent: "center", marginBottom: space.md },
	emptyTitle: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.callout.fontSize, fontWeight: "600" },
	emptyCopy: { fontFamily: "Geist_400Regular", maxWidth: 270, color: t.textTertiary, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, textAlign: "center", marginTop: space.xs },
});
