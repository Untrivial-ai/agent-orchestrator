import { Feather } from "@expo/vector-icons";
import { useLocalSearchParams, useNavigation } from "expo-router";
import { useCallback, useEffect, useLayoutEffect, useState } from "react";
import { ActivityIndicator, RefreshControl, ScrollView, StyleSheet, Text, View } from "react-native";
import { getSessionReviews, type ReviewRun, type SessionReviews } from "../../lib/api";
import { haptics } from "../../lib/haptics";
import { reviewForPullRequest, reviewStatusLabel, reviewVerdictLabel, shortCommit } from "../../lib/reviewView";
import { useApp } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { Button, Card, EmptyState } from "../../lib/ui";

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";

export default function ReviewDetailScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const navigation = useNavigation();
	const { sessionId, prUrl, prNumber } = useLocalSearchParams<{ sessionId: string; prUrl?: string; prNumber?: string }>();
	const { config } = useApp();
	const [data, setData] = useState<SessionReviews>();
	const [error, setError] = useState("");
	const [refreshing, setRefreshing] = useState(false);

	const load = useCallback(async () => {
		if (!config || !sessionId) return;
		setError("");
		try {
			setData(await getSessionReviews(config, sessionId));
		} catch (value) {
			setError(value instanceof Error ? value.message : "Could not load this review.");
		}
	}, [config, sessionId]);

	useEffect(() => { void load(); }, [load]);
	const review = reviewForPullRequest(data?.reviews ?? [], prUrl, Number(prNumber) || undefined);
	useLayoutEffect(() => navigation.setOptions({ title: review?.title || "Review" }), [navigation, review?.title]);

	const refresh = async () => {
		haptics.tap();
		setRefreshing(true);
		await load();
		setRefreshing(false);
	};

	if (!data && !error) return <View style={styles.center}><ActivityIndicator color={t.blue} /></View>;
	if (!data || !review) return <EmptyState icon={error ? "alert-triangle" : "git-pull-request"} title={error ? "Could not load review" : "No review found"} message={error || "AO has no review state for this pull request yet."} action={<Button title="Try again" icon="refresh-cw" variant="ghost" onPress={() => void load()} />} />;

	return (
		<ScrollView style={styles.screen} contentContainerStyle={styles.content} refreshControl={<RefreshControl refreshing={refreshing} onRefresh={refresh} tintColor={t.blue} />}>
			<View style={styles.heading}>
				<View style={[styles.statusIcon, { backgroundColor: review.status === "changes_requested" ? t.tintAmber : t.tintBlue }]}>
					<Feather name={review.status === "changes_requested" ? "alert-circle" : review.status === "running" ? "loader" : "check-circle"} size={20} color={review.status === "changes_requested" ? t.amber : t.blue} />
				</View>
				<View style={styles.headingCopy}>
					<Text style={styles.title}>{review.title}</Text>
					<Text style={styles.subtitle}>PR #{review.prNumber} · {reviewStatusLabel(review.status)}</Text>
				</View>
			</View>

			<Card style={styles.metaCard}>
				<Meta label="Reviewer" value={data.reviewerHarness || data.reviewerSurface?.harness || "Not selected"} />
				<Meta label="Commit" value={shortCommit(review.targetSha)} mono />
				{data.reviewerActivityState ? <Meta label="Activity" value={data.reviewerActivityState.replaceAll("_", " ")} /> : null}
			</Card>

			<Text style={styles.sectionLabel}>LATEST RESULT</Text>
			{review.latestRun ? <RunCard run={review.latestRun} /> : <Card><Text style={styles.emptyTitle}>No result for this commit</Text><Text style={styles.body}>This commit still needs a review. An earlier verdict is shown below only as context.</Text></Card>}

			{review.previousRun ? <><Text style={styles.sectionLabel}>EARLIER RESULT</Text><RunCard run={review.previousRun} previous /></> : null}
		</ScrollView>
	);
}

function Meta({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
	const styles = useThemedStyles(makeStyles);
	return <View style={styles.metaRow}><Text style={styles.metaLabel}>{label}</Text><Text style={[styles.metaValue, mono && styles.mono]}>{value}</Text></View>;
}

function RunCard({ run, previous = false }: { run: ReviewRun; previous?: boolean }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const requested = run.verdict === "changes_requested";
	return <Card style={previous ? styles.previousCard : undefined}>
		<View style={styles.runHeader}><Feather name={requested ? "alert-circle" : run.verdict === "approved" ? "check-circle" : "clock"} size={17} color={requested ? t.amber : run.verdict === "approved" ? t.green : t.textSecondary} /><Text style={styles.runTitle}>{reviewVerdictLabel(run)}</Text><Text style={styles.sha}>{shortCommit(run.targetSha)}</Text></View>
		<Text style={styles.runBy}>{run.harness} · {run.triggerSource}</Text>
		{run.body ? <Text style={styles.body}>{run.body}</Text> : <Text style={styles.bodyMuted}>No written findings.</Text>}
	</Card>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	content: { padding: 16, paddingBottom: 40, gap: 12 },
	center: { flex: 1, alignItems: "center", justifyContent: "center", backgroundColor: t.bgBase },
	heading: { flexDirection: "row", alignItems: "center", gap: 12, paddingVertical: 4 },
	statusIcon: { width: 42, height: 42, borderRadius: 13, alignItems: "center", justifyContent: "center" },
	headingCopy: { flex: 1, gap: 3 },
	title: { color: t.textPrimary, fontSize: 20, lineHeight: 25, fontWeight: "700" },
	subtitle: { color: t.textSecondary, fontSize: 13 },
	metaCard: { gap: 10 },
	metaRow: { flexDirection: "row", alignItems: "center", gap: 12 },
	metaLabel: { width: 72, color: t.textTertiary, fontSize: 12, textTransform: "uppercase", letterSpacing: 0.5 },
	metaValue: { flex: 1, color: t.textPrimary, fontSize: 14, textTransform: "capitalize" },
	mono: { fontFamily: t.fontMono, textTransform: "none" },
	sectionLabel: { color: t.textTertiary, fontSize: 11, fontWeight: "700", letterSpacing: 0.8, marginTop: 8, marginLeft: 4 },
	runHeader: { flexDirection: "row", alignItems: "center", gap: 8 },
	runTitle: { flex: 1, color: t.textPrimary, fontSize: 16, fontWeight: "700" },
	sha: { color: t.textTertiary, fontSize: 11, fontFamily: t.fontMono },
	runBy: { color: t.textTertiary, fontSize: 12, marginTop: 6, textTransform: "capitalize" },
	body: { color: t.textSecondary, fontSize: 14, lineHeight: 21, marginTop: 12 },
	bodyMuted: { color: t.textTertiary, fontSize: 14, marginTop: 12, fontStyle: "italic" },
	emptyTitle: { color: t.textPrimary, fontSize: 15, fontWeight: "700" },
	previousCard: { opacity: 0.78 },
});
