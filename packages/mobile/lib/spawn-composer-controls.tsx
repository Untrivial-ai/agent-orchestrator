import { Button, Host, Picker } from "@expo/ui";
import { StyleSheet, View } from "react-native";
import { useTheme, useThemeState } from "./ThemeProvider";
import type { SpawnComposerControlsProps } from "./spawn-composer-controls.types";

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
	const { scheme } = useThemeState();
	return (
		<View style={styles.stack}>
			<Host style={styles.projectHost} colorScheme={scheme} seedColor={t.blue}>
				<Picker selectedValue={projectId ?? ""} onValueChange={onSelectProject} appearance="menu">
					<Picker.Item label="Choose project" value="" />
					{projects.map((project) => <Picker.Item key={project.id} label={project.label} value={project.id} />)}
				</Picker>
			</Host>
			<View style={styles.rail}>
				<Host style={styles.iconHost} colorScheme={scheme} seedColor={t.blue}>
					<Button label="📎" variant="text" onPress={onAttach} style={styles.iconButton} />
				</Host>
				<Host style={styles.menuHost} colorScheme={scheme} seedColor={t.blue}>
					<Picker selectedValue={harness} onValueChange={onSelectHarness} appearance="menu">
						{agents.map((agent) => <Picker.Item key={agent.id} label={agent.label} value={agent.id} />)}
					</Picker>
				</Host>
				<Host style={styles.menuHost} colorScheme={scheme} seedColor={t.blue}>
					<Picker selectedValue={modelSelection} onValueChange={onSelectModel} appearance="menu">
						<Picker.Item label={modelLabel} value="__auto__" />
						{models.map((model) => <Picker.Item key={model.id} label={model.label} value={model.id} />)}
					</Picker>
				</Host>
			</View>
			<Host style={styles.spawnHost} colorScheme={scheme} seedColor={t.blue}>
				<Button label={busy ? "Starting..." : "Start Task"} variant="filled" onPress={onSpawn} disabled={disabled} style={styles.spawnButton} />
			</Host>
		</View>
	);
}

const styles = StyleSheet.create({
	stack: { gap: 2 },
	projectHost: { width: 180, height: 36 },
	rail: { minHeight: 52, flexDirection: "row", alignItems: "center", gap: 6 },
	iconHost: { width: 44, height: 44 },
	iconButton: { width: 44, height: 44, borderRadius: 22 },
	menuHost: { flex: 1, minWidth: 0, height: 44 },
	spawnHost: { width: "100%", height: 44 },
	spawnButton: { width: "100%", height: 44, borderRadius: 16 },
});
