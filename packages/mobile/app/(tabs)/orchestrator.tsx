import { useRouter } from "expo-router";
import { useMemo, useState } from "react";
import { ActivityIndicator, Alert, Platform, Pressable, RefreshControl, ScrollView, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { AgentLogo } from "../../lib/AgentLogo";
import { ApiError, type DashboardSession, type OrchestratorLink } from "../../lib/api";
import { classifyConnectionFailure, describeConnectionFailure } from "../../lib/connectionError";
import { chatErrorCopy, isChatPreflightError } from "../../lib/chatError";
import { haptics } from "../../lib/haptics";
import { launchIntent, orchestratorState, orchestratorStatus, workersOf, zoneCounts } from "../../lib/orchestratorView";
import { useApp } from "../../lib/store";
import { attentionMetaFor, type AttentionLevel, type Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { useTabScrollToTop } from "../../lib/useTabScrollToTop";
import { Button, cardShell, Dot, EmptyState, IconButton, ScreenHeader } from "../../lib/ui";

const ZONE_ORDER: AttentionLevel[] = ["merge", "respond", "review", "pending", "working", "done"];

export default function OrchestratorScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const insets = useSafeAreaInsets();
	const { configured, loading, error, errorStatus, connection, config, projects, sessions, orchestrators, refresh } =
		useApp();
	const [refreshing, setRefreshing] = useState(false);

	const scrollRef = useTabScrollToTop<ScrollView>();
	const failure = useMemo(
		() =>
			describeConnectionFailure(classifyConnectionFailure(errorStatus ?? undefined), {
				host: config?.host ?? "",
				port: config?.httpPort ?? "",
				platform: Platform.OS,
			}),
		[errorStatus, config?.host, config?.httpPort],
	);

	const onRefresh = async () => {
		haptics.tap();
		setRefreshing(true);
		await refresh();
		setRefreshing(false);
	};

	if (!configured) {
		return (
			<View style={styles.screen}>
				<View style={{ height: insets.top }} />
				<EmptyState icon="share-2" title="No server" message="Connect to AO in Settings." />
			</View>
		);
	}

	return (
		<View style={styles.screen}>
			<View style={{ height: insets.top }} />
			<ScreenHeader title="Orchestrator" status={connection} />

			{loading && projects.length === 0 ? (
				<View style={styles.center}>
					<ActivityIndicator color={t.blue} />
				</View>
			) : (
				<ScrollView
					ref={scrollRef}
					contentContainerStyle={{ paddingBottom: 110, paddingTop: 4 }}
					refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={t.blue} />}
				>
					{/* Deliberately every project, ignoring the active-project filter the
					    other tabs honour: this tab is the one cross-project overview. */}
					{projects.length === 0 ? (
						error ? (
							<EmptyState
								icon="wifi-off"
								title={failure.title}
								message={failure.message}
								action={<Button title="Retry" icon="refresh-cw" variant="ghost" onPress={onRefresh} />}
							/>
						) : (
							<EmptyState icon="folder" title="No projects" message="Add a project in AO to get started." />
						)
					) : (
						projects.map((p) => {
							const link = orchestrators.find((o) => o.projectId === p.id) ?? null;
							return (
								<OrchestratorCard
									key={p.id}
									projectId={p.id}
									projectName={p.name}
									link={link}
									workers={workersOf(sessions, p.id, link)}
								/>
							);
						})
					)}
				</ScrollView>
			)}
		</View>
	);
}

function OrchestratorCard({
	projectId,
	projectName,
	link,
	workers,
}: {
	projectId: string;
	projectName: string;
	link: OrchestratorLink | null;
	workers: DashboardSession[];
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const { launchConductor, setActiveProject } = useApp();
	const [busy, setBusy] = useState(false);

	const state = orchestratorState(link);
	const status = orchestratorStatus(t, link);
	const intent = launchIntent(state);
	const zones = zoneCounts(workers);

	const openSession = (id: string) => router.push({ pathname: "/session/[id]", params: { id, projectId } });

	// Jump to the board scoped to this project — the natural next move after
	// reading "1 needs you". Same call pair Settings' project picker uses.
	const openZone = () => {
		haptics.select();
		setActiveProject(projectId);
		router.navigate("/");
	};

	const runLaunch = async (mode: "chat" | "tui" = "chat") => {
		setBusy(true);
		try {
			const l = await launchConductor(projectId, intent.clean, mode);
			if (l?.id) openSession(l.id);
		} catch (e) {
			haptics.error();
			if (mode === "chat" && isChatPreflightError(e)) {
				Alert.alert("Chat is unavailable", chatErrorCopy(e), [
					{ text: "Cancel", style: "cancel" },
					{ text: "Start Terminal UI", onPress: () => void runLaunch("tui") },
				]);
				return;
			}
			// Human copy, not raw daemon prose — this screen was the last place in
			// the app still surfacing "409 Conflict - …" to a user.
			const httpStatus = e instanceof ApiError ? e.status : undefined;
			const { title, message } = describeConnectionFailure(classifyConnectionFailure(httpStatus), {
				host: "",
				port: "",
				platform: Platform.OS,
			});
			Alert.alert(title, message);
		} finally {
			setBusy(false);
		}
	};

	const onLaunch = () => {
		if (!intent.confirm) {
			void runLaunch();
			return;
		}
		// clean:true retires the running orchestrator with a notice telling it to
		// stop coordinating work. That deserves a question first.
		Alert.alert(
			"Restart orchestrator?",
			`The orchestrator for ${projectName} will be retired and replaced with a fresh one. Its workers keep running.`,
			[
				{ text: "Cancel", style: "cancel" },
				{ text: "Restart", style: "destructive", onPress: () => void runLaunch() },
			],
		);
	};

	const running = state === "running";

	return (
		<View style={styles.card}>
			<View style={styles.head}>
				<AgentLogo harness={link?.harness} size={26} />
				<View style={{ flex: 1 }}>
					<Text style={styles.projName} numberOfLines={1}>
						{projectName}
					</Text>
					<View style={styles.statusRow}>
						<Dot color={status.color} size={7} breathing={status.breathing} />
						<Text style={[styles.statusText, { color: status.color }]}>{status.label}</Text>
						{link?.harness ? (
							<Text style={styles.harness} numberOfLines={1}>
								· {link.harness}
							</Text>
						) : null}
					</View>
				</View>
			</View>

			{workers.length > 0 ? (
				<View style={styles.zones}>
					{ZONE_ORDER.filter((z) => zones[z]).map((z) => {
						const m = attentionMetaFor(t)[z];
						return (
							<Pressable
								key={z}
								accessibilityRole="button"
								accessibilityLabel={`${zones[z]} ${m.label} in ${projectName}. Opens the board.`}
								onPress={openZone}
								style={({ pressed }) => [styles.zonePill, { backgroundColor: m.tint }, pressed && { opacity: 0.6 }]}
							>
								<Dot color={m.color} size={6} />
								<Text style={[styles.zoneN, { color: m.color }]}>{zones[z]}</Text>
								<Text style={styles.zoneLabel}>{m.label}</Text>
							</Pressable>
						);
					})}
				</View>
			) : null}

			<View style={styles.footer}>
				<Text style={styles.workers}>
					{workers.length} worker{workers.length === 1 ? "" : "s"}
				</Text>
				{running ? (
					<View style={styles.actions}>
						<IconButton icon="rotate-ccw" label="Restart orchestrator" loading={busy} onPress={onLaunch} />
						<IconButton icon={link?.mode === "chat" ? "message-square" : "terminal"} label="Open orchestrator" onPress={() => link && openSession(link.id)} />
					</View>
				) : (
					<Pressable
						accessibilityRole="button"
						accessibilityLabel={intent.label}
						disabled={busy}
						onPress={() => { haptics.tap(); onLaunch(); }}
						style={({ pressed }) => [styles.start, pressed && { opacity: 0.85 }, busy && { opacity: 0.5 }]}
					>
						<Text style={styles.startText}>{busy ? "Starting…" : intent.label}</Text>
					</Pressable>
				)}
			</View>
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgBase },
		center: { flex: 1, alignItems: "center", justifyContent: "center" },
		card: cardShell(t),
		head: { flexDirection: "row", alignItems: "center", gap: 10 },
		projName: { color: t.textPrimary, fontSize: 15, fontWeight: "600" },
		statusRow: { flexDirection: "row", alignItems: "center", gap: 6, marginTop: 3 },
		statusText: { fontSize: 12, fontWeight: "600" },
		harness: { color: t.textTertiary, fontSize: 12, fontFamily: t.fontMono, flexShrink: 1 },
		zones: { flexDirection: "row", flexWrap: "wrap", gap: 6, marginTop: 12 },
		zonePill: {
			flexDirection: "row",
			alignItems: "center",
			gap: 5,
			paddingHorizontal: 8,
			paddingVertical: 5,
			borderRadius: 8,
		},
		zoneN: { fontSize: 12, fontWeight: "800", fontFamily: t.fontMono },
		zoneLabel: { color: t.textSecondary, fontSize: 11, fontWeight: "600" },
		// Matches PRCard's footer: content left, actions bottom-right against the
		// card's own padding, centred rather than baseline-aligned.
		footer: { flexDirection: "row", alignItems: "center", gap: 10, marginTop: 12 },
		workers: { flex: 1, color: t.textTertiary, fontSize: 12 },
		actions: { flexDirection: "row", gap: 6 },
		start: {
			backgroundColor: t.blue,
			borderRadius: 9,
			paddingHorizontal: 14,
			height: 32,
			alignItems: "center",
			justifyContent: "center",
		},
		startText: { color: t.onAccent, fontSize: 13, fontWeight: "700" },
	});
