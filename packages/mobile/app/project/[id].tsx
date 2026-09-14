import { useLocalSearchParams, useRouter } from "expo-router";
import { useMemo } from "react";
import { Platform, SectionList, StyleSheet, View } from "react-native";
import { collectPRs, comparePRs } from "../../lib/prView";
import { PRCard } from "../../lib/PRCard";
import { ProjectSummaryCard } from "../../lib/project-summary-card";
import { projectSummaries, projectWorkers } from "../../lib/projects-view";
import { SessionCard } from "../../lib/SessionCard";
import { useApp } from "../../lib/store";
import { MINUTE_MS, useNow } from "../../lib/useNow";
import type { DashboardPR, DashboardSession } from "../../lib/api";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles, useThemeState } from "../../lib/ThemeProvider";
import { Button, EmptyState, SectionHeader } from "../../lib/ui";
import { Host, Button as NativeButton } from "@expo/ui";

type OverviewItem =
	| { kind: "worker"; session: DashboardSession }
	| { kind: "pr"; pr: DashboardPR; session: DashboardSession };

export default function ProjectOverviewScreen() {
	const { id } = useLocalSearchParams<{ id: string }>();
	const router = useRouter();
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const { scheme } = useThemeState();
	const now = useNow(MINUTE_MS);
	const { projects, sessions } = useApp();
	const summary = useMemo(
		() => projectSummaries(projects, sessions).find((candidate) => candidate.project.id === id),
		[projects, sessions, id],
	);

	if (!summary) {
		return (
			<View style={styles.screen}>
				<EmptyState
					icon="folder"
					title="Project not found"
					message="This project is no longer available on the connected AO host."
					action={<Button title="Back to Projects" onPress={() => router.replace("/projects")} />}
				/>
			</View>
		);
	}

	const workers = projectWorkers(summary.project.id, sessions);
	const prs = collectPRs(sessions.filter((session) => session.projectId === summary.project.id))
		.sort((a, b) => comparePRs(a.pr, b.pr));
	const sections = [
		{ title: "Active workers", color: t.orange, data: workers.map((session) => ({ kind: "worker" as const, session })) },
		{ title: "Pull requests", color: t.green, data: prs.map(({ pr, session }) => ({ kind: "pr" as const, pr, session })) },
	].filter((section) => section.data.length > 0);

	return (
		<View style={styles.screen}>
			<SectionList<OverviewItem>
				sections={sections}
				keyExtractor={(item) => item.kind === "worker" ? `worker:${item.session.id}` : `pr:${item.session.projectId}:${item.pr.number}`}
				contentContainerStyle={styles.content}
				stickySectionHeadersEnabled={false}
				ListHeaderComponent={<ProjectSummaryCard summary={summary} />}
				renderSectionHeader={({ section }) => <SectionHeader label={section.title} color={section.color} count={section.data.length} />}
				renderItem={({ item }) => item.kind === "worker" ? <SessionCard session={item.session} now={now} /> : <PRCard pr={item.pr} session={item.session} />}
				ListEmptyComponent={<EmptyState icon="moon" title="No project activity" message="Spawn a worker to start work in this project." />}
			/>
			<View style={styles.spawnDock}>
				{Platform.OS === "android" ? (
					<Button title="Spawn worker" onPress={() => router.push({ pathname: "/spawn", params: { projectId: summary.project.id } })} />
				) : (
					<Host style={styles.spawnHost} colorScheme={scheme} seedColor={t.blue}>
						<NativeButton
							label="Spawn worker"
							onPress={() => router.push({ pathname: "/spawn", params: { projectId: summary.project.id } })}
							style={{ height: 50, borderRadius: 17 }}
						/>
					</Host>
				)}
			</View>
		</View>
	);
}

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	content: { paddingHorizontal: 16, paddingTop: 16, paddingBottom: 94, flexGrow: 1 },
	spawnDock: { position: "absolute", left: 16, right: 16, bottom: 16 },
	spawnHost: { width: "100%", height: 50 },
});
