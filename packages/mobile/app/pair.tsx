import { Feather } from "@expo/vector-icons";
import { CameraView, useCameraPermissions } from "expo-camera";
import { useFocusEffect, useLocalSearchParams, useRouter } from "expo-router";
import { useCallback, useRef, useState } from "react";
import { Linking, Platform, Pressable, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { pingServer } from "../lib/api";
import { pickNormalLens } from "../lib/cameraLens";
import { saveConfig } from "../lib/config";
import {
	classifyConnectionFailure,
	describeConnectionFailure,
	LOCAL_NETWORK_HINT,
	type ConnectionErrorCopy,
} from "../lib/connectionError";
import type { Theme } from "../lib/theme";
import { haptics } from "../lib/haptics";
import { clearOnboardingSkipped } from "../lib/onboardingStore";
import { pairFromCode } from "../lib/pairFlow";
import { isLegacyPairingCode, parsePairingCode } from "../lib/pairingCode";
import { saveHost, setActiveHost } from "../lib/hosts";
import { probeEndpoint } from "../lib/connectRuntime";
import { raceEndpoints } from "../lib/race";
import { connectSheetRoute } from "../lib/sheetResult";
import { useApp } from "../lib/store";
import { Button, NumberedStep } from "../lib/ui";
import { useTheme, useThemedStyles } from "../lib/ThemeProvider";
import { MinimalBackButton } from "../lib/MinimalBackButton";
import { MOBILE_EVENTS } from "../lib/telemetry/events";
import { mobileTelemetry } from "../lib/telemetry/runtime";

export default function PairScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const insets = useSafeAreaInsets();
	const { from } = useLocalSearchParams<{ from?: string }>();
	const fromOnboarding = from === "onboarding";
	const { reloadConfig } = useApp();
	const [permission, requestPermission] = useCameraPermissions();
	const [failure, setFailure] = useState<ConnectionErrorCopy | null>(null);
	const [busy, setBusy] = useState(false);
	// The camera keeps running under the manual-connect sheet, so a QR code in
	// frame would otherwise pair behind it. This replaces the old `manualOpen`
	// flag now that the sheet is a route pushed on top rather than local state.
	const focused = useRef(true);
	useFocusEffect(
		useCallback(() => {
			focused.current = true;
			return () => {
				focused.current = false;
			};
		}, []),
	);
	const scanned = useRef(false);
	const rejected = useRef<string | null>(null);
	const pendingCode = useRef<string | null>(null);

	const camera = useRef<CameraView>(null);
	const [lens, setLens] = useState<string | undefined>(undefined);

	async function onCameraReady() {
		if (Platform.OS !== "ios" || lens) return;
		try {
			const lenses = (await camera.current?.getAvailableLensesAsync()) ?? [];
			if (__DEV__) console.log("[pair] available lenses", lenses);
			setLens(pickNormalLens(lenses));
		} catch {
			/* keep the native default */
		}
	}

	async function finish() {
		await clearOnboardingSkipped();
		await reloadConfig(); // reconnect with the new credentials
		if (fromOnboarding) router.replace("/");
		else router.back();
	}

	async function onScan({ data }: { data: string }) {
		if (scanned.current || busy || !focused.current) return;
		// Cheap reject first: the camera sees every barcode in frame, and only a
		// code we can actually parse should stop the scanner.
		if (!parsePairingCode(data)) {
			if (rejected.current !== data) {
				rejected.current = data;
				// A v1 code is a recognisable thing, not noise: say what to do
				// about it rather than claiming it is not a pairing code.
				const reason = isLegacyPairingCode(data) ? "outdated-desktop" : "not-ao-qr";
				setFailure(describeConnectionFailure(reason, { host: "", port: "", platform: Platform.OS }));
			}
			return;
		}
		rejected.current = null;
		scanned.current = true;
		await pair(data);
	}

	// Races the code's endpoints, verifies the winner, then stores the machine.
	// The scanned code is kept so "Try again" can re-run the whole thing rather
	// than making the user re-scan.
	async function pair(code: string) {
		pendingCode.current = code;
		setBusy(true);
		setFailure(null);

		const result = await pairFromCode(code, {
			race: (endpoints, expectedHostId) => raceEndpoints(endpoints, expectedHostId, probeEndpoint),
			verify: async (config) => void (await pingServer(config)),
			saveHost,
			setActiveHost,
		});

		if (!result.ok) {
			haptics.warning();
			setFailure(
				describeConnectionFailure(
					result.reason === "not-ao-qr" ? "not-ao-qr" : classifyConnectionFailure(undefined),
					{ host: "", port: "", platform: Platform.OS },
				),
			);
			setBusy(false);
			return;
		}

		// The rest of the app still runs off ServerConfig, so the winning
		// endpoint is written there as well as into the host list.
		await saveConfig(result.config);
		mobileTelemetry()?.capture(MOBILE_EVENTS.paired, { method: "qr", from_onboarding: fromOnboarding });
		if (fromOnboarding) mobileTelemetry()?.capture(MOBILE_EVENTS.onboardingCompleted);
		haptics.success();
		await finish();
	}

	function retry() {
		setFailure(null);
		rejected.current = null;
		if (pendingCode.current) {
			void pair(pendingCode.current);
			return;
		}
		scanned.current = false;
	}

	const back = () => (router.canGoBack() ? router.back() : router.replace("/"));

	return (
		<View style={[styles.screen, { paddingTop: insets.top }]}>
			<View style={styles.topBar}><MinimalBackButton onPress={back} /></View>

			<View style={styles.steps}>
				<NumberedStep n={1} title="Open AO on your computer" compact />
				<NumberedStep n={2} title="Go to Settings → Connect Mobile" compact />
				<NumberedStep n={3} title="Scan the QR code" compact />
			</View>

			<View style={styles.viewfinder}>
				{permission?.granted ? (
					<>
						<CameraView
							ref={camera}
							style={StyleSheet.absoluteFill}
							facing="back"
							selectedLens={lens}
							onCameraReady={onCameraReady}
							barcodeScannerSettings={{ barcodeTypes: ["qr"] }}
							onBarcodeScanned={onScan}
						/>
						<Corner style={styles.cTL} />
						<Corner style={styles.cTR} />
						<Corner style={styles.cBL} />
						<Corner style={styles.cBR} />
					</>
				) : (
					<CameraGate
						// `permission` is null only while the hook resolves on first render.
						loading={!permission}
						// Once permanently denied, requestPermission is a silent no-op —
						// only the system settings page can flip it back.
						canAskAgain={permission?.canAskAgain ?? true}
						onRequest={requestPermission}
					/>
				)}
			</View>

			{failure ? (
				<View style={styles.errorBox}>
					<Feather name="alert-circle" size={15} color={t.red} />
					<View style={{ flex: 1 }}>
						<Text style={styles.errorText}>{failure.message}</Text>
						{failure.showLocalNetworkHint ? (
							<Text style={[styles.errorText, { marginTop: 6 }]}>{LOCAL_NETWORK_HINT}</Text>
						) : null}
						<View style={styles.errorActions}>
							{/* Re-arms the scanner; without it a failed scan is a dead end,
							    since the camera intentionally stops after one attempt. */}
							<Button title="Try again" variant="ghost" icon="refresh-cw" onPress={retry} />
							{failure.showLocalNetworkHint ? (
								<Button
									title="Open settings"
									variant="ghost"
									icon="settings"
									onPress={() => Linking.openSettings()}
								/>
							) : null}
						</View>
					</View>
				</View>
			) : null}

			{/* Always reachable — including when the camera is permanently denied,
			    which would otherwise leave the user with no way forward at all. */}
			<Pressable
				onPress={() => { haptics.tap(); router.push(connectSheetRoute(() => void finish())); }}
				style={[styles.manual, { paddingBottom: insets.bottom + 14 }]}
				accessibilityRole="button"
			>
				<Feather name="edit-3" size={15} color={t.textSecondary} />
				<Text style={styles.manualText}>Enter details manually</Text>
			</Pressable>
		</View>
	);
}

function Corner({ style }: { style: object }) {
	const styles = useThemedStyles(makeStyles);
	return <View style={[styles.corner, style]} />;
}

function CameraGate({
	loading,
	canAskAgain,
	onRequest,
}: {
	loading: boolean;
	canAskAgain: boolean;
	onRequest: () => void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	if (loading) {
		return (
			<View style={styles.gate}>
				<Text style={styles.gateHint}>Starting camera…</Text>
			</View>
		);
	}
	return (
		<View style={styles.gate}>
			<Feather name="camera-off" size={24} color={t.textTertiary} />
			<Text style={styles.gateTitle}>Camera access needed</Text>
			<Text style={styles.gateHint}>
				{canAskAgain
					? "AO uses the camera only to read the pairing QR code on your desktop."
					: "Camera access is turned off for AO. Enable it in system settings, or enter your details manually below."}
			</Text>
			{canAskAgain ? (
				// App Review 5.1.1(iv): the button ahead of the system permission
				// prompt must read "Continue"/"Next", never "Allow ...", so the grant
				// decision is only ever made in the system dialog itself.
				<Button title="Continue" onPress={onRequest} style={{ marginTop: 18 }} />
			) : (
				<Button
					title="Open settings"
					variant="ghost"
					icon="settings"
					onPress={() => Linking.openSettings()}
					style={{ marginTop: 18 }}
				/>
			)}
		</View>
	);
}

const CORNER = 26;
const CORNER_W = 3;

const makeStyles = (t: Theme) =>
	StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	topBar: { paddingHorizontal: 16, paddingTop: 4, paddingBottom: 8 },

	steps: { paddingHorizontal: 20, paddingBottom: 16 },

	viewfinder: {
		flex: 1,
		marginHorizontal: 16,
		borderRadius: 18,
		overflow: "hidden",
		// The viewfinder is a camera preview, so it stays dark in both themes.
		backgroundColor: "#0c0d10",
	},
	corner: {
		position: "absolute",
		width: CORNER,
		height: CORNER,
		borderColor: "rgba(255,255,255,0.92)",
	},
	cTL: { top: 14, left: 14, borderTopWidth: CORNER_W, borderLeftWidth: CORNER_W, borderTopLeftRadius: 6 },
	cTR: { top: 14, right: 14, borderTopWidth: CORNER_W, borderRightWidth: CORNER_W, borderTopRightRadius: 6 },
	cBL: { bottom: 14, left: 14, borderBottomWidth: CORNER_W, borderLeftWidth: CORNER_W, borderBottomLeftRadius: 6 },
	cBR: { bottom: 14, right: 14, borderBottomWidth: CORNER_W, borderRightWidth: CORNER_W, borderBottomRightRadius: 6 },

	gate: { flex: 1, alignItems: "center", justifyContent: "center", padding: 28 },
	gateTitle: { color: t.textPrimary, fontSize: 16, fontWeight: "700", marginTop: 12 },
	gateHint: {
		color: t.textSecondary,
		fontSize: 13,
		lineHeight: 19,
		textAlign: "center",
		marginTop: 8,
		maxWidth: 300,
	},

	errorBox: {
		flexDirection: "row",
		gap: 9,
		alignItems: "flex-start",
		backgroundColor: t.tintRed,
		borderRadius: 10,
		padding: 12,
		marginHorizontal: 16,
		marginTop: 14,
	},
	errorText: { color: t.red, fontSize: 13, lineHeight: 19 },
	errorActions: { flexDirection: "row", flexWrap: "wrap", gap: 8, marginTop: 10 },

	manual: {
		flexDirection: "row",
		alignItems: "center",
		justifyContent: "center",
		gap: 8,
		paddingTop: 18,
	},
	manualText: { color: t.textSecondary, fontSize: 15, fontWeight: "600" },
});
