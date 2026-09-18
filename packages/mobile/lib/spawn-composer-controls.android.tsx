import { Feather } from "@expo/vector-icons";
import { useState } from "react";
import { ActivityIndicator, Modal, Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { AgentLogo } from "./AgentLogo";
import { useTheme } from "./ThemeProvider";
import type { Theme } from "./theme";
import type { SpawnComposerControlsProps, SpawnComposerOption } from "./spawn-composer-controls.types";

type OpenMenu = "project" | "harness" | "model" | null;

export function SpawnComposerControls({
	projects,
	projectId,
	onSelectProject,
	agents,
	harness,
	onSelectHarness,
	models,
	modelSelection,
	modelLabel,
	onSelectModel,
	onAttach,
	onSpawn,
	busy,
	disabled,
}: SpawnComposerControlsProps) {
	const t = useTheme();
	const styles = makeStyles(t);
	const [openMenu, setOpenMenu] = useState<OpenMenu>(null);
	const projectLabel = projects.find((project) => project.id === projectId)?.label ?? "Choose project";
	const harnessLabel = agents.find((agent) => agent.id === harness)?.label ?? "Choose harness";
	const modelOptions = [{ id: "__auto__", label: "Automatic" }, ...models];
	const options = openMenu === "project" ? projects : openMenu === "harness" ? agents : modelOptions;
	const selectedValue = openMenu === "project" ? (projectId ?? "") : openMenu === "harness" ? harness : modelSelection;
	const menuTitle = openMenu === "project" ? "Project" : openMenu === "harness" ? "Agent" : "Model";

	const selectOption = (value: string) => {
		if (openMenu === "project") onSelectProject(value);
		if (openMenu === "harness") onSelectHarness(value);
		if (openMenu === "model") onSelectModel(value);
		setOpenMenu(null);
	};

	return (
		<View style={styles.stack}>
			<SelectorButton label={projectLabel} icon="folder" onPress={() => setOpenMenu("project")} style={styles.projectButton} />

			<View style={styles.rail}>
				<Pressable
					accessibilityRole="button"
					accessibilityLabel="Attach a file"
					android_ripple={{ color: t.tintBlue, borderless: true, radius: 20 }}
					onPress={onAttach}
					style={styles.attach}
				>
					<Feather name="paperclip" size={20} color={t.textSecondary} />
				</Pressable>
				<View style={styles.divider} />
				<SelectorButton label={harnessLabel} icon="terminal" harness={harness} onPress={() => setOpenMenu("harness")} style={styles.railButton} />
				<View style={styles.divider} />
				<SelectorButton label={modelLabel} icon="cpu" onPress={() => setOpenMenu("model")} style={styles.railButton} />
			</View>

			<Pressable
				accessibilityRole="button"
				accessibilityLabel={busy ? "Spawning worker" : "Spawn worker"}
				accessibilityState={{ disabled }}
				testID="spawn-submit"
				disabled={disabled}
				android_ripple={{ color: "rgba(255,255,255,0.18)" }}
				onPress={onSpawn}
				style={[styles.spawn, { opacity: disabled ? 0.68 : 1 }]}
			>
				{busy ? <ActivityIndicator size="small" color={t.onAccent} /> : null}
				<Text style={styles.spawnLabel}>{busy ? "Spawning…" : "Spawn"}</Text>
			</Pressable>

			<OptionSheet
				open={openMenu !== null}
				title={menuTitle}
				options={options}
				selectedValue={selectedValue}
				showAgentLogos={openMenu === "harness"}
				onSelect={selectOption}
				onDismiss={() => setOpenMenu(null)}
			/>
		</View>
	);
}

function SelectorButton({ label, icon, harness, onPress, style }: {
	label: string;
	icon: keyof typeof Feather.glyphMap;
	harness?: string;
	onPress: () => void;
	style?: object;
}) {
	const t = useTheme();
	const styles = makeStyles(t);
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			android_ripple={{ color: t.tintBlue }}
			onPress={onPress}
			style={[styles.selector, style]}
		>
			{harness ? <AgentLogo harness={harness} size={20} /> : <Feather name={icon} size={15} color={t.textSecondary} />}
			<Text numberOfLines={1} style={styles.selectorLabel}>{label}</Text>
			<Feather name="chevron-down" size={14} color={t.textTertiary} />
		</Pressable>
	);
}

function OptionSheet({ open, title, options, selectedValue, showAgentLogos, onSelect, onDismiss }: {
	open: boolean;
	title: string;
	options: readonly SpawnComposerOption[];
	selectedValue: string;
	showAgentLogos?: boolean;
	onSelect: (value: string) => void;
	onDismiss: () => void;
}) {
	const t = useTheme();
	const styles = makeStyles(t);
	const insets = useSafeAreaInsets();
	return (
		<Modal
			visible={open}
			transparent
			animationType="fade"
			presentationStyle="overFullScreen"
			statusBarTranslucent
			navigationBarTranslucent
			onRequestClose={onDismiss}
		>
			<View style={styles.modalRoot}>
				<Pressable accessibilityRole="button" accessibilityLabel="Close options" onPress={onDismiss} style={StyleSheet.absoluteFill} />
				<View style={[styles.optionSheet, { paddingBottom: Math.max(insets.bottom, 16) }]}>
					<View style={styles.grabber} />
					<View style={styles.optionHeader}>
						<Text style={styles.optionTitle}>{title}</Text>
						<Pressable accessibilityRole="button" accessibilityLabel="Close" onPress={onDismiss} hitSlop={12}>
							<Feather name="x" size={22} color={t.textSecondary} />
						</Pressable>
					</View>
					<ScrollView style={styles.optionList} showsVerticalScrollIndicator={false}>
						{options.map((option, index) => {
							const selected = option.id === selectedValue;
							return (
								<Pressable
									key={option.id}
									accessibilityRole="button"
									accessibilityState={{ selected }}
									android_ripple={{ color: t.tintBlue }}
									onPress={() => onSelect(option.id)}
									style={[styles.optionRow, index > 0 && styles.optionBorder, selected && styles.optionSelected]}
								>
									{showAgentLogos ? <AgentLogo harness={option.id} size={24} /> : null}
									<Text numberOfLines={2} style={[styles.optionLabel, selected && styles.optionLabelSelected]}>{option.label}</Text>
									{selected ? <Feather name="check" size={20} color={t.blue} /> : null}
								</Pressable>
							);
						})}
					</ScrollView>
				</View>
			</View>
		</Modal>
	);
}

const makeStyles = (t: Theme) => StyleSheet.create({
	stack: { gap: 9 },
	projectButton: { alignSelf: "flex-start", maxWidth: "72%", height: 36, paddingHorizontal: 10, backgroundColor: "transparent" },
	rail: {
		height: 52,
		flexDirection: "row",
		alignItems: "center",
		paddingHorizontal: 5,
		borderRadius: 18,
		borderCurve: "continuous",
		backgroundColor: t.bgElevated,
		borderWidth: StyleSheet.hairlineWidth,
		borderColor: t.borderDefault,
		overflow: "hidden",
	},
	attach: { width: 42, height: 42, borderRadius: 21, alignItems: "center", justifyContent: "center", overflow: "hidden" },
	divider: { width: StyleSheet.hairlineWidth, height: 24, backgroundColor: t.borderDefault },
	selector: { minHeight: 36, flexDirection: "row", alignItems: "center", gap: 7, borderRadius: 12, overflow: "hidden" },
	railButton: { flex: 1, minWidth: 0, height: 42, paddingHorizontal: 10 },
	selectorLabel: { flexShrink: 1, color: t.textPrimary, fontSize: 14, lineHeight: 19, fontWeight: "600" },
	spawn: { height: 44, flexDirection: "row", gap: 8, borderRadius: 16, borderCurve: "continuous", alignItems: "center", justifyContent: "center", overflow: "hidden", backgroundColor: t.blue },
	spawnLabel: { color: t.onAccent, fontSize: 15, lineHeight: 20, fontWeight: "700" },
	modalRoot: { flex: 1, justifyContent: "flex-end", backgroundColor: t.scrim },
	optionSheet: {
		maxHeight: "70%",
		paddingTop: 8,
		paddingHorizontal: 16,
		borderTopLeftRadius: 26,
		borderTopRightRadius: 26,
		backgroundColor: t.bgSurface,
	},
	grabber: { alignSelf: "center", width: 40, height: 4, borderRadius: 2, backgroundColor: t.borderStrong, marginBottom: 8 },
	optionHeader: { minHeight: 48, flexDirection: "row", alignItems: "center", justifyContent: "space-between", paddingHorizontal: 4 },
	optionTitle: { color: t.textPrimary, fontSize: 20, lineHeight: 26, fontWeight: "700" },
	optionList: { maxHeight: 420, borderRadius: 16, backgroundColor: t.bgElevated, overflow: "hidden" },
	optionRow: { minHeight: 54, paddingHorizontal: 16, paddingVertical: 12, flexDirection: "row", alignItems: "center", gap: 12 },
	optionBorder: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle },
	optionSelected: { backgroundColor: t.tintBlue },
	optionLabel: { flex: 1, color: t.textPrimary, fontSize: 16, lineHeight: 21 },
	optionLabelSelected: { color: t.blue, fontWeight: "700" },
});
