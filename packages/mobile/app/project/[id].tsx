import { useLocalSearchParams, useRouter } from "expo-router";
import { useCallback, useMemo, useState } from "react";
import { ActivityIndicator, StyleSheet, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { haptics } from "../../lib/haptics";
import { hostRouteMatches } from "../../lib/hostRoute";
import { orchestratorProjectSections, projectDetailSessions, projectPageStats } from "../../lib/orchestratorView";
import { ProjectPageHeader } from "../../lib/project-card";
import { StaleBanner } from "../../lib/StaleBanner";
import { HostScope, useApp } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { useOrchestratorLauncher } from "../../lib/useOrchestratorLauncher";
import { Button, EmptyState, HeaderIconButton, ListSectionHeader, ScreenHeader } from "../../lib/ui";
import { WorkerBoardList } from "../../lib/worker-board-list";
import { backOr } from "../../lib/backNavigation";
import { hostedRowKey } from "../../lib/hostedRows";

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";

/**
 * One project: its orchestrator on top, and below it that project's workers
 * exactly as the Workers board shows them — same sections, same row actions,
 * archive included — so nothing here has to be relearned.
 */
export default function ProjectScreen() {
	const { hostId } = useLocalSearchParams<{ hostId?: string }>();
	return hostId ? <HostScope key={hostId} hostId={hostId}><ProjectScreenContent /></HostScope> : <ProjectScreenContent />;
}

function ProjectScreenContent() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const insets = useSafeAreaInsets();
	const { id, hostId: routeHostId } = useLocalSearchParams<{ id: string; hostId?: string }>();
	const { config, currentHostId, connection, loading, error, refresh, projects, sessions, orchestrators } = useApp();
	const hostMatches = hostRouteMatches(routeHostId, currentHostId);
	const { busyProjects, openOrchestrator } = useOrchestratorLauncher();
	const [refreshing, setRefreshing] = useState(false);

	const row = useMemo(
		() => hostMatches
			? orchestratorProjectSections(projects, sessions, orchestrators)
				.flatMap((section) => section.data)
				.find((candidate) => candidate.project.id === id)
			: undefined,
		[projects, sessions, orchestrators, id, hostMatches],
	);
	const projectSessions = useMemo(() => hostMatches ? projectDetailSessions(id ?? "", sessions) : [], [hostMatches, id, sessions]);
	const stats = useMemo(() => projectPageStats(projectSessions, row?.link), [projectSessions, row?.link]);

	const onRefresh = useCallback(async () => {
		haptics.tap();
		setRefreshing(true);
		try {
			await refresh();
		} finally {
			setRefreshing(false);
		}
	}, [refresh]);

	const startTask = () => {
		if (!hostMatches) return;
		haptics.tap();
		router.push({ pathname: "/spawn", params: { projectId: id, hostId: routeHostId } });
	};

	return (
		<View style={styles.screen}>
			<View style={{ height: insets.top }} />
			<ScreenHeader
				title={row?.project.name ?? "Project"}
				left={
					<HeaderIconButton
						icon="back"
						label="Back"
						// A deep link can open this page as the only screen in the stack, with
						// nothing beneath it to go back to. Land on Projects instead.
						onPress={() => backOr(router, "/projects")}
					/>
				}
			/>
			{hostMatches ? <StaleBanner error={!!error} onRetry={onRefresh} /> : null}

			{!hostMatches ? (
				loading && !config ? (
					<View style={styles.center}>
						<ActivityIndicator color={t.accent} />
					</View>
				) : (
					<EmptyState
						icon="server"
						title="Project belongs to another machine"
						message="Open it from that machine's project list."
						action={<Button title="Open projects" icon="folder" onPress={() => router.navigate("/projects")} />}
					/>
				)
			) : !row ? (
				loading ? (
					<View style={styles.center}>
						<ActivityIndicator color={t.accent} />
					</View>
				) : connection !== "open" ? (
					<EmptyState icon="wifi-off" title="Machine offline" message="This project loads once the app reconnects." />
				) : (
					<EmptyState icon="folder" title="Project not found" message="It may have been removed from AO." />
				)
			) : (
				<WorkerBoardList
					sessions={projectSessions}
					showProject={false}
					contentBottomInset={insets.bottom + 32}
					refreshing={refreshing}
					onRefresh={onRefresh}
					ListHeaderComponent={
						<ProjectPageHeader
							row={row}
							stats={stats}
							busy={busyProjects.has(hostedRowKey(currentHostId ?? "", row.project.id))}
							onPress={openOrchestrator}
						/>
					}
					ListEmptyComponent={
						<View>
							<ListSectionHeader label="Workers" count={0} />
							<EmptyState
								icon="moon"
								title="No workers yet"
								message="Start a task to put this project to work."
								action={<Button title="Start task" icon="plus" onPress={startTask} />}
							/>
						</View>
					}
				/>
			)}
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgBase },
		center: { flex: 1, alignItems: "center", justifyContent: "center", paddingVertical: 60 },
	});
