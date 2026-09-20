import { Feather } from "./icons";
import { useState } from "react";
import { ActivityIndicator, FlatList, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import type { AgentModelCatalog } from "./api";
import { haptics } from "./haptics";
import type { Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { SHEET_SCROLL_CONTENT, SheetHeader } from "./ui";
import { iconSize, press, space, type } from "./tokens";

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
		ListHeaderComponent={<><SheetHeader title="Model" subtitle="Choose how this agent runs the task." right={<Pressable accessibilityRole="button" accessibilityLabel="Refresh model list" disabled={refreshing} onPress={() => { haptics.tap(); onRefresh(); }} style={styles.refresh}>{refreshing ? <ActivityIndicator size="small" color={t.accent} /> : <><Feather name="refresh-cw" size={iconSize.xs} color={t.accent} /><Text style={styles.refreshText}>Refresh</Text></>}</Pressable>} />{error || catalog?.warning ? <Text style={styles.error}>{error || catalog?.warning}</Text> : null}{loading ? <ActivityIndicator color={t.accent} style={{ marginVertical: space.xxl }} /> : null}</>}
		renderItem={({ item }) => <Pressable accessibilityRole="radio" accessibilityState={{ selected: selected === item.id }} onPress={() => choose(item.id)} style={({ pressed }) => [styles.option, pressed && { opacity: press.opacity }]}><View style={{ flex: 1 }}><Text style={[styles.label, selected === item.id && { color: t.accent }]}>{item.label}</Text>{item.id === "" ? <Text style={styles.hint}>Let the agent choose</Text> : item.isDefault ? <Text style={styles.hint}>Agent default</Text> : null}</View>{selected === item.id ? <Feather name="check" size={iconSize.md} color={t.accent} /> : null}</Pressable>}
		ListFooterComponent={catalog && (catalog.selectionMode === "text" || catalog.allowCustom) ? <View style={styles.custom}><Text style={styles.customLabel}>CUSTOM MODEL</Text><View style={styles.customRow}><TextInput value={custom} onChangeText={setCustom} placeholder="Model identifier" placeholderTextColor={t.textFaint} autoCapitalize="none" autoCorrect={false} style={styles.input} /><Pressable disabled={!custom.trim()} onPress={() => choose(custom.trim())} style={[styles.use, !custom.trim() && { opacity: 0.4 }]}><Text style={styles.useText}>Use</Text></Pressable></View></View> : null}
	/>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	list: { flex: 1, backgroundColor: t.bgSurface },
	refresh: { flexDirection: "row", alignItems: "center", gap: space.xxs }, refreshText: { fontFamily: "Geist_600SemiBold", color: t.accent, fontSize: type.footnote.fontSize, fontWeight: "600" },
	error: { fontFamily: "Geist_400Regular", color: t.amber, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, marginBottom: space.sm },
	option: { minHeight: 54, flexDirection: "row", alignItems: "center", gap: space.sm, paddingVertical: space.sm, paddingHorizontal: space.hair },
	label: { fontFamily: "Geist_500Medium", color: t.textPrimary, fontSize: type.subheadline.fontSize, fontWeight: "500" }, hint: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.caption2.fontSize, marginTop: space.hair },
	custom: { marginTop: space.md, paddingTop: space.md, borderTopWidth: 1, borderTopColor: t.borderSubtle }, customLabel: { fontFamily: "Geist_600SemiBold", color: t.textTertiary, fontSize: type.caption2.fontSize, fontWeight: "600", letterSpacing: 1, marginBottom: space.sm },
	customRow: { flexDirection: "row", gap: space.sm }, input: { flex: 1, minHeight: 44, borderWidth: 1, borderColor: t.borderDefault, backgroundColor: t.bgElevated, borderRadius: 8, borderCurve: "continuous", color: t.textPrimary, paddingHorizontal: space.md },
	use: { minWidth: 58, alignItems: "center", justifyContent: "center", borderRadius: 8, borderCurve: "continuous", backgroundColor: t.accent }, useText: { fontFamily: "Geist_600SemiBold", color: t.onAccent, fontWeight: "600" },
});
