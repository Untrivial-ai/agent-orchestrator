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
			data={choices}
			keyExtractor={(choice) => choice.value}
			ListHeaderComponent={(
				<>
					<SheetHeader
						title={kind === "skills" ? "Skills" : "Worktree files"}
						subtitle={kind === "skills" ? "Insert a skill into your message." : "Mention a file from this worktree."}
					/>
					<TextInput
						value={query}
						onChangeText={setQuery}
						placeholder={kind === "skills" ? "Find a skill" : "Find a file"}
						placeholderTextColor={t.textFaint}
						style={styles.search}
					/>
					{kind === "files" && truncated ? (
						<Text style={styles.notice}>Showing the daemon&apos;s capped path list. Narrow your search or type a path directly.</Text>
					) : null}
				</>
			)}
			ListEmptyComponent={<Text style={styles.empty}>No matches</Text>}
			renderItem={({ item }) => <SuggestionRow choice={item} onSelect={onSelect} />}
		/>
	);
}

function SuggestionRow({ choice, onSelect }: { choice: RankedSuggestion; onSelect(value: string): void }) {
	const styles = useThemedStyles(makeStyles);
	return (
		<Pressable
			onPress={() => {
				haptics.select();
				onSelect(choice.value);
			}}
			style={({ pressed }) => [styles.row, pressed && { opacity: 0.6 }]}
		>
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
	search: { minHeight: 38, color: t.textPrimary, paddingHorizontal: 12, marginTop: 10, borderBottomWidth: 2, borderBottomColor: t.borderDefault },
	notice: { color: t.amber, fontSize: 11, lineHeight: 16, marginBottom: 8 },
	row: { minHeight: 52, flexDirection: "row", alignItems: "center", gap: 10, paddingVertical: 9 },
	label: { color: t.textPrimary, fontSize: 14, fontWeight: "600" },
	detail: { color: t.textTertiary, fontSize: 11, marginTop: 2 },
	badge: { color: t.textTertiary, fontSize: 9 },
	empty: { color: t.textTertiary, textAlign: "center", paddingVertical: 28 },
});
