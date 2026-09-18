import { StyleSheet, Text, View } from "react-native";
import { AgentLogo } from "../AgentLogo";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";

export function ConversationTitle({
	title,
	subtitle,
	harness,
	state,
}: {
	title: string;
	subtitle: string;
	harness?: string | null;
	state?: string;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const stateColor = state === "busy" ? t.orange : state === "ready" ? t.green : state === "stopped" ? t.red : t.amber;

	return (
		<View style={styles.header}>
			<AgentLogo harness={harness} size={22} />
			<View style={styles.copy}>
				<Text numberOfLines={1} style={styles.title}>{title}</Text>
				<View style={styles.subtitleRow}>
					<Text numberOfLines={1} style={styles.subtitle}>{subtitle}</Text>
					{state ? <View style={[styles.dot, { backgroundColor: stateColor }]} /> : null}
				</View>
			</View>
		</View>
	);
}

const makeStyles = (t: Theme) => StyleSheet.create({
	header: { maxWidth: 240, flexDirection: "row", alignItems: "center", gap: 8 },
	copy: { minWidth: 0, flexShrink: 1, alignItems: "flex-start", justifyContent: "center" },
	title: { maxWidth: "100%", color: t.textPrimary, fontSize: 15, lineHeight: 19, fontWeight: "700" },
	subtitleRow: { maxWidth: "100%", flexDirection: "row", alignItems: "center", gap: 6 },
	subtitle: { flexShrink: 1, color: t.textTertiary, fontSize: 10, lineHeight: 14 },
	dot: { width: 7, height: 7, borderRadius: 4 },
});
