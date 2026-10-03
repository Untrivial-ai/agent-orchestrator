import { Feather } from "./icons";
import { useState } from "react";
import { Linking, Platform, Pressable, ScrollView, StyleSheet, Switch, Text, TextInput, View } from "react-native";
import { ApiError, pingServer } from "./api";
import { DEFAULT_CONFIG, saveConfig, type ServerConfig } from "./config";
import { saveHost, setActiveHost, renameHost, type Host } from "./hosts";
import { adoptManualConnection, editedManualHost } from "./manualConnect";
import { configForEndpoint } from "./connect";
import { normalizeServerHost } from "./endpoints";
import { probeEndpoint, probeIdentity } from "./connectRuntime";
import { IncompatibleHostVersionError } from "./race";
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
import { iconSize, space, touchTarget, type } from "./tokens";

// The typing fallback behind the QR scanner, also reused to edit a pairing.
// The legacy mux port is unused against the Go daemon, so it stays hidden.
export function ManualConnectSheet({ onConnected, editingHost, editingEndpointIndex = 0 }: {
	onConnected: () => void;
	editingHost?: Host;
	editingEndpointIndex?: number;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	// New pairings start blank; editing preloads only the pairing the user selected.
	const [cfg, setCfg] = useState<ServerConfig>(() => {
		const endpoint = editingHost?.endpoints[editingEndpointIndex];
		return endpoint ? configForEndpoint(endpoint, editingHost.token, editingHost.id) : { ...DEFAULT_CONFIG, password: editingHost?.token ?? "" };
	});
	const [machineName, setMachineName] = useState(editingHost?.name ?? "");
	const [busy, setBusy] = useState(false);
	const [failure, setFailure] = useState<ConnectionErrorCopy | null>(null);
	const [showPassword, setShowPassword] = useState(false);

	const set = (k: keyof ServerConfig) => (v: string) => setCfg((prev) => ({ ...prev, [k]: v }));

	async function connect() {
		setBusy(true);
		setFailure(null);
		const target = { ...cfg, host: normalizeServerHost(cfg.host), httpPort: cfg.httpPort.trim() };
		try {
			if (editingHost) {
				const edited = editedManualHost(editingHost, target, machineName, editingEndpointIndex);
				const before = editingHost.endpoints[editingEndpointIndex];
				const after = edited.endpoints[editingEndpointIndex];
				const connectionChanged = !before || before.host !== after.host || before.port !== after.port ||
					before.secure !== after.secure || editingHost.token !== target.password;
				if (connectionChanged) {
					// Check identity before presenting a stored password to a new address.
					const { hostId } = await probeEndpoint(after, new AbortController().signal);
					if (editingHost.id && hostId !== editingHost.id) {
						setFailure({
							title: "Different machine",
							message: "That address belongs to another machine. This pairing was not changed.",
							icon: "alert-circle",
							showLocalNetworkHint: false,
						});
						haptics.warning();
						return;
					}
					await pingServer({ ...target, hostId: editingHost.id || hostId });
					await saveHost(edited);
				} else if (edited.name !== editingHost.name) {
					await renameHost(editingHost.id, edited.name);
				}
				haptics.success();
				onConnected();
				return;
			}
			// Identity is public; reject unsupported hosts before presenting a password.
			let hostId = "";
			try { hostId = await probeIdentity(target); }
			catch (error) { if (error instanceof IncompatibleHostVersionError) throw error; }
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
				identity: async () => hostId,
				saveHost,
				setActiveHost,
			}, machineName);
			mobileTelemetry()?.capture(MOBILE_EVENTS.paired, { method: "manual" });
			haptics.success();
			onConnected();
		} catch (e) {
			haptics.warning();
			const status = e instanceof IncompatibleHostVersionError ? 426 : e instanceof ApiError ? e.status : undefined;
			const copy = describeConnectionFailure(classifyConnectionFailure(status), {
				host: target.host,
				port: target.httpPort,
				platform: Platform.OS,
			});
			setFailure(editingHost && classifyConnectionFailure(status) === "auth"
				? { ...copy, message: "That password was rejected. Check it and try again." }
				: copy);
		} finally {
			setBusy(false);
		}
	}

	const validPort = /^\d+$/.test(cfg.httpPort) && Number(cfg.httpPort) >= 1 && Number(cfg.httpPort) <= 65535;
	const title = editingHost ? "Edit connection" : "Connect manually";
	const subtitle = editingHost
		? "Update this machine's name, address, port, password, or TLS setting."
		: "Enter the address and password from Connect Mobile or ao remote-host enable.";
	const form = (
		<>
			<Field
				label={editingHost ? "MACHINE NAME" : "MACHINE NAME (OPTIONAL)"}
				value={machineName}
				onChangeText={setMachineName}
				placeholder="AzureLinux"
				maxLength={48}
				returnKeyType="next"
			/>
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
			<View style={styles.field}>
				<Text style={styles.fieldLabel}>PASSWORD</Text>
				<View style={styles.passwordRow}>
					<TextInput
						value={cfg.password}
						onChangeText={set("password")}
						placeholder="Connection password"
						placeholderTextColor={t.textFaint}
						selectionColor={t.accent}
						autoCapitalize="none"
						autoCorrect={false}
						secureTextEntry={!showPassword}
						style={[styles.input, styles.passwordInput]}
					/>
					<Pressable
						accessibilityRole="button"
						accessibilityLabel={showPassword ? "Hide password" : "Show password"}
						onPress={() => {
							haptics.tap();
							setShowPassword((visible) => !visible);
						}}
						style={({ pressed }) => [styles.passwordToggle, pressed && styles.passwordTogglePressed]}
					>
						<Feather name={showPassword ? "eye-off" : "eye"} size={iconSize.lg} color={t.textSecondary} />
					</Pressable>
				</View>
			</View>

			<View style={styles.toggleRow}>
				<Text style={styles.toggleLabel}>Use TLS (https / wss)</Text>
				<Switch
					value={!!cfg.secure}
					onValueChange={(v) => setCfg((prev) => ({ ...prev, secure: v }))}
					trackColor={{ true: t.green, false: t.borderStrong }}
				/>
			</View>

			{failure ? (
				<View style={styles.errorBox}>
					<Feather name="alert-circle" size={iconSize.sm} color={t.red} />
					<View style={{ flex: 1 }}>
						<Text style={styles.errorText}>{failure.message}</Text>
						{failure.showLocalNetworkHint ? (
							<>
								<Text style={[styles.errorText, { marginTop: space.xs }]}>{LOCAL_NETWORK_HINT}</Text>
								<Button
									title="Open settings"
									variant="ghost"
									icon="settings"
									onPress={() => Linking.openSettings()}
									style={{ marginTop: space.sm }}
								/>
							</>
						) : null}
					</View>
				</View>
			) : null}

			<Button
				title={editingHost ? "Save changes" : "Connect"}
				icon={editingHost ? "check" : "link"}
				loading={busy}
				disabled={!cfg.host.trim() || !validPort || (!!editingHost && !machineName.trim())}
				onPress={connect}
				style={{ marginTop: space.lg }}
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
					title={title}
					subtitle={subtitle}
				/>
				{form}
			</ScrollView>
		);
	}

	// iOS lifts a presented form sheet over the keyboard by itself.
	return (
		<SheetScreen title={title} subtitle={subtitle}>
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
			<TextInput {...input} style={styles.input} placeholderTextColor={t.textFaint} selectionColor={t.accent} />
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgSurface },
		field: { marginTop: space.lg },
		fieldLabel: { fontFamily: "Geist_600SemiBold",
			color: t.textTertiary,
			fontSize: type.caption2.fontSize,
			fontWeight: "600",
			letterSpacing: 1.1,
			marginBottom: space.xs,
		},
		input: { fontFamily: "Geist_400Regular",
			backgroundColor: t.bgElevated,
			borderWidth: 1,
			borderColor: t.borderDefault,
			borderRadius: 8, borderCurve: "continuous",
			paddingHorizontal: space.md,
			paddingVertical: space.md,
			color: t.textPrimary,
			fontSize: type.subheadline.fontSize,
		},
		passwordRow: {
			flexDirection: "row",
			alignItems: "center",
			backgroundColor: t.bgElevated,
			borderWidth: 1,
			borderColor: t.borderDefault,
			borderRadius: 8, borderCurve: "continuous",
		},
		passwordInput: { flex: 1, backgroundColor: "transparent", borderWidth: 0, paddingRight: 0 },
		passwordToggle: {
			width: touchTarget,
			minHeight: touchTarget,
			alignItems: "center",
			justifyContent: "center",
		},
		passwordTogglePressed: { opacity: 0.6 },
		toggleRow: {
			flexDirection: "row",
			alignItems: "center",
			justifyContent: "space-between",
			marginTop: space.lg,
		},
		toggleLabel: { fontFamily: "Geist_400Regular", color: t.textSecondary, fontSize: type.subheadline.fontSize, flex: 1 },
		errorBox: {
			flexDirection: "row",
			gap: space.sm,
			alignItems: "flex-start",
			backgroundColor: t.tintRed,
			borderRadius: 8, borderCurve: "continuous",
			padding: space.md,
			marginTop: space.lg,
		},
		errorText: { fontFamily: "Geist_400Regular", color: t.red, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight },
	});
