import { Button, Column, Host, Row, Spacer, Text as NativeText } from "@expo/ui";
import { Platform, StyleSheet, View } from "react-native";
import { describePrompt } from "./storeUpdate";
import type { Theme } from "./theme";
import { useTheme, useThemedStyles, useThemeState } from "./ThemeProvider";
import { SheetScreen } from "./ui";
import { space, type } from "./tokens";

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
				<Host matchContents={{ vertical: true }} style={{ width: "100%" }} colorScheme={scheme} seedColor={t.accent}>
					<Column spacing={22} style={{ width: "100%" }}>
						<Column spacing={8} style={{ width: "100%" }}>
							<NativeText textStyle={{ fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.title3.fontSize, fontWeight: "600" }}>A newer AO is ready</NativeText>
							<NativeText textStyle={{ fontFamily: "Geist_400Regular", color: t.textSecondary, fontSize: type.subheadline.fontSize }}>
								Update the native app for the latest compatibility, fixes, and system integrations.
							</NativeText>
						</Column>
						<Row alignment="center" spacing={10} style={{ width: "100%" }}>
							<Button label="Not now" variant="text" onPress={onDismiss} style={{ width: 104, height: 46, borderRadius: 16}} />
							<Spacer flexible />
							<Button label={`Open ${storeName}`} variant="filled" onPress={onUpdate} style={{ width: 170, height: 46, borderRadius: 16}} />
						</Row>
					</Column>
				</Host>
			</View>
		</SheetScreen>
	);
}

const makeStyles = (_t: Theme) => StyleSheet.create({
	nativeWrap: { width: "100%", paddingTop: space.xxl },
});
