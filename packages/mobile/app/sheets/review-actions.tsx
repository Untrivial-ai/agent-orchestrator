import { Feather } from "@expo/vector-icons";
import { useLocalSearchParams } from "expo-router";
import { useCallback, useEffect, useMemo, useState } from "react";
import { ActivityIndicator, Alert, Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import {
	getAgents,
	getSessionPR,
	requestSessionRereview,
	resolveSessionReviewComment,
	switchSessionReviewer,
	type AgentInfo,
	type PRReviewCommentLink,
	type PRUnresolvedReviewer,
	type SessionPRSummary,
} from "../../lib/api";
import { haptics } from "../../lib/haptics";
import { useApp } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { SHEET_SCROLL_CONTENT, SheetHeader } from "../../lib/ui";

export { SheetErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";

type BusyAction = { kind: "reviewer"; id: string } | { kind: "rerequest"; id: string } | { kind: "resolve"; id: string };

export default function ReviewActionsSheet() {
	const styles = useThemedStyles(makeStyles);
	const { config } = useApp();
	const { sessionId = "", prUrl = "", reviewer = "" } = useLocalSearchParams<{ sessionId?: string; prUrl?: string; reviewer?: string }>();
	const [agents, setAgents] = useState<AgentInfo[]>([]);
	const [pr, setPR] = useState<SessionPRSummary>();
	const [selectedReviewer, setSelectedReviewer] = useState(reviewer);
	const [busy, setBusy] = useState<BusyAction>();
	const [error, setError] = useState("");

	const load = useCallback(async () => {
		if (!config || !sessionId) return;
		setError("");
		try {
			const [catalog, prs] = await Promise.all([getAgents(config), getSessionPR(config, sessionId)]);
			setAgents(catalog.authorized);
			setPR(prs.find((item) => item.url === prUrl || item.htmlUrl === prUrl) ?? prs[0]);
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : "Could not load review actions.");
		}
	}, [config, prUrl, sessionId]);

	useEffect(() => { void load(); }, [load]);
	const externalReviewers = useMemo(() => uniqueReviewers(pr), [pr]);
	const comments = useMemo(() => unresolvedComments(pr), [pr]);

	async function chooseReviewer(agent: AgentInfo | undefined) {
		if (!config || busy) return;
		const id = agent?.id ?? "";
		haptics.select();
		setBusy({ kind: "reviewer", id });
		setError("");
		try {
			await switchSessionReviewer(config, sessionId, id || undefined);
			setSelectedReviewer(id);
			haptics.success();
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : "Could not change reviewer.");
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

	return <ScrollView style={styles.screen} contentContainerStyle={SHEET_SCROLL_CONTENT}>
		<SheetHeader title="Review actions" subtitle={pr ? `PR #${pr.number} · ${pr.title}` : "Reviewer and GitHub feedback"} />
		{error ? <Text accessibilityRole="alert" style={styles.error}>{error}</Text> : null}
		<Section title="AO REVIEWER" subtitle="Choose who runs the next AO review.">
			<ActionRow icon="users" title="Project default" selected={!selectedReviewer} loading={busy?.kind === "reviewer" && !busy.id} disabled={Boolean(busy)} onPress={() => void chooseReviewer(undefined)} />
			{agents.map((agent) => <ActionRow key={agent.id} icon="user" title={agent.label || agent.id} subtitle={agent.id} selected={selectedReviewer === agent.id} loading={busy?.kind === "reviewer" && busy.id === agent.id} disabled={Boolean(busy)} onPress={() => void chooseReviewer(agent)} />)}
			{!agents.length ? <Text style={styles.empty}>No authorized reviewer agents are available.</Text> : null}
		</Section>
		{externalReviewers.length ? <Section title="EXTERNAL REVIEWERS" subtitle="Ask a GitHub reviewer to look at the latest changes again.">
			{externalReviewers.map((item) => <ActionRow key={item.reviewerId} icon="refresh-cw" title={item.reviewerId} subtitle={`${item.count} ${item.count === 1 ? "comment" : "comments"}`} loading={busy?.kind === "rerequest" && busy.id === item.reviewerId} disabled={Boolean(busy)} onPress={() => void rerequest(item)} />)}
		</Section> : null}
		{comments.length ? <Section title="ADDRESSED FEEDBACK" subtitle="Resolve only comments you have confirmed are addressed.">
			{comments.map((comment) => <ActionRow key={comment.url} icon="check-circle" title={comment.file ? `${comment.file}${comment.line ? `:${comment.line}` : ""}` : "Review comment"} subtitle={comment.body || comment.url} loading={busy?.kind === "resolve" && busy.id === comment.url} disabled={Boolean(busy)} onPress={() => confirmResolve(comment)} />)}
		</Section> : null}
		{!pr && !error ? <Text style={styles.empty}>No GitHub feedback is available for this pull request yet.</Text> : null}
	</ScrollView>;
}

function uniqueReviewers(pr?: SessionPRSummary): PRUnresolvedReviewer[] {
	const byId = new Map<string, PRUnresolvedReviewer>();
	for (const item of [...(pr?.review.unresolvedBy ?? []), ...(pr?.review.resolvedBy ?? [])]) {
		if (item.reviewerId && !byId.has(item.reviewerId)) byId.set(item.reviewerId, item);
	}
	for (const item of pr?.review.reviews ?? []) {
		if (item.reviewerId && !byId.has(item.reviewerId)) byId.set(item.reviewerId, { reviewerId: item.reviewerId, count: 0, links: [] });
	}
	return [...byId.values()];
}

function unresolvedComments(pr?: SessionPRSummary): PRReviewCommentLink[] {
	const byURL = new Map<string, PRReviewCommentLink>();
	for (const reviewer of pr?.review.unresolvedBy ?? []) {
		for (const link of reviewer.links ?? []) if (link.url) byURL.set(link.url, link);
	}
	return [...byURL.values()];
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
	rowCopy: { flex: 1, gap: 2 },
	rowTitle: { color: t.textPrimary, fontSize: 15, fontWeight: "600" },
	rowSubtitle: { color: t.textTertiary, fontSize: 12, lineHeight: 16 },
	pressed: { opacity: 0.6 },
	disabled: { opacity: 0.5 },
	empty: { color: t.textTertiary, fontSize: 13, lineHeight: 18, paddingVertical: 12 },
});
