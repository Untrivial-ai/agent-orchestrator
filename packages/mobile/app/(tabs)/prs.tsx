import { useMemo, useState } from "react";
import { ActivityIndicator, Platform, RefreshControl, SectionList, StyleSheet, View } from "react-native";
import { useRouter } from "expo-router";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import type { Theme } from "../../lib/theme";
import { classifyConnectionFailure, describeConnectionFailure } from "../../lib/connectionError";
import { haptics } from "../../lib/haptics";
import { PRCard } from "../../lib/PRCard";
import { PRFilterDock } from "../../lib/pr-filter-dock";
import { ProjectSwitcher } from "../../lib/ProjectSwitcher";
import { prLifecycle, prListSections, type PRListFilter } from "../../lib/prView";
import { useApp, usePRs } from "../../lib/store";
import { usePRSummaries } from "../../lib/usePRSummaries";
import { useTabScrollToTop } from "../../lib/useTabScrollToTop";
import { Button, EmptyState, HeaderIconButton, ListSectionHeader, ScreenHeader } from "../../lib/ui";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";

type Filter = PRListFilter;

// Drafts are open PRs — they belong in the Open bucket even though the card
// labels them "draft". Before, `state` had already folded draft into "open", so
// the distinction did not exist anywhere.
const inBucket = (filter: Filter, life: ReturnType<typeof prLifecycle>) => {
	if (filter === "all") return true;
	if (filter === "open") return life === "open" || life === "draft";
	return life === "merged";
};

export default function PRsScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const insets = useSafeAreaInsets();
	const router = useRouter();
	const { configured, loading, error, errorStatus, connection, config, refresh, notificationsUnread } = useApp();
	const prs = usePRs();
	const [filter, setFilter] = useState<Filter>("open");
	const [refreshing, setRefreshing] = useState(false);

	const scrollRef = useTabScrollToTop<SectionList>();

	// Grouped around the user's next action, matching the Workers board rather
	// than presenting a flat stream in daemon order.
	const filtered = useMemo(() => prs.filter(({ pr }) => inBucket(filter, prLifecycle(pr))), [prs, filter]);
	const sections = useMemo(() => prListSections(prs, filter), [prs, filter]);

	// The rich per-PR detail the cards show lives on a separate endpoint, fetched
	// once per session and cached — see usePRSummaries. Pull-to-refresh is the
	// only thing that re-fetches it.
	const sessionIds = useMemo(() => [...new Set(filtered.map(({ session }) => session.id))], [filtered]);
	const summaries = usePRSummaries(sessionIds);
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
		summaries.reload();
		await refresh();
		setRefreshing(false);
	};

	if (!configured) {
		return (
			<View style={styles.screen}>
				<View style={{ height: insets.top }} />
				<EmptyState icon="git-pull-request" title="No server" message="Connect to AO in Settings." />
			</View>
		);
	}

	const counts = {
		open: prs.filter((p) => inBucket("open", prLifecycle(p.pr))).length,
		merged: prs.filter((p) => prLifecycle(p.pr) === "merged").length,
		all: prs.length,
	};

	return (
		<View style={styles.screen}>
			<View style={{ height: insets.top }} />
			<ScreenHeader
				title="Pull Requests"
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
			<ProjectSwitcher />

			{loading && prs.length === 0 ? (
				<View style={styles.center}>
					<ActivityIndicator color={t.blue} />
				</View>
			) : (
				<SectionList
					ref={scrollRef}
					sections={sections}
					keyExtractor={({ pr, session }) => `${session.projectId}#${pr.number}`}
					contentContainerStyle={{ paddingBottom: 110 }}
					stickySectionHeadersEnabled={false}
					refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={t.blue} />}
					renderSectionHeader={({ section }) => <ListSectionHeader label={section.label} />}
					renderItem={({ item: { pr, session } }) => (
						<PRCard pr={pr} session={session} summary={summaries.summaryFor(session.id, pr.number)} />
					)}
					ListEmptyComponent={
						filtered.length === 0 ? (
							error ? (
								<EmptyState
									icon="wifi-off"
									title={failure.title}
									message={failure.message}
									action={<Button title="Retry" icon="refresh-cw" variant="ghost" onPress={onRefresh} />}
								/>
							) : (
								<EmptyState
									icon="git-pull-request"
									title="No pull requests"
									message={filter === "open" ? "No open PRs right now." : "Nothing here yet."}
								/>
							)
						) : null
					}
				/>
			)}

			<View style={[styles.dock, { bottom: Math.max(insets.bottom, 12) }]}>
				<PRFilterDock filter={filter} counts={counts} onChange={setFilter} />
			</View>
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgBase },
		center: { flex: 1, alignItems: "center", justifyContent: "center" },
		dock: {
			position: "absolute",
			left: 16,
			right: 16,
			height: 52,
			flexDirection: "row",
			alignItems: "center",
			justifyContent: "center",
			gap: 8,
		},
	});
