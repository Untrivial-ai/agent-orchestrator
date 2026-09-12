import { Feather } from "@expo/vector-icons";
import { useEffect, useState } from "react";
import { Linking, Platform, ScrollView, StyleSheet, Switch, Text, TextInput, View } from "react-native";
import { ApiError, pingServer } from "./api";
import { DEFAULT_CONFIG, loadConfig, saveConfig, type ServerConfig } from "./config";
import { saveHost, setActiveHost } from "./hosts";
import { adoptManualConnection } from "./manualConnect";
import { probeIdentity } from "./connectRuntime";
import {
	classifyConnectionFailure,
	describeConnectionFailure,
	LOCAL_NETWORK_HINT,
	type ConnectionErrorCopy,
} from "./connectionError";
import type { Theme } from "./theme";
import { haptics } from "./haptics";
import { Button, SHEET_SCROLL_CONTENT, SheetHeader, SheetScreen } from "./ui";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { MOBILE_EVENTS } from "./telemetry/events";
import { mobileTelemetry } from "./telemetry/runtime";

// The typing fallback behind the QR scanner: Tailscale users and anyone whose
// desktop isn't in front of them. Deliberately narrower than the Settings form —
// it omits the legacy "TERMINAL PORT" (`muxPort`), which is unused against the
// Go daemon, and it collapses Settings' three buttons ("Scan QR", "Test
// connection", "Save & connect") into one: during onboarding there is no reason
// to make someone test and save as separate acts.
export function ManualConnectSheet({ onConnected }: { onConnected: () => void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [cfg, setCfg] = useState<ServerConfig>(DEFAULT_CONFIG);
	const [busy, setBusy] = useState(false);
	const [failure, setFailure] = useState<ConnectionErrorCopy | null>(null);

	// Load whatever is already saved when the sheet opens, so a user who closes it
	// to re-try the scanner doesn't lose what they typed. Mount is the open now
	// that this is a route rather than an always-rendered component.
	useEffect(() => {
		loadConfig().then(setCfg);
	}, []);

	const set = (k: keyof ServerConfig) => (v: string) => setCfg((prev) => ({ ...prev, [k]: v }));

	async function connect() {
		setBusy(true);
		setFailure(null);
		const target = { ...cfg, host: cfg.host.trim() };
		try {
			// Verify BEFORE persisting. pingServer takes the config it is handed, so
			// nothing needs to be saved to test it — and saving first would leave
			// known-bad credentials on disk. The background poller retries every 8s,
			// and the daemon locks a device out for a minute after 5 failed auths,
			// so a persisted wrong password locks the user out on its own in ~40s,
			// with no further input from them.
			await pingServer(target);
			await saveConfig(target);
			// Store it as a machine and select it. Writing only the legacy config
			// left resolution reconnecting the previously active machine on the
			// next launch, so a manual connection silently did not stick.
			await adoptManualConnection(target, {
				identity: async (c) => {
					try {
						return await probeIdentity(c);
					} catch {
						return ""; // Older daemon, or an endpoint that cannot say: still worth storing.
					}
				},
				saveHost,
				setActiveHost,
			});
			mobileTelemetry()?.capture(MOBILE_EVENTS.paired, { method: "manual" });
			haptics.success();
			onConnected();
		} catch (e) {
			haptics.warning();
			const status = e instanceof ApiError ? e.status : undefined;
			setFailure(
				describeConnectionFailure(classifyConnectionFailure(status), {
					host: target.host,
					port: target.httpPort,
					platform: Platform.OS,
				}),
			);
		} finally {
			setBusy(false);
		}
	}

	const form = (
		<>
			<Field
				label="HOST"
				value={cfg.host}
				onChangeText={set("host")}
				placeholder="192.168.x.x  or  my-pc.tailXXXX.ts.net"
				autoCapitalize="none"
				autoCorrect={false}
				keyboardType="url"
			/>
			<Field label="API PORT" value={cfg.httpPort} onChangeText={set("httpPort")} keyboardType="number-pad" />
			<Field
				label="PASSWORD"
				value={cfg.password}
				onChangeText={set("password")}
				placeholder="Connection password"
				autoCapitalize="none"
				secureTextEntry
			/>

			<View style={styles.toggleRow}>
				<Text style={styles.toggleLabel}>Use TLS (https / wss)</Text>
				<Switch
					value={!!cfg.secure}
					onValueChange={(v) => setCfg((prev) => ({ ...prev, secure: v }))}
					trackColor={{ true: t.blue, false: t.borderStrong }}
				/>
			</View>

			{failure ? (
				<View style={styles.errorBox}>
					<Feather name="alert-circle" size={15} color={t.red} />
					<View style={{ flex: 1 }}>
						<Text style={styles.errorText}>{failure.message}</Text>
						{failure.showLocalNetworkHint ? (
							<>
								<Text style={[styles.errorText, { marginTop: 6 }]}>{LOCAL_NETWORK_HINT}</Text>
								<Button
									title="Open settings"
									variant="ghost"
									icon="settings"
									onPress={() => Linking.openSettings()}
									style={{ marginTop: 10 }}
								/>
							</>
						) : null}
					</View>
				</View>
			) : null}

			<Button
				title="Connect"
				icon="link"
				loading={busy}
				disabled={!cfg.host.trim()}
				onPress={connect}
				style={{ marginTop: 16 }}
			/>
		</>
	);

	// Android's sheet is sized by detents rather than by its content (see
	// CONNECT_SHEET_OPTIONS — two detents are what lets the OS expand it for the
	// keyboard). A detent gives the content a definite height, so the form scrolls
	// within it and the Connect button stays reachable once the sheet expands.
	if (Platform.OS !== "ios") {
		return (
			<ScrollView
				style={styles.screen}
				contentContainerStyle={SHEET_SCROLL_CONTENT}
				keyboardShouldPersistTaps="handled"
			>
				<SheetHeader
					title="Connect manually"
					subtitle="Enter your computer's address from AO → Settings → Connect Mobile."
				/>
				{form}
			</ScrollView>
		);
	}

	// iOS lifts a presented form sheet over the keyboard by itself.
	return (
		<SheetScreen title="Connect manually" subtitle="Enter your computer's address from AO → Settings → Connect Mobile.">
			{form}
		</SheetScreen>
	);
}

function Field({ label, ...input }: { label: string } & React.ComponentProps<typeof TextInput>) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return (
		<View style={styles.field}>
			<Text style={styles.fieldLabel}>{label}</Text>
			<TextInput {...input} style={styles.input} placeholderTextColor={t.textFaint} selectionColor={t.blue} />
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgSurface },
		field: { marginTop: 16 },
		fieldLabel: {
			color: t.textTertiary,
			fontSize: 10,
			fontWeight: "700",
			letterSpacing: 1.1,
			marginBottom: 7,
		},
		input: {
			backgroundColor: t.bgElevated,
			borderWidth: 1,
			borderColor: t.borderDefault,
			borderRadius: 10,
			paddingHorizontal: 13,
			paddingVertical: 12,
			color: t.textPrimary,
			fontSize: 15,
		},
		toggleRow: {
			flexDirection: "row",
			alignItems: "center",
			justifyContent: "space-between",
			marginTop: 18,
		},
		toggleLabel: { color: t.textSecondary, fontSize: 14, flex: 1 },
		errorBox: {
			flexDirection: "row",
			gap: 9,
			alignItems: "flex-start",
			backgroundColor: t.tintRed,
			borderRadius: 10,
			padding: 12,
			marginTop: 16,
		},
		errorText: { color: t.red, fontSize: 13, lineHeight: 19 },
	});
