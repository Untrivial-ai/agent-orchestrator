import * as Application from "expo-application";
import Constants from "expo-constants";
import { useFocusEffect, useRouter } from "expo-router";
import * as Updates from "expo-updates";
import { useCallback, useEffect, useState } from "react";
import { ActivityIndicator, Alert, Platform, ScrollView, StyleSheet, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { ApiError, pingServer } from "../../lib/api";
import { bugReportBody, formatVersionLine, type BuildInfo } from "../../lib/appInfo";
import { DEFAULT_CONFIG, isConfigured, loadConfig, type ServerConfig } from "../../lib/config";
import { classifyConnectionFailure, describeConnectionFailure } from "../../lib/connectionError";
import { forgetServer } from "../../lib/disconnect";
import type { Theme } from "../../lib/theme";
import { haptics } from "../../lib/haptics";
import { projectSheetRoute } from "../../lib/sheetResult";
import { preferenceLabel } from "../../lib/themePreference";
import { getPushStatus, openNotificationSettings, registerForPush, unregisterFromPush } from "../../lib/push";
import { describePushToggle, describeRegisterFailure, type PushStatus } from "../../lib/pushStatus";
import { openGitHub } from "../../lib/openGitHub";
import { useApp } from "../../lib/store";
import { checkAndDownload, describeUpdateRow, type UpdateOutcome } from "../../lib/updates";
import { useTabScrollToTop } from "../../lib/useTabScrollToTop";
import { Dot, ScreenHeader, SettingsGroup, SettingsRow, SettingsToggle } from "../../lib/ui";
import { useTheme, useThemedStyles, useThemeState } from "../../lib/ThemeProvider";

const ISSUES_URL = "https://github.com/AgentWrapper/agent-orchestrator/issues/new";

export default function SettingsScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const insets = useSafeAreaInsets();
	const router = useRouter();
	const { reloadConfig, projects, connection, activeProjectId, setActiveProject } = useApp();
	const scrollRef = useTabScrollToTop<ScrollView>();

	const [cfg, setCfg] = useState<ServerConfig>(DEFAULT_CONFIG);
	const [loaded, setLoaded] = useState(false);
	const { preference } = useThemeState();

	// Reload the saved config every time the screen regains focus — not just on
	// mount — so returning from the pairing flow (which writes host/port to
	// storage then navigates back here) repaints the rows with the new values.
	useFocusEffect(
		useCallback(() => {
			loadConfig().then((c) => {
				setCfg(c);
				setLoaded(true);
			});
		}, []),
	);

	if (!loaded) {
		return (
			<View style={styles.center}>
				<ActivityIndicator color={t.blue} />
			</View>
		);
	}

	const paired = isConfigured(cfg);
	const activeProject = projects.find((p) => p.id === activeProjectId);

	return (
		<View style={styles.screen}>
			<View style={{ height: insets.top }} />
			<ScreenHeader title="Settings" status={connection} />
			<ScrollView
				ref={scrollRef}
				contentContainerStyle={{ padding: 16, paddingBottom: 120 }}
				keyboardShouldPersistTaps="handled"
			>
				<ConnectionSection cfg={cfg} paired={paired} connection={connection} />

				<SettingsGroup title="Projects" footer="Scopes the Agents and PRs tabs.">
					<SettingsRow
						icon="folder"
						label="Active project"
						value={activeProject?.name ?? "All projects"}
						onPress={() =>
							router.push(
								projectSheetRoute({
									selected: activeProjectId,
									onSelect: (id) => {
										// Picking a project scopes the board and takes you there —
										// the choice and its effect land in one step.
										setActiveProject(id);
										router.navigate("/");
									},
								}),
							)
						}
					/>
				</SettingsGroup>

				<SettingsGroup title="Appearance">
					<SettingsRow
						icon="moon"
						label="Theme"
						value={preferenceLabel(preference)}
						onPress={() => router.push("/sheets/theme")}
					/>
				</SettingsGroup>

				<NotificationsSection />

				<AboutSection
					onForget={async () => {
						await forgetServer();
						await reloadConfig();
						router.replace("/onboarding");
					}}
				/>
			</ScrollView>
		</View>
	);
}

// Connection is one row, not a form. `/pair` already owns the whole flow —
// camera scan, permission fallbacks, and the "Enter details manually" sheet that
// opens prefilled from the saved config — so editing a connection and creating
// one go through the same door instead of two divergent forms.
function ConnectionSection({
	cfg,
	paired,
	connection,
}: {
	cfg: ServerConfig;
	paired: boolean;
	connection: string;
}) {
	const t = useTheme();
	const router = useRouter();
	const [testing, setTesting] = useState(false);
	const [result, setResult] = useState<{ ok: boolean; msg: string } | null>(null);

	// Drop a stale failure once the background poller reports a live connection,
	// so the row doesn't keep showing a scary error while the app is connected.
	useEffect(() => {
		if (connection === "open") setResult((r) => (r && !r.ok ? null : r));
	}, [connection]);

	const dotColor =
		connection === "open" ? t.green : connection === "connecting" ? t.amber : t.textFaint;

	async function test() {
		setTesting(true);
		setResult(null);
		try {
			const count = await pingServer(cfg);
			haptics.success();
			setResult({ ok: true, msg: `Connected — ${count} session${count === 1 ? "" : "s"}` });
		} catch (e) {
			haptics.error();
			const status = e instanceof ApiError ? e.status : undefined;
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

	return (
		<SettingsGroup
			title="Connection"
			footer="Your PC's Tailscale name / 100.x address, or its LAN IP on the same Wi-Fi."
		>
			<SettingsRow
				icon="link"
				label="Connect AO"
				value={paired ? `${cfg.host}:${cfg.httpPort}` : "Not connected"}
				leading={paired ? <Dot color={dotColor} size={7} breathing={connection === "connecting"} /> : undefined}
				onPress={() => router.navigate("/pair")}
			/>
			<SettingsRow
				icon="activity"
				label="Test connection"
				value={result?.msg}
				valueColor={result ? (result.ok ? t.green : t.red) : undefined}
				loading={testing}
				disabled={!paired}
				onPress={test}
			/>
		</SettingsGroup>
	);
}

// Push collapsed to a single switch. The old card offered up to three different
// buttons (Enable / Register / Open settings) for what a user thinks of as one
// setting; `describePushToggle` folds those states into one control plus a
// footer that explains where it currently stands.
function NotificationsSection() {
	const router = useRouter();
	const { config, connection } = useApp();
	const [status, setStatus] = useState<PushStatus | null>(null);
	const [busy, setBusy] = useState(false);

	const refresh = useCallback(() => {
		getPushStatus()
			.then(setStatus)
			.catch(() => {});
	}, []);

	// Reload on focus and whenever the connection flips — registration happens
	// automatically on a successful connect, so the state can change without any
	// action on this screen.
	useFocusEffect(useCallback(() => refresh(), [refresh]));
	useEffect(() => refresh(), [connection, refresh]);

	const toggle = describePushToggle(status, config);

	async function onToggle(next: boolean) {
		// A permanent denial can only be undone in system settings; the OS will
		// not let the app prompt again, so say so rather than failing silently.
		if (toggle.blocked) {
			Alert.alert(
				"Notifications are blocked",
				"Allow notifications for AO in your system settings, then come back.",
				[{ text: "Not now", style: "cancel" }, { text: "Open settings", onPress: openNotificationSettings }],
			);
			return;
		}
		setBusy(true);
		try {
			if (!next) {
				await unregisterFromPush();
				haptics.tap();
			} else if (config) {
				// A deliberate tap is the right moment to spend the one-shot OS prompt.
				const result = await registerForPush(config, { ask: true });
				if (result.ok) {
					haptics.success();
				} else {
					haptics.error();
					const { title, message } = describeRegisterFailure(result.reason, Platform.OS, result.status);
					Alert.alert(title, message);
				}
			}
		} finally {
			setBusy(false);
			refresh();
		}
	}

	return (
		<SettingsGroup title="Notifications" footer={toggle.footer}>
			<SettingsToggle
				icon="bell"
				label="Agent notifications"
				value={toggle.value}
				disabled={toggle.disabled}
				busy={busy}
				onValueChange={onToggle}
			/>
			<SettingsRow icon="clock" label="History" onPress={() => router.navigate("/notifications")} />
		</SettingsGroup>
	);
}

function AboutSection({ onForget }: { onForget: () => Promise<void> }) {
	const [forgetting, setForgetting] = useState(false);

	// From the binary: with EAS-managed build numbers app.json has no buildNumber.
	const build: BuildInfo = {
		version: Application.nativeApplicationVersion ?? Constants.expoConfig?.version,
		build: Application.nativeBuildVersion,
		updateId: Updates.updateId,
		channel: Updates.channel,
		runtimeVersion: Updates.runtimeVersion,
		embedded: Updates.isEnabled ? Updates.isEmbeddedLaunch : undefined,
	};

	// Routed through openGitHub for consistency, though this one always lands in
	// the browser: the GitHub app has no deep link that accepts a prefilled issue
	// body, and the attached diagnostics are the point of this button.
	function report() {
		const body = encodeURIComponent(bugReportBody(build, Platform.OS, Platform.Version));
		void openGitHub(`${ISSUES_URL}?body=${body}`);
	}

	function confirmForget() {
		Alert.alert(
			"Disconnect & forget server?",
			"This device will stop receiving notifications and the saved address and password will be removed.",
			[
				{ text: "Cancel", style: "cancel" },
				{
					text: "Disconnect",
					style: "destructive",
					onPress: async () => {
						setForgetting(true);
						try {
							await onForget();
						} finally {
							setForgetting(false);
						}
					},
				},
			],
		);
	}

	return (
		<SettingsGroup title="About">
			<SettingsRow icon="info" label="Version" value={formatVersionLine(build)} />
			<UpdateRow />
			<SettingsRow icon="mail" label="Report a problem" onPress={report} />
			<SettingsRow
				icon="power"
				label="Disconnect & forget server"
				destructive
				loading={forgetting}
				onPress={confirmForget}
			/>
		</SettingsGroup>
	);
}

// Check on demand, or restart into a downloaded update.
function UpdateRow() {
	const t = useTheme();
	const { isUpdatePending, isChecking, isDownloading } = Updates.useUpdates();
	const [manual, setManual] = useState<UpdateOutcome | null>(null);
	const [busy, setBusy] = useState(false);

	const row = describeUpdateRow({
		enabled: Updates.isEnabled,
		pending: isUpdatePending,
		phase: isDownloading ? "downloading" : isChecking || busy ? "checking" : "idle",
		lastManual: manual,
	});

	async function onPress() {
		if (row.action === "restart") {
			try {
				await Updates.reloadAsync();
			} catch (e) {
				console.warn("[updates] reload failed", e);
			}
			return;
		}
		setBusy(true);
		setManual(null);
		try {
			const outcome = await checkAndDownload(Updates);
			setManual(outcome);
			if (outcome.kind === "error") haptics.error();
			else haptics.success();
		} finally {
			setBusy(false);
		}
	}

	return (
		<SettingsRow
			icon="download-cloud"
			label="App updates"
			value={row.value}
			valueColor={row.tone === "good" ? t.green : row.tone === "bad" ? t.red : undefined}
			loading={row.busy}
			onPress={row.action ? onPress : undefined}
		/>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	center: { flex: 1, alignItems: "center", justifyContent: "center", backgroundColor: t.bgBase },
});
