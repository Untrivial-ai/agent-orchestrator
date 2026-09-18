import { Host, Picker, Switch } from "@expo/ui";
import { Feather } from "@expo/vector-icons";
import * as Application from "expo-application";
import Constants from "expo-constants";
import { useFocusEffect, useRouter } from "expo-router";
import * as Updates from "expo-updates";
import { Children, useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { ActivityIndicator, Alert, Linking, Platform, Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { ApiError, pingServer } from "../lib/api";
import { bugReportBody, formatVersionLine, type BuildInfo } from "../lib/appInfo";
import { DEFAULT_CONFIG, isConfigured, loadConfig, type ServerConfig } from "../lib/config";
import { classifyConnectionFailure, describeConnectionFailure } from "../lib/connectionError";
import { discordFeatureRequestURL } from "../lib/discord";
import { forgetServer } from "../lib/disconnect";
import { haptics } from "../lib/haptics";
import { checkStore, openOrStartUpdate } from "../lib/inAppUpdates";
import { NativeHeaderButton } from "../lib/native-header-button";
import { openGitHub } from "../lib/openGitHub";
import { getPushStatus, openNotificationSettings, registerForPush, unregisterFromPush } from "../lib/push";
import { describePushToggle, describeRegisterFailure, type PushStatus } from "../lib/pushStatus";
import { storeUpdateSheetRoute } from "../lib/sheetResult";
import { useApp } from "../lib/store";
import {
	describeSoftwareUpdateRow,
	describeStoreRow,
	floorSignal,
	floorTarget,
	storeRowResult,
	tierOf,
	type StoreCheck,
	type StoreRowResult,
} from "../lib/storeUpdate";
import type { Theme } from "../lib/theme";
import { preferenceLabel, type ThemePreference } from "../lib/themePreference";
import { useTheme, useThemedStyles, useThemeState } from "../lib/ThemeProvider";
import { checkAndDownload, describeUpdateRow, type UpdateOutcome } from "../lib/updates";
import { VERSION_FLOOR } from "../lib/versionFloor";

const ISSUES_URL = "https://github.com/AgentWrapper/agent-orchestrator/issues/new";

export { RouteErrorBoundary as ErrorBoundary } from "../lib/RouteErrorBoundary";

export default function SettingsScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const { reloadConfig } = useApp();
	const scrollRef = useRef<ScrollView>(null);
	const [cfg, setCfg] = useState<ServerConfig>(DEFAULT_CONFIG);
	const [loaded, setLoaded] = useState(false);

	useFocusEffect(useCallback(() => {
		loadConfig().then((saved) => {
			setCfg(saved);
			setLoaded(true);
		});
	}, []));

	if (!loaded) return <View style={styles.center}><ActivityIndicator color={t.blue} /></View>;

	const paired = isConfigured(cfg);
	return (
		<View style={styles.screen} collapsable={false}>
			<View style={styles.header}>
				<Text style={styles.headerTitle}>Settings</Text>
				<View style={styles.closeButton}>
					<NativeHeaderButton icon="close" label="Close settings" onPress={() => router.back()} />
				</View>
			</View>
			<ScrollView
				ref={scrollRef}
				contentInsetAdjustmentBehavior="automatic"
				contentContainerStyle={styles.content}
				keyboardShouldPersistTaps="handled"
			>
				<SettingsSection title="Desktop" footer={paired ? `${cfg.host}:${cfg.httpPort}` : "Pair this phone with AO on your computer."}>
					<SettingsCard>
						<CardRow
							icon="monitor"
							label="Connected desktop"
							value={paired ? "Paired" : "Set up"}
							onPress={() => router.navigate("/pair")}
						/>
						<ConnectionTestRow cfg={cfg} paired={paired} />
					</SettingsCard>
				</SettingsSection>

				<SettingsSection title="Preferences">
					<SettingsCard>
						<AppearanceRow />
						<NotificationsRow />
					</SettingsCard>
				</SettingsSection>

				<SettingsSection title="Updates" footer="AO installs compatible updates automatically. Native releases open in your app store.">
					<SettingsCard><SoftwareUpdateRow /></SettingsCard>
				</SettingsSection>

				<SettingsSection title="Support">
					<SettingsCard>
						<ReportProblemRow />
						<FeatureRequestRow />
					</SettingsCard>
				</SettingsSection>

				<DisconnectRow
					onForget={async () => {
						await forgetServer();
						await reloadConfig();
						router.replace("/onboarding");
					}}
				/>
				<VersionFooter />
			</ScrollView>
		</View>
	);
}

function SettingsSection({ title, footer, children }: { title: string; footer?: string; children: ReactNode }) {
	const styles = useThemedStyles(makeStyles);
	return (
		<View style={styles.section}>
			<Text style={styles.sectionTitle}>{title}</Text>
			{children}
			{footer ? <Text style={styles.sectionFooter}>{footer}</Text> : null}
		</View>
	);
}

function SettingsCard({ children }: { children: ReactNode }) {
	const styles = useThemedStyles(makeStyles);
	const rows = Children.toArray(children);
	return (
		<View style={styles.card}>
			{rows.map((row, index) => (
				<View key={(row as { key?: string }).key ?? index}>
					{index > 0 ? <View style={styles.separator} /> : null}
					{row}
				</View>
			))}
		</View>
	);
}

function CardRow({
	icon,
	label,
	value,
	valueColor,
	onPress,
	disabled = false,
	loading = false,
	right,
}: {
	icon: keyof typeof Feather.glyphMap;
	label: string;
	value?: string;
	valueColor?: string;
	onPress?: () => void;
	disabled?: boolean;
	loading?: boolean;
	right?: ReactNode;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const content = (
		<>
			<Feather name={icon} size={18} color={disabled ? t.textFaint : t.textSecondary} style={styles.rowIcon} />
			<Text style={[styles.rowLabel, disabled && styles.disabled]} numberOfLines={1}>{label}</Text>
			{right ?? (loading ? <ActivityIndicator size="small" color={t.textTertiary} /> : (
				<>
					{value ? <Text style={[styles.rowValue, valueColor ? { color: valueColor } : null]} numberOfLines={1}>{value}</Text> : null}
					{onPress ? <Feather name="chevron-right" size={18} color={t.textFaint} /> : null}
				</>
			))}
		</>
	);
	if (!onPress) return <View style={styles.row}>{content}</View>;
	return <Pressable disabled={disabled || loading} onPress={() => { haptics.tap(); onPress(); }} style={({ pressed }) => [styles.row, pressed && styles.rowPressed, disabled && styles.disabled]}>{content}</Pressable>;
}

function ConnectionTestRow({ cfg, paired }: { cfg: ServerConfig; paired: boolean }) {
	const t = useTheme();
	const [testing, setTesting] = useState(false);
	const [result, setResult] = useState<{ ok: boolean; msg: string } | null>(null);

	useEffect(() => setResult(null), [cfg.host, cfg.httpPort]);

	async function test() {
		setTesting(true);
		setResult(null);
		try {
			await pingServer(cfg);
			haptics.success();
			setResult({ ok: true, msg: "Connected" });
		} catch (error) {
			haptics.error();
			const status = error instanceof ApiError ? error.status : undefined;
			const { title } = describeConnectionFailure(classifyConnectionFailure(status), {
				host: cfg.host,
				port: cfg.httpPort,
				platform: Platform.OS,
			});
			setResult({ ok: false, msg: title });
		} finally {
			setTesting(false);
		}
	}

	const value = testing ? "Testing…" : result?.msg ?? "Test now";
	const color = result ? (result.ok ? t.green : t.red) : t.textSecondary;
	return (
		<CardRow
			icon="activity"
			label="Test connection"
			value={value}
			valueColor={color}
			disabled={!paired}
			loading={testing}
			onPress={paired ? test : undefined}
		/>
	);
}

function AppearanceRow() {
	const t = useTheme();
	const router = useRouter();
	const { preference, scheme, setPreference } = useThemeState();
	if (Platform.OS === "android") {
		return (
			<CardRow
				icon="sun"
				label="Appearance"
				value={preferenceLabel(preference)}
				onPress={() => router.push("/sheets/theme")}
			/>
		);
	}
	return (
		<CardRow
			icon="sun"
			label="Appearance"
			right={
				<Host style={{ width: 96, height: 38 }} colorScheme={scheme} seedColor={t.blue}>
					<Picker
						selectedValue={preference}
						onValueChange={(value) => {
							haptics.select();
							setPreference(String(value) as ThemePreference);
						}}
						appearance="menu"
						testID="settings-appearance"
					>
						<Picker.Item label="System" value="system" />
						<Picker.Item label="Light" value="light" />
						<Picker.Item label="Dark" value="dark" />
					</Picker>
				</Host>
			}
		/>
	);
}

function NotificationsRow() {
	const t = useTheme();
	const { scheme } = useThemeState();
	const { config, connection } = useApp();
	const [status, setStatus] = useState<PushStatus | null>(null);
	const [busy, setBusy] = useState(false);
	const refresh = useCallback(() => { getPushStatus().then(setStatus).catch(() => {}); }, []);

	useFocusEffect(useCallback(() => refresh(), [refresh]));
	useEffect(() => refresh(), [connection, refresh]);
	const toggle = describePushToggle(status, config);

	async function onToggle(next: boolean) {
		if (toggle.blocked) {
			Alert.alert("Notifications are blocked", "Allow notifications for AO in your system settings, then come back.", [
				{ text: "Not now", style: "cancel" },
				{ text: "Open settings", onPress: openNotificationSettings },
			]);
			return;
		}
		setBusy(true);
		try {
			if (!next) {
				await unregisterFromPush();
				haptics.tap();
			} else if (config) {
				const registered = await registerForPush(config, { ask: true });
				if (registered.ok) haptics.success();
				else {
					haptics.error();
					const { title, message } = describeRegisterFailure(registered.reason, Platform.OS, registered.status);
					Alert.alert(title, message);
				}
			}
		} finally {
			setBusy(false);
			refresh();
		}
	}

	return (
		<CardRow
			icon="bell"
			label="Agent notifications"
			disabled={toggle.disabled}
			right={
				busy ? <ActivityIndicator size="small" color={t.textTertiary} /> : (
					<Host style={{ width: 54, height: 34 }} colorScheme={scheme} seedColor={t.blue}>
						<Switch value={toggle.value} disabled={toggle.disabled} onValueChange={onToggle} />
					</Host>
				)
			}
		/>
	);
}

function SoftwareUpdateRow() {
	const t = useTheme();
	const router = useRouter();
	const { isUpdatePending, isChecking, isDownloading } = Updates.useUpdates();
	const [otaManual, setOtaManual] = useState<UpdateOutcome | null>(null);
	const [manualBusy, setManualBusy] = useState(false);
	const [storeChecking, setStoreChecking] = useState(false);
	const [storeLast, setStoreLast] = useState<StoreRowResult | null>(null);
	const [storeCheck, setStoreCheck] = useState<StoreCheck | null>(null);

	const ota = describeUpdateRow({
		enabled: Updates.isEnabled,
		pending: isUpdatePending,
		phase: isDownloading ? "downloading" : isChecking || manualBusy ? "checking" : "idle",
		lastManual: otaManual,
	});
	const store = describeStoreRow({ enabled: !__DEV__, checking: storeChecking, last: storeLast });
	const row = describeSoftwareUpdateRow({ ota, store });

	function presentStoreSheet(check: StoreCheck | null) {
		const floor = floorSignal(Application.nativeApplicationVersion, VERSION_FLOOR);
		const confirmed = check?.updateAvailable === true;
		const version = confirmed && Platform.OS === "ios" ? check?.storeVersion : floorTarget(VERSION_FLOOR);
		router.push(storeUpdateSheetRoute({
			version,
			storeConfirmed: confirmed,
			onAction: (action) => {
				if (action === "update") void openOrStartUpdate(check);
			},
		}));
	}

	async function onPress() {
		if (row.action === "store") {
			presentStoreSheet(storeCheck);
			return;
		}
		if (row.action === "restart") {
			try { await Updates.reloadAsync(); } catch (error) { console.warn("[updates] reload failed", error); }
			return;
		}
		if (row.action !== "check") return;

		setManualBusy(Updates.isEnabled);
		setStoreChecking(!__DEV__);
		setOtaManual(null);
		setStoreLast(null);
		try {
			const [otaResult, nativeResult] = await Promise.all([
				checkAndDownload(Updates),
				__DEV__ ? Promise.resolve<StoreCheck | null>(null) : checkStore(),
			]);
			setOtaManual(otaResult);
			if (!__DEV__) {
				setStoreCheck(nativeResult);
				const floor = floorSignal(Application.nativeApplicationVersion, VERSION_FLOOR);
				const nativeOutcome = storeRowResult(nativeResult, tierOf(nativeResult, Platform.OS, floor));
				setStoreLast(nativeOutcome);
				if (nativeOutcome.kind === "available") presentStoreSheet(nativeResult);
				else if (nativeOutcome.kind === "error" || otaResult.kind === "error") haptics.error();
				else haptics.success();
			} else if (otaResult.kind === "error") haptics.error();
			else haptics.success();
		} finally {
			setManualBusy(false);
			setStoreChecking(false);
		}
	}

	return (
		<CardRow
			icon="download-cloud"
			label="Software update"
			value={row.value}
			valueColor={row.tone === "good" ? t.green : row.tone === "bad" ? t.red : undefined}
			loading={row.busy}
			onPress={row.action === null ? undefined : onPress}
		/>
	);
}

function buildInfo(): BuildInfo {
	return {
		version: Application.nativeApplicationVersion ?? Constants.expoConfig?.version,
		build: Application.nativeBuildVersion,
		updateId: Updates.updateId,
		channel: Updates.channel,
		runtimeVersion: Updates.runtimeVersion,
		embedded: Updates.isEnabled ? Updates.isEmbeddedLaunch : undefined,
	};
}

function ReportProblemRow() {
	function report() {
		const body = encodeURIComponent(bugReportBody(buildInfo(), Platform.OS, Platform.Version));
		void openGitHub(`${ISSUES_URL}?body=${body}`);
	}
	return <CardRow icon="help-circle" label="Report a problem" onPress={report} />;
}

function FeatureRequestRow() {
	return (
		<CardRow
			icon="message-circle"
			label="Request a feature"
			value="Discord"
			onPress={() => { void Linking.openURL(discordFeatureRequestURL()).catch(() => {}); }}
		/>
	);
}

function DisconnectRow({ onForget }: { onForget: () => Promise<void> }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [forgetting, setForgetting] = useState(false);
	function confirmForget() {
		Alert.alert("Disconnect from desktop?", "This phone will stop receiving notifications and its saved connection will be removed.", [
			{ text: "Cancel", style: "cancel" },
			{
				text: "Disconnect",
				style: "destructive",
				onPress: async () => {
					setForgetting(true);
					try { await onForget(); } finally { setForgetting(false); }
				},
			},
		]);
	}
	return (
		<Pressable
			disabled={forgetting}
			onPress={() => { haptics.warning(); confirmForget(); }}
			style={({ pressed }) => [styles.disconnect, pressed && styles.rowPressed]}
		>
			{forgetting ? <ActivityIndicator color={t.red} /> : <Feather name="log-out" size={18} color={t.red} />}
			<Text style={styles.disconnectText}>{forgetting ? "Disconnecting…" : "Disconnect from desktop"}</Text>
		</Pressable>
	);
}

function VersionFooter() {
	const styles = useThemedStyles(makeStyles);
	return <Text style={styles.versionFooter}>AO {formatVersionLine(buildInfo())}</Text>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	center: { flex: 1, alignItems: "center", justifyContent: "center", backgroundColor: t.bgBase },
	header: { height: 64, alignItems: "center", justifyContent: "center", paddingHorizontal: 16 },
	headerTitle: { color: t.textPrimary, fontSize: 20, lineHeight: 26, fontWeight: "800", letterSpacing: -0.3 },
	closeButton: { position: "absolute", right: 14, top: 10 },
	content: { paddingHorizontal: 16, paddingTop: 6, paddingBottom: 32, gap: 18 },
	section: { gap: 7 },
	sectionTitle: { color: t.textTertiary, fontSize: 12, lineHeight: 16, fontWeight: "600", paddingHorizontal: 10 },
	sectionFooter: { color: t.textTertiary, fontSize: 11, lineHeight: 16, paddingHorizontal: 10 },
	card: { backgroundColor: t.bgElevated, borderRadius: 16, borderCurve: "continuous", overflow: "hidden" },
	separator: { height: StyleSheet.hairlineWidth, backgroundColor: t.borderSubtle, marginLeft: 50 },
	row: { minHeight: 52, flexDirection: "row", alignItems: "center", paddingHorizontal: 14, gap: 10 },
	rowPressed: { backgroundColor: t.bgElevatedHover },
	rowIcon: { width: 26, textAlign: "center" },
	rowLabel: { color: t.textPrimary, fontSize: 15, lineHeight: 20, fontWeight: "600", flex: 1 },
	rowValue: { color: t.textSecondary, fontSize: 13, lineHeight: 18, maxWidth: "42%" },
	disabled: { opacity: 0.45 },
	disconnect: { minHeight: 52, flexDirection: "row", alignItems: "center", gap: 10, paddingHorizontal: 14, borderRadius: 16, borderCurve: "continuous" },
	disconnectText: { color: t.red, fontSize: 15, lineHeight: 20, fontWeight: "600" },
	versionFooter: { color: t.textFaint, fontSize: 10, lineHeight: 14, textAlign: "center", marginTop: -6 },
});
