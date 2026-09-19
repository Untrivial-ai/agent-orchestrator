import { Host, Switch } from "@expo/ui";
import { Feather } from "@expo/vector-icons";
import { useLocalSearchParams, useRouter } from "expo-router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ActivityIndicator, Alert, Platform, Pressable, RefreshControl, ScrollView, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import {
	ApiError,
	cancelReview,
	getSessionPR,
	getSessionReviews,
	requestRereview,
	resolveReviewComment,
	setAutoReview,
	triggerReview,
	type PRReviewState,
	type SessionPRSummary,
} from "../../lib/api";
import { ChatMarkdown } from "../../lib/chat/ChatMarkdown";
import { isConfigured, type ServerConfig } from "../../lib/config";
import { classifyConnectionFailure, describeConnectionFailure } from "../../lib/connectionError";
import { haptics } from "../../lib/haptics";
import { relativeTime } from "../../lib/notificationView";
import { openGitHub } from "../../lib/openGitHub";
import { toneColor } from "../../lib/prView";
import {
	commentLocation,
	prReviewEntries,
	reviewIsRunning,
	reviewRunActionLabel,
	reviewRunDisabled,
	reviewStateFor,
	reviewStateLabel,
	reviewVerdictLabel,
	reviewsSummaryLine,
	type ReviewComment,
	type ReviewEntry,
} from "../../lib/reviewsView";
import { useApp } from "../../lib/store";
import { UnpairedState } from "../../lib/UnpairedState";
import type { Theme } from "../../lib/theme";
import { fontScaleCap, radius, space, type as typeScale } from "../../lib/tokens";
import { useTheme, useThemedStyles, useThemeState } from "../../lib/ThemeProvider";
import { Button, EmptyState, HeaderIconButton, ScreenHeader } from "../../lib/ui";

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";

/**
 * What the reviewers said about one pull request.
 *
 * Desktop puts this in the session inspector's Reviews tab. On the phone a
 * session is a conversation, not an inspector, so reviews hang off the PR —
 * which is the thing they are about, and the thing the PRs tab already lists.
 *
 * Both halves of the desktop tab: the controls that run AO's own reviewer, and
 * the reviews themselves. The two come from different endpoints —
 * GET /sessions/{id}/reviews is AO's per-PR review *state*, GET /sessions/{id}/pr
 * carries what humans and bots actually wrote — and desktop joins them the same
 * way (SessionInspector.tsx ReviewsSection).
 */
export default function PRReviewsScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const insets = useSafeAreaInsets();
	const { number: rawNumber, sessionId: rawSessionId } = useLocalSearchParams<{ number: string; sessionId: string }>();
	const number = Number(rawNumber);
	const sessionId = String(rawSessionId ?? "");
	const { config, sessions, refresh } = useApp();
	// Workers only: a pull request belongs to the session that opened it, and an
	// orchestrator carries no review policy of its own.
	const session = sessions.find((item) => item.id === sessionId);
	// Tri-state, like the session route: `config` is null until it has been read
	// back from storage, and a boolean "not paired" during that window would
	// flash the onboarding copy at someone who is paired.
	const paired = config === null ? null : isConfigured(config);
	// `Number("")` is 0 and `Number("x")` is NaN; either means the route was
	// opened with something that is not a pull request, which is a miss, not a
	// request to make.
	const usable = Number.isFinite(number) && number > 0 && sessionId !== "";
	const [summary, setSummary] = useState<SessionPRSummary | null>(null);
	const [reviewStates, setReviewStates] = useState<PRReviewState[]>([]);
	const [status, setStatus] = useState<"loading" | "ready" | "failed">("loading");
	const [failureStatus, setFailureStatus] = useState<number | undefined>(undefined);
	const [refreshing, setRefreshing] = useState(false);

	// The screen owns its fetch rather than reading the PRs tab's cache: that
	// cache is scoped to that screen, and a notification or a deep link can land
	// here without the list ever having been shown.
	const generation = useRef(0);
	const load = useCallback(async () => {
		if (!usable) {
			// Nothing to ask for. Settle on the empty state rather than leaving the
			// spinner up forever.
			setSummary(null);
			setStatus("ready");
			return;
		}
		// Config arrives from storage a tick after mount; the effect re-runs when
		// it lands, so staying on the spinner here is the right wait.
		if (!config) return;
		const run = ++generation.current;
		try {
			// The PR detail is what the screen is for; the review state only drives
			// the controls, so a daemon that cannot answer it still gets to show the
			// reviews rather than an error page.
			const [prs, reviews] = await Promise.all([
				getSessionPR(config, sessionId),
				getSessionReviews(config, sessionId).catch(() => null),
			]);
			if (run !== generation.current) return;
			setSummary(prs.find((pr) => pr.number === number) ?? null);
			if (reviews) setReviewStates(reviews.reviews);
			setFailureStatus(undefined);
			setStatus("ready");
		} catch (e) {
			// A superseded response says nothing about the current one.
			if (run !== generation.current) return;
			setFailureStatus(e instanceof ApiError ? e.status : undefined);
			setStatus("failed");
		}
	}, [config, sessionId, number, usable]);

	useEffect(() => {
		void load();
	}, [load]);

	const onRefresh = useCallback(async () => {
		haptics.tap();
		setRefreshing(true);
		await load();
		setRefreshing(false);
	}, [load]);

	const prUrlForActions = summary?.url || "";
	const reviewState = reviewStateFor(reviewStates, prUrlForActions);
	const running = reviewIsRunning(reviewState);
	const autoReview = session?.autoReviewEnabled === true;
	const [busy, setBusy] = useState<"run" | "auto" | null>(null);
	// Guards re-entrancy without going through state: a second tap lands before
	// React has re-rendered with the flag set.
	const inFlight = useRef(false);
	const alive = useRef(true);
	useEffect(() => {
		alive.current = true;
		return () => {
			alive.current = false;
		};
	}, []);

	const runOrStop = useCallback(async () => {
		if (inFlight.current || !config) return;
		inFlight.current = true;
		haptics.tap();
		setBusy("run");
		try {
			if (running) {
				setReviewStates(await cancelReview(config, sessionId));
			} else {
				const { created, reviews } = await triggerReview(config, sessionId);
				if (reviews.length) setReviewStates(reviews);
				// 200 means the daemon reused the pass already recorded for this
				// commit. Nothing visible changes, so say so — otherwise the tap
				// reads as dropped.
				if (!created && alive.current) {
					Alert.alert("Already reviewed", "This commit has already been reviewed. Push a new commit to review again.");
				}
			}
			await load();
		} catch (e) {
			if (alive.current) Alert.alert("Review", e instanceof Error ? e.message : "The daemon could not be reached.");
		} finally {
			inFlight.current = false;
			if (alive.current) setBusy(null);
		}
	}, [config, sessionId, running, load]);

	const toggleAutoReview = useCallback(
		async (next: boolean) => {
			if (inFlight.current || !config) return;
			inFlight.current = true;
			haptics.tap();
			setBusy("auto");
			try {
				await setAutoReview(config, sessionId, next);
				// The flag lives on the session, so the board poll owns it; ask for a
				// fresh one rather than keeping a second copy here that could disagree.
				await refresh();
			} catch (e) {
				if (alive.current) Alert.alert("Auto review", e instanceof Error ? e.message : "The daemon could not be reached.");
			} finally {
				inFlight.current = false;
				if (alive.current) setBusy(null);
			}
		},
		[config, sessionId, refresh],
	);

	const resolveComment = useCallback(
		async (comment: ReviewComment) => {
			if (!config || !comment.url) return;
			await resolveReviewComment(config, sessionId, { pullRequestUrl: prUrlForActions || undefined, commentUrl: comment.url });
			await load();
		},
		[config, sessionId, prUrlForActions, load],
	);

	const askForRereview = useCallback(
		async (reviewerId: string) => {
			if (!config) return;
			await requestRereview(config, sessionId, { pullRequestUrl: prUrlForActions || undefined, reviewerId });
		},
		[config, sessionId, prUrlForActions],
	);

	// The store polls every 8s and this screen subscribes to it, so re-renders
	// are routine while the input is not.
	const entries = useMemo(() => prReviewEntries(summary ?? undefined), [summary]);
	const subtitle = reviewsSummaryLine(entries);
	const prUrl = summary?.htmlUrl || summary?.url || "";

	// Mirrors the PRs tab: a deep link can reach this route on a phone that is
	// not paired, and "no reviews" would be the wrong answer to "not connected".
	if (paired === false) {
		return (
			<View style={styles.screen}>
				<View style={{ height: insets.top }} />
				<ScreenHeader title="Reviews" left={<HeaderIconButton icon="back" label="Back" onPress={() => router.back()} />} />
				<UnpairedState />
			</View>
		);
	}

	return (
		<View style={styles.screen}>
			<View style={{ height: insets.top }} />
			<ScreenHeader
				title="Reviews"
				left={<HeaderIconButton icon="back" label="Back" onPress={() => router.back()} />}
			/>

			<View style={styles.caption}>
				<View style={styles.captionCopy}>
					<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.captionPr} numberOfLines={2}>
						{Number.isFinite(number) ? `#${number}` : "Pull request"}
						{summary?.title ? `  ·  ${summary.title}` : ""}
					</Text>
					{subtitle ? <Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.captionMeta}>{subtitle}</Text> : null}
				</View>
				{/* The header only takes the platform's own glyphs (menu/bell/close/
				    check/back), so the way out to GitHub lives here instead — where
				    it is also easier to find than an icon in the corner. */}
				{prUrl ? (
					<Pressable
						accessibilityRole="link"
						accessibilityLabel={`Open pull request ${number} on GitHub`}
						hitSlop={8}
						onPress={() => {
							haptics.tap();
							void openGitHub(prUrl);
						}}
						style={({ pressed }) => [styles.external, pressed && styles.externalPressed]}
					>
						<Feather name="external-link" size={16} color={t.textTertiary} />
					</Pressable>
				) : null}
			</View>

			{status === "loading" ? (
				<View style={styles.center}>
					<ActivityIndicator color={t.blue} />
				</View>
			) : (
				<ScrollView
					contentContainerStyle={styles.list}
					refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={t.blue} />}
				>
					{status === "ready" && summary ? (
						<ReviewControls
							autoReview={autoReview}
							busy={busy}
							disabled={reviewRunDisabled(reviewState, busy !== null)}
							label={reviewRunActionLabel(reviewState, busy === "run")}
							onRunOrStop={runOrStop}
							onToggleAutoReview={toggleAutoReview}
							running={running}
							state={reviewState}
						/>
					) : null}

					{status === "failed" ? (
						<FailureState config={config} status={failureStatus} onRetry={onRefresh} />
					) : entries.length === 0 ? (
						<EmptyState
							icon="message-square"
							title="No reviews yet"
							message={
								summary
									? "Nobody has reviewed this pull request."
									: "This pull request is not on that session any more."
							}
						/>
					) : (
						entries.map((entry) => (
							<ReviewEntryCard
								key={entry.id}
								entry={entry}
								onResolveComment={resolveComment}
								onRequestRereview={askForRereview}
							/>
						))
					)}
				</ScrollView>
			)}
		</View>
	);
}

/**
 * The controls half of desktop's Reviews tab: whether AO reviews this PR by
 * itself, and a button to run or stop a pass now.
 *
 * The reviewer-agent picker is deliberately absent — it needs the agent catalog
 * and a 21-entry sheet, and the daemon already falls back to the project's
 * default reviewer when the trigger names none.
 */
function ReviewControls({
	autoReview,
	busy,
	disabled,
	label,
	onRunOrStop,
	onToggleAutoReview,
	running,
	state,
}: {
	autoReview: boolean;
	busy: "run" | "auto" | null;
	disabled: boolean;
	label: string;
	onRunOrStop: () => void;
	onToggleAutoReview: (next: boolean) => void;
	running: boolean;
	state: ReturnType<typeof reviewStateFor>;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const { scheme } = useThemeState();
	const status = reviewStateLabel(state);
	return (
		<View style={styles.controls}>
			<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.eyebrow}>
				AO REVIEW
			</Text>
			<View style={styles.controlsCard}>
				<View style={styles.controlsRow}>
					<Feather name="repeat" size={18} color={t.textSecondary} style={styles.rowIcon} />
					<Text maxFontSizeMultiplier={fontScaleCap.body} style={styles.rowLabel} numberOfLines={1}>
						Auto review
					</Text>
					{/* The platform's own switch, as app/settings.tsx does — an RN Switch
					    beside it would read as a different app on iOS 26. */}
					{busy === "auto" ? (
						<ActivityIndicator size="small" color={t.textTertiary} />
					) : (
						<Host style={{ width: 54, height: 34 }} colorScheme={scheme} seedColor={t.blue}>
							<Switch value={autoReview} disabled={busy !== null} onValueChange={onToggleAutoReview} />
						</Host>
					)}
				</View>
				<View style={styles.divider} />
				<View style={styles.runRow}>
					<View style={styles.runCopy}>
						{status ? (
							<Text
								maxFontSizeMultiplier={fontScaleCap.body}
								style={[styles.runStatus, { color: toneColor(t, status.tone) }]}
								numberOfLines={2}
							>
								{status.text}
							</Text>
						) : (
							<Text maxFontSizeMultiplier={fontScaleCap.body} style={styles.runStatusMuted} numberOfLines={2}>
								AO has not reviewed this pull request.
							</Text>
						)}
					</View>
					<Button
						title={running ? (busy === "run" ? "Stopping…" : "Stop review") : label}
						icon={running ? "x" : "play"}
						variant={running ? "ghost" : "primary"}
						loading={busy === "run"}
						// Auto review owns the timing when it is on, so running a pass by
						// hand underneath it would fight the daemon.
						disabled={running ? busy !== null : disabled || autoReview}
						onPress={onRunOrStop}
					/>
				</View>
			</View>
		</View>
	);
}

function FailureState({
	config,
	status,
	onRetry,
}: {
	config: ServerConfig | null;
	status?: number;
	onRetry: () => void;
}) {
	const failure = describeConnectionFailure(classifyConnectionFailure(status), {
		host: config?.host ?? "",
		port: config?.httpPort ?? "",
		platform: Platform.OS,
	});
	return (
		<EmptyState
			icon="wifi-off"
			title={failure.title}
			message={failure.message}
			action={<Button title="Retry" icon="refresh-cw" variant="ghost" onPress={onRetry} />}
		/>
	);
}

/** One reviewer's pass: their verdict, what they wrote, and the lines they marked. */
function ReviewEntryCard({
	entry,
	onResolveComment,
	onRequestRereview,
}: {
	entry: ReviewEntry;
	onResolveComment: (comment: ReviewComment) => Promise<void>;
	onRequestRereview: (reviewerId: string) => Promise<void>;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const verdict = reviewVerdictLabel(entry.verdict);
	const when = entry.submittedAt ? relativeTime(entry.submittedAt) : "";

	return (
		<View style={styles.card}>
			<View style={styles.cardHead}>
				<Text maxFontSizeMultiplier={fontScaleCap.body} style={styles.reviewer} numberOfLines={1}>
					{entry.reviewerId}
				</Text>
				{entry.isBot ? <Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.bot}>bot</Text> : null}
				<View style={styles.spacer} />
				{when ? <Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.when}>{when}</Text> : null}
				<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={[styles.verdict, { color: toneColor(t, verdict.tone) }]} numberOfLines={1}>
					{verdict.text}
				</Text>
			</View>

			{entry.body ? (
				<View style={styles.body}>
					<ChatMarkdown text={entry.body} />
				</View>
			) : null}

			{entry.comments.length > 0 ? (
				<CommentGroup
					label={`${entry.comments.length} open ${entry.comments.length === 1 ? "comment" : "comments"}`}
					comments={entry.comments}
					onResolve={onResolveComment}
				/>
			) : null}
			{entry.resolvedComments.length > 0 ? (
				<CommentGroup
					label={`${entry.resolvedComments.length} resolved`}
					comments={entry.resolvedComments}
					collapsedByDefault
				/>
			) : null}

			{/* Desktop offers this on anything short of an approval: once you have
			    pushed a fix, the reviewer has to be asked to look again. */}
			{entry.verdict !== "approved" ? (
				<RereviewButton reviewerId={entry.reviewerId} onPress={onRequestRereview} />
			) : null}
		</View>
	);
}

/** "Ask for another look", with its own outcome — a silent success reads as a dead tap. */
function RereviewButton({ reviewerId, onPress }: { reviewerId: string; onPress: (reviewerId: string) => Promise<void> }) {
	const styles = useThemedStyles(makeStyles);
	const [state, setState] = useState<"idle" | "asking" | "asked" | "failed">("idle");
	const alive = useRef(true);
	useEffect(() => {
		alive.current = true;
		return () => {
			alive.current = false;
		};
	}, []);
	if (state === "asked") return <Text maxFontSizeMultiplier={fontScaleCap.body} style={styles.actionDone}>Asked {reviewerId} for another review</Text>;
	return (
		<View style={styles.actionRow}>
			<Button
				title={state === "asking" ? "Asking…" : state === "failed" ? "Retry re-review" : "Request another review"}
				icon="refresh-cw"
				variant="ghost"
				loading={state === "asking"}
				disabled={state === "asking"}
				onPress={() => {
					haptics.tap();
					setState("asking");
					void onPress(reviewerId)
						.then(() => alive.current && setState("asked"))
						.catch(() => alive.current && setState("failed"));
				}}
			/>
		</View>
	);
}

/**
 * Inline comments, grouped. Resolved ones start closed: they are history, and
 * on a phone they would otherwise push the open ones off the screen.
 */
function CommentGroup({
	label,
	comments,
	collapsedByDefault = false,
	onResolve,
}: {
	label: string;
	comments: ReviewComment[];
	collapsedByDefault?: boolean;
	onResolve?: (comment: ReviewComment) => Promise<void>;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [open, setOpen] = useState(!collapsedByDefault);
	return (
		<View style={styles.group}>
			<Pressable
				accessibilityRole="button"
				accessibilityState={{ expanded: open }}
				accessibilityLabel={label}
				hitSlop={6}
				onPress={() => {
					haptics.tap();
					setOpen((current) => !current);
				}}
				style={styles.groupHead}
			>
				<Feather name={open ? "chevron-down" : "chevron-right"} size={14} color={t.textTertiary} />
				<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.groupLabel}>{label}</Text>
			</Pressable>
			{open
				? comments.map((comment) => {
						const where = commentLocation(comment);
						return (
							<View key={comment.id} style={styles.comment}>
								{where ? (
									<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.where} numberOfLines={1} ellipsizeMode="head">
										{where}
									</Text>
								) : null}
								{comment.body ? <ChatMarkdown text={comment.body} /> : null}
								{/* Resolving addresses the provider thread, which is keyed by
								    the comment's own url — without one there is nothing to
								    resolve, so the control is not offered. */}
								{onResolve && comment.url ? <ResolveButton comment={comment} onResolve={onResolve} /> : null}
							</View>
						);
					})
				: null}
		</View>
	);
}

function ResolveButton({
	comment,
	onResolve,
}: {
	comment: ReviewComment;
	onResolve: (comment: ReviewComment) => Promise<void>;
}) {
	const styles = useThemedStyles(makeStyles);
	const [state, setState] = useState<"idle" | "resolving" | "failed">("idle");
	const alive = useRef(true);
	useEffect(() => {
		alive.current = true;
		return () => {
			alive.current = false;
		};
	}, []);
	return (
		<View style={styles.actionRow}>
			<Button
				title={state === "resolving" ? "Resolving…" : state === "failed" ? "Retry resolve" : "Resolve"}
				icon="check"
				variant="ghost"
				loading={state === "resolving"}
				disabled={state === "resolving"}
				onPress={() => {
					haptics.tap();
					setState("resolving");
					// On success the reload drops this comment into the resolved group,
					// which unmounts this button — so there is no "resolved" state to set.
					void onResolve(comment).catch(() => alive.current && setState("failed"));
				}}
			/>
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgBase },
		center: { flex: 1, alignItems: "center", justifyContent: "center" },
		caption: {
			flexDirection: "row",
			alignItems: "center",
			gap: space.sm,
			paddingHorizontal: space.lg,
			paddingBottom: space.md,
		},
		captionCopy: { flex: 1, gap: 2 },
		external: {
			width: 36,
			height: 36,
			alignItems: "center",
			justifyContent: "center",
			borderRadius: radius.md,
		},
		externalPressed: { backgroundColor: t.bgElevated },
		captionPr: { ...typeScale.meta, color: t.textSecondary },
		captionMeta: { ...typeScale.meta, color: t.textTertiary, fontFamily: t.fontMono },
		list: { paddingHorizontal: space.lg, paddingBottom: 40, gap: space.md },
		controls: { gap: 6 },
		// Matches app/settings.tsx's card: same radius, continuous curve, clipped
		// so the divider meets the edges.
		controlsCard: {
			backgroundColor: t.bgElevated,
			borderRadius: radius.lg,
			borderCurve: "continuous",
			overflow: "hidden",
		},
		controlsRow: {
			minHeight: 52,
			flexDirection: "row",
			alignItems: "center",
			paddingHorizontal: space.md,
			gap: 10,
		},
		rowIcon: { width: 26, textAlign: "center" },
		rowLabel: { ...typeScale.body, color: t.textPrimary, fontWeight: "600", flex: 1 },
		divider: { height: StyleSheet.hairlineWidth, backgroundColor: t.borderSubtle, marginLeft: 50 },
		eyebrow: { ...typeScale.eyebrow, color: t.textTertiary, paddingHorizontal: space.xs },
		runRow: {
			flexDirection: "row",
			alignItems: "center",
			gap: space.md,
			paddingHorizontal: space.md,
			paddingVertical: space.sm,
		},
		runCopy: { flex: 1 },
		runStatus: typeScale.meta,
		runStatusMuted: { ...typeScale.meta, color: t.textTertiary, fontWeight: "400" },
		// The card look of cardShell(), minus its own margins — this list sets its
		// gutter and gap once, so a per-card margin would double it.
		card: {
			backgroundColor: t.bgElevated,
			borderRadius: radius.md,
			borderWidth: 1,
			borderColor: t.borderSubtle,
			paddingHorizontal: space.md,
			paddingVertical: space.md,
			gap: space.sm,
		},
		cardHead: { flexDirection: "row", alignItems: "center", gap: 6 },
		reviewer: { ...typeScale.body, color: t.textPrimary, fontWeight: "600", flexShrink: 1 },
		bot: { ...typeScale.micro, color: t.textTertiary, fontWeight: "400", fontFamily: t.fontMono },
		spacer: { flex: 1, minWidth: space.sm },
		when: { ...typeScale.meta, color: t.textTertiary, fontFamily: t.fontMono },
		verdict: { ...typeScale.meta, fontWeight: "600", flexShrink: 0 },
		body: { marginTop: -2 },
		group: { gap: 6 },
		groupHead: { flexDirection: "row", alignItems: "center", gap: 6, minHeight: 28 },
		groupLabel: { ...typeScale.meta, color: t.textSecondary, fontWeight: "600" },
		comment: {
			gap: space.xs,
			paddingLeft: 10,
			borderLeftWidth: 2,
			borderLeftColor: t.borderSubtle,
		},
		where: { ...typeScale.micro, color: t.textTertiary, fontWeight: "400", fontFamily: t.fontMono },
		actionRow: { flexDirection: "row", marginTop: 2 },
		actionDone: { ...typeScale.meta, color: t.green },
	});
