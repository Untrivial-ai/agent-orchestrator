import { Feather } from "@expo/vector-icons";
import { useMemo, useState } from "react";
import { FlatList, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { SheetHeader } from "../ui";
import { composerSheetContentStyle } from "./chatLayout";
import { rankComposerCatalog, type ComposerPickerCatalog, type RankedSuggestion } from "./composerSuggestions";

export function ComposerPickerSheet({
	catalog,
	initialQuery = "",
	truncated,
	onSelect,
}: {
	catalog: ComposerPickerCatalog;
	initialQuery?: string;
	truncated?: boolean;
	onSelect(value: string): void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [query, setQuery] = useState(initialQuery);
	const choices = useMemo(() => rankComposerCatalog(catalog, query), [catalog, query]);
	const kind = catalog.kind;

	return (
		<FlatList
			style={styles.screen}
			contentContainerStyle={composerSheetContentStyle}
			keyboardShouldPersistTaps="handled"
			keyboardDismissMode="interactive"
			data={choices}
			keyExtractor={(choice) => choice.value}
			ListHeaderComponent={(
				<>
					<SheetHeader
						title={kind === "skills" ? "Skills" : "Worktree files"}
						subtitle={kind === "skills" ? "Insert a skill into your message." : "Mention a file from this worktree."}
					/>
					<View style={styles.searchSurface}>
						<Feather name="search" size={17} color={t.textTertiary} />
						<TextInput
							value={query}
							onChangeText={setQuery}
							placeholder={kind === "skills" ? "Search skills" : "Search worktree files"}
							placeholderTextColor={t.textTertiary}
							style={styles.search}
						/>
						{query ? <Pressable accessibilityRole="button" accessibilityLabel="Clear search" hitSlop={8} onPress={() => { haptics.tap(); setQuery(""); }}><Feather name="x-circle" size={17} color={t.textTertiary} /></Pressable> : null}
					</View>
					{kind === "files" && truncated ? (
						<Text style={styles.notice}>Showing the daemon&apos;s capped path list. Narrow your search or type a path directly.</Text>
					) : null}
					<Text style={styles.results}>{choices.length} {kind === "skills" ? (choices.length === 1 ? "skill" : "skills") : (choices.length === 1 ? "file" : "files")}</Text>
				</>
			)}
			ListEmptyComponent={<Text style={styles.empty}>No matches</Text>}
			ItemSeparatorComponent={() => <View style={styles.separator} />}
			renderItem={({ item }) => <SuggestionRow kind={kind} choice={item} onSelect={onSelect} />}
		/>
	);
}


function SuggestionRow({ kind, choice, onSelect }: { kind: "skills" | "files"; choice: RankedSuggestion; onSelect(value: string): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return (
		<Pressable
			onPress={() => {
				haptics.select();
				onSelect(choice.value);
			}}
			style={({ pressed }) => [styles.row, pressed && { opacity: 0.6 }]}
		>
			<Feather name={kind === "skills" ? "zap" : "file-text"} size={17} color={t.textSecondary} style={styles.rowIcon} />
			<View style={{ flex: 1 }}>
				<Text style={styles.label}>{choice.label}</Text>
				{choice.detail ? <Text numberOfLines={2} style={styles.detail}>{choice.detail}</Text> : null}
			</View>
			{choice.badge ? <Text style={styles.badge}>{choice.badge}</Text> : null}
		</Pressable>
	);
}

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgSurface },
	searchSurface: { minHeight: 48, flexDirection: "row", alignItems: "center", gap: 9, marginTop: 18, paddingHorizontal: 13, borderRadius: 16, borderCurve: "continuous", backgroundColor: t.bgElevated, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault },
	search: { flex: 1, minHeight: 46, color: t.textPrimary, fontSize: 15, paddingVertical: 0 },
	notice: { color: t.amber, fontSize: 11, lineHeight: 16, marginTop: 9 },
	results: { color: t.textTertiary, fontSize: 12, fontWeight: "600", marginTop: 20, marginBottom: 4 },
	row: { minHeight: 56, flexDirection: "row", alignItems: "center", gap: 11, paddingVertical: 10, paddingHorizontal: 2 },
	rowIcon: { width: 21, textAlign: "center" },
	label: { color: t.textPrimary, fontSize: 15, fontWeight: "500" },
	detail: { color: t.textTertiary, fontSize: 12, lineHeight: 16, marginTop: 2 },
	badge: { color: t.textTertiary, fontSize: 10 },
	separator: { height: StyleSheet.hairlineWidth, backgroundColor: t.borderSubtle, marginLeft: 34 },
	empty: { color: t.textTertiary, textAlign: "center", paddingVertical: 28 },
});
