import { Feather } from "@expo/vector-icons";
import { Platform, Pressable, StyleSheet, Text, View } from "react-native";
import { describePrompt } from "./storeUpdate";
import { useTheme } from "./ThemeProvider";
import { SheetScreen } from "./ui";

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
				<View style={[styles.icon, { backgroundColor: t.tintBlue }]}>
					<Feather name="download-cloud" size={24} color={t.blue} />
				</View>
				<View style={styles.copy}>
					<Text style={[styles.title, { color: t.textPrimary }]}>A newer AO is ready</Text>
					<Text style={[styles.message, { color: t.textSecondary }]}>Update the native app for the latest compatibility, fixes, and system integrations.</Text>
				</View>
				<View style={styles.actions}>
					<Pressable onPress={onDismiss} android_ripple={{ color: t.bgSubtle }} style={styles.secondaryAction}>
						<Text style={[styles.actionLabel, { color: t.textPrimary }]}>Not now</Text>
					</Pressable>
					<Pressable onPress={onUpdate} android_ripple={{ color: "rgba(255,255,255,0.18)" }} style={[styles.primaryAction, { backgroundColor: t.blue }]}>
						<Text style={[styles.actionLabel, { color: t.onAccent }]}>Open {storeName}</Text>
					</Pressable>
				</View>
			</View>
		</SheetScreen>
	);
}

const styles = StyleSheet.create({
	content: { paddingTop: 20, gap: 16 },
	icon: { width: 48, height: 48, borderRadius: 16, alignItems: "center", justifyContent: "center" },
	copy: { gap: 6 },
	title: { fontSize: 20, lineHeight: 25, fontWeight: "700" },
	message: { fontSize: 14, lineHeight: 20 },
	actions: { flexDirection: "row", justifyContent: "flex-end", gap: 8, paddingTop: 4 },
	secondaryAction: { height: 44, minWidth: 92, paddingHorizontal: 16, borderRadius: 14, alignItems: "center", justifyContent: "center", overflow: "hidden" },
	primaryAction: { height: 44, minWidth: 148, paddingHorizontal: 16, borderRadius: 14, alignItems: "center", justifyContent: "center", overflow: "hidden" },
	actionLabel: { fontSize: 14, fontWeight: "700" },
});
