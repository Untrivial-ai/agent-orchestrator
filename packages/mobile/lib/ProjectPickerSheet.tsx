import { Feather } from "@expo/vector-icons";
import { FlatList, Pressable, StyleSheet, Text } from "react-native";
import type { ProjectInfo } from "./api";
import type { Theme } from "./theme";
import { haptics } from "./haptics";
import { SHEET_SCROLL_CONTENT, SheetHeader } from "./ui";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { ALL_PROJECTS } from "./projectFilter";
import { iconSize, press, space, type } from "./tokens";

// Picks the active project — the filter behind `useVisibleSessions()` (Agents +
// PRs) and the default project in the spawn screen.
//
// The sheet only reports the choice; what happens next is the caller's call.
// Settings follows a selection through to the Agents tab, so choosing a project
// and seeing it applied are one step rather than two.

export function ProjectPickerSheet({
	onClose,
	projects,
	activeProjectId,
	onSelect,
	includeAll = true,
	title = "Active project",
	subtitle = "Scopes Pull Requests.",
}: {
	/** Dismisses the sheet route. */
	onClose: () => void;
	projects: ProjectInfo[];
	activeProjectId: string;
	onSelect: (id: string) => void;
	/** "All projects" is a filter, so the spawn screen turns it off — you cannot spawn into it. */
	includeAll?: boolean;
	title?: string;
	subtitle?: string;
}) {
	const s = useThemedStyles(makeS);
	function choose(id: string) {
		haptics.select();
		// Dismiss before reporting the choice: the caller may navigate, and a
		// still-presented sheet would sit over the destination.
		onClose();
		onSelect(id);
	}

	// "All projects" is just another row, so the list is one flat data array and
	// the header can live inside it — see SheetHeader for why that matters.
	const rows: Row[] = [
		...(includeAll ? [{ id: ALL_PROJECTS, label: "All projects", icon: "layers" as const }] : []),
		...projects.map((p) => ({
			id: p.id,
			label: p.name,
			hint: p.sessionPrefix,
			icon: "folder" as const,
		})),
	];

	return (
		<FlatList
			style={s.list}
			data={rows}
			keyExtractor={(r) => r.id}
			contentContainerStyle={SHEET_SCROLL_CONTENT}
			ListHeaderComponent={<SheetHeader title={title} subtitle={subtitle} />}
			ListEmptyComponent={<Text style={s.empty}>No projects yet. Add one from the AO dashboard on your computer.</Text>}
			renderItem={({ item }) => (
				<Option
					label={item.label}
					hint={item.hint}
					icon={item.icon}
					selected={activeProjectId === item.id}
					onPress={() => choose(item.id)}
				/>
			)}
		/>
	);
}

type Row = {
	id: string;
	label: string;
	hint?: string;
	icon: keyof typeof Feather.glyphMap;
};

function Option({
	label,
	hint,
	icon,
	selected,
	onPress,
}: {
	label: string;
	hint?: string;
	icon: keyof typeof Feather.glyphMap;
	selected: boolean;
	onPress: () => void;
}) {
	const t = useTheme();
	const s = useThemedStyles(makeS);
	return (
		<Pressable onPress={onPress} style={({ pressed }) => [s.option, pressed && s.optionPressed]}>
			<Feather name={icon} size={iconSize.sm} color={selected ? t.accent : t.textTertiary} />
			<Text style={[s.label, selected && { color: t.accent }]} numberOfLines={1}>
				{label}
			</Text>
			{hint ? (
				<Text style={s.hint} numberOfLines={1}>
					{hint}
				</Text>
			) : null}
			{selected ? <Feather name="check" size={iconSize.md} color={t.accent} /> : null}
		</Pressable>
	);
}

const makeS = (t: Theme) =>
	StyleSheet.create({
		list: { flex: 1, backgroundColor: t.bgSurface },
		option: {
			flexDirection: "row",
			alignItems: "center",
			gap: space.md,
			paddingVertical: space.md,
			paddingHorizontal: space.hair,
		},
		optionPressed: { opacity: press.opacity },
		label: { fontFamily: "Geist_500Medium", flex: 1, color: t.textPrimary, fontSize: type.subheadline.fontSize, fontWeight: "500" },
		hint: {
			color: t.textFaint,
			fontSize: type.caption1.fontSize,
			fontFamily: t.fontMono,
			flexShrink: 1,
		},
		empty: { fontFamily: "Geist_400Regular",
			color: t.textTertiary,
			fontSize: type.footnote.fontSize,
			lineHeight: type.footnote.lineHeight,
			paddingVertical: space.md,
		},
	});
