import { Feather } from "../icons";
import { Pressable, StyleSheet, Text, View } from "react-native";
import type { Theme } from "../theme";
import { useThemedStyles } from "../ThemeProvider";
import { iconSize, space, type } from "../tokens";

export function BrowserErrorBanner({ message, onDismiss, onRetry }: { message: string; onDismiss: () => void; onRetry: () => void }) {
	const styles = useThemedStyles(makeStyles);
	return (
		<View accessibilityRole="alert" style={styles.banner}>
			<Feather name="alert-triangle" size={iconSize.sm} color={styles.colors.red} />
			<Text numberOfLines={2} style={styles.message}>{message}</Text>
			<Pressable accessibilityRole="button" accessibilityLabel="Retry loading page" hitSlop={8} onPress={onRetry} style={styles.action}>
				<Text style={styles.actionText}>Retry</Text>
			</Pressable>
			<Pressable accessibilityRole="button" accessibilityLabel="Dismiss browser error" hitSlop={8} onPress={onDismiss} style={styles.iconButton}>
				<Feather name="x" size={iconSize.sm} color={styles.colors.textSecondary} />
			</Pressable>
		</View>
	);
}

const makeStyles = (t: Theme) => Object.assign(StyleSheet.create({
	banner: { position: "absolute", left: 12, right: 12, bottom: 78, minHeight: 44, flexDirection: "row", alignItems: "center", gap: space.sm, borderRadius: 12, borderCurve: "continuous", borderWidth: 1, borderColor: t.tintRed, backgroundColor: t.bgElevated, paddingHorizontal: space.md, paddingVertical: space.sm },
	message: { fontFamily: "Geist_400Regular", flex: 1, color: t.textSecondary, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight },
	action: { minHeight: 28, justifyContent: "center", borderRadius: 7, borderCurve: "continuous", backgroundColor: t.accentTint, paddingHorizontal: space.sm },
	actionText: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.caption2.fontSize, fontWeight: "600" },
	iconButton: { minWidth: 28, minHeight: 28, alignItems: "center", justifyContent: "center" },
}), { colors: t });
