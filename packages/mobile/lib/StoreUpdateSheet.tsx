import { Platform, StyleSheet, Text, View } from "react-native";
import { describePrompt } from "./storeUpdate";
import type { Theme } from "./theme";
import { useThemedStyles } from "./ThemeProvider";
import { Button, SheetScreen } from "./ui";

// The soft tier of the store-update prompt: dismissible, rate-limited by
// `storeUpdate.ts`, and the only tier iOS has. The insistent tier is Play's own
// fullscreen updater, which Google renders — there is no custom screen for it.

export function StoreUpdateSheet({
	version,
	storeConfirmed,
	onUpdate,
	onDismiss,
}: {
	/**
	 * The version to name. Absent on Android when it came from the store, where
	 * the answer is a versionCode — an internal number that means nothing here.
	 */
	version?: string;
	/** Whether the store itself reported the update; false when only the floor did. */
	storeConfirmed: boolean;
	onUpdate: () => void;
	onDismiss: () => void;
}) {
	const s = useThemedStyles(makeStyles);
	const storeName = Platform.OS === "ios" ? "App Store" : "Play Store";

	return (
		<SheetScreen title="Update available" subtitle={describePrompt({ version, storeConfirmed, storeName })}>
			<View style={s.body}>
				<Text style={s.blurb}>
					Update to the latest version for the best experience.
				</Text>
				<Button title="Update" icon="download" onPress={onUpdate} />
				<Button title="Not now" variant="ghost" onPress={onDismiss} />
			</View>
		</SheetScreen>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		body: { paddingTop: 12, gap: 12 },
		blurb: { color: t.textSecondary, fontSize: 14, lineHeight: 20 },
	});
