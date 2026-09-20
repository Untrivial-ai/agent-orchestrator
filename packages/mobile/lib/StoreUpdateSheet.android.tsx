import { Feather } from "@expo/vector-icons";
import { Platform, Pressable, StyleSheet, Text, View } from "react-native";
import { describePrompt } from "./storeUpdate";
import { useTheme } from "./ThemeProvider";
import { SheetScreen } from "./ui";
import { iconSize, space, type } from "./tokens";

export function StoreUpdateSheet({
	version,
	storeConfirmed,
	onUpdate,
	onDismiss,
}: {
	version?: string;
	storeConfirmed: boolean;
	onUpdate: () => void;
	onDismiss: () => void;
}) {
	const t = useTheme();
	const storeName = Platform.OS === "ios" ? "App Store" : "Play Store";
	return (
		<SheetScreen title="Software update" subtitle={describePrompt({ version, storeConfirmed, storeName })}>
			<View style={styles.content}>
				<View style={[styles.icon, { backgroundColor: t.accentTint }]}>
					<Feather name="download-cloud" size={iconSize.xl} color={t.accent} />
				</View>
				<View style={styles.copy}>
					<Text style={[styles.title, { color: t.textPrimary }]}>A newer AO is ready</Text>
					<Text style={[styles.message, { color: t.textSecondary }]}>Update the native app for the latest compatibility, fixes, and system integrations.</Text>
				</View>
				<View style={styles.actions}>
					<Pressable onPress={onDismiss} android_ripple={{ color: t.bgSubtle }} style={styles.secondaryAction}>
						<Text style={[styles.actionLabel, { color: t.textPrimary }]}>Not now</Text>
					</Pressable>
					<Pressable onPress={onUpdate} android_ripple={{ color: "rgba(255,255,255,0.18)" }} style={[styles.primaryAction, { backgroundColor: t.accent }]}>
						<Text style={[styles.actionLabel, { color: t.onAccent }]}>Open {storeName}</Text>
					</Pressable>
				</View>
			</View>
		</SheetScreen>
	);
}

const styles = StyleSheet.create({
	content: { paddingTop: space.xl, gap: space.lg },
	icon: { width: 48, height: 48, borderRadius: 16, borderCurve: "continuous", alignItems: "center", justifyContent: "center" },
	copy: { gap: space.xs },
	title: { fontFamily: "Geist_600SemiBold", fontSize: type.title3.fontSize, lineHeight: type.title3.lineHeight, fontWeight: "600" },
	message: { fontFamily: "Geist_400Regular", fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight },
	actions: { flexDirection: "row", justifyContent: "flex-end", gap: space.sm, paddingTop: space.xxs },
	secondaryAction: { height: 44, minWidth: 92, paddingHorizontal: space.lg, borderRadius: 12, borderCurve: "continuous", alignItems: "center", justifyContent: "center", overflow: "hidden" },
	primaryAction: { height: 44, minWidth: 148, paddingHorizontal: space.lg, borderRadius: 12, borderCurve: "continuous", alignItems: "center", justifyContent: "center", overflow: "hidden" },
	actionLabel: { fontFamily: "Geist_600SemiBold", fontSize: type.subheadline.fontSize, fontWeight: "600" },
});
