import { Feather } from "@expo/vector-icons";
import { Pressable, StyleSheet, Text } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { turnSettingsSummary } from "./turnSettingsModel";
import type { ChatTurnSettingsControlProps } from "./ChatTurnSettingsControl.types";

export function ChatTurnSettingsControl({ snapshot, models, options, disabled, onOpenFallback }: ChatTurnSettingsControlProps) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const label = turnSettingsSummary(snapshot, models, options);
	return <Pressable accessibilityRole="button" accessibilityLabel={`Turn settings, ${label}`} accessibilityState={{ disabled }} disabled={disabled} onPress={() => { haptics.tap(); onOpenFallback(); }} style={styles.control}><Text numberOfLines={1} style={styles.label}>{label}</Text><Feather name="chevron-right" size={14} color={t.textTertiary} /></Pressable>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	control: { alignSelf: "flex-start", maxWidth: "100%", minHeight: 44, flexDirection: "row", alignItems: "center", gap: 7, paddingHorizontal: 10 },
	label: { flexShrink: 1, color: t.textSecondary, fontSize: 13, fontWeight: "600" },
});
