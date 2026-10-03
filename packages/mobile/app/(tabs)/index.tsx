import { useRouter } from "expo-router";
import { useCallback, useEffect, useMemo, useState } from "react";
import { ActivityIndicator, FlatList, Keyboard, StyleSheet, View } from "react-native";
import Animated, { useAnimatedStyle } from "react-native-reanimated";
import { useKeyboardState, useReanimatedKeyboardAnimation } from "react-native-keyboard-controller";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { haptics } from "../../lib/haptics";
import { hostedProjectKey } from "../../lib/hostedRows";
import { StaleBanner } from "../../lib/StaleBanner";
import { useApp } from "../../lib/store";
import { useBoardFailure } from "../../lib/useBoardFailure";
import { UnpairedState } from "../../lib/UnpairedState";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { useTabScrollToTop } from "../../lib/useTabScrollToTop";
import { Button, EmptyState, HeaderIconButton, ScreenHeader } from "../../lib/ui";
import { WorkerBoardList, type BoardRow } from "../../lib/worker-board-list";
import { WorkerDock } from "../../lib/worker-dock";
import { workerDockKeyboardLayout, workerDockLift, workerListBottomInset } from "../../lib/worker-dock-layout";
import { WorkerControlsSheet } from "../../lib/worker-controls-sheet";
import {
	ALL_WORKER_PROJECTS,
	filterWorkersByProject,
	spawnProjectParam,
	workerProjectLabel,
	workerSearchPresentation,
} from "../../lib/worker-controls";
import { space } from "../../lib/tokens";

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";

export default function FleetScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);

	const router = useRouter();
	const insets = useSafeAreaInsets();
	const { config, configured, connection, loading, error, refreshAll, allSessions, allProjects, hostStates, notificationsUnread } = useApp();
	const fleetLoading = hostStates.length ? hostStates.some((host) => host.loading) : loading;
	const fleetError = hostStates.length > 1
		? hostStates.every((host) => host.connection === "closed" && !host.loading)
		: Boolean(error);
	const unreadCount = hostStates.length > 1
		? hostStates.reduce((count, host) => count + host.notificationsUnread, 0)
		: notificationsUnread;
	const [refreshing, setRefreshing] = useState(false);
	const [query, setQuery] = useState("");
	const [searchRequested, setSearchRequested] = useState(false);
	const [controlsOpen, setControlsOpen] = useState(false);
	const [workerProjectId, setWorkerProjectId] = useState(ALL_WORKER_PROJECTS);
	// Stable identities so the memoised dock is not rebuilt on every poll — a
	// re-render mid-tap is what made the filter menu open only sometimes.
	const openSearch = useCallback(() => setSearchRequested(true), []);
	const closeSearch = useCallback(() => {
		Keyboard.dismiss();
		setQuery("");
		setSearchRequested(false);
	}, []);
	const openControls = useCallback(() => {
		Keyboard.dismiss();
		haptics.tap();
		setControlsOpen(true);
	}, []);
	const spawnWorker = useCallback(() => {
		Keyboard.dismiss();
		haptics.tap();
		const project = spawnProjectParam(workerProjectId, allProjects);
		const connectedHostId = connection === "open" ? config?.hostId : hostStates.find((host) => host.connection === "open")?.hostId;
		router.push({ pathname: "/spawn", params: { hostId: project?.hostId ?? connectedHostId ?? config?.hostId, ...project } });
	}, [allProjects, config?.hostId, connection, hostStates, router, workerProjectId]);
	// Two selectors rather than the whole state object, so the board re-renders
	// only when one of these two values actually changes.
	//
	// The hook listens on keyboardWillShow / keyboardDidHide — `will`, not `did`.
	// That is the fix: the Android branch this replaces listened for
	// keyboardDidShow, which fires only once the IME has finished animating, so
	// the dock and the list inset arrived a beat after the keyboard had landed.
	const keyboardHeight = useKeyboardState((state) => state.height);
	const keyboardVisible = useKeyboardState((state) => state.isVisible);
	// The keyboard's own animated height, for the dock. The `isVisible` flag above
	// turns over when the keyboard has *finished* moving, so anything positioned
	// from it arrives late — see `workerDockLift`.
	const keyboardAnimation = useReanimatedKeyboardAnimation();
	const listRef = useTabScrollToTop<FlatList<BoardRow>>();

	const projectSessions = useMemo(
		() => filterWorkersByProject(allSessions, workerProjectId, allProjects),
		[allSessions, allProjects, workerProjectId],
	);
	const searchOpen = workerSearchPresentation(searchRequested, query) === "expanded";
	const selectedProjectLabel = workerProjectLabel(allProjects, workerProjectId);

	useEffect(() => {
		if (
			workerProjectId !== ALL_WORKER_PROJECTS &&
			!allProjects.some((project) => hostedProjectKey(project) === workerProjectId)
		) {
			setWorkerProjectId(ALL_WORKER_PROJECTS);
		}
	}, [allProjects, workerProjectId]);

	// The poll's failure as the same human copy the pairing screens use.
	const failure = useBoardFailure();

	const onRefresh = useCallback(async () => {
		haptics.tap();
		setRefreshing(true);
		try { await refreshAll(); }
		finally { setRefreshing(false); }
	}, [refreshAll]);

	const keyboardLayout = workerDockKeyboardLayout(keyboardHeight, insets.bottom, keyboardVisible);
	// `progress`, not the animated `height`: that value is the keyboard's frame
	// origin, which is negative while the keyboard is up (the library's own
	// avoiding view negates it before use). Feeding it to a `max(…, 0)` produced a
	// lift of exactly nothing, which parked the search field under the keyboard.
	const dockRise = useAnimatedStyle(() => ({
		transform: [{ translateY: -keyboardAnimation.progress.value * workerDockLift(keyboardHeight, insets.bottom) }],
	}));

	if (!configured && hostStates.length === 0) {
		return (
			<View style={styles.screen}>
				<View style={{ height: insets.top }} />
				<ScreenHeader title="Workers" />
				<UnpairedState />
			</View>
		);
	}

	return (
		<View style={[styles.screen, { paddingBottom: keyboardLayout.rootPaddingBottom }]}>
			<View style={{ height: insets.top }} />
			<ScreenHeader
				title="Workers"
				right={
					<HeaderIconButton
						icon="bell"
						label="Notifications"
						badge={unreadCount}
						onPress={() => router.navigate("/notifications")}
					/>
				}
			/>
			{/* Above the list rather than inside ListEmptyComponent: the case this
			    exists for is a populated board whose poll has died. */}
			{hostStates.length <= 1 ? <StaleBanner error={!!error} onRetry={onRefresh} /> : null}

			{fleetLoading && allSessions.length === 0 ? (
				<View style={styles.center}>
					<ActivityIndicator color={t.accent} />
				</View>
			) : (
				<WorkerBoardList
					sessions={projectSessions}
					query={query}
					identityKey={`${workerProjectId}|${query.trim()}`}
					listRef={listRef}
					contentBottomInset={workerListBottomInset(keyboardLayout.dockBottom)}
					refreshing={refreshing}
					onRefresh={onRefresh}
					ListEmptyComponent={
						query.trim() ? (
							<EmptyState icon="search" title="No workers found" message={`No workers match “${query.trim()}”.`} />
						) : fleetError ? (
							<EmptyState
								icon={hostStates.length > 1 ? "unplug" : failure.icon}
								title={hostStates.length > 1 ? "No machines connected" : failure.title}
								message={hostStates.length > 1 ? undefined : failure.hint}
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
								message="Spawn a worker to get started."
								action={<Button title="New agent" icon="plus" onPress={spawnWorker} />}
							/>
						)
					}
				/>
			)}

			{/* Resting position plus the keyboard's lift, animated: the dock travels with
			    the keys instead of jumping once they have finished moving. */}
			<Animated.View style={[styles.dock, { bottom: keyboardLayout.restingBottom }, dockRise]}>
				<WorkerDock
					query={query}
					onQueryChange={setQuery}
					searchOpen={searchOpen}
					onSearchOpen={openSearch}
					onSearchClose={closeSearch}
					onOpenControls={openControls}
					projectFiltered={workerProjectId !== ALL_WORKER_PROJECTS}
					projects={allProjects}
					selectedProjectId={workerProjectId}
					onSelectProject={setWorkerProjectId}
					onSpawn={spawnWorker}
				/>
			</Animated.View>

			<WorkerControlsSheet
				open={controlsOpen}
				onDismiss={() => setControlsOpen(false)}
				onSearch={() => setSearchRequested(true)}
				projects={allProjects}
				selectedProjectId={workerProjectId}
				onSelectProject={setWorkerProjectId}
			/>
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgBase },
		center: { flex: 1, alignItems: "center", justifyContent: "center", paddingVertical: 60 },
		errorActions: { flexDirection: "row", gap: space.sm, alignItems: "center" },
		dock: {
			position: "absolute",
			left: 16,
			right: 16,
			height: 52,
			flexDirection: "row",
		},
	});
