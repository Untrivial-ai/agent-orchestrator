import { Feather } from "@expo/vector-icons";
import { useRouter } from "expo-router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ActivityIndicator, Alert, Keyboard, LayoutAnimation, Platform, Pressable, RefreshControl, SectionList, StyleSheet, Text, useWindowDimensions, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import type { DashboardSession } from "../../lib/api";
import { classifyConnectionFailure, describeConnectionFailure } from "../../lib/connectionError";
import { tunnelMayHaveRotated } from "../../lib/staleTunnel";
import { haptics } from "../../lib/haptics";
import { groupSessions, type BoardSection } from "../../lib/agentsView";
import { useApp } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { statusVisual } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { useTabScrollToTop } from "../../lib/useTabScrollToTop";
import { Button, EmptyState, HeaderIconButton, ListSectionHeader, ScreenHeader } from "../../lib/ui";
import { WorkerDock } from "../../lib/worker-dock";
import { keyboardOverlap, workerDockKeyboardLayout, workerListBottomInset } from "../../lib/worker-dock-layout";
import { WorkerControlsSheet } from "../../lib/worker-controls-sheet";
import {
	ALL_WORKER_PROJECTS,
	filterWorkersByProject,
	spawnProjectParam,
	workerProjectLabel,
	workerSearchPresentation,
} from "../../lib/worker-controls";
import { WorkerListRow } from "../../lib/worker-list-row";
import { filterWorkerSessions } from "../../lib/worker-search";

// The archive rides along as one more section so it scrolls with the board
// rather than being pinned like desktop's strip — a phone has no room for a
// permanent footer above the tab bar.
type ListSection =
	| BoardSection
	| { zone: "pinned"; label: string; color: string; data: DashboardSession[] }
	| { zone: "archive"; label: string; color: string; data: DashboardSession[] }
	| { zone: "search"; label: string; color: string; data: DashboardSession[] };

export default function FleetScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const insets = useSafeAreaInsets();
	const { height: windowHeight } = useWindowDimensions();
	const { configured, loading, error, errorStatus, connection, config, refresh, sessions, projects, notificationsUnread, activeEndpoints, kill, renameWorker, setWorkerPinned } =
		useApp();
	const [refreshing, setRefreshing] = useState(false);
	const [query, setQuery] = useState("");
	const [searchRequested, setSearchRequested] = useState(false);
	const [controlsOpen, setControlsOpen] = useState(false);
	const [workerProjectId, setWorkerProjectId] = useState(ALL_WORKER_PROJECTS);
	const [keyboardHeight, setKeyboardHeight] = useState(0);
	const [keyboardVisible, setKeyboardVisible] = useState(false);
	const [renamingWorkerId, setRenamingWorkerId] = useState<string>();
	const [activeSwipeId, setActiveSwipeId] = useState<string>();
	const activeSwipeRef = useRef<{ id: string; close(): void } | undefined>(undefined);
	// Collapsed by default, like desktop's archive strip: it is history, and on a
	// long-running project it is most of the sessions.
	const [archiveOpen, setArchiveOpen] = useState(false);

	const listRef = useTabScrollToTop<SectionList<DashboardSession, ListSection>>();

	const projectNames = useMemo(
		() => new Map(projects.map((project) => [project.id, project.name])),
		[projects],
	);
	const projectSessions = useMemo(
		() => filterWorkersByProject(sessions, workerProjectId),
		[sessions, workerProjectId],
	);
	const filteredSessions = useMemo(
		() =>
			filterWorkerSessions(
				projectSessions,
				query,
				(projectId) => projectNames.get(projectId) ?? projectId,
				(status) => statusVisual(t, status).label,
			),
		[projectSessions, query, projectNames, t],
	);
	const { pinned, sections, archived } = useMemo(() => groupSessions(t, projectSessions), [t, projectSessions]);
	const filteredGroups = useMemo(() => groupSessions(t, filteredSessions), [t, filteredSessions]);
	const searchOpen = workerSearchPresentation(searchRequested, query) === "expanded";
	const selectedProjectLabel = workerProjectLabel(projects, workerProjectId);

	useEffect(() => {
		if (
			workerProjectId !== ALL_WORKER_PROJECTS &&
			!projects.some((project) => project.id === workerProjectId)
		) {
			setWorkerProjectId(ALL_WORKER_PROJECTS);
		}
	}, [projects, workerProjectId]);

	// The archive is the last section, rendered only when expanded so a collapsed
	// strip costs nothing to scroll past.
	const listSections = useMemo<ListSection[]>(() => {
		if (query.trim()) {
			const data = [...filteredGroups.pinned, ...filteredGroups.sections.flatMap((section) => section.data), ...filteredGroups.archived];
			return data.length === 0 ? [] : [{ zone: "search", label: "Search results", color: t.blue, data }];
		}
		const liveSections: ListSection[] = [
			...(pinned.length ? [{ zone: "pinned" as const, label: "Pinned", color: t.amber, data: pinned }] : []),
			...sections,
		];
		if (archived.length === 0) return liveSections;
		return [
			...liveSections,
			{ zone: "archive" as const, label: "Archive", color: t.textFaint, data: archiveOpen ? archived : [] },
		];
	}, [query, filteredGroups, pinned, sections, archived, archiveOpen, t]);

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

	const onRefresh = useCallback(async () => {
		haptics.tap();
		setRefreshing(true);
		await refresh();
		setRefreshing(false);
	}, [refresh]);

	// Swipeable's Android callbacks arrive after the UI thread has already begun
	// opening the next rail. Close the previous native row synchronously so two
	// action rails cannot be visible while React propagates the active id.
	const openExclusiveSwipe = useCallback((id: string, close: () => void) => {
		const previous = activeSwipeRef.current;
		if (previous?.id !== id) previous?.close();
		activeSwipeRef.current = { id, close };
		setActiveSwipeId(id);
	}, []);
	const closeExclusiveSwipe = useCallback((id: string) => {
		if (activeSwipeRef.current?.id === id) activeSwipeRef.current = undefined;
		setActiveSwipeId((activeId) => (activeId === id ? undefined : activeId));
	}, []);

	const updateWorkerPin = useCallback(async (session: DashboardSession, pinned: boolean) => {
		try {
			await setWorkerPinned(session.id, pinned);
			haptics.success();
		} catch (cause) {
			haptics.error();
			Alert.alert(
				"Couldn't update pin",
				cause instanceof Error ? cause.message : "Please try again.",
			);
		}
	}, [setWorkerPinned]);

	const confirmDeleteSession = useCallback((session: DashboardSession) => {
		haptics.warning();
		Alert.alert(
			"Delete session?",
			`This terminates ${session.displayName?.trim() || "this worker"}. Its conversation and worktree are preserved.`,
			[
				{ text: "Cancel", style: "cancel" },
				{ text: "Delete session", style: "destructive", onPress: () => void kill(session.id).catch(() => {}) },
			],
		);
	}, [kill]);

	useEffect(() => {
		if (Platform.OS === "ios") {
			const updateFromFrame = (event: Parameters<typeof Keyboard.scheduleLayoutAnimation>[0]) => {
				setKeyboardVisible(event.endCoordinates.height > 0 && event.endCoordinates.screenY < windowHeight);
				setKeyboardHeight(
					keyboardOverlap(windowHeight, event.endCoordinates.screenY, event.endCoordinates.height),
				);
			};
			const willChange = Keyboard.addListener("keyboardWillChangeFrame", (event) => {
				Keyboard.scheduleLayoutAnimation(event);
				updateFromFrame(event);
			});
			const didChange = Keyboard.addListener("keyboardDidChangeFrame", updateFromFrame);
			const didHide = Keyboard.addListener("keyboardDidHide", () => { setKeyboardVisible(false); setKeyboardHeight(0); });
			return () => {
				willChange.remove();
				didChange.remove();
				didHide.remove();
			};
		}

		const animate = (duration?: number) =>
			LayoutAnimation.configureNext({
				duration: duration || 250,
				update: { type: LayoutAnimation.Types.keyboard },
			});
		const show = Keyboard.addListener("keyboardDidShow", (event) => {
			animate(event.duration);
			setKeyboardVisible(true);
			setKeyboardHeight(keyboardOverlap(windowHeight, event.endCoordinates.screenY, event.endCoordinates.height));
		});
		const hide = Keyboard.addListener("keyboardDidHide", (event) => {
			animate(event?.duration);
			setKeyboardVisible(false);
			setKeyboardHeight(0);
		});
		return () => {
			show.remove();
			hide.remove();
		};
	}, [windowHeight]);

	const keyboardLayout = workerDockKeyboardLayout(keyboardHeight, insets.bottom, keyboardVisible);

	if (!configured) {
		return (
			<View style={styles.screen}>
				<View style={{ height: insets.top }} />
				<ScreenHeader title="Workers" status={connection} />
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
		<View style={[styles.screen, { paddingBottom: keyboardLayout.rootPaddingBottom }]}>
			<View style={{ height: insets.top }} />
			<ScreenHeader
				title="Workers"
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

			{loading && sessions.length === 0 ? (
				<View style={styles.center}>
					<ActivityIndicator color={t.blue} />
				</View>
			) : (
				<SectionList
					ref={listRef}
					sections={listSections}
					keyExtractor={(item) => `${item.projectId}:${item.id}`}
					contentContainerStyle={{ paddingBottom: workerListBottomInset(keyboardLayout.dockBottom) }}
					stickySectionHeadersEnabled={false}
					keyboardDismissMode={Platform.OS === "ios" ? "interactive" : "on-drag"}
					keyboardShouldPersistTaps="handled"
					refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={t.blue} />}
					renderSectionHeader={({ section }) =>
						section.zone === "archive" ? (
							<ArchiveHeader count={archived.length} open={archiveOpen} onToggle={() => setArchiveOpen((v) => !v)} />
						) : (
							<ListSectionHeader label={section.label} />
						)
					}
						renderItem={({ item }) => (
							<WorkerListRow
								session={item}
								projectName={projectNames.get(item.projectId)}
								isRenaming={renamingWorkerId === item.id}
								activeSwipeId={activeSwipeId}
								onSwipeOpen={openExclusiveSwipe}
								onSwipeClose={closeExclusiveSwipe}
								onRenameStart={() => setRenamingWorkerId(item.id)}
								onRenameCancel={() => setRenamingWorkerId(undefined)}
								onRename={(title) => renameWorker(item.id, title)}
								onSetPinned={(pinned) => updateWorkerPin(item, pinned)}
								onDelete={() => confirmDeleteSession(item)}
							/>
					)}
					ListEmptyComponent={
						query.trim() ? (
							<EmptyState icon="search" title="No workers found" message={`No workers match “${query.trim()}”.`} />
						) : error ? (
							<EmptyState
								icon="wifi-off"
								title={failure.title}
								message={failure.message}
								action={
									<View style={styles.errorActions}>
										<Button title="Retry" icon="refresh-cw" variant="ghost" onPress={onRefresh} />
										{/* Re-scanning is the only fix for a rotated password, and the
										    fastest one for a moved/renamed host — so it belongs beside
										    Retry rather than three taps away in Settings. */}
										<Button title="Scan" icon="maximize" onPress={() => router.push("/pair")} />
									</View>
								}
							/>
						) : workerProjectId !== ALL_WORKER_PROJECTS ? (
							<EmptyState
								icon="folder"
								title={`No workers in ${selectedProjectLabel}`}
								message="Choose another project from the Workers controls."
							/>
						) : (
							<EmptyState
								icon="moon"
								title="No active workers"
								message="Spawn a worker to put your fleet to work."
								action={<Button title="New agent" icon="plus" onPress={() => router.push({ pathname: "/spawn", params: spawnProjectParam(workerProjectId) })} />}
							/>
						)
					}
				/>
			)}

			<View style={[styles.dock, { bottom: keyboardLayout.dockBottom }]}>
				<WorkerDock
					query={query}
					onQueryChange={setQuery}
					searchOpen={searchOpen}
					onSearchOpen={() => setSearchRequested(true)}
					onSearchClose={() => {
						Keyboard.dismiss();
						setQuery("");
						setSearchRequested(false);
					}}
					onOpenControls={() => {
						Keyboard.dismiss();
						haptics.tap();
						setControlsOpen(true);
					}}
					projectFiltered={workerProjectId !== ALL_WORKER_PROJECTS}
					projects={projects}
					selectedProjectId={workerProjectId}
					onSelectProject={setWorkerProjectId}
					onSpawn={() => {
						Keyboard.dismiss();
						haptics.tap();
						router.push({ pathname: "/spawn", params: spawnProjectParam(workerProjectId) });
					}}
				/>
			</View>

			<WorkerControlsSheet
				open={controlsOpen}
				onDismiss={() => setControlsOpen(false)}
				onSearch={() => setSearchRequested(true)}
				projects={projects}
				selectedProjectId={workerProjectId}
				onSelectProject={setWorkerProjectId}
			/>
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
			<Text style={styles.archiveLabel}>Archive</Text>
			<Text style={styles.archiveCount}>{count}</Text>
		</Pressable>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgBase },
		center: { flex: 1, alignItems: "center", justifyContent: "center", paddingVertical: 60 },
		errorActions: { flexDirection: "row", gap: 10, alignItems: "center" },
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
		dock: {
			position: "absolute",
			left: 16,
			right: 16,
			height: 52,
			flexDirection: "row",
		},
	});
