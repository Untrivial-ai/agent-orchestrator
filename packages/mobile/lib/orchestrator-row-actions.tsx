import { Button, Host } from "@expo/ui";
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
				disabled={busy}
				variant="filled"
				testID={`orchestrator-${action}-${projectName}`}
				style={{ width: 88, height: 44, borderRadius: 14 }}
			/>
		</Host>
	);
}
