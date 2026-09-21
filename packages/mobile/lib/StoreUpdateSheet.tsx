import { Button, Column, Host, Row, Spacer, Text as NativeText } from "@expo/ui";
import { Platform, StyleSheet, View } from "react-native";
import { describePrompt } from "./storeUpdate";
import type { Theme } from "./theme";
import { useTheme, useThemedStyles, useThemeState } from "./ThemeProvider";
import { SheetScreen } from "./ui";

// OTA updates intentionally never use this sheet. This is the native-binary
// handoff to the App Store or Play Store, rendered with platform controls so it
// feels like the rest of the app rather than a web card inside a native sheet.
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
	const { scheme } = useThemeState();
	const styles = useThemedStyles(makeStyles);
	const storeName = Platform.OS === "ios" ? "App Store" : "Play Store";

	return (
		<SheetScreen title="Software update" subtitle={describePrompt({ version, storeConfirmed, storeName })}>
			<View style={styles.nativeWrap}>
				<Host matchContents={{ vertical: true }} style={{ width: "100%" }} colorScheme={scheme} seedColor={t.blue}>
					<Column spacing={22} style={{ width: "100%" }}>
						<Column spacing={8} style={{ width: "100%" }}>
							<NativeText textStyle={{ color: t.textPrimary, fontSize: 20, fontWeight: "700" }}>A newer AO is ready</NativeText>
							<NativeText textStyle={{ color: t.textSecondary, fontSize: 14 }}>
								Update the native app for the latest compatibility, fixes, and system integrations.
							</NativeText>
						</Column>
						<Row alignment="center" spacing={10} style={{ width: "100%" }}>
							<Button label="Not now" variant="text" onPress={onDismiss} style={{ width: 104, height: 46, borderRadius: 15 }} />
							<Spacer flexible />
							<Button label={`Open ${storeName}`} variant="filled" onPress={onUpdate} style={{ width: 170, height: 46, borderRadius: 15 }} />
						</Row>
					</Column>
				</Host>
			</View>
		</SheetScreen>
	);
}

const makeStyles = (_t: Theme) => StyleSheet.create({
	nativeWrap: { width: "100%", paddingTop: 24 },
});
