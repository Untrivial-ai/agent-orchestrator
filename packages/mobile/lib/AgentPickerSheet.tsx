import { Feather } from "./icons";
import { ActivityIndicator, FlatList, Pressable, StyleSheet, Text } from "react-native";
import { AgentLogo } from "./AgentLogo";
import type { RankedAgent } from "./agentPicker";
import { haptics } from "./haptics";
import type { Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { SHEET_SCROLL_CONTENT, SheetHeader } from "./ui";
import { iconSize, press, space, type } from "./tokens";

// Picks the agent CLI that will run a session.
//
// Rows carry the brand mark, matching desktop's AgentSelectMenuItem — this was
// the last agent-bearing surface in the app still showing bare text.
//
// Refresh lives here rather than on the form: a stale catalog is something you
// discover while looking for an agent that is missing or unauthorised, which is
// this list, not the screen behind it.
export function AgentPickerSheet({
	onClose,
	agents,
	selected,
	onSelect,
	onRefresh,
	refreshing = false,
	error,
	title = "Agent",
	subtitle = "The CLI that runs this session.",
}: {
	/** Dismisses the sheet route. */
	onClose: () => void;
	agents: RankedAgent[];
	selected: string;
	onSelect: (id: string) => void;
	onRefresh: () => void;
	refreshing?: boolean;
	error?: string | null;
	title?: string;
	subtitle?: string;
}) {
	const t = useTheme();
	const s = useThemedStyles(makeStyles);

	return (
		<FlatList
			style={s.list}
			data={agents}
			keyExtractor={(a) => a.id}
			contentContainerStyle={SHEET_SCROLL_CONTENT}
			ListEmptyComponent={
				<Text style={s.empty}>No agents reported. Check that AO is running on your computer, then refresh.</Text>
			}
			ListHeaderComponent={
				<>
					<SheetHeader
						title={title}
						subtitle={subtitle}
						right={
							<Pressable
								accessibilityRole="button"
								accessibilityLabel="Refresh agent list"
								accessibilityState={{ disabled: refreshing }}
								disabled={refreshing}
								hitSlop={8}
								onPress={() => {
									haptics.tap();
									onRefresh();
								}}
								style={({ pressed }) => [s.refresh, (pressed || refreshing) && { opacity: 0.5 }]}
							>
								{refreshing ? (
									<ActivityIndicator size="small" color={t.accent} />
								) : (
									<>
										<Feather name="refresh-cw" size={iconSize.xs} color={t.accent} />
										<Text style={s.refreshText}>Refresh</Text>
									</>
								)}
							</Pressable>
						}
					/>
					{error ? <Text style={s.error}>{error}</Text> : null}
				</>
			}
			renderItem={({ item: a }) => {
				const isSelected = a.id === selected;
				return (
					<Pressable
						accessibilityRole="button"
						accessibilityState={{
							selected: isSelected,
							disabled: !a.selectable,
						}}
						accessibilityLabel={a.status ? `${a.label}, ${a.status}` : a.label}
						// Genuinely disabled, not just ignored on press. The previous
						// picker let you tap an unusable agent and silently did nothing.
						disabled={!a.selectable}
						onPress={() => {
							haptics.select();
							// Dismiss before reporting the choice, matching the other
							// sheets. onClose leaves the sheet, so a callback that
							// navigated first would have that dismiss pop the
							// destination instead of this sheet.
							onClose();
							onSelect(a.id);
						}}
						style={({ pressed }) => [
							s.option,
							!a.selectable && s.optionDisabled,
							pressed && a.selectable && s.optionPressed,
						]}
					>
						<AgentLogo harness={a.id} size={22} />
						<Text style={[s.label, isSelected && { color: t.accent }]} numberOfLines={1}>
							{a.label}
						</Text>
						{a.status ? (
							<Text
								style={[
									s.status,
									// Amber for something you can act on, muted for a missing
									// install — desktop's tone split.
									a.availability === "auth-unknown" || a.availability === "needs-auth" ? { color: t.amber } : null,
								]}
							>
								{a.status}
							</Text>
						) : null}
						{isSelected ? <Feather name="check" size={iconSize.md} color={t.accent} /> : null}
					</Pressable>
				);
			}}
		/>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		list: { flex: 1, backgroundColor: t.bgSurface },
		refresh: { flexDirection: "row", alignItems: "center", gap: space.xxs },
		refreshText: { fontFamily: "Geist_600SemiBold", color: t.accent, fontSize: type.footnote.fontSize, fontWeight: "600" },
		option: {
			flexDirection: "row",
			alignItems: "center",
			gap: space.md,
			paddingVertical: space.md,
			paddingHorizontal: space.hair,
		},
		optionPressed: { opacity: press.opacity },
		// Desktop's opacity for an unpickable agent. The row still shows its mark
		// and reason, so it reads as "not yet" rather than missing.
		optionDisabled: { opacity: 0.45 },
		label: { fontFamily: "Geist_500Medium", flex: 1, color: t.textPrimary, fontSize: type.subheadline.fontSize, fontWeight: "500" },
		status: { fontFamily: "Geist_600SemiBold", color: t.textTertiary, fontSize: type.caption2.fontSize, fontWeight: "600" },
		empty: { fontFamily: "Geist_400Regular",
			color: t.textTertiary,
			fontSize: type.footnote.fontSize,
			lineHeight: type.footnote.lineHeight,
			paddingVertical: space.md,
		},
		error: { fontFamily: "Geist_400Regular", color: t.red, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, marginTop: space.sm },
	});
