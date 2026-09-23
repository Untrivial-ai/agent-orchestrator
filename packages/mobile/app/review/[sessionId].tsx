import { Feather } from "@expo/vector-icons";
import { useFocusEffect, useLocalSearchParams, useNavigation, useRouter } from "expo-router";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ActivityIndicator, Alert, Pressable, RefreshControl, ScrollView, StyleSheet, Text, View } from "react-native";
import { cancelSessionReview, getSessionReviews, killSessionReviewer, restoreSessionReviewer, sendMessage, triggerSessionReview, type ReviewRun, type SessionReviews } from "../../lib/api";
import { ChatMarkdown } from "../../lib/chat/ChatMarkdown";
import { haptics } from "../../lib/haptics";
import { openGitHub } from "../../lib/openGitHub";
import { formatReviewSummaryMessage, reviewRunsForPullRequest, reviewRunUrl } from "../../lib/reviewFeedback";
import { latestAutoReviewFailure, reviewBatchAction, reviewerDestination, reviewForPullRequest, reviewPrimaryActionLabel, reviewStatusLabel, reviewStatusVisual, reviewVerdictLabel, shortCommit } from "../../lib/reviewView";
import { useApp } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { Button, Card, EmptyState } from "../../lib/ui";

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";

export default function ReviewDetailScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const navigation = useNavigation();
	const router = useRouter();
	const { sessionId, prUrl, prNumber } = useLocalSearchParams<{ sessionId: string; prUrl?: string; prNumber?: string }>();
	const { config, sessions } = useApp();
	const autoReviewEnabled = sessions.find((session) => session.id === sessionId)?.autoReviewEnabled === true;
	const [data, setData] = useState<SessionReviews>();
	const [error, setError] = useState("");
	const [reviewNotice, setReviewNotice] = useState("");
	const [dismissedAutoFailureId, setDismissedAutoFailureId] = useState<string>();
	const [refreshing, setRefreshing] = useState(false);
	const [mutation, setMutation] = useState<"review" | "restore" | string>();
	const latestLoad = useRef(0);

	const load = useCallback(async (quiet = false) => {
		if (!config || !sessionId) return;
		const request = ++latestLoad.current;
		if (!quiet) setError("");
		try {
			const next = await getSessionReviews(config, sessionId);
			if (request === latestLoad.current) {
				setData(next);
				setError("");
			}
		} catch (value) {
			if (!quiet && request === latestLoad.current) setError(value instanceof Error ? value.message : "Could not load this review.");
		}
	}, [config, sessionId]);

	useFocusEffect(useCallback(() => { void load(); }, [load]));
	const review = reviewForPullRequest(data?.reviews ?? [], prUrl, Number(prNumber) || undefined);
	const autoReviewFailure = latestAutoReviewFailure(data?.reviews ?? [], autoReviewEnabled);
	useEffect(() => {
		if (!autoReviewFailure || autoReviewFailure.id === dismissedAutoFailureId) return;
		const timer = setTimeout(() => setDismissedAutoFailureId(autoReviewFailure.id), 10_000);
		return () => clearTimeout(timer);
	}, [autoReviewFailure, dismissedAutoFailureId]);
	const anyReviewRunning = data?.reviews.some((item) => item.status === "running") ?? false;
	useLayoutEffect(() => navigation.setOptions({ title: review?.title || "Review" }), [navigation, review?.title]);
	useLayoutEffect(() => navigation.setOptions({
		headerRight: review?.prUrl ? () => <View style={styles.headerActions}>
			<Pressable accessibilityRole="link" accessibilityLabel={`Open pull request ${review.prNumber} in GitHub`} hitSlop={8} style={styles.headerAction} onPress={() => { haptics.tap(); void openGitHub(review.prUrl); }}><Feather name="external-link" size={19} color={t.textSecondary} /></Pressable>
			<Pressable accessibilityRole="button" accessibilityLabel="More review actions" hitSlop={8} style={styles.headerAction} onPress={() => { haptics.tap(); router.push({ pathname: "/sheets/review-actions", params: { sessionId, prUrl: review.prUrl, reviewer: data?.reviewerHarness ?? "", running: String(anyReviewRunning) } }); }}><Feather name="more-horizontal" size={21} color={t.textSecondary} /></Pressable>
		</View> : undefined,
	}), [anyReviewRunning, data?.reviewerHarness, navigation, review?.prNumber, review?.prUrl, router, sessionId, styles.headerAction, styles.headerActions, t.textSecondary]);
	useEffect(() => {
		if (review?.status !== "running" && !autoReviewEnabled) return;
		const timer = setInterval(() => void load(true), 2_000);
		return () => clearInterval(timer);
	}, [autoReviewEnabled, load, review?.status]);

	const refresh = async () => {
		haptics.tap();
		setRefreshing(true);
		await load();
		setRefreshing(false);
	};

	if (!data && !error) return <View style={styles.center}><ActivityIndicator color={t.blue} /></View>;
	if (!data || !review) return <EmptyState icon={error ? "alert-triangle" : "git-pull-request"} title={error ? "Could not load review" : "No review found"} message={error || "AO has no review state for this pull request yet."} action={<Button title="Try again" icon="refresh-cw" variant="ghost" onPress={() => void load()} />} />;
	const primaryAction = reviewBatchAction(review, data.reviews);
	const runs = reviewRunsForPullRequest([...(data.runs ?? []), ...(review.latestRun ? [review.latestRun] : []), ...(review.previousRun ? [review.previousRun] : [])], review.prUrl);
	const multiplePullRequests = data.reviews.length > 1;
	const openReviewer = () => {
		const destination = reviewerDestination(data, review, sessionId);
		if (!destination) return;
		haptics.tap();
		router.push(destination);
	};
	const restoreReviewer = async () => {
		if (!config || mutation) return;
		haptics.tap();
		setMutation("restore");
		setError("");
		try {
			await restoreSessionReviewer(config, sessionId);
			await load();
		} catch (value) {
			setError(value instanceof Error ? value.message : "Could not restore the reviewer.");
		} finally {
			setMutation(undefined);
		}
	};
	const confirmKillReviewer = () => {
		if (!config || mutation || !data.reviewerHandleId || autoReviewEnabled) return;
		Alert.alert("Stop reviewer session?", "This closes the persistent reviewer and cancels any review it is currently running. Review history is preserved.", [
			{ text: "Keep reviewer", style: "cancel" },
			{ text: "Stop reviewer", style: "destructive", onPress: () => void (async () => {
				setMutation("kill"); setError("");
				try { setData(await killSessionReviewer(config, sessionId)); haptics.success(); }
				catch (value) { setError(value instanceof Error ? value.message : "Could not stop the reviewer session."); }
				finally { setMutation(undefined); }
			})() },
		]);
	};
	const runPrimaryAction = async () => {
		if (!config || primaryAction === "none" || mutation) return;
		haptics.tap();
		setMutation("review");
		setError("");
		setReviewNotice("");
		try {
			if (primaryAction === "cancel") await cancelSessionReview(config, sessionId);
			else {
				const result = await triggerSessionReview(config, sessionId);
				if (!result.created) setReviewNotice("This commit has already been reviewed. Showing its existing result.");
			}
			await load();
		} catch (value) {
			setError(value instanceof Error ? value.message : "The review action failed.");
		} finally {
			setMutation(undefined);
		}
	};
	const sendRun = async (run: ReviewRun) => {
		if (!config || mutation) return;
		setMutation(`send:${run.id}`);
		setError("");
		try {
			await sendMessage(config, sessionId, formatReviewSummaryMessage(run));
			haptics.success();
		} catch (value) {
			setError(value instanceof Error ? value.message : "Could not send this review to the worker.");
		} finally {
			setMutation(undefined);
		}
	};

	return (
		<ScrollView style={styles.screen} contentContainerStyle={styles.content} refreshControl={<RefreshControl refreshing={refreshing} onRefresh={refresh} tintColor={t.blue} />}>
			<View style={styles.heading}>
				<View style={[styles.statusIcon, { backgroundColor: statusColor(t, reviewStatusVisual(review.status).tone, true) }]}>
					<Feather name={reviewStatusVisual(review.status).icon} size={20} color={statusColor(t, reviewStatusVisual(review.status).tone)} />
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
			{autoReviewFailure && autoReviewFailure.id !== dismissedAutoFailureId ? <View accessibilityRole="alert" style={styles.autoReviewFailure}>
				<Feather name="alert-circle" size={16} color={t.red} />
				<View style={styles.failureCopy}><Text style={styles.failureTitle}>Automatic review failed</Text><Text style={styles.failureBody}>{autoReviewFailure.body.trim()}</Text></View>
				<Pressable accessibilityRole="button" accessibilityLabel="Dismiss automatic review failure" hitSlop={8} onPress={() => setDismissedAutoFailureId(autoReviewFailure.id)}><Feather name="x" size={17} color={t.red} /></Pressable>
			</View> : null}
			{reviewNotice ? <View style={styles.notice}><Feather name="check" size={15} color={t.green} /><Text style={styles.noticeText}>{reviewNotice}</Text></View> : null}
			{data.reviewerActivityState === "exited" || data.reviewerSurface?.controllerError
				? <Button title="Restore reviewer" icon="refresh-cw" variant="ghost" loading={mutation === "restore"} disabled={Boolean(mutation)} onPress={() => void restoreReviewer()} />
				: data.reviewerSurface ? <Button title={data.reviewerSurface.mode === "chat" ? "Open reviewer chat" : "Open reviewer terminal"} icon={data.reviewerSurface.mode === "chat" ? "message-circle" : "terminal"} variant="ghost" disabled={Boolean(mutation)} onPress={openReviewer} /> : null}
			{data.reviewerHandleId ? <><Button title="Stop reviewer session" icon="power" variant="ghost" loading={mutation === "kill"} disabled={Boolean(mutation) || autoReviewEnabled} onPress={confirmKillReviewer} />{autoReviewEnabled ? <Text style={styles.scopeNote}>Turn off automatic review before stopping its reviewer session.</Text> : null}</> : null}
			{data.reviewerSurface?.controllerError ? <Text accessibilityRole="alert" style={styles.error}>{data.reviewerSurface.controllerError}</Text> : null}
			{primaryAction !== "none" ? <Button title={reviewPrimaryActionLabel(primaryAction, multiplePullRequests)} icon={primaryAction === "cancel" ? "x" : "play"} variant={primaryAction === "cancel" ? "danger" : "primary"} loading={mutation === "review"} disabled={Boolean(mutation) || primaryAction !== "cancel" && autoReviewEnabled} onPress={() => void runPrimaryAction()} /> : null}
			{primaryAction !== "cancel" && autoReviewEnabled ? <Text style={styles.scopeNote}>Automatic review is watching for new commits. Turn it off in Review actions to run reviews manually.</Text> : null}
			{primaryAction !== "none" && multiplePullRequests ? <Text style={styles.scopeNote}>This action applies to every eligible pull request in this session.</Text> : null}
			{error ? <Text accessibilityRole="alert" style={styles.error}>{error}</Text> : null}

			<Text style={styles.sectionLabel}>AO REVIEW HISTORY</Text>
			{runs.length ? runs.map((run, index) => <RunCard key={run.id} run={run} previous={index > 0} sending={mutation === `send:${run.id}`} disabled={Boolean(mutation)} onSend={() => void sendRun(run)} />) : <Card><Text style={styles.emptyTitle}>No result for this pull request</Text><Text style={styles.body}>This pull request still needs a review.</Text></Card>}
		</ScrollView>
	);
}

function Meta({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
	const styles = useThemedStyles(makeStyles);
	return <View style={styles.metaRow}><Text style={styles.metaLabel}>{label}</Text><Text style={[styles.metaValue, mono && styles.mono]}>{value}</Text></View>;
}

function RunCard({ run, previous = false, sending, disabled, onSend }: { run: ReviewRun; previous?: boolean; sending: boolean; disabled: boolean; onSend: () => void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const requested = run.verdict === "changes_requested";
	const url = reviewRunUrl(run);
	return <Card style={previous ? styles.previousCard : undefined}>
		<View style={styles.runHeader}><Feather name={requested ? "alert-circle" : run.verdict === "approved" ? "check-circle" : "clock"} size={17} color={requested ? t.amber : run.verdict === "approved" ? t.green : t.textSecondary} /><Text style={styles.runTitle}>{reviewVerdictLabel(run)}</Text><Text style={styles.sha}>{shortCommit(run.targetSha)}</Text></View>
		<Text style={styles.runBy}>{run.harness} · {run.triggerSource} · {run.status}{run.deliveredAt ? " · delivered" : ""}</Text>
		{run.autoInjectReview === false ? <View style={styles.notInjected}><Feather name="info" size={13} color={t.amber} /><Text style={styles.notInjectedText}>Not automatically sent to the worker</Text></View> : null}
		{run.body ? <View style={styles.markdown}><ChatMarkdown text={run.body} /></View> : <Text style={styles.bodyMuted}>No written findings.</Text>}
		<View style={styles.runActions}>{url ? <Pressable accessibilityRole="link" onPress={() => void openGitHub(url)} style={styles.smallAction}><Feather name="external-link" size={14} color={t.blue} /><Text style={styles.smallActionText}>Open on GitHub</Text></Pressable> : null}<Pressable accessibilityRole="button" disabled={disabled} onPress={onSend} style={[styles.smallAction, disabled && styles.actionDisabled]}>{sending ? <ActivityIndicator size="small" color={t.blue} /> : <Feather name="send" size={14} color={t.blue} />}<Text style={styles.smallActionText}>Send to worker</Text></Pressable></View>
	</Card>;
}

function statusColor(t: Theme, tone: ReturnType<typeof reviewStatusVisual>["tone"], tint = false): string {
	if (tone === "amber") return tint ? t.tintAmber : t.amber;
	if (tone === "green") return tint ? t.tintGreen : t.green;
	if (tone === "blue") return tint ? t.tintBlue : t.blue;
	return tint ? t.bgSubtle : t.textTertiary;
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
	notice: { flexDirection: "row", alignItems: "center", gap: 8, backgroundColor: t.tintGreen, borderRadius: 10, paddingHorizontal: 12, paddingVertical: 10 },
	noticeText: { color: t.green, flex: 1, fontSize: 13 },
	autoReviewFailure: { flexDirection: "row", alignItems: "flex-start", gap: 9, borderWidth: StyleSheet.hairlineWidth, borderColor: t.red, backgroundColor: t.tintRed, borderRadius: 10, padding: 11 },
	failureCopy: { flex: 1, gap: 3 },
	failureTitle: { color: t.red, fontSize: 13, fontWeight: "700" },
	failureBody: { color: t.red, fontSize: 13, lineHeight: 18 },
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
	markdown: { marginTop: 12 },
	bodyMuted: { color: t.textTertiary, fontSize: 14, marginTop: 12, fontStyle: "italic" },
	emptyTitle: { color: t.textPrimary, fontSize: 15, fontWeight: "700" },
	error: { color: t.red, fontSize: 13, lineHeight: 18 },
	scopeNote: { color: t.textTertiary, fontSize: 12, lineHeight: 17, textAlign: "center", paddingHorizontal: 12 },
	previousCard: { opacity: 0.78 },
	notInjected: { flexDirection: "row", alignItems: "center", gap: 6, marginTop: 8 },
	notInjectedText: { color: t.amber, fontSize: 12 },
	runActions: { flexDirection: "row", flexWrap: "wrap", gap: 10, marginTop: 14 },
	smallAction: { flexDirection: "row", alignItems: "center", gap: 6, minHeight: 34, paddingHorizontal: 4 },
	smallActionText: { color: t.blue, fontSize: 13, fontWeight: "600" },
	actionDisabled: { opacity: 0.45 },
	headerActions: { flexDirection: "row", alignItems: "center", gap: 2 },
	headerAction: { width: 36, height: 36, alignItems: "center", justifyContent: "center" },
});
