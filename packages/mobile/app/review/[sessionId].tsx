import { Feather } from "@expo/vector-icons";
import { useFocusEffect, useLocalSearchParams, useNavigation, useRouter } from "expo-router";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ActivityIndicator, Alert, Pressable, RefreshControl, ScrollView, StyleSheet, Text, View } from "react-native";
import { cancelSessionReview, getSessionPR, getSessionReviews, killSessionReviewer, mergeSessionPR, restoreSessionReviewer, sendMessage, triggerSessionReview, type ReviewRun, type SessionPRSummary, type SessionReviews } from "../../lib/api";
import { ChatMarkdown } from "../../lib/chat/ChatMarkdown";
import { haptics } from "../../lib/haptics";
import { ItemActionsMenu } from "../../lib/item-actions-menu";
import type { ItemAction } from "../../lib/item-actions-menu.types";
import { openGitHub } from "../../lib/openGitHub";
import { useOpenPage } from "../../lib/pageNavigation";
import { mergeReadiness, type MergeTone } from "../../lib/prMerge";
import { formatReviewSummaryMessage, reviewRunsForPullRequest, reviewRunUrl } from "../../lib/reviewFeedback";
import { latestAutoReviewFailure, pullRequestSummaryForURL, reviewBatchAction, reviewerControls, reviewerDestination, reviewForPullRequest, reviewPrimaryActionLabel, reviewRunMeta, reviewRunSendable, reviewStatusLabel, reviewVerdictLabel, shortCommit } from "../../lib/reviewView";
import { useApp } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { AgentLogo } from "../../lib/AgentLogo";
import { rowDividerWidth } from "../../lib/divider";
import { space, type } from "../../lib/tokens";
import { Button, Dot, EmptyState, ListSectionHeader } from "../../lib/ui";

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";

export default function ReviewDetailScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const navigation = useNavigation();
	const router = useRouter();
	const openPage = useOpenPage();
	const { sessionId, prUrl, prNumber } = useLocalSearchParams<{ sessionId: string; prUrl?: string; prNumber?: string }>();
	const { config, sessions } = useApp();
	const autoReviewEnabled = sessions.find((session) => session.id === sessionId)?.autoReviewEnabled === true;
	const [data, setData] = useState<SessionReviews>();
	const [prs, setPRs] = useState<SessionPRSummary[]>([]);
	const [sentRuns, setSentRuns] = useState<ReadonlySet<string>>(new Set());
	// The daemon learns a merge from its next GitHub observation, seconds after
	// the merge call returns. Show the merge at once and poll until it agrees.
	const [mergedURL, setMergedURL] = useState<string>();
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
			const [next, nextPRs] = await Promise.all([getSessionReviews(config, sessionId), getSessionPR(config, sessionId).catch(() => undefined)]);
			if (request === latestLoad.current) {
				setData(next);
				if (nextPRs) setPRs(nextPRs);
				setError("");
			}
		} catch (value) {
			if (!quiet && request === latestLoad.current) setError(value instanceof Error ? value.message : "Could not load this review.");
		}
	}, [config, sessionId]);

	useFocusEffect(useCallback(() => { void load(); }, [load]));
	const review = reviewForPullRequest(data?.reviews ?? [], prUrl, Number(prNumber) || undefined);
	const autoReviewFailure = latestAutoReviewFailure(data?.reviews ?? [], autoReviewEnabled);
	const autoReviewFailureId = autoReviewFailure?.id;
	useEffect(() => {
		if (!autoReviewFailureId || autoReviewFailureId === dismissedAutoFailureId) return;
		const timer = setTimeout(() => setDismissedAutoFailureId(autoReviewFailureId), 10_000);
		return () => clearTimeout(timer);
	}, [autoReviewFailureId, dismissedAutoFailureId]);
	useLayoutEffect(() => navigation.setOptions({
		title: review?.title || "Review",
		headerTitleStyle: { color: t.textPrimary, fontFamily: "Geist_600SemiBold", fontWeight: "600" },
	}), [navigation, review?.title, t.textPrimary]);
	useLayoutEffect(() => navigation.setOptions({
		headerRight: review?.prUrl ? () => <View style={styles.headerActions}>
			<Pressable accessibilityRole="link" accessibilityLabel={`Open pull request ${review.prNumber} in GitHub`} hitSlop={8} style={styles.headerAction} onPress={() => { haptics.tap(); void openGitHub(review.prUrl); }}><Feather name="external-link" size={19} color={t.textSecondary} /></Pressable>
			<Pressable accessibilityRole="button" accessibilityLabel="More review actions" hitSlop={8} style={styles.headerAction} onPress={() => { haptics.tap(); router.push({ pathname: "/sheets/review-actions", params: { sessionId, prUrl: review.prUrl, reviewer: data?.reviewerHarness ?? "" } }); }}><Feather name="more-horizontal" size={21} color={t.textSecondary} /></Pressable>
		</View> : undefined,
	}), [data?.reviewerHarness, navigation, review?.prNumber, review?.prUrl, router, sessionId, styles.headerAction, styles.headerActions, t.textSecondary]);
	const observedPR = review ? pullRequestSummaryForURL(prs, review.prUrl) : undefined;
	const awaitingMerge = Boolean(mergedURL && observedPR && observedPR.url === mergedURL && observedPR.state !== "merged");
	useEffect(() => {
		if (!awaitingMerge) return;
		const timer = setInterval(() => void load(true), 2_000);
		const giveUp = setTimeout(() => setMergedURL(undefined), 60_000);
		return () => { clearInterval(timer); clearTimeout(giveUp); };
	}, [awaitingMerge, load]);
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

	if (!data && !error) return <View style={styles.center}><ActivityIndicator color={t.accent} /></View>;
	if (!data || !review) return <EmptyState icon={error ? "alert-triangle" : "git-pull-request"} title={error ? "Could not load review" : "No review found"} message={error || "AO has no review state for this pull request yet."} action={<Button title="Try again" icon="refresh-cw" variant="ghost" onPress={() => void load()} />} />;
	const primaryAction = reviewBatchAction(review, data.reviews);
	const runs = reviewRunsForPullRequest([...(data.runs ?? []), ...(review.latestRun ? [review.latestRun] : []), ...(review.previousRun ? [review.previousRun] : [])], review.prUrl);
	const multiplePullRequests = data.reviews.length > 1;
	const openReviewer = () => {
		const destination = reviewerDestination(data, review, sessionId);
		if (!destination) return;
		haptics.tap();
		openPage(destination);
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
				if (!result.created) setReviewNotice("This commit has already been reviewed. Push a new commit to run another review.");
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
			setSentRuns((current) => new Set(current).add(run.id));
			haptics.success();
		} catch (value) {
			setError(value instanceof Error ? value.message : "Could not send this review to the worker.");
		} finally {
			setMutation(undefined);
		}
	};

	const controls = reviewerControls(data, review, sessionId);
	const pr = observedPR && awaitingMerge ? { ...observedPR, state: "merged" as const } : observedPR;
	const merge = pr ? mergeReadiness(pr) : undefined;
	const closedPR = pr?.state === "merged" || pr?.state === "closed";
	const confirmMerge = () => {
		if (!config || !pr || !merge?.canMerge || mutation) return;
		Alert.alert(`Merge PR #${pr.number}?`, `This will squash-merge PR #${pr.number} in the remote repository.`, [
			{ text: "Cancel", style: "cancel" },
			{ text: "Merge", onPress: () => void (async () => {
				setMutation("merge"); setError("");
				try { await mergeSessionPR(config, pr); setMergedURL(pr.url); haptics.success(); await load(true); }
				catch (value) { setError(value instanceof Error ? value.message : `Could not merge PR #${pr.number}.`); }
				finally { setMutation(undefined); }
			})() },
		]);
	};

	const reviewerHarness = data.reviewerSurface?.harness || data.reviewerHarness || "";
	const activity = data.reviewerActivityState;
	const activityColor = activity === "active" ? t.orange : activity === "exited" ? t.red : t.textTertiary;
	const stopActions: ItemAction[] = controls.stop && !autoReviewEnabled
		? [{ id: "stop", label: "Stop reviewer session", systemImage: "power", destructive: true, onPress: confirmKillReviewer }]
		: [];
	// The review row describes the AO review itself; merge state lives in the PR row.
	const latestRun = review.latestRun ?? review.previousRun;
	const reviewLabel = review.status === "running" ? reviewStatusLabel("running") : latestRun ? reviewVerdictLabel(latestRun) : reviewStatusLabel(review.status);
	const reviewColor = review.status === "running" ? t.orange : latestRun?.verdict === "approved" ? t.green : latestRun?.verdict === "changes_requested" ? t.amber : latestRun?.status === "failed" ? t.red : t.textSecondary;
	const reviewDetail = closedPR ? `No new reviews: this pull request is ${pr?.state}.` : reviewStatusLabel(review.status);
	const reviewNote = primaryAction !== "cancel" && autoReviewEnabled
		? "Automatic review is watching for new commits. Turn it off in Review actions to run reviews manually."
		: primaryAction !== "none" && multiplePullRequests ? "Applies to every eligible pull request in this session." : undefined;

	return (
		<ScrollView style={styles.screen} contentContainerStyle={styles.content} refreshControl={<RefreshControl refreshing={refreshing} onRefresh={refresh} tintColor={t.accent} />}>
			{/* Flat rows under section labels, like the worker board and project pages. */}
			<ListSectionHeader label="Pull request" />
			<View style={styles.row}>
				<View style={styles.main}>
					<View style={styles.eyebrow}>
						<Feather name="git-pull-request" size={13} color={t.textSecondary} />
						<Text style={styles.eyebrowText} numberOfLines={1}>PR #{review.prNumber}{pr?.targetBranch ? ` into ${pr.targetBranch}` : ""}</Text>
					</View>
					<Text style={styles.title} numberOfLines={2}>{review.title}</Text>
					{merge ? <View style={styles.statusLine}>
						<Dot color={toneColor(t, merge.tone)} size={6} />
						<Text style={[styles.statusStrong, { color: toneColor(t, merge.tone) }]}>{merge.label}</Text>
					</View> : null}
					{merge?.reason ? <Text style={styles.detail}>{merge.reason}</Text> : null}
				</View>
				{merge?.canMerge ? <RowPill label={mutation === "merge" ? "Merging…" : "Merge"} icon="git-merge" tone="merge" busy={mutation === "merge"} disabled={Boolean(mutation)} onPress={confirmMerge} /> : null}
			</View>

			<ListSectionHeader label="Reviewer" />
			{/* The row opens the reviewer; its pill and menu are laid over the row rather
			    than inside its Pressable (as on the project page's orchestrator row), so
			    a tap on "⋯" never also opens the reviewer. */}
			<View>
			<Pressable accessibilityRole={controls.open ? "button" : undefined} accessibilityLabel={controls.open ? `Open reviewer ${controls.open}` : undefined} disabled={!controls.open || Boolean(mutation)} onPress={openReviewer} style={({ pressed }) => [styles.row, styles.rowWithOverlay, pressed && styles.rowPressed]}>
				<View style={styles.main}>
					<View style={styles.eyebrow}>
						{reviewerHarness ? <AgentLogo harness={reviewerHarness} size={14} /> : <Feather name="user" size={13} color={t.textSecondary} />}
						<Text style={styles.eyebrowText} numberOfLines={1}>AO reviewer</Text>
					</View>
					<Text style={styles.title} numberOfLines={1}>{reviewerName(reviewerHarness)}</Text>
					<View style={styles.statusLine}>
						<Dot color={activityColor} size={6} breathing={activity === "active"} />
						<Text style={[styles.statusStrong, { color: activityColor }]}>{activity ? capitalize(activity.replaceAll("_", " ")) : "Not started"}</Text>
						<Text style={styles.statusDetail} numberOfLines={1}>{controls.open === "chat" ? " · chat" : controls.open === "terminal" ? " · terminal" : ""}</Text>
					</View>
				</View>
			</Pressable>
				<View style={styles.overlaySlot} pointerEvents="box-none">
				<View style={styles.trailing}>
					{controls.open
						? <RowPill label="Open" icon={controls.open === "chat" ? "message-circle" : "terminal"} tone="solid" disabled={Boolean(mutation)} onPress={openReviewer} />
						: controls.restore ? <RowPill label="Restore" icon="refresh-cw" tone="outline" busy={mutation === "restore"} disabled={Boolean(mutation)} onPress={() => void restoreReviewer()} /> : null}
					<ItemActionsMenu accessibilityLabel="Reviewer session actions" actions={stopActions} loading={mutation === "kill"} disabled={Boolean(mutation)} />
				</View>
				</View>
			</View>
			{controls.stop && autoReviewEnabled ? <Text style={styles.note}>Turn off automatic review before stopping its reviewer session.</Text> : null}
			{data.reviewerSurface?.controllerError ? <Text accessibilityRole="alert" style={[styles.note, styles.noteError]}>{data.reviewerSurface.controllerError}</Text> : null}

			<ListSectionHeader label="AO review" />
			<View style={styles.row}>
				<View style={styles.main}>
					<View style={styles.eyebrow}>
						<Feather name="git-commit" size={13} color={t.textSecondary} />
						<Text style={[styles.eyebrowText, styles.mono]} numberOfLines={1}>{shortCommit(review.targetSha)}</Text>
					</View>
					<Text style={styles.title} numberOfLines={1}>{reviewLabel}</Text>
					<View style={styles.statusLine}>
						<Dot color={reviewColor} size={6} breathing={review.status === "running"} />
						<Text style={styles.detail} numberOfLines={2}>{reviewDetail}</Text>
					</View>
					{reviewNote ? <Text style={styles.detail}>{reviewNote}</Text> : null}
				</View>
				{primaryAction !== "none" ? <RowPill label={reviewPrimaryActionLabel(primaryAction, multiplePullRequests)} icon={primaryAction === "cancel" ? "x" : "play"} tone={primaryAction === "cancel" ? "danger" : primaryAction === "start" ? "solid" : "outline"} busy={mutation === "review"} disabled={Boolean(mutation) || primaryAction !== "cancel" && autoReviewEnabled} onPress={() => void runPrimaryAction()} /> : null}
			</View>
			{reviewNotice ? <View style={styles.inlineNote}><Feather name="check" size={13} color={t.green} /><Text style={[styles.inlineNoteText, { color: t.green }]}>{reviewNotice}</Text></View> : null}
			{autoReviewFailure && autoReviewFailure.id !== dismissedAutoFailureId ? <View accessibilityRole="alert" style={styles.inlineNote}>
				<Feather name="alert-circle" size={13} color={t.red} />
				<Text style={[styles.inlineNoteText, { color: t.red }]}><Text style={styles.inlineNoteStrong}>Automatic review failed. </Text>{autoReviewFailure.body.trim()}</Text>
				<Pressable accessibilityRole="button" accessibilityLabel="Dismiss automatic review failure" hitSlop={10} onPress={() => setDismissedAutoFailureId(autoReviewFailure.id)}><Feather name="x" size={15} color={t.red} /></Pressable>
			</View> : null}
			{error ? <Text accessibilityRole="alert" style={[styles.note, styles.noteError]}>{error}</Text> : null}

			<ListSectionHeader label="History" count={runs.length} />
			{runs.length
				? runs.map((run) => <RunRow key={run.id} run={run} sent={sentRuns.has(run.id)} sending={mutation === `send:${run.id}`} disabled={Boolean(mutation)} onSend={() => void sendRun(run)} />)
				: <Text style={styles.note}>No AO review for this pull request yet.</Text>}
		</ScrollView>
	);
}

type PillTone = "solid" | "outline" | "danger" | "merge";

/** The board's row pill: solid for the live action, outlined for the rest. */
function RowPill({ label, icon, tone, busy = false, disabled = false, onPress }: { label: string; icon: keyof typeof Feather.glyphMap; tone: PillTone; busy?: boolean; disabled?: boolean; onPress: () => void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const filled = tone === "solid" || tone === "merge";
	const ink = tone === "solid" ? t.bgBase : tone === "merge" ? t.bgBase : tone === "danger" ? t.red : t.textPrimary;
	return <Pressable
		accessibilityRole="button"
		accessibilityLabel={label}
		accessibilityState={{ busy, disabled: disabled || busy }}
		disabled={disabled || busy}
		hitSlop={8}
		onPress={() => { haptics.tap(); onPress(); }}
		style={({ pressed }) => [styles.pill, tone === "solid" && styles.pillSolid, tone === "merge" && styles.pillMerge, !filled && styles.pillOutline, tone === "danger" && styles.pillDanger, (disabled && !busy) && styles.pillDisabled, pressed && styles.pillPressed]}
	>
		{busy ? <ActivityIndicator size="small" color={ink} /> : <Feather name={icon} size={14} color={ink} />}
		<Text style={[styles.pillLabel, { color: ink }]} numberOfLines={1}>{label}</Text>
	</Pressable>;
}

function RunRow({ run, sent, sending, disabled, onSend }: { run: ReviewRun; sent: boolean; sending: boolean; disabled: boolean; onSend: () => void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const requested = run.verdict === "changes_requested";
	const color = requested ? t.amber : run.verdict === "approved" ? t.green : run.status === "failed" ? t.red : t.textSecondary;
	const url = reviewRunUrl(run);
	// Desktop keeps these in a per-review "⋯" menu.
	const actions: ItemAction[] = [
		...(url ? [{ id: "open", label: "Open on GitHub", systemImage: "arrow.up.right.square", onPress: () => void openGitHub(url) }] : []),
		...(reviewRunSendable(run) && !sent ? [{ id: "send", label: "Send to worker", systemImage: "paperplane", onPress: onSend }] : []),
	];
	return <View style={styles.runRow}>
		<View style={styles.runHead}>
			<Dot color={color} size={6} breathing={run.status === "running"} />
			<Text style={[styles.statusStrong, styles.runVerdict, { color }]} numberOfLines={1}>{reviewVerdictLabel(run)}</Text>
			<Text style={[styles.detail, styles.mono]}>{shortCommit(run.targetSha)}</Text>
			<ItemActionsMenu accessibilityLabel={`Actions for the ${reviewVerdictLabel(run).toLowerCase()} review`} actions={actions} disabled={disabled} loading={sending} />
		</View>
		<Text style={styles.detail}>{reviewRunMeta({ ...run, harness: reviewerName(run.harness) }).split(" · ").map(capitalize).join(" · ")}</Text>
		{run.autoInjectReview === false ? <Text style={[styles.detail, { color: t.amber }]}>Not automatically sent to the worker</Text> : null}
		{run.body ? <View style={styles.markdown}><ChatMarkdown text={run.body} /></View> : <Text style={styles.detail}>{run.status === "running" ? "Findings appear here when the review finishes." : "No written findings."}</Text>}
		{sent ? <View style={styles.statusLine}><Feather name="check" size={13} color={t.green} /><Text style={[styles.statusStrong, { color: t.green }]}>Sent to worker</Text></View> : null}
	</View>;
}

function reviewerName(harness: string): string {
	if (!harness) return "Not selected";
	return harness.split("-").map(capitalize).join(" ");
}

function capitalize(value: string): string {
	return value ? value[0].toUpperCase() + value.slice(1) : value;
}

function toneColor(t: Theme, tone: MergeTone, tint = false): string {
	switch (tone) {
		case "green": return tint ? t.tintGreen : t.green;
		case "purple": return tint ? t.tintPurple : t.purple;
		case "amber": return tint ? t.tintAmber : t.amber;
		case "red": return tint ? t.tintRed : t.red;
		default: return tint ? t.bgSubtle : t.textSecondary;
	}
}

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	content: { paddingBottom: space.huge },
	center: { flex: 1, alignItems: "center", justifyContent: "center", backgroundColor: t.bgBase },
	// A worker row's metrics: eyebrow, title, status line, divider underneath.
	row: { minHeight: 76, flexDirection: "row", alignItems: "center", gap: space.md, paddingHorizontal: space.lg, paddingVertical: space.sm, borderBottomWidth: rowDividerWidth, borderBottomColor: t.borderSubtle, backgroundColor: t.bgBase },
	rowPressed: { backgroundColor: t.bgSubtle },
	// Room for the pill and menu laid over the row's trailing edge.
	rowWithOverlay: { paddingRight: 150 },
	overlaySlot: { position: "absolute", right: space.lg - space.xxs, top: 0, bottom: 0, justifyContent: "center" },
	main: { flex: 1, minWidth: 0, gap: space.hair },
	trailing: { flexDirection: "row", alignItems: "center", gap: space.xxs },
	eyebrow: { flexDirection: "row", alignItems: "center", gap: space.xs, minHeight: 17 },
	eyebrowText: { fontFamily: "Geist_500Medium", flex: 1, color: t.textSecondary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "500" },
	title: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.callout.fontSize, lineHeight: type.callout.lineHeight, fontWeight: "600", letterSpacing: -0.15 },
	statusLine: { flexDirection: "row", alignItems: "center", gap: space.xs, minWidth: 0 },
	statusStrong: { fontFamily: "Geist_600SemiBold", fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "600" },
	statusDetail: { fontFamily: "Geist_400Regular", flexShrink: 1, marginLeft: -space.xs, color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight },
	detail: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight },
	mono: { fontFamily: t.fontMono },
	note: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, paddingHorizontal: space.lg, paddingVertical: space.sm },
	noteError: { color: t.red },
	inlineNote: { flexDirection: "row", alignItems: "flex-start", gap: space.xs, paddingHorizontal: space.lg, paddingVertical: space.sm, borderBottomWidth: rowDividerWidth, borderBottomColor: t.borderSubtle },
	inlineNoteText: { flex: 1, fontFamily: "Geist_400Regular", fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight },
	inlineNoteStrong: { fontFamily: "Geist_600SemiBold", fontWeight: "600" },
	runRow: { paddingHorizontal: space.lg, paddingVertical: space.md, gap: space.xxs, borderBottomWidth: rowDividerWidth, borderBottomColor: t.borderSubtle },
	runHead: { flexDirection: "row", alignItems: "center", gap: space.xs, minHeight: 28 },
	runVerdict: { flex: 1, fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight },
	markdown: { marginTop: space.xs },
	pill: { flexDirection: "row", alignItems: "center", gap: space.xs, height: 28, paddingHorizontal: space.md, borderRadius: 12 },
	pillSolid: { backgroundColor: t.textPrimary },
	pillMerge: { backgroundColor: t.green },
	pillOutline: { borderWidth: 1, borderColor: t.borderStrong },
	pillDanger: { borderColor: t.red },
	pillDisabled: { opacity: 0.45 },
	pillPressed: { opacity: 0.8 },
	pillLabel: { fontFamily: "Geist_600SemiBold", fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, fontWeight: "600" },
	headerActions: { flexDirection: "row", alignItems: "center", gap: 2 },
	headerAction: { width: 36, height: 36, alignItems: "center", justifyContent: "center" },
});
