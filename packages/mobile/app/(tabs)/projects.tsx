import { useRouter } from "expo-router";
import { useMemo, useRef, useState } from "react";
import { ActivityIndicator, Alert, Platform, RefreshControl, SectionList, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { ApiError } from "../../lib/api";
import { chatErrorCopy, isChatPreflightError } from "../../lib/chatError";
import { classifyConnectionFailure, describeConnectionFailure } from "../../lib/connectionError";
import { haptics } from "../../lib/haptics";
import { OrchestratorProjectRowView } from "../../lib/orchestrator-project-row";
import { orchestratorProjectSections, type OrchestratorProjectRow } from "../../lib/orchestratorView";
import { useApp } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { useTabScrollToTop } from "../../lib/useTabScrollToTop";
import { Button, EmptyState, HeaderIconButton, ScreenHeader } from "../../lib/ui";

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
		connection,
		config,
		projects,
		sessions,
		orchestrators,
		notificationsUnread,
		refresh,
		launchConductor,
	} = useApp();
	const [refreshing, setRefreshing] = useState(false);
	const [busyProjects, setBusyProjects] = useState<ReadonlySet<string>>(() => new Set());
	const launchingProjects = useRef(new Set<string>());
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

	const setProjectBusy = (projectId: string, busy: boolean) => {
		setBusyProjects((current) => {
			const next = new Set(current);
			if (busy) next.add(projectId);
			else next.delete(projectId);
			return next;
		});
	};

	const openSession = (row: OrchestratorProjectRow, id: string) => {
		router.push({ pathname: "/session/[id]", params: { id, projectId: row.project.id } });
	};

	const onRefresh = async () => {
		haptics.tap();
		setRefreshing(true);
		try {
			await refresh();
		} finally {
			setRefreshing(false);
		}
	};

	const runLaunch = async (row: OrchestratorProjectRow, mode: "chat" | "tui" = "chat") => {
		if (launchingProjects.current.has(row.project.id)) return;
		launchingProjects.current.add(row.project.id);
		setProjectBusy(row.project.id, true);
		try {
			const next = await launchConductor(row.project.id, false, mode);
			if (next?.id) openSession(row, next.id);
			else await refresh();
		} catch (cause) {
			haptics.error();
			if (mode === "chat" && isChatPreflightError(cause)) {
				Alert.alert("Chat is unavailable", chatErrorCopy(cause), [
					{ text: "Cancel", style: "cancel" },
					{ text: "Start Terminal UI", onPress: () => void runLaunch(row, "tui") },
				]);
				return;
			}
			const httpStatus = cause instanceof ApiError ? cause.status : undefined;
			const copy = describeConnectionFailure(classifyConnectionFailure(httpStatus), {
				host: config?.host ?? "",
				port: config?.httpPort ?? "",
				platform: Platform.OS,
			});
			Alert.alert(copy.title, copy.message);
		} finally {
			launchingProjects.current.delete(row.project.id);
			setProjectBusy(row.project.id, false);
		}
	};

	const openOrchestrator = (row: OrchestratorProjectRow) => {
		if (!row.link?.id) {
			void refresh();
			return;
		}
		haptics.select();
		openSession(row, row.link.id);
	};

	const openWorker = (row: OrchestratorProjectRow, workerId: string) => {
		haptics.select();
		openSession(row, workerId);
	};

	const launchOrchestrator = (row: OrchestratorProjectRow) => {
		haptics.tap();
		void runLaunch(row);
	};

	if (!configured) {
		return (
			<View style={styles.screen}>
				<View style={{ height: insets.top }} />
				<ScreenHeader title="Projects" status={connection} />
				<EmptyState icon="share-2" title="No server" message="Connect to AO in Settings." />
			</View>
		);
	}

	return (
		<View style={styles.screen}>
			<View style={{ height: insets.top }} />
			<ScreenHeader
				title="Projects"
				subtitle="Ordered by attention"
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
					renderSectionHeader={({ section }) => <ProjectSectionHeader label={section.title} />}
					renderItem={({ item }) => (
						<OrchestratorProjectRowView
							row={item}
							busy={busyProjects.has(item.project.id)}
							onOpen={openOrchestrator}
							onOpenWorker={openWorker}
							onLaunch={launchOrchestrator}
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

function ProjectSectionHeader({ label }: { label: string }) {
	const styles = useThemedStyles(makeStyles);
	return (
		<View style={styles.sectionHeader}>
			<Text style={styles.sectionLabel}>{label}</Text>
			<View style={styles.sectionRule} />
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgBase },
		center: { flex: 1, alignItems: "center", justifyContent: "center", paddingVertical: 60 },
		sectionHeader: {
			flexDirection: "row",
			alignItems: "center",
			gap: 10,
			paddingHorizontal: 18,
			paddingTop: 18,
			paddingBottom: 5,
		},
		sectionLabel: { color: t.textTertiary, fontSize: 12, lineHeight: 16, fontWeight: "500" },
		sectionRule: { flex: 1, height: StyleSheet.hairlineWidth, backgroundColor: t.borderSubtle },
	});
