import { useRouter } from "expo-router";
import { useMemo, useState } from "react";
import { ActivityIndicator, RefreshControl, SectionList, StyleSheet, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { haptics } from "../../lib/haptics";
import { hostedProjectKey, hostedProjectSections, type HostedProjectRow } from "../../lib/hostedRows";
import type { OrchestratorProjectRow } from "../../lib/orchestratorView";
import { ProjectCard } from "../../lib/project-card";
import { StaleBanner } from "../../lib/StaleBanner";
import { useApp } from "../../lib/store";
import { UnpairedState } from "../../lib/UnpairedState";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { useOrchestratorLauncher } from "../../lib/useOrchestratorLauncher";
import { useBoardFailure } from "../../lib/useBoardFailure";
import { useTabScrollToTop } from "../../lib/useTabScrollToTop";
import { Button, EmptyState, HeaderIconButton, ListSectionHeader, ScreenHeader } from "../../lib/ui";

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";

export default function ProjectsScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const insets = useSafeAreaInsets();
	const router = useRouter();
	const {
		configured,
		loading,
		error,
		allProjects,
		hostStates,
		notificationsUnread,
		refreshAll,
	} = useApp();
	const fleetLoading = hostStates.length ? hostStates.some((host) => host.loading) : loading;
	const fleetError = hostStates.length > 1
		? hostStates.every((host) => host.connection === "closed" && !host.loading)
		: Boolean(error);
	const unreadCount = hostStates.length > 1
		? hostStates.reduce((count, host) => count + host.notificationsUnread, 0)
		: notificationsUnread;
	const [refreshing, setRefreshing] = useState(false);
	const { busyProjects, openOrchestrator } = useOrchestratorLauncher();
	const listRef = useTabScrollToTop<SectionList<HostedProjectRow>>();
	const sections = useMemo(
		() => hostedProjectSections(hostStates),
		[hostStates],
	);
	const failure = useBoardFailure();

	const onRefresh = async () => {
		haptics.tap();
		setRefreshing(true);
		try {
			await refreshAll();
		} finally {
			setRefreshing(false);
		}
	};

	const openProject = (row: OrchestratorProjectRow) => {
		const hostId = "hostId" in row.project && typeof row.project.hostId === "string" ? row.project.hostId : undefined;
		if (!hostId) return;
		haptics.select();
		router.push({ pathname: "/project/[id]", params: { id: row.project.id, hostId } });
	};

	if (!configured && hostStates.length === 0) {
		return (
			<View style={styles.screen}>
				<View style={{ height: insets.top }} />
				<ScreenHeader title="Projects" />
				<UnpairedState />
			</View>
		);
	}

	return (
		<View style={styles.screen}>
			<View style={{ height: insets.top }} />
			<ScreenHeader
				title="Projects"
				right={
					<HeaderIconButton
						icon="bell"
						label="Notifications"
						badge={unreadCount}
						onPress={() => router.navigate("/notifications")}
					/>
				}
			/>
			{hostStates.length <= 1 ? <StaleBanner error={!!error} onRetry={onRefresh} /> : null}

			{fleetLoading && allProjects.length === 0 ? (
				<View style={styles.center}>
					<ActivityIndicator color={t.accent} />
				</View>
			) : (
				<SectionList
					ref={listRef}
					sections={sections}
					keyExtractor={(row) => hostedProjectKey(row.project)}
					contentInsetAdjustmentBehavior="automatic"
					contentContainerStyle={{ paddingBottom: insets.bottom + 92 }}
					stickySectionHeadersEnabled={false}
					refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={t.accent} />}
					renderSectionHeader={({ section }) => (
						<ListSectionHeader label={section.title} count={section.data.length} />
					)}
					renderItem={({ item }) => (
						<ProjectCard
							row={item}
							busy={busyProjects.has(hostedProjectKey(item.project))}
							onOpenProject={openProject}
							onOrchestrator={openOrchestrator}
						/>
					)}
					ListEmptyComponent={
						fleetError ? (
							<EmptyState
								icon={hostStates.length > 1 ? "unplug" : failure.icon}
								title={hostStates.length > 1 ? "No machines connected" : failure.title}
								message={hostStates.length > 1 ? undefined : failure.hint}
								action={<Button title="Retry" icon="refresh-cw" variant="ghost" onPress={onRefresh} />}
							/>
						) : (
							<EmptyState icon="folder" title="No projects" message="Add a project in AO to get started." />
						)
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
