import { Host } from "@expo/ui";
import { Button, HStack, Image, Menu, Section, Text } from "@expo/ui/swift-ui";
import { buttonStyle, disabled as disabledModifier, font, frame, padding, tint } from "@expo/ui/swift-ui/modifiers";
import { StyleSheet } from "react-native";
import { haptics } from "../haptics";
import { useTheme, useThemeState } from "../ThemeProvider";
import type { ChatTurnSettingsControlProps } from "./ChatTurnSettingsControl.types";
import { applyTurnSettingChoice, turnSettingsRows, turnSettingsSummary, type TurnSettingRow } from "./turnSettingsModel";
import { iconSize, type } from "../tokens";

export function ChatTurnSettingsControl({ snapshot, models, options, disabled, onSettings, onOption }: ChatTurnSettingsControlProps) {
	const t = useTheme();
	const { scheme } = useThemeState();
	const rows = turnSettingsRows(snapshot, models, options);
	const label = turnSettingsSummary(snapshot, models, options);

	const choose = (row: TurnSettingRow, value: string) => {
		haptics.select();
		if (row.target.kind === "option") {
			void onOption(row.target.optionId, row.kind === "boolean" ? { enabled: value === "on" } : { value }).catch(() => {});
			return;
		}
		void onSettings(applyTurnSettingChoice(snapshot.settings, row, value)).catch(() => {});
	};

	return (
		<Host matchContents={{ horizontal: true, vertical: true }} ignoreSafeArea="all" style={styles.host} colorScheme={scheme} seedColor={t.accent}>
			<Menu
				label={<HStack spacing={7}><Text modifiers={[font({ size: 13, weight: "semibold" })]}>{label}</Text><Image systemName="chevron.right" size={iconSize.xs} /></HStack>}
				modifiers={[buttonStyle("plain"), tint(t.textSecondary), padding({ horizontal: 10, vertical: 10 }), frame({ minHeight: 44, alignment: "leading" }), disabledModifier(Boolean(disabled))]}
			>
				<Section title="Turn settings">
					{rows.map((row) => (
						<Menu key={row.id} label={`${row.label} · ${row.value}`} systemImage={settingSymbol(row)}>
							{choicesFor(row).map((choice) => (
								<Button key={choice.value} label={choice.label} systemImage={choice.selected ? "checkmark" : undefined} onPress={() => choose(row, choice.value)} />
							))}
						</Menu>
					))}
				</Section>
			</Menu>
		</Host>
	);
}

function choicesFor(row: TurnSettingRow) {
	if (row.kind !== "boolean") return row.choices;
	return [
		{ value: "on", label: "On", selected: Boolean(row.enabled) },
		{ value: "off", label: "Off", selected: !row.enabled },
	];
}

function settingSymbol(row: TurnSettingRow): string {
	const id = row.id.toLowerCase();
	if (id.includes("mode") || id.includes("approval")) return "shield";
	if (id.includes("model") || id.includes("agent")) return "cpu";
	if (id.includes("effort") || id.includes("thought") || id.includes("reason")) return "gauge.with.dots.needle.67percent";
	if (id.includes("fast")) return "bolt";
	return "slider.horizontal.3";
}

const styles = StyleSheet.create({
	host: { alignSelf: "flex-start", height: 44 },
});
