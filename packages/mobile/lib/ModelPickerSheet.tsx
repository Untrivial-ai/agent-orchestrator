import { Feather } from "@expo/vector-icons";
import { useState } from "react";
import { ActivityIndicator, FlatList, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import type { AgentModelCatalog } from "./api";
import { haptics } from "./haptics";
import type { Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { SHEET_SCROLL_CONTENT, SheetHeader } from "./ui";

export function ModelPickerSheet({ catalog, selected, loading, refreshing, error, onClose, onSelect, onRefresh }: {
	catalog?: AgentModelCatalog;
	selected: string;
	loading?: boolean;
	refreshing?: boolean;
	error?: string;
	onClose(): void;
	onSelect(value: string): void;
	onRefresh(): void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [custom, setCustom] = useState(selected && !catalog?.models.some((item) => item.id === selected) ? selected : "");
	const rows = [{ id: "", label: "Auto", isDefault: false }, ...(catalog?.models ?? [])];
	const choose = (value: string) => { haptics.select(); onClose(); onSelect(value); };
	return <FlatList
		style={styles.list}
		data={rows}
		keyExtractor={(item) => item.id || "__auto__"}
		contentContainerStyle={SHEET_SCROLL_CONTENT}
		ListHeaderComponent={<><SheetHeader title="Model" subtitle="Choose how this agent runs the task." right={<Pressable accessibilityRole="button" accessibilityLabel="Refresh model list" disabled={refreshing} onPress={() => { haptics.tap(); onRefresh(); }} style={styles.refresh}>{refreshing ? <ActivityIndicator size="small" color={t.blue} /> : <><Feather name="refresh-cw" size={13} color={t.blue} /><Text style={styles.refreshText}>Refresh</Text></>}</Pressable>} />{error || catalog?.warning ? <Text style={styles.error}>{error || catalog?.warning}</Text> : null}{loading ? <ActivityIndicator color={t.blue} style={{ marginVertical: 24 }} /> : null}</>}
		renderItem={({ item }) => <Pressable accessibilityRole="radio" accessibilityState={{ selected: selected === item.id }} onPress={() => choose(item.id)} style={({ pressed }) => [styles.option, pressed && { opacity: 0.6 }]}><View style={{ flex: 1 }}><Text style={[styles.label, selected === item.id && { color: t.blue }]}>{item.label}</Text>{item.id === "" ? <Text style={styles.hint}>Let the agent choose</Text> : item.isDefault ? <Text style={styles.hint}>Agent default</Text> : null}</View>{selected === item.id ? <Feather name="check" size={17} color={t.blue} /> : null}</Pressable>}
		ListFooterComponent={catalog && (catalog.selectionMode === "text" || catalog.allowCustom) ? <View style={styles.custom}><Text style={styles.customLabel}>CUSTOM MODEL</Text><View style={styles.customRow}><TextInput value={custom} onChangeText={setCustom} placeholder="Model identifier" placeholderTextColor={t.textFaint} autoCapitalize="none" autoCorrect={false} style={styles.input} /><Pressable disabled={!custom.trim()} onPress={() => choose(custom.trim())} style={[styles.use, !custom.trim() && { opacity: 0.4 }]}><Text style={styles.useText}>Use</Text></Pressable></View></View> : null}
	/>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	list: { flex: 1, backgroundColor: t.bgSurface },
	refresh: { flexDirection: "row", alignItems: "center", gap: 5 }, refreshText: { color: t.blue, fontSize: 13, fontWeight: "600" },
	error: { color: t.amber, fontSize: 12, lineHeight: 17, marginBottom: 8 },
	option: { minHeight: 54, flexDirection: "row", alignItems: "center", gap: 10, paddingVertical: 10, paddingHorizontal: 2 },
	label: { color: t.textPrimary, fontSize: 15, fontWeight: "500" }, hint: { color: t.textTertiary, fontSize: 11, marginTop: 2 },
	custom: { marginTop: 14, paddingTop: 14, borderTopWidth: 1, borderTopColor: t.borderSubtle }, customLabel: { color: t.textTertiary, fontSize: 10, fontWeight: "700", letterSpacing: 1, marginBottom: 8 },
	customRow: { flexDirection: "row", gap: 8 }, input: { flex: 1, minHeight: 44, borderWidth: 1, borderColor: t.borderDefault, backgroundColor: t.bgElevated, borderRadius: 10, color: t.textPrimary, paddingHorizontal: 12 },
	use: { minWidth: 58, alignItems: "center", justifyContent: "center", borderRadius: 10, backgroundColor: t.blue }, useText: { color: t.onAccent, fontWeight: "700" },
});
