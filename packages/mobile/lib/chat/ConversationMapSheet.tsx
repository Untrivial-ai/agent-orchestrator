import { Feather } from "@expo/vector-icons";
import { FlatList, Pressable, StyleSheet, Text, View } from "react-native";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { SHEET_SCROLL_CONTENT, SheetHeader } from "../ui";
import { haptics } from "../haptics";
import type { ConversationMarker } from "./timelineModel";

export function ConversationMapSheet({ markers, onSelect }: { markers: ConversationMarker[]; onSelect(sequence: number): void }) {
	const t = useTheme(); const styles = useThemedStyles(makeStyles);
	return <FlatList style={styles.list} contentContainerStyle={SHEET_SCROLL_CONTENT} data={markers} keyExtractor={(item) => item.key} ListHeaderComponent={<SheetHeader title="Conversation map" subtitle={`${markers.length} ${markers.length === 1 ? "exchange" : "exchanges"}`} />} ListEmptyComponent={<Text style={styles.empty}>No conversation entries yet.</Text>} renderItem={({ item }) => <Pressable accessibilityRole="button" accessibilityLabel={`Jump to ${item.title}`} onPress={() => { haptics.select(); onSelect(item.sequence); }} style={({ pressed }) => [styles.row, pressed && { opacity: 0.6 }]}><View style={[styles.dot, item.state === "failed" && { backgroundColor: t.red }, item.state === "running" && { backgroundColor: t.orange }]} /><View style={{ flex: 1 }}><Text numberOfLines={2} style={styles.title}>{item.title}</Text>{item.detail ? <Text numberOfLines={3} style={styles.detail}>{item.detail}</Text> : null}</View>{item.state ? <Text style={styles.state}>{item.state}</Text> : null}<Feather name="chevron-right" size={15} color={t.textFaint} /></Pressable>} />;
}
const makeStyles = (t: Theme) => StyleSheet.create({ list: { flex: 1, backgroundColor: t.bgSurface }, row: { minHeight: 58, flexDirection: "row", alignItems: "center", gap: 10, paddingVertical: 10 }, dot: { width: 7, height: 7, borderRadius: 4, backgroundColor: t.blue }, title: { color: t.textPrimary, fontSize: 14, fontWeight: "600" }, detail: { color: t.textTertiary, fontSize: 11, lineHeight: 15, marginTop: 3 }, state: { color: t.textFaint, fontSize: 9, textTransform: "uppercase" }, empty: { color: t.textTertiary, textAlign: "center", paddingVertical: 30 } });
