import { Host } from "@expo/ui";
import { Button } from "@expo/ui/swift-ui";
import {
	accessibilityIdentifier,
	accessibilityLabel,
	buttonBorderShape,
	buttonStyle,
	controlSize,
	disabled as disabledModifier,
	tint,
} from "@expo/ui/swift-ui/modifiers";
import { useTheme, useThemeState } from "./ThemeProvider";
import type { OrchestratorRowActionProps } from "./orchestrator-row-actions.types";

export function OrchestratorRowAction({ action, projectName, busy, onPress }: OrchestratorRowActionProps) {
	const t = useTheme();
	const { scheme } = useThemeState();
	const label = action === "resume" ? "Resume" : "Start";
	return (
		<Host style={{ width: 88, height: 44 }} colorScheme={scheme} seedColor={t.blue}>
			<Button
				label={busy ? `${label}…` : label}
				onPress={onPress}
				modifiers={[
					buttonStyle("glassProminent"),
					controlSize("large"),
					buttonBorderShape("roundedRectangle"),
					tint(t.blue),
					disabledModifier(busy),
					accessibilityLabel(`${label} orchestrator for ${projectName}`),
					accessibilityIdentifier(`orchestrator-${action}-${projectName}`),
				]}
			/>
		</Host>
	);
}
