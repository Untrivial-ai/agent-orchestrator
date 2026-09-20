import { Feather } from "../icons";
import { useMemo, useState } from "react";
import { FlatList, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { SheetHeader } from "../ui";
import { composerSheetContentStyle } from "./chatLayout";
import { rankComposerCatalog, type ComposerPickerCatalog, type RankedSuggestion } from "./composerSuggestions";
import { iconSize, press, space, type } from "../tokens";

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
						<Feather name="search" size={iconSize.md} color={t.textTertiary} />
						<TextInput
							value={query}
							onChangeText={setQuery}
							placeholder={kind === "skills" ? "Search skills" : "Search worktree files"}
							placeholderTextColor={t.textTertiary}
							style={styles.search}
						/>
						{query ? <Pressable accessibilityRole="button" accessibilityLabel="Clear search" hitSlop={8} onPress={() => { haptics.tap(); setQuery(""); }}><Feather name="x-circle" size={iconSize.md} color={t.textTertiary} /></Pressable> : null}
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
			style={({ pressed }) => [styles.row, pressed && { opacity: press.opacity }]}
		>
			<Feather name={kind === "skills" ? "zap" : "file-text"} size={iconSize.md} color={t.textSecondary} style={styles.rowIcon} />
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
	searchSurface: { minHeight: 48, flexDirection: "row", alignItems: "center", gap: space.sm, marginTop: space.lg, paddingHorizontal: space.md, borderRadius: 16, borderCurve: "continuous", backgroundColor: t.bgElevated, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault },
	search: { fontFamily: "Geist_400Regular", flex: 1, minHeight: 46, color: t.textPrimary, fontSize: type.subheadline.fontSize, paddingVertical: space.none },
	notice: { fontFamily: "Geist_400Regular", color: t.amber, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight, marginTop: space.sm },
	results: { fontFamily: "Geist_600SemiBold", color: t.textTertiary, fontSize: type.caption1.fontSize, fontWeight: "600", marginTop: space.xl, marginBottom: space.xxs },
	row: { minHeight: 56, flexDirection: "row", alignItems: "center", gap: space.md, paddingVertical: space.sm, paddingHorizontal: space.hair },
	rowIcon: { fontFamily: "Geist_400Regular", width: 21, textAlign: "center" },
	label: { fontFamily: "Geist_500Medium", color: t.textPrimary, fontSize: type.subheadline.fontSize, fontWeight: "500" },
	detail: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, marginTop: space.hair },
	badge: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.caption2.fontSize },
	separator: { height: StyleSheet.hairlineWidth, backgroundColor: t.borderSubtle, marginLeft: space.xxxl },
	empty: { fontFamily: "Geist_400Regular", color: t.textTertiary, textAlign: "center", paddingVertical: space.xxl },
});
