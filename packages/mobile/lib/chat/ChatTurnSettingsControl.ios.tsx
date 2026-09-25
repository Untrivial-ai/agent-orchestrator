import { Host } from "@expo/ui";
import { Button, HStack, Image, Menu, Section, Spacer, Text } from "@expo/ui/swift-ui";
import { buttonStyle, disabled as disabledModifier, font, frame, lineLimit, padding, tint, truncationMode } from "@expo/ui/swift-ui/modifiers";
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
		<Host matchContents={{ horizontal: false, vertical: true }} ignoreSafeArea="all" style={styles.host} colorScheme={scheme} seedColor={t.accent}>
			<Menu
				label={<HStack spacing={7} modifiers={[frame({ maxWidth: 1000, alignment: "leading" })]}><Text modifiers={[font({ size: 13, weight: "semibold" }), lineLimit(1), truncationMode("tail")]}>{label}</Text><Spacer /><Image systemName="chevron.right" size={iconSize.xs} /></HStack>}
				modifiers={[buttonStyle("plain"), tint(t.textSecondary), padding({ horizontal: 10, vertical: 10 }), frame({ maxWidth: 1000, minHeight: 44, alignment: "leading" }), disabledModifier(Boolean(disabled))]}
			>
				<Section title="Turn settings">
					{rows.map((row) => (
						<Menu key={row.id} label={isApprovalRow(row) ? <ApprovalRowLabel row={row} /> : `${row.label} · ${row.value}`} systemImage={settingSymbol(row)}>
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

function ApprovalRowLabel({ row }: { row: TurnSettingRow }) {
	// A single native asset stays crisp at every display scale; SwiftUI menu rows
	// discard the second image when the ring and check are separate symbols.
	return <HStack spacing={12}><Image assetName="CircleDashedCheck" /><Text>{`${row.label} · ${row.value}`}</Text></HStack>;
}

function isApprovalRow(row: TurnSettingRow): boolean {
	const id = row.id.toLowerCase();
	return row.providerKind === "permissions" || (!id.includes("model") && (id.includes("mode") || id.includes("approval")));
}

function choicesFor(row: TurnSettingRow) {
	if (row.kind !== "boolean") return row.choices;
	return [
		{ value: "on", label: "On", selected: Boolean(row.enabled) },
		{ value: "off", label: "Off", selected: !row.enabled },
	];
}

function settingSymbol(row: TurnSettingRow): string | undefined {
	const id = row.id.toLowerCase();
	if (row.providerKind === "model" || id.includes("model")) return "square.stack.3d.up";
	if (id.includes("agent")) return undefined;
	if (isApprovalRow(row)) return undefined;
	if (id.includes("effort") || id.includes("thought") || id.includes("reason")) return "gauge.with.dots.needle.67percent";
	if (id.includes("fast")) return "bolt";
	return "slider.horizontal.3";
}

const styles = StyleSheet.create({
	host: { width: "100%", height: 44 },
});
