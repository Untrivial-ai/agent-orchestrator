import { Feather } from "@expo/vector-icons";
import BottomSheet, { BottomSheetScrollView, BottomSheetView } from "@expo/ui/community/bottom-sheet";
import { Pressable, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import type { ProjectInfo } from "./api";
import { haptics } from "./haptics";
import type { Theme } from "./theme";
import { useTheme } from "./ThemeProvider";
import { ALL_WORKER_PROJECTS } from "./worker-controls";

export function WorkerControlsSheet({
	open,
	onDismiss,
	onSearch,
	projects,
	selectedProjectId,
	onSelectProject,
}: {
	open: boolean;
	onDismiss: () => void;
	onSearch: () => void;
	projects: ProjectInfo[];
	selectedProjectId: string;
	onSelectProject: (projectId: string) => void;
}) {
	const t = useTheme();
	const styles = makeStyles(t);
	const insets = useSafeAreaInsets();
	const options = [{ id: ALL_WORKER_PROJECTS, name: "All projects" }, ...projects];

	return (
		<BottomSheet
			index={open ? 0 : -1}
			snapPoints={["55%", "85%"]}
			enablePanDownToClose
			enableDynamicSizing={false}
			backgroundStyle={{ backgroundColor: t.bgSurface }}
			onClose={onDismiss}
		>
			<BottomSheetView style={[styles.sheet, { paddingBottom: Math.max(insets.bottom, 16) }]}>
				<View style={styles.header}>
					<View>
						<Text style={styles.title}>Workers</Text>
						<Text style={styles.subtitle}>Find and scope this list.</Text>
					</View>
					<Pressable accessibilityRole="button" accessibilityLabel="Close" onPress={onDismiss} hitSlop={12}>
						<Feather name="x" size={22} color={t.textSecondary} />
					</Pressable>
				</View>

				<Pressable
					accessibilityRole="button"
					accessibilityLabel="Search workers"
					testID="worker-controls-search"
					onPress={() => {
						haptics.tap();
						onDismiss();
						setTimeout(onSearch, 280);
					}}
					style={({ pressed }) => [styles.searchRow, pressed && styles.pressed]}
				>
					<Feather name="search" size={19} color={t.blue} />
					<Text style={styles.searchLabel}>Search workers</Text>
					<Feather name="chevron-right" size={18} color={t.textFaint} />
				</Pressable>

				<Text style={styles.sectionLabel}>PROJECTS</Text>
				<BottomSheetScrollView style={styles.projectList} showsVerticalScrollIndicator={false}>
					{options.map((project, index) => {
						const selected = project.id === selectedProjectId;
						return (
							<Pressable
								key={project.id}
								accessibilityRole="button"
								accessibilityState={{ selected }}
								testID={project.id === ALL_WORKER_PROJECTS ? "worker-project-filter" : undefined}
								onPress={() => {
									haptics.select();
									onSelectProject(project.id);
									onDismiss();
								}}
								style={({ pressed }) => [styles.projectRow, index > 0 && styles.separator, selected && styles.selectedRow, pressed && styles.pressed]}
							>
								<Feather name={project.id === ALL_WORKER_PROJECTS ? "layers" : "folder"} size={18} color={selected ? t.blue : t.textSecondary} />
								<Text numberOfLines={1} style={[styles.projectLabel, selected && styles.selectedLabel]}>{project.name}</Text>
								{selected ? <Feather name="check" size={20} color={t.blue} /> : null}
							</Pressable>
						);
					})}
				</BottomSheetScrollView>
			</BottomSheetView>
		</BottomSheet>
	);
}

const makeStyles = (t: Theme) => StyleSheet.create({
	sheet: { flex: 1, paddingHorizontal: 16, backgroundColor: t.bgSurface },
	header: { minHeight: 58, flexDirection: "row", alignItems: "center", justifyContent: "space-between", paddingHorizontal: 4 },
	title: { color: t.textPrimary, fontSize: 21, lineHeight: 27, fontWeight: "700" },
	subtitle: { marginTop: 2, color: t.textTertiary, fontSize: 13, lineHeight: 18 },
	searchRow: { height: 52, marginTop: 8, paddingHorizontal: 15, flexDirection: "row", alignItems: "center", gap: 11, borderRadius: 16, backgroundColor: t.bgElevated, overflow: "hidden" },
	searchLabel: { flex: 1, color: t.textPrimary, fontSize: 16, lineHeight: 21, fontWeight: "600" },
	sectionLabel: { marginTop: 20, marginBottom: 8, paddingHorizontal: 4, color: t.textTertiary, fontSize: 11, lineHeight: 15, letterSpacing: 1, fontWeight: "700" },
	projectList: { flex: 1, borderRadius: 16, backgroundColor: t.bgElevated, overflow: "hidden" },
	projectRow: { minHeight: 52, paddingHorizontal: 15, flexDirection: "row", alignItems: "center", gap: 11 },
	separator: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle },
	selectedRow: { backgroundColor: t.tintBlue },
	projectLabel: { flex: 1, color: t.textPrimary, fontSize: 16, lineHeight: 21 },
	selectedLabel: { color: t.blue, fontWeight: "700" },
	pressed: { opacity: 0.7 },
});
