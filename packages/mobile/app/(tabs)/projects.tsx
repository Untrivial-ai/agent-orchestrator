import { useRouter } from "expo-router";
import { useMemo, useState } from "react";
import { ActivityIndicator, Platform, RefreshControl, SectionList, StyleSheet, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { classifyConnectionFailure, describeConnectionFailure } from "../../lib/connectionError";
import { haptics } from "../../lib/haptics";
import { orchestratorProjectSections, type OrchestratorProjectRow } from "../../lib/orchestratorView";
import { ProjectCard } from "../../lib/project-card";
import { StaleBanner } from "../../lib/StaleBanner";
import { useApp } from "../../lib/store";
import { UnpairedState } from "../../lib/UnpairedState";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { useOrchestratorLauncher } from "../../lib/useOrchestratorLauncher";
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
		errorStatus,
		config,
		projects,
		sessions,
		orchestrators,
		notificationsUnread,
		refresh,
	} = useApp();
	const [refreshing, setRefreshing] = useState(false);
	const { busyProjects, openOrchestrator } = useOrchestratorLauncher();
	const listRef = useTabScrollToTop<SectionList<OrchestratorProjectRow>>();
	const sections = useMemo(
		() => orchestratorProjectSections(projects, sessions, orchestrators),
		[projects, sessions, orchestrators],
	);
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
		try {
			await refresh();
		} finally {
			setRefreshing(false);
		}
	};

	const openProject = (row: OrchestratorProjectRow) => {
		haptics.select();
		router.push({ pathname: "/project/[id]", params: { id: row.project.id } });
	};

	if (!configured) {
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
						badge={notificationsUnread}
						onPress={() => router.navigate("/notifications")}
					/>
				}
			/>
			<StaleBanner error={!!error} onRetry={onRefresh} />

			{loading && projects.length === 0 ? (
				<View style={styles.center}>
					<ActivityIndicator color={t.blue} />
				</View>
			) : (
				<SectionList
					ref={listRef}
					sections={sections}
					keyExtractor={(row) => row.project.id}
					contentInsetAdjustmentBehavior="automatic"
					contentContainerStyle={{ paddingBottom: insets.bottom + 92 }}
					stickySectionHeadersEnabled={false}
					refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={t.blue} />}
					renderSectionHeader={({ section }) => (
						<ListSectionHeader label={section.title} count={section.data.length} />
					)}
					renderItem={({ item }) => (
						<ProjectCard
							row={item}
							busy={busyProjects.has(item.project.id)}
							onOpenProject={openProject}
							onOrchestrator={openOrchestrator}
						/>
					)}
					ListEmptyComponent={
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
