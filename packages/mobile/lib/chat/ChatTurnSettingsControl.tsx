import { Feather } from "../icons";
import { Pressable, StyleSheet, Text } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { turnSettingsSummary } from "./turnSettingsModel";
import type { ChatTurnSettingsControlProps } from "./ChatTurnSettingsControl.types";
import { iconSize, space, type } from "../tokens";

export function ChatTurnSettingsControl({ snapshot, models, options, disabled, onOpenFallback }: ChatTurnSettingsControlProps) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const label = turnSettingsSummary(snapshot, models, options);
	return <Pressable accessibilityRole="button" accessibilityLabel={`Turn settings, ${label}`} accessibilityState={{ disabled }} disabled={disabled} onPress={() => { haptics.tap(); onOpenFallback(); }} style={styles.control}><Text numberOfLines={1} style={styles.label}>{label}</Text><Feather name="chevron-right" size={iconSize.sm} color={t.textTertiary} /></Pressable>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	// Stretched, not content-sized: the label is the longest thing in the composer's
	// meta row, and a content-sized box let it decide how wide it wanted to be
	// rather than how wide it was allowed. `overflow: "hidden"` is the backstop —
	// with it, a label that fails to truncate is clipped instead of drawn across the
	// context meter beside it.
	control: { alignSelf: "stretch", maxWidth: "100%", minHeight: 44, flexDirection: "row", alignItems: "center", gap: space.xs, paddingHorizontal: space.sm, overflow: "hidden" },
	label: { fontFamily: "Geist_600SemiBold", flexShrink: 1, color: t.textSecondary, fontSize: type.footnote.fontSize, fontWeight: "600" },
});
