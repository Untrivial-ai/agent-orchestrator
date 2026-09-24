import { Host, Picker, Switch } from "@expo/ui";
import { Feather } from "../lib/icons";
import * as Application from "expo-application";
import * as Clipboard from "expo-clipboard";
import * as Device from "expo-device";
import Constants from "expo-constants";
import { useFocusEffect, useRouter } from "expo-router";
import * as Updates from "expo-updates";
import { Children, useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { ActivityIndicator, Alert, Linking, Platform, Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { ApiError, pingServer } from "../lib/api";
import { formatVersionLine, type BuildInfo } from "../lib/appInfo";
import { bugReportClipboard, bugReportOpenUrl, bugReportUrl } from "../lib/bugReport";
import { DEFAULT_CONFIG, isConfigured, loadConfig, type ServerConfig } from "../lib/config";
import { classifyConnectionFailure, describeConnectionFailure } from "../lib/connectionError";
import { discordFeatureRequestURL } from "../lib/discord";
import { forgetServer } from "../lib/disconnect";
import { haptics } from "../lib/haptics";
import { toggleLayoutGrid, useLayoutGrid } from "../lib/layoutGrid";
import { checkStore, openOrStartUpdate } from "../lib/inAppUpdates";
import { describePrompt } from "../lib/storeUpdate";
import { NativeHeaderButton } from "../lib/native-header-button";
import { openGitHub } from "../lib/openGitHub";
import { getPushStatus, openNotificationSettings, registerForPush, unregisterFromPush } from "../lib/push";
import { describePushToggle, describeRegisterFailure, type PushStatus } from "../lib/pushStatus";
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

// Light / Dark / System, in the desktop app's order and wording.
const THEME_OPTIONS: { value: ThemePreference; icon: keyof typeof Feather.glyphMap }[] = [
	{ value: "light", icon: "sun" },
	{ value: "dark", icon: "moon" },
	{ value: "system", icon: "smartphone" },
];
import { useTheme, useThemedStyles, useThemeState } from "../lib/ThemeProvider";
import { checkAndDownload, describeUpdateRow, type UpdateOutcome } from "../lib/updates";
import { VERSION_FLOOR } from "../lib/versionFloor";
import { type, space } from "../lib/tokens";
import { backOr } from "../lib/backNavigation";


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

	if (!loaded) return <View style={styles.center}><ActivityIndicator color={t.accent} /></View>;

	const paired = isConfigured(cfg);
	return (
		<View style={styles.screen} collapsable={false}>
			<View style={styles.header}>
				<Text style={styles.headerTitle}>Settings</Text>
				<View style={styles.closeButton}>
					<NativeHeaderButton icon="close" label="Close settings" onPress={() => backOr(router)} />
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

				{__DEV__ ? (
					<SettingsSection title="Developer" footer="Layout grid draws the app's 4pt steps, with the 44pt control lines emphasised.">
						<SettingsCard>
							<LayoutGridRow />
						</SettingsCard>
					</SettingsSection>
				) : null}

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

/** The theme options, inline inside the Settings sheet on Android. */
function ThemeChoices({ preference, onSelect }: { preference: ThemePreference; onSelect(next: ThemePreference): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return (
		<View style={styles.inlineChoices}>
			{THEME_OPTIONS.map((option) => {
				const selected = preference === option.value;
				return (
					<Pressable
						key={option.value}
						accessibilityRole="button"
						accessibilityState={{ selected }}
						onPress={() => { haptics.select(); onSelect(option.value); }}
						style={({ pressed }) => [styles.inlineChoice, pressed && { opacity: 0.6 }]}
					>
						<Feather name={option.icon} size={15} color={selected ? t.textPrimary : t.textTertiary} />
						<Text style={[styles.inlineChoiceLabel, selected && { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontWeight: "600" }]}>{preferenceLabel(option.value)}</Text>
						{selected ? <Feather name="check" size={15} color={t.textPrimary} /> : null}
					</Pressable>
				);
			})}
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
			<Feather name={icon} size={17} color={disabled ? t.textFaint : t.textSecondary} style={styles.rowIcon} />
			<Text style={[styles.rowLabel, disabled && styles.disabled]} numberOfLines={1}>{label}</Text>
			{right ?? (loading ? <ActivityIndicator size="small" color={t.textTertiary} /> : (
				<>
					{value ? <Text style={[styles.rowValue, valueColor ? { color: valueColor } : null]} numberOfLines={1}>{value}</Text> : null}
					{onPress ? <Feather name="chevron-right" size={17} color={t.textFaint} /> : null}
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

function LayoutGridRow() {
	const active = useLayoutGrid();
	return (
		<CardRow
			icon="grid"
			label="Layout grid"
			value={active ? "On" : "Off"}
			onPress={() => {
				haptics.select();
				toggleLayoutGrid();
			}}
		/>
	);
}

function AppearanceRow() {
	const t = useTheme();
	const { preference, scheme, setPreference } = useThemeState();
	const [open, setOpen] = useState(false);
	if (Platform.OS === "android") {
		// Settings is itself a sheet on Android, so the choices expand in place
		// rather than opening a second sheet on top of it.
		return (
			<>
				<CardRow
					icon="sun"
					label="Appearance"
					value={preferenceLabel(preference)}
					onPress={() => { haptics.tap(); setOpen((value) => !value); }}
				/>
				{open ? <ThemeChoices preference={preference} onSelect={(next) => { setPreference(next); setOpen(false); }} /> : null}
			</>
		);
	}
	return (
		<CardRow
			icon="sun"
			label="Appearance"
			right={
				<Host style={{ width: 96, height: 38 }} colorScheme={scheme} seedColor={t.accent}>
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
					<Host style={{ width: 54, height: 34 }} colorScheme={scheme} seedColor={t.accent}>
						<Switch value={toggle.value} disabled={toggle.disabled} onValueChange={onToggle} />
					</Host>
				)
			}
		/>
	);
}

function SoftwareUpdateRow() {
	const t = useTheme();
	const { isUpdatePending, isChecking, isDownloading } = Updates.useUpdates();
	const [otaManual, setOtaManual] = useState<UpdateOutcome | null>(null);
	const [manualBusy, setManualBusy] = useState(false);
	const [storeChecking, setStoreChecking] = useState(false);
	const [storeLast, setStoreLast] = useState<StoreRowResult | null>(null);
	const [storeCheck, setStoreCheck] = useState<StoreCheck | null>(null);
	const [storePrompt, setStorePrompt] = useState<{ version?: string; storeConfirmed: boolean; check: StoreCheck | null } | null>(null);

	const ota = describeUpdateRow({
		enabled: Updates.isEnabled,
		pending: isUpdatePending,
		phase: isDownloading ? "downloading" : isChecking || manualBusy ? "checking" : "idle",
		lastManual: otaManual,
	});
	const store = describeStoreRow({ enabled: !__DEV__, checking: storeChecking, last: storeLast });
	const row = describeSoftwareUpdateRow({ ota, store });

	// Shown in the Settings sheet rather than as a second sheet over it. Away
	// from Settings the nudge still gets its own sheet route — there it is the
	// only thing on screen.
	function presentStoreSheet(check: StoreCheck | null) {
		const confirmed = check?.updateAvailable === true;
		setStorePrompt({
			version: confirmed && Platform.OS === "ios" ? check?.storeVersion : floorTarget(VERSION_FLOOR),
			storeConfirmed: confirmed,
			check,
		});
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
		<>
			<CardRow
				icon="download-cloud"
				label="Software update"
				value={row.value}
				valueColor={row.tone === "good" ? t.green : row.tone === "bad" ? t.red : undefined}
				loading={row.busy}
				onPress={row.action === null ? undefined : onPress}
			/>
			{storePrompt ? (
				<InlinePanel
					title="A newer AO is ready"
					copy={describePrompt({ version: storePrompt.version, storeConfirmed: storePrompt.storeConfirmed, storeName: Platform.OS === "ios" ? "App Store" : "Play Store" })}
					primary={`Open ${Platform.OS === "ios" ? "App Store" : "Play Store"}`}
					secondary="Not now"
					onPrimary={() => { const check = storePrompt.check; setStorePrompt(null); void openOrStartUpdate(check); }}
					onSecondary={() => setStorePrompt(null)}
				/>
			) : null}
		</>
	);
}

/** A confirm step that opens inside the Settings sheet instead of over it. */
function InlinePanel({ title, copy, primary, secondary, onPrimary, onSecondary }: { title: string; copy: string; primary: string; secondary: string; onPrimary(): void; onSecondary(): void }) {
	const styles = useThemedStyles(makeStyles);
	return (
		<View style={styles.inlinePanel}>
			<Text style={styles.inlinePanelTitle}>{title}</Text>
			<Text style={styles.inlinePanelCopy}>{copy}</Text>
			<View style={styles.inlinePanelActions}>
				<Pressable accessibilityRole="button" onPress={() => { haptics.tap(); onSecondary(); }} style={({ pressed }) => [styles.inlinePanelAction, pressed && { opacity: 0.7 }]}>
					<Text style={styles.inlinePanelActionLabel}>{secondary}</Text>
				</Pressable>
				<Pressable accessibilityRole="button" onPress={() => { haptics.tap(); onPrimary(); }} style={({ pressed }) => [styles.inlinePanelAction, styles.inlinePanelPrimary, pressed && { opacity: 0.85 }]}>
					<Text style={styles.inlinePanelPrimaryLabel}>{primary}</Text>
				</Pressable>
			</View>
		</View>
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
	const { config, connection } = useApp();
	// Says what just happened, so the copy is discoverable if the form comes up empty.
	const [copied, setCopied] = useState(false);
	function report() {
		const environment = {
			build: buildInfo(),
			platform: Platform.OS,
			osVersion: Platform.Version,
			deviceModel: Device.modelName,
			paired: !!config && isConfigured(config),
			connection,
		};
		// Copied as well as prefilled: a signed-out browser, or a GitHub app that
		// intercepts the link anyway, can drop the form and leave the reporter
		// typing into an empty box with no idea what to include.
		void Clipboard.setStringAsync(bugReportClipboard(environment));
		setCopied(true);
		void openGitHub(bugReportOpenUrl(bugReportUrl(environment), Platform.OS));
	}
	return <CardRow icon="help-circle" label="Report a problem" value={copied ? "Details copied" : undefined} onPress={report} />;
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
			{forgetting ? <ActivityIndicator color={t.red} /> : <Feather name="log-out" size={17} color={t.red} />}
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
	header: { height: 64, alignItems: "center", justifyContent: "center", paddingHorizontal: space.lg },
	headerTitle: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.title3.fontSize, lineHeight: type.title3.lineHeight, fontWeight: "600", letterSpacing: -0.3 },
	closeButton: { position: "absolute", right: 14, top: 10 },
	content: { paddingHorizontal: space.lg, paddingTop: space.xs, paddingBottom: space.xxxl, gap: space.lg },
	section: { gap: space.xs },
	sectionTitle: { fontFamily: "Geist_600SemiBold", color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "600", paddingHorizontal: space.sm },
	sectionFooter: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight, paddingHorizontal: space.sm },
	card: { backgroundColor: t.bgElevated, borderRadius: 16, borderCurve: "continuous", overflow: "hidden" },
	separator: { height: StyleSheet.hairlineWidth, backgroundColor: t.borderSubtle, marginLeft: 50 },
	// Choices that expand inside a row's own card, indented under its label so
	// they read as belonging to the row above rather than as a new group.
	inlineChoices: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle, backgroundColor: t.bgSubtle, paddingVertical: space.hair },
	inlineChoice: { minHeight: 44, flexDirection: "row", alignItems: "center", gap: space.sm, paddingLeft: 50, paddingRight: space.md },
	inlineChoiceLabel: { fontFamily: "Geist_400Regular", flex: 1, color: t.textSecondary, fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight },
	inlinePanel: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle, backgroundColor: t.bgSubtle, paddingHorizontal: space.md, paddingVertical: space.md, gap: space.sm },
	inlinePanelTitle: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight, fontWeight: "600" },
	inlinePanelCopy: { fontFamily: "Geist_400Regular", color: t.textSecondary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight },
	inlinePanelActions: { flexDirection: "row", gap: space.sm },
	inlinePanelAction: { minHeight: 38, justifyContent: "center", paddingHorizontal: space.md, borderRadius: 8, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault },
	inlinePanelPrimary: { backgroundColor: t.textPrimary, borderColor: t.textPrimary },
	inlinePanelActionLabel: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.footnote.fontSize, fontWeight: "600" },
	inlinePanelPrimaryLabel: { fontFamily: "Geist_600SemiBold", color: t.bgBase, fontSize: type.footnote.fontSize, fontWeight: "600" },
	row: { minHeight: 52, flexDirection: "row", alignItems: "center", paddingHorizontal: space.md, gap: space.sm },
	rowPressed: { backgroundColor: t.bgElevatedHover },
	rowIcon: { fontFamily: "Geist_400Regular", width: 26, textAlign: "center" },
	rowLabel: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight, fontWeight: "600", flex: 1 },
	rowValue: { fontFamily: "Geist_400Regular", color: t.textSecondary, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, maxWidth: "42%" },
	disabled: { opacity: 0.45 },
	disconnect: { minHeight: 52, flexDirection: "row", alignItems: "center", gap: space.sm, paddingHorizontal: space.md, borderRadius: 16, borderCurve: "continuous" },
	disconnectText: { fontFamily: "Geist_600SemiBold", color: t.red, fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight, fontWeight: "600" },
	versionFooter: { fontFamily: "Geist_400Regular", color: t.textFaint, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight, textAlign: "center", marginTop: -6 },
});
