import { Feather } from "./icons";
import { useState } from "react";
import { ActivityIndicator, Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { AgentLogo } from "./AgentLogo";
import { useTheme } from "./ThemeProvider";
import type { Theme } from "./theme";
import type { SpawnComposerControlsProps, SpawnComposerOption } from "./spawn-composer-controls.types";
import { type, space } from "./tokens";
import { MicKey } from "./voice/MicKey";

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
	voice,
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

	// Choices replace the controls inside Spawn's own sheet. They used to open a
	// second sheet on top of it, so picking a project, agent and model stacked
	// three sheets deep.
	if (openMenu) {
		return (
			<OptionList
				title={menuTitle}
				options={options}
				selectedValue={selectedValue}
				showAgentLogos={openMenu === "harness"}
				onSelect={selectOption}
				onBack={() => setOpenMenu(null)}
			/>
		);
	}

	return (
		<View style={styles.stack}>
			<SelectorButton label={projectLabel} icon="folder" onPress={() => setOpenMenu("project")} style={styles.projectButton} />

			<View style={styles.rail}>
				<Pressable
					accessibilityRole="button"
					accessibilityLabel="Attach a file"
					android_ripple={{ color: t.accentTint, borderless: true, radius: 20 }}
					onPress={onAttach}
					style={styles.attach}
				>
					<Feather name="paperclip" size={20} color={t.textSecondary} />
				</Pressable>
				<View style={styles.divider} />
				<SelectorButton label={harnessLabel} icon="terminal" harness={harness} onPress={() => setOpenMenu("harness")} style={styles.railButton} />
				<View style={styles.divider} />
				<SelectorButton label={modelLabel} onPress={() => setOpenMenu("model")} style={styles.railButton} />
				<View style={styles.divider} />
				{/* Plain, like the paperclip: the rail is the surface, and a second
				    disc inside it would compete with Start task. */}
				<MicKey
					variant="plain"
					size={42}
					glyphSize={20}
					state={voice.state}
					mode={voice.mode}
					onPressIn={voice.onPressIn}
					onPressOut={voice.onPressOut}
				/>
			</View>

			<Pressable
				accessibilityRole="button"
				accessibilityLabel={busy ? "Starting task" : "Start task"}
				accessibilityState={{ disabled }}
				testID="spawn-submit"
				disabled={disabled}
				android_ripple={{ color: "rgba(255,255,255,0.18)" }}
				onPress={onSpawn}
				style={[styles.spawn, { opacity: disabled ? 0.68 : 1 }]}
			>
				{busy ? <ActivityIndicator size="small" color={t.onAccent} /> : null}
				<Text style={styles.spawnLabel}>{busy ? "Starting…" : "Start task"}</Text>
			</Pressable>

		</View>
	);
}

function SelectorButton({ label, icon, harness, onPress, style }: {
	label: string;
	icon?: keyof typeof Feather.glyphMap;
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
			android_ripple={{ color: t.accentTint }}
			onPress={onPress}
			style={[styles.selector, style]}
		>
			{harness ? <AgentLogo harness={harness} size={20} /> : icon ? <Feather name={icon} size={15} color={t.textSecondary} /> : null}
			<Text numberOfLines={1} style={styles.selectorLabel}>{label}</Text>
			<Feather name="chevron-down" size={15} color={t.textTertiary} />
		</Pressable>
	);
}

function OptionList({ title, options, selectedValue, showAgentLogos, onSelect, onBack }: {
	title: string;
	options: readonly SpawnComposerOption[];
	selectedValue: string;
	showAgentLogos?: boolean;
	onSelect: (value: string) => void;
	onBack: () => void;
}) {
	const t = useTheme();
	const styles = makeStyles(t);
	return (
		<View style={styles.stack}>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel="Back"
				android_ripple={{ color: t.accentTint }}
				onPress={onBack}
				style={styles.optionHeader}
			>
				<Feather name="chevron-left" size={20} color={t.textSecondary} />
				<Text style={styles.optionTitle}>{title}</Text>
			</Pressable>
			<ScrollView style={styles.optionList} showsVerticalScrollIndicator={false}>
				{options.map((option, index) => {
					const selected = option.id === selectedValue;
					return (
						<Pressable
							key={option.id}
							accessibilityRole="button"
							accessibilityState={{ selected }}
							android_ripple={{ color: t.accentTint }}
							onPress={() => onSelect(option.id)}
							style={[styles.optionRow, index > 0 && styles.optionBorder, selected && styles.optionSelected]}
						>
							{showAgentLogos ? <AgentLogo harness={option.id} size={24} /> : null}
							<Text numberOfLines={2} style={[styles.optionLabel, selected && styles.optionLabelSelected]}>{option.label}</Text>
							{selected ? <Feather name="check" size={20} color={t.accent} /> : null}
						</Pressable>
					);
				})}
			</ScrollView>
		</View>
	);
}

const makeStyles = (t: Theme) => StyleSheet.create({
	stack: { gap: space.sm },
	projectButton: { alignSelf: "flex-start", maxWidth: "72%", height: 36, paddingHorizontal: space.sm, backgroundColor: "transparent" },
	rail: {
		height: 52,
		flexDirection: "row",
		alignItems: "center",
		paddingHorizontal: space.xxs,
		borderRadius: 16,
		borderCurve: "continuous",
		backgroundColor: t.bgElevated,
		borderWidth: StyleSheet.hairlineWidth,
		borderColor: t.borderDefault,
		overflow: "hidden",
	},
	attach: { width: 42, height: 42, borderRadius: 20, alignItems: "center", justifyContent: "center", overflow: "hidden" },
	divider: { width: StyleSheet.hairlineWidth, height: 24, backgroundColor: t.borderDefault },
	selector: { minHeight: 36, flexDirection: "row", alignItems: "center", gap: space.xs, borderRadius: 12, overflow: "hidden" },
	railButton: { flex: 1, minWidth: 0, height: 42, paddingHorizontal: space.sm },
	selectorLabel: { fontFamily: "Geist_600SemiBold", flexShrink: 1, color: t.textPrimary, fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight, fontWeight: "600" },
	spawn: { height: 44, flexDirection: "row", gap: space.sm, borderRadius: 16, borderCurve: "continuous", alignItems: "center", justifyContent: "center", overflow: "hidden", backgroundColor: t.accent },
	spawnLabel: { fontFamily: "Geist_600SemiBold", color: t.onAccent, fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight, fontWeight: "600" },
	optionHeader: { minHeight: 44, flexDirection: "row", alignItems: "center", gap: space.xs, borderRadius: 12, overflow: "hidden" },
	optionTitle: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.body.fontSize, lineHeight: type.body.lineHeight, fontWeight: "600" },
	optionList: { maxHeight: 340, borderRadius: 16, backgroundColor: t.bgElevated, overflow: "hidden" },
	optionRow: { minHeight: 54, paddingHorizontal: space.lg, paddingVertical: space.md, flexDirection: "row", alignItems: "center", gap: space.md },
	optionBorder: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle },
	optionSelected: { backgroundColor: t.accentTint },
	optionLabel: { fontFamily: "Geist_400Regular", flex: 1, color: t.textPrimary, fontSize: type.callout.fontSize, lineHeight: type.callout.lineHeight },
	optionLabelSelected: { fontFamily: "Geist_600SemiBold", color: t.accent, fontWeight: "600" },
});
