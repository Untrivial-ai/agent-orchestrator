import { Feather } from "@expo/vector-icons";
import { useRouter } from "expo-router";
import { useCallback, useMemo, useState } from "react";
import { ActivityIndicator, Platform, Pressable, RefreshControl, SectionList, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { attentionOf, type DashboardSession } from "../../lib/api";
import { classifyConnectionFailure, describeConnectionFailure } from "../../lib/connectionError";
import { tunnelMayHaveRotated } from "../../lib/staleTunnel";
import { haptics } from "../../lib/haptics";
import { groupSessions, type BoardSection, type BoardZone } from "../../lib/agentsView";
import { ALL_PROJECTS, filteredEmptyCopy } from "../../lib/projectFilter";
import { ProjectSwitcher } from "../../lib/ProjectSwitcher";
import { SessionCard } from "../../lib/SessionCard";
import { useApp, useVisibleSessions } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { MINUTE_MS, useNow } from "../../lib/useNow";
import { useTabScrollToTop } from "../../lib/useTabScrollToTop";
import { Button, EmptyState, HeaderIconButton, ScreenHeader, SectionHeader } from "../../lib/ui";

// The archive rides along as one more section so it scrolls with the board
// rather than being pinned like desktop's strip — a phone has no room for a
// permanent footer above the tab bar.
type ListSection = BoardSection | { zone: "archive"; label: string; color: string; data: DashboardSession[] };

export default function FleetScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const insets = useSafeAreaInsets();
	const {
		configured,
		loading,
		error,
		errorStatus,
		connection,
		config,
		refresh,
		activeProjectId,
		setActiveProject,
		projects,
		projectsKnown,
		sessions: allSessions,
		notificationsUnread,
		activeEndpoints,
	} = useApp();
	const sessions = useVisibleSessions();
	// Rendered by ListEmptyComponent when every session the daemon returned is
	// hidden by the filter: the empty state then says so instead of "No active
	// agents", which had the reporter clearing app storage. Settings can clear
	// a filter too, but nothing there said the board had one, and the switcher
	// above hides below two projects. Null whenever the ordinary empty state is
	// the right one; the helper owns that decision.
	const filteredEmpty = useMemo(
		() => filteredEmptyCopy(activeProjectId, projects, projectsKnown, allSessions),
		[activeProjectId, projects, projectsKnown, allSessions],
	);
	const [refreshing, setRefreshing] = useState(false);
	// Collapsed by default, like desktop's archive strip: it is history, and on a
	// long-running project it is most of the sessions.
	const [archiveOpen, setArchiveOpen] = useState(false);

	const listRef = useTabScrollToTop<SectionList<DashboardSession, ListSection>>();

	// Relative timestamps go stale on their own; the board owns the clock.
	const now = useNow(MINUTE_MS);

	const { sections, archived } = useMemo(() => groupSessions(t, sessions), [t, sessions]);

	// The archive is the last section, rendered only when expanded so a collapsed
	// strip costs nothing to scroll past.
	const listSections = useMemo<ListSection[]>(() => {
		if (archived.length === 0) return sections;
		return [
			...sections,
			{ zone: "archive" as const, label: "Archive", color: t.textFaint, data: archiveOpen ? archived : [] },
		];
	}, [sections, archived, archiveOpen, t]);

	// Turn the poll's raw failure ("401 - missing or invalid connection password")
	// into the same human copy the pairing screens use, keyed on the cause.
	const failure = useMemo(
		() =>
			describeConnectionFailure(
				// A stored tunnel that no longer answers means the hostname
				// rotated, which no amount of retrying fixes — rescanning does.
				// Distinguished here rather than in the classifier because it
				// depends on what the machine advertised, not on a status code.
				classifyConnectionFailure(errorStatus ?? undefined) === "unreachable" &&
					tunnelMayHaveRotated(activeEndpoints, connection === "open")
					? "tunnel-rotated"
					: classifyConnectionFailure(errorStatus ?? undefined),
				{
					host: config?.host ?? "",
					port: config?.httpPort ?? "",
					platform: Platform.OS,
				},
			),
		[errorStatus, config?.host, config?.httpPort, activeEndpoints, connection],
	);

	const counts = useMemo(() => {
		let working = 0;
		let needsYou = 0;
		let mergeable = 0;
		for (const s of sessions) {
			const a = attentionOf(s);
			if (a === "working") working++;
			else if (a === "respond" || a === "action") needsYou++;
			else if (a === "merge") mergeable++;
		}
		return { working, needsYou, mergeable };
	}, [sessions]);

	// A stat scrolls to its section. Guarded: scrollToLocation on a section that
	// isn't rendered warns and does nothing, so a zero-count stat is inert rather
	// than appearing broken.
	const jumpTo = useCallback(
		(zone: BoardZone) => {
			const index = listSections.findIndex((s) => s.zone === zone);
			if (index < 0) return;
			haptics.select();
			listRef.current?.scrollToLocation({ sectionIndex: index, itemIndex: 0, viewOffset: 8 });
		},
		[listSections, listRef],
	);

	const onRefresh = useCallback(async () => {
		haptics.tap();
		setRefreshing(true);
		await refresh();
		setRefreshing(false);
	}, [refresh]);

	if (!configured) {
		return (
			<View style={styles.screen}>
				<View style={{ height: insets.top }} />
				<EmptyState
					icon="server"
					// Where a user who skipped onboarding lands. Deliberately not a
					// restatement of the welcome screen — they've already read that and
					// chosen to move past it. This says what is missing and offers the
					// one action that fixes it, going straight to the scanner rather
					// than sending them to Settings to hunt for a field.
					title="No desktop paired"
					message="Scan the pairing code from AO → Settings → Connect Mobile to drive your agents from here."
					action={<Button title="Scan pairing code" icon="maximize" onPress={() => router.push("/pair")} />}
				/>
			</View>
		);
	}

	return (
		<View style={styles.screen}>
			<View style={{ height: insets.top }} />
			<ScreenHeader
				title="Agents"
				subtitle={config?.host}
				status={connection}
				right={
					<HeaderIconButton
						icon="bell"
						label="Notifications"
						badge={notificationsUnread}
						onPress={() => router.navigate("/notifications")}
					/>
				}
			/>

			<View style={styles.stats}>
				<Stat n={counts.working} label="working" color={t.orange} onPress={() => jumpTo("working")} />
				<Stat n={counts.needsYou} label="need you" color={t.amber} onPress={() => jumpTo("action")} />
				<Stat n={counts.mergeable} label="mergeable" color={t.green} onPress={() => jumpTo("merge")} />
			</View>

			<ProjectSwitcher />

			{loading && sessions.length === 0 ? (
				<View style={styles.center}>
					<ActivityIndicator color={t.blue} />
				</View>
			) : (
				<SectionList
					ref={listRef}
					sections={listSections}
					keyExtractor={(item) => `${item.projectId}:${item.id}`}
					contentContainerStyle={{ paddingBottom: 120 }}
					stickySectionHeadersEnabled={false}
					refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={t.blue} />}
					renderSectionHeader={({ section }) =>
						section.zone === "archive" ? (
							<ArchiveHeader count={archived.length} open={archiveOpen} onToggle={() => setArchiveOpen((v) => !v)} />
						) : (
							<SectionHeader label={section.label} color={section.color} count={section.data.length} />
						)
					}
					renderItem={({ item }) => <SessionCard session={item} showProject={activeProjectId === ALL_PROJECTS} now={now} />}
					ListEmptyComponent={
						error ? (
							<EmptyState
								icon="wifi-off"
								title={failure.title}
								message={failure.message}
								action={
									<View style={styles.emptyActions}>
										<Button title="Retry" icon="refresh-cw" variant="ghost" onPress={onRefresh} />
										{/* Re-scanning is the only fix for a rotated password, and the
										    fastest one for a moved/renamed host — so it belongs beside
										    Retry rather than three taps away in Settings. */}
										<Button title="Scan" icon="maximize" onPress={() => router.push("/pair")} />
									</View>
								}
							/>
						) : filteredEmpty ? (
							<EmptyState
								icon="filter"
								{...filteredEmpty}
								action={
									<View style={styles.emptyActions}>
										<Button title="Show all projects" icon="layers" variant="ghost" onPress={() => setActiveProject(ALL_PROJECTS)} />
										<Button title="New agent" icon="plus" onPress={() => router.push("/spawn")} />
									</View>
								}
							/>
						) : (
							<EmptyState
								icon="moon"
								title="No active agents"
								message="Spawn a worker to put your fleet to work."
								action={<Button title="New agent" icon="plus" onPress={() => router.push("/spawn")} />}
							/>
						)
					}
				/>
			)}

			{/* Spawn FAB */}
			<Pressable
				onPress={() => {
					haptics.tap();
					router.push("/spawn");
				}}
				style={({ pressed }) => [styles.fab, pressed && { opacity: 0.85 }]}
			>
				<Feather name="plus" size={24} color={t.onAccent} />
			</Pressable>
		</View>
	);
}

function ArchiveHeader({ count, open, onToggle }: { count: number; open: boolean; onToggle: () => void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityState={{ expanded: open }}
			accessibilityLabel={`Archive, ${count} session${count === 1 ? "" : "s"}`}
			onPress={() => {
				haptics.tap();
				onToggle();
			}}
			style={({ pressed }) => [styles.archiveHeader, pressed && { opacity: 0.6 }]}
		>
			<Feather name={open ? "chevron-down" : "chevron-right"} size={14} color={t.textTertiary} />
			<Text style={styles.archiveLabel}>ARCHIVE</Text>
			<Text style={styles.archiveCount}>{count}</Text>
		</Pressable>
	);
}

function Stat({
	n,
	label,
	color,
	onPress,
}: {
	n: number;
	label: string;
	color: string;
	onPress: () => void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	// Zero means the section isn't on the board, so there is nowhere to jump.
	const active = n > 0;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`${n} ${label}${active ? ". Scroll to section." : ""}`}
			disabled={!active}
			onPress={onPress}
			style={({ pressed }) => [styles.stat, pressed && active && styles.statPressed]}
		>
			<Text style={[styles.statN, { color: active ? color : t.textFaint }]}>{n}</Text>
			<Text style={styles.statLabel}>{label}</Text>
		</Pressable>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgBase },
		center: { flex: 1, alignItems: "center", justifyContent: "center", paddingVertical: 60 },
		emptyActions: { flexDirection: "row", gap: 10, alignItems: "center" },
		stats: {
			flexDirection: "row",
			gap: 10,
			paddingHorizontal: 16,
			paddingTop: 4,
			paddingBottom: 10,
		},
		stat: {
			flex: 1,
			backgroundColor: t.bgElevated,
			borderRadius: 12,
			borderWidth: 1,
			borderColor: t.borderSubtle,
			paddingVertical: 12,
			paddingHorizontal: 14,
		},
		statPressed: { backgroundColor: t.bgElevatedHover, borderColor: t.borderDefault },
		statN: { fontSize: 24, fontWeight: "800", fontFamily: t.fontMono },
		statLabel: { color: t.textTertiary, fontSize: 11, fontWeight: "600", marginTop: 2 },
		archiveHeader: {
			flexDirection: "row",
			alignItems: "center",
			gap: 8,
			paddingHorizontal: 16,
			paddingTop: 22,
			paddingBottom: 10,
		},
		archiveLabel: { color: t.textTertiary, fontSize: 11, letterSpacing: 1.2, fontWeight: "700", flex: 1 },
		archiveCount: { color: t.textFaint, fontSize: 12, fontWeight: "700", fontFamily: t.fontMono },
		fab: {
			position: "absolute",
			right: 18,
			bottom: 24,
			width: 56,
			height: 56,
			borderRadius: 28,
			backgroundColor: t.blue,
			alignItems: "center",
			justifyContent: "center",
			shadowColor: "#000",
			shadowOpacity: 0.4,
			shadowRadius: 12,
			shadowOffset: { width: 0, height: 4 },
			elevation: 8,
		},
	});
