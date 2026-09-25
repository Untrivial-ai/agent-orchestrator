import { Feather } from "./icons";
import { useRouter } from "expo-router";
import { Platform, Pressable, StyleSheet, Text, View } from "react-native";
import type { Theme } from "./theme";
import { haptics } from "./haptics";
import { activeProjectLabel } from "./projectFilter";
import { projectSheetRoute } from "./sheetResult";
import { useApp } from "./store";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { iconSize, space, type } from "./tokens";

// Scopes the board to one project (or All). A header row — label, current
// scope, overflow button — rather than the horizontal pill row it replaced: the
// pills grew a scrollable list that pushed the board down and hid every project
// past the third one off the right edge.
//
// It opens the same sheet Settings uses, so there is one project picker in the
// app instead of two controls that drift apart.
export function ProjectSwitcher() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const { projects, projectsKnown, activeProjectId, setActiveProject } = useApp();
	const router = useRouter();

	// Nothing to switch between — a single-project user never sees this.
	if (projects.length <= 1) return null;

	const active = projects.find((p) => p.id === activeProjectId);

	return (
		<>
			<View style={styles.row}>
				<Text style={styles.label}>PROJECTS</Text>
				<Pressable
					onPress={() => {
						haptics.tap();
						// No navigation on select — the board is already on screen, so the
						// filter applying in place is the whole feedback.
						router.push(projectSheetRoute({ selected: activeProjectId, onSelect: setActiveProject }));
					}}
					hitSlop={10}
					style={({ pressed }) => [styles.trigger, pressed && styles.triggerPressed]}
					accessibilityRole="button"
					accessibilityLabel="Change active project"
				>
					<Text style={[styles.value, active && { color: t.accent }]} numberOfLines={1}>
						{activeProjectLabel(activeProjectId, projects, projectsKnown)}
					</Text>
					{/* A chevron, not an overflow "…": this changes a value rather than
					    revealing a menu of actions. */}
					<Feather name="chevron-down" size={iconSize.sm} color={active ? t.accent : t.textTertiary} />
				</Pressable>
			</View>

		</>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
	row: {
		flexDirection: "row",
		alignItems: "center",
		gap: space.md,
		paddingHorizontal: space.lg,
		paddingBottom: space.xxs,
	},
	label: { fontFamily: "Geist_600SemiBold",
		color: t.textSecondary,
		fontSize: type.footnote.fontSize,
		letterSpacing: 0.8,
		fontWeight: "600",
		flex: 1,
	},
	trigger: {
		flexDirection: "row",
		alignItems: "center",
		gap: space.xs,
		// Keep the right-hand picker in one place as project names change.
		...(Platform.OS === "ios" ? { width: "60%" as const, justifyContent: "flex-end" as const } : { maxWidth: "70%" as const }),
		paddingVertical: space.xxs,
		paddingHorizontal: space.sm,
		marginRight: -8,
		borderRadius: 8, borderCurve: "continuous",
	},
	triggerPressed: { backgroundColor: t.bgElevated },
	value: { fontFamily: "Geist_600SemiBold", color: t.textTertiary, fontSize: type.footnote.fontSize, fontWeight: "600", flexShrink: 1, ...(Platform.OS === "ios" ? { textAlign: "right" as const } : {}) },
});
