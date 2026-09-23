import { Feather } from "@expo/vector-icons";
import { useLocalSearchParams } from "expo-router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ActivityIndicator, Alert, Pressable, ScrollView, StyleSheet, Switch, Text, View } from "react-native";
import {
	getAgents,
	getAgentModels,
	getSession,
	getSessionPR,
	getSessionReviews,
	requestSessionRereview,
	resolveSessionReviewComment,
	sendMessage,
	setSessionAutoInjectCI,
	setSessionAutoInjectReview,
	setSessionAutoReview,
	switchSessionReviewer,
	type AgentModelCatalog,
	type ReviewerAgentConfig,
	type PRReviewCommentLink,
	type PRUnresolvedReviewer,
	type SessionPRSummary,
	type SessionReviews,
} from "../../lib/api";
import { haptics } from "../../lib/haptics";
import { openGitHub } from "../../lib/openGitHub";
import { formatExternalReviewMessage, formatInlineReviewCommentMessage } from "../../lib/reviewFeedback";
import { reviewerChoices, reviewerSwitchSelection, reviewerSwitchWarning } from "../../lib/reviewerControls";
import { useApp } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { SHEET_SCROLL_CONTENT, SheetHeader } from "../../lib/ui";

export { SheetErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";

type PolicyKey = "autoReviewEnabled" | "autoInjectReview" | "autoInjectCI";
type ReviewPolicies = Record<PolicyKey, boolean>;
type BusyAction = { kind: "reviewer" | "rerequest" | "resolve" | "send"; id: string } | { kind: "policy"; id: PolicyKey };

export default function ReviewActionsSheet() {
	const styles = useThemedStyles(makeStyles);
	const { config } = useApp();
	const { sessionId = "", prUrl = "", reviewer = "", running = "" } = useLocalSearchParams<{ sessionId?: string; prUrl?: string; reviewer?: string; running?: string }>();
	const [agents, setAgents] = useState<ReturnType<typeof reviewerChoices>>([]);
	const [models, setModels] = useState<AgentModelCatalog>();
	const [reviewerConfig, setReviewerConfig] = useState<ReviewerAgentConfig>({});
	const [pr, setPR] = useState<SessionPRSummary>();
	const [reviews, setReviews] = useState<SessionReviews>();
	const [reviewerOverride, setReviewerOverride] = useState("");
	const [effectiveReviewer, setEffectiveReviewer] = useState(reviewer);
	const modelRequest = useRef(0);
	const [policies, setPolicies] = useState<ReviewPolicies>();
	const [busy, setBusy] = useState<BusyAction>();
	const [error, setError] = useState("");

	const load = useCallback(async () => {
		if (!config || !sessionId) return;
		setError("");
		try {
			const [catalog, prs, session, reviewState] = await Promise.all([getAgents(config), getSessionPR(config, sessionId), getSession(config, sessionId), getSessionReviews(config, sessionId)]);
			setAgents(reviewerChoices(catalog));
			setReviewerOverride(session.reviewerHarness || "");
			setEffectiveReviewer(reviewState.reviewerHarness || reviewer);
			setReviewerConfig(session.reviewerConfig ?? {});
			setPR(prs.find((item) => item.url === prUrl || item.htmlUrl === prUrl) ?? prs[0]);
			setReviews(reviewState);
			setPolicies({
				autoReviewEnabled: session.autoReviewEnabled ?? false,
				autoInjectReview: session.autoInjectReview ?? true,
				autoInjectCI: session.autoInjectCI ?? true,
			});
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : "Could not load review actions.");
		}
	}, [config, prUrl, sessionId]);

	useEffect(() => { void load(); }, [load]);
	useEffect(() => {
		const request = ++modelRequest.current;
		setModels(undefined);
		if (!config || !effectiveReviewer) return;
		void getAgentModels(config, effectiveReviewer)
			.then((result) => { if (request === modelRequest.current) setModels(result); })
			.catch(() => { if (request === modelRequest.current) setModels(undefined); });
	}, [config, effectiveReviewer]);
	const aoReviewIds = useMemo(() => new Set((reviews?.runs ?? []).filter((run) => run.prUrl === pr?.url).map((run) => run.githubReviewId).filter(Boolean)), [pr?.url, reviews?.runs]);
	const externalReviewers = useMemo(() => uniqueReviewers(pr, aoReviewIds), [aoReviewIds, pr]);
	const comments = useMemo(() => reviewComments(pr, false), [pr]);
	const resolvedComments = useMemo(() => reviewComments(pr, true), [pr]);
	const externalReviews = useMemo(() => (pr?.review.reviews ?? []).filter((item) => item.reviewerId !== pr?.author && ![...aoReviewIds].some((id) => item.reviewUrl?.includes(`pullrequestreview-${id}`))), [aoReviewIds, pr]);

	async function saveReviewer(id: string, agentConfig: ReviewerAgentConfig) {
		if (!config || busy) return;
		haptics.select();
		setBusy({ kind: "reviewer", id });
		setError("");
		try {
			const selection = reviewerSwitchSelection(id, agentConfig);
			const result = await switchSessionReviewer(config, sessionId, selection.harness, selection.agentConfig);
			setReviewerOverride(id);
			setEffectiveReviewer(result.reviewerHarness || reviewer);
			setReviewerConfig(selection.agentConfig ?? {});
			haptics.success();
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : "Could not change reviewer.");
		} finally {
			setBusy(undefined);
		}
	}

	function chooseReviewer(id: string) {
		const nextConfig = id && id === reviewerOverride ? reviewerConfig : {};
		const warning = reviewerSwitchWarning(running === "true");
		if (!warning) { void saveReviewer(id, nextConfig); return; }
		Alert.alert("Switch active reviewer?", warning, [
			{ text: "Keep current", style: "cancel" },
			{ text: "Switch reviewer", style: "destructive", onPress: () => void saveReviewer(id, nextConfig) },
		]);
	}

	function chooseModel(value: string) {
		const key = models?.selectionMode === "mode" ? "mode" : "model";
		void saveReviewer(reviewerOverride, { ...reviewerConfig, [key]: value });
	}

	async function updatePolicy(key: PolicyKey, value: boolean) {
		if (!config || !policies || busy) return;
		const previous = policies;
		haptics.select();
		setPolicies({ ...policies, [key]: value });
		setBusy({ kind: "policy", id: key });
		setError("");
		try {
			if (key === "autoReviewEnabled") await setSessionAutoReview(config, sessionId, value);
			else if (key === "autoInjectReview") await setSessionAutoInjectReview(config, sessionId, value);
			else await setSessionAutoInjectCI(config, sessionId, value);
			haptics.success();
		} catch (cause) {
			setPolicies(previous);
			setError(cause instanceof Error ? cause.message : "Could not update the review automation setting.");
		} finally {
			setBusy(undefined);
		}
	}

	async function rerequest(item: PRUnresolvedReviewer) {
		if (!config || !pr || busy) return;
		setBusy({ kind: "rerequest", id: item.reviewerId });
		setError("");
		try {
			await requestSessionRereview(config, sessionId, pr.url, item.reviewerId);
			haptics.success();
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : "Could not request another review.");
		} finally {
			setBusy(undefined);
		}
	}

	function confirmResolve(comment: PRReviewCommentLink) {
		if (!config || !pr || busy) return;
		Alert.alert("Resolve feedback?", comment.body?.trim() || "Mark this addressed comment as resolved on GitHub.", [
			{ text: "Cancel", style: "cancel" },
			{ text: "Resolve", onPress: () => void resolve(comment) },
		]);
	}

	async function resolve(comment: PRReviewCommentLink) {
		if (!config || !pr) return;
		const commentUrl = comment.url;
		if (!commentUrl) return;
		setBusy({ kind: "resolve", id: commentUrl });
		setError("");
		try {
			await resolveSessionReviewComment(config, sessionId, pr.url, commentUrl);
			haptics.success();
			await load();
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : "Could not resolve this comment.");
		} finally {
			setBusy(undefined);
		}
	}

	async function send(id: string, message: string) {
		if (!config || busy) return;
		setBusy({ kind: "send", id });
		setError("");
		try {
			await sendMessage(config, sessionId, message);
			haptics.success();
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : "Could not send this feedback to the worker.");
		} finally {
			setBusy(undefined);
		}
	}

	return <ScrollView style={styles.screen} contentContainerStyle={SHEET_SCROLL_CONTENT}>
		<SheetHeader title="Review actions" subtitle={pr ? `PR #${pr.number} · ${pr.title}` : "Reviewer and GitHub feedback"} />
		{error ? <Text accessibilityRole="alert" style={styles.error}>{error}</Text> : null}
		<Section title="AUTOMATION" subtitle="Keep review and delivery behavior in sync with the desktop app.">
			<PolicyRow
				title="Automatically review new commits"
				description="Run the reviewer whenever this pull request gets a new commit."
				value={policies?.autoReviewEnabled ?? false}
				loading={!policies || busy?.kind === "policy" && busy.id === "autoReviewEnabled"}
				disabled={!policies || Boolean(busy)}
				onChange={(value) => void updatePolicy("autoReviewEnabled", value)}
			/>
			<PolicyRow
				title="Send review feedback to worker"
				description="Automatically give completed AO and GitHub review feedback to the worker."
				value={policies?.autoInjectReview ?? true}
				loading={!policies || busy?.kind === "policy" && busy.id === "autoInjectReview"}
				disabled={!policies || Boolean(busy)}
				onChange={(value) => void updatePolicy("autoInjectReview", value)}
			/>
			<PolicyRow
				title="Send CI failures to worker"
				description="Automatically give failing checks to the worker so it can fix them."
				value={policies?.autoInjectCI ?? true}
				loading={!policies || busy?.kind === "policy" && busy.id === "autoInjectCI"}
				disabled={!policies || Boolean(busy)}
				onChange={(value) => void updatePolicy("autoInjectCI", value)}
			/>
		</Section>
		<Section title="AO REVIEWER" subtitle="Choose who runs the next AO review.">
			<ActionRow icon="users" title="Project default" selected={!reviewerOverride} loading={busy?.kind === "reviewer" && !busy.id} disabled={Boolean(busy)} onPress={() => chooseReviewer("")} />
			{agents.map((agent) => <ActionRow key={agent.id} icon="user" title={agent.label || agent.id} subtitle={[agent.id, agent.status].filter(Boolean).join(" · ")} selected={reviewerOverride === agent.id} loading={busy?.kind === "reviewer" && busy.id === agent.id} disabled={Boolean(busy) || !agent.selectable} onPress={() => chooseReviewer(agent.id)} />)}
			{!agents.length ? <Text style={styles.empty}>No reviewer agents were found.</Text> : null}
		</Section>
		{models?.models.length && effectiveReviewer ? <Section title={models.selectionMode === "mode" ? "REVIEWER MODE" : "REVIEWER MODEL"} subtitle="Applied to this worker's future review runs.">
			<ActionRow icon="sliders" title="Provider default" selected={!(models.selectionMode === "mode" ? reviewerConfig.mode : reviewerConfig.model)} loading={busy?.kind === "reviewer"} disabled={Boolean(busy)} onPress={() => chooseModel("")} />
			{models.models.map((model) => <ActionRow key={model.id} icon="cpu" title={model.label || model.id} subtitle={model.isDefault ? "Provider default" : undefined} selected={(models.selectionMode === "mode" ? reviewerConfig.mode : reviewerConfig.model) === model.id} loading={busy?.kind === "reviewer" && busy.id === reviewerOverride} disabled={Boolean(busy)} onPress={() => chooseModel(model.id)} />)}
		</Section> : null}
		{externalReviewers.length ? <Section title="EXTERNAL REVIEWERS" subtitle="Ask a GitHub reviewer to look at the latest changes again.">
			{externalReviewers.map((item) => <ActionRow key={item.reviewerId} icon="refresh-cw" title={item.reviewerId} subtitle={`${item.count} ${item.count === 1 ? "comment" : "comments"}`} loading={busy?.kind === "rerequest" && busy.id === item.reviewerId} disabled={Boolean(busy)} onPress={() => void rerequest(item)} />)}
		</Section> : null}
		{externalReviews.length > 0 && pr ? <Section title="GITHUB REVIEWS" subtitle="Complete review summaries submitted on GitHub.">
			{externalReviews.map((item) => <View key={item.reviewUrl || `${item.reviewerId}:${item.submittedAt}`} style={styles.feedbackCard}><View style={styles.feedbackHeading}><Text style={styles.feedbackAuthor}>{item.reviewerId}</Text><Text style={styles.feedbackVerdict}>{item.verdict.replaceAll("_", " ")}</Text></View>{item.autoInjectReview === false ? <Text style={styles.notInjected}>Not automatically sent to the worker</Text> : null}{item.body ? <Text style={styles.feedbackBody}>{item.body}</Text> : <Text style={styles.feedbackMuted}>No written summary.</Text>}<View style={styles.feedbackActions}>{item.reviewUrl ? <MiniAction icon="external-link" title="Open review" onPress={() => void openGitHub(item.reviewUrl!)} /> : null}<MiniAction icon="send" title="Send to worker" loading={busy?.kind === "send" && busy.id === (item.reviewUrl || item.reviewerId)} disabled={Boolean(busy)} onPress={() => void send(item.reviewUrl || item.reviewerId, formatExternalReviewMessage(item, pr.url))} /></View></View>)}
		</Section> : null}
		{comments.length ? <Section title="UNRESOLVED COMMENTS" subtitle="Open the exact GitHub file and line, send feedback to the worker, or resolve it once addressed.">
			{comments.map((item) => <FeedbackCard key={item.comment.url || `${item.reviewerId}:${item.comment.file}`} item={item} aoOwned={Boolean(item.comment.reviewId && aoReviewIds.has(item.comment.reviewId))} busy={busy} disabled={Boolean(busy)} onOpen={() => item.comment.url && void openGitHub(item.comment.url)} onSend={() => void send(item.comment.url || `${item.reviewerId}:${item.comment.file}`, formatInlineReviewCommentMessage(item.comment, item.reviewerId))} onResolve={() => confirmResolve(item.comment)} />)}
		</Section> : null}
		{resolvedComments.length ? <Section title="RESOLVED COMMENTS" subtitle="Previously addressed AO and GitHub feedback.">
			{resolvedComments.map((item) => <FeedbackCard key={item.comment.url || `${item.reviewerId}:${item.comment.file}`} item={item} resolved aoOwned={Boolean(item.comment.reviewId && aoReviewIds.has(item.comment.reviewId))} busy={busy} disabled={Boolean(busy)} onOpen={() => item.comment.url && void openGitHub(item.comment.url)} onSend={() => void send(item.comment.url || `${item.reviewerId}:${item.comment.file}`, formatInlineReviewCommentMessage(item.comment, item.reviewerId))} onResolve={() => undefined} />)}
		</Section> : null}
		{!pr && !error ? <Text style={styles.empty}>No GitHub feedback is available for this pull request yet.</Text> : null}
	</ScrollView>;
}

function PolicyRow({ title, description, value, loading, disabled, onChange }: { title: string; description: string; value: boolean; loading?: boolean; disabled?: boolean; onChange: (value: boolean) => void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <View style={[styles.policyRow, disabled && styles.disabled]}>
		<View style={styles.rowCopy}><Text style={styles.rowTitle}>{title}</Text><Text style={styles.rowSubtitle}>{description}</Text></View>
		{loading ? <ActivityIndicator size="small" color={t.blue} /> : <Switch accessibilityLabel={title} disabled={disabled} value={value} onValueChange={onChange} trackColor={{ true: t.blue }} />}
	</View>;
}

function uniqueReviewers(pr: SessionPRSummary | undefined, aoReviewIds: Set<string>): PRUnresolvedReviewer[] {
	const byId = new Map<string, PRUnresolvedReviewer>();
	for (const item of [...(pr?.review.unresolvedBy ?? []), ...(pr?.review.resolvedBy ?? [])]) {
		const links = item.links.filter((link) => !link.reviewId || !aoReviewIds.has(link.reviewId));
		if (item.reviewerId && item.reviewerId !== pr?.author && links.length && !byId.has(item.reviewerId)) byId.set(item.reviewerId, { ...item, count: links.length, links });
	}
	for (const item of pr?.review.reviews ?? []) {
		const isAOReview = [...aoReviewIds].some((id) => item.reviewUrl?.includes(`pullrequestreview-${id}`));
		if (!isAOReview && item.reviewerId && item.reviewerId !== pr?.author && !byId.has(item.reviewerId)) byId.set(item.reviewerId, { reviewerId: item.reviewerId, count: 0, links: [] });
	}
	return [...byId.values()];
}

type ReviewCommentItem = { reviewerId: string; comment: PRReviewCommentLink };

function reviewComments(pr: SessionPRSummary | undefined, resolved: boolean): ReviewCommentItem[] {
	const result: ReviewCommentItem[] = [];
	for (const reviewer of resolved ? pr?.review.resolvedBy ?? [] : pr?.review.unresolvedBy ?? []) {
		for (const comment of reviewer.links ?? []) result.push({ reviewerId: reviewer.reviewerId, comment });
	}
	return result;
}

function FeedbackCard({ item, resolved = false, aoOwned, busy, disabled, onOpen, onSend, onResolve }: { item: ReviewCommentItem; resolved?: boolean; aoOwned: boolean; busy?: BusyAction; disabled: boolean; onOpen: () => void; onSend: () => void; onResolve: () => void }) {
	const styles = useThemedStyles(makeStyles);
	const { reviewerId, comment } = item;
	const id = comment.url || `${reviewerId}:${comment.file}`;
	return <View style={styles.feedbackCard}><View style={styles.feedbackHeading}><Text style={styles.feedbackAuthor}>{reviewerId}</Text><Text style={styles.feedbackVerdict}>{aoOwned ? "AO review" : "GitHub"} · {resolved ? "resolved" : "open"}</Text></View>{comment.autoInjectReview === false ? <Text style={styles.notInjected}>Not automatically sent to the worker</Text> : null}<Text style={styles.feedbackLocation}>{comment.file ? `${comment.file}${comment.line ? `:${comment.line}` : ""}` : "Inline comment"}</Text><Text style={styles.feedbackBody}>{comment.body || "No comment text."}</Text><View style={styles.feedbackActions}>{comment.url ? <MiniAction icon="external-link" title={comment.file ? "Open file & line" : "Open on GitHub"} onPress={onOpen} /> : null}<MiniAction icon="send" title="Send to worker" loading={busy?.kind === "send" && busy.id === id} disabled={disabled} onPress={onSend} />{!resolved && comment.url ? <MiniAction icon="check-circle" title="Resolve" loading={busy?.kind === "resolve" && busy.id === comment.url} disabled={disabled} onPress={onResolve} /> : null}</View></View>;
}

function MiniAction({ icon, title, loading, disabled = false, onPress }: { icon: keyof typeof Feather.glyphMap; title: string; loading?: boolean; disabled?: boolean; onPress: () => void }) {
	const t = useTheme(); const styles = useThemedStyles(makeStyles);
	return <Pressable accessibilityRole="button" disabled={disabled} onPress={onPress} style={[styles.miniAction, disabled && styles.disabled]}>{loading ? <ActivityIndicator size="small" color={t.blue} /> : <Feather name={icon} size={14} color={t.blue} />}<Text style={styles.miniActionText}>{title}</Text></Pressable>;
}

function Section({ title, subtitle, children }: { title: string; subtitle: string; children: React.ReactNode }) {
	const styles = useThemedStyles(makeStyles);
	return <View style={styles.section}><Text style={styles.sectionTitle}>{title}</Text><Text style={styles.sectionSubtitle}>{subtitle}</Text><View style={styles.rows}>{children}</View></View>;
}

function ActionRow({ icon, title, subtitle, selected, loading, disabled, onPress }: { icon: keyof typeof Feather.glyphMap; title: string; subtitle?: string; selected?: boolean; loading?: boolean; disabled?: boolean; onPress: () => void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <Pressable accessibilityRole="button" accessibilityState={{ selected, disabled }} disabled={disabled} onPress={onPress} style={({ pressed }) => [styles.row, pressed && styles.pressed, disabled && styles.disabled]}>
		<Feather name={icon} size={17} color={selected ? t.blue : t.textTertiary} />
		<View style={styles.rowCopy}><Text style={[styles.rowTitle, selected && { color: t.blue }]}>{title}</Text>{subtitle ? <Text numberOfLines={2} style={styles.rowSubtitle}>{subtitle}</Text> : null}</View>
		{loading ? <ActivityIndicator size="small" color={t.blue} /> : selected ? <Feather name="check" size={17} color={t.blue} /> : <Feather name="chevron-right" size={16} color={t.textFaint} />}
	</Pressable>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgSurface },
	error: { color: t.red, backgroundColor: t.tintRed, borderRadius: 10, padding: 10, fontSize: 13, lineHeight: 18, marginBottom: 8 },
	section: { marginTop: 18 },
	sectionTitle: { color: t.textTertiary, fontSize: 11, fontWeight: "700", letterSpacing: 0.7 },
	sectionSubtitle: { color: t.textFaint, fontSize: 12, lineHeight: 17, marginTop: 4, marginBottom: 7 },
	rows: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle },
	row: { minHeight: 56, flexDirection: "row", alignItems: "center", gap: 11, paddingVertical: 10, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: t.borderSubtle },
	policyRow: { minHeight: 64, flexDirection: "row", alignItems: "center", gap: 12, paddingVertical: 10, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: t.borderSubtle },
	rowCopy: { flex: 1, gap: 2 },
	rowTitle: { color: t.textPrimary, fontSize: 15, fontWeight: "600" },
	rowSubtitle: { color: t.textTertiary, fontSize: 12, lineHeight: 16 },
	feedbackCard: { borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: t.borderSubtle, paddingVertical: 12, gap: 7 },
	feedbackHeading: { flexDirection: "row", alignItems: "center", gap: 8 },
	feedbackAuthor: { flex: 1, color: t.textPrimary, fontSize: 14, fontWeight: "700" },
	feedbackVerdict: { color: t.textTertiary, fontSize: 11, textTransform: "capitalize" },
	feedbackLocation: { color: t.textSecondary, fontFamily: t.fontMono, fontSize: 12 },
	feedbackBody: { color: t.textSecondary, fontSize: 13, lineHeight: 19 },
	feedbackMuted: { color: t.textTertiary, fontSize: 13, fontStyle: "italic" },
	notInjected: { color: t.amber, fontSize: 11 },
	feedbackActions: { flexDirection: "row", flexWrap: "wrap", gap: 14, marginTop: 2 },
	miniAction: { flexDirection: "row", alignItems: "center", gap: 5, minHeight: 32 },
	miniActionText: { color: t.blue, fontSize: 12, fontWeight: "600" },
	pressed: { opacity: 0.6 },
	disabled: { opacity: 0.5 },
	empty: { color: t.textTertiary, fontSize: 13, lineHeight: 18, paddingVertical: 12 },
});
