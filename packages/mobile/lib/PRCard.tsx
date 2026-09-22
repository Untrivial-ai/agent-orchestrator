import { Feather } from "@expo/vector-icons";
import { useRouter } from "expo-router";
import { Pressable, StyleSheet, Text, View } from "react-native";
import { sessionTitle, shortLabel, type DashboardPR, type DashboardSession, type SessionPRSummary } from "./api";
import { haptics } from "./haptics";
import { openGitHub } from "./openGitHub";
import type { Theme } from "./theme";
import {
	prBlockerLine,
	prStateVisual,
	prStatusAtoms,
	prSummaryLine,
	prTitle,
	stateVisualOf,
	toneColor,
	type PRLifecycle,
} from "./prView";
import { useTheme, useThemedStyles } from "./ThemeProvider";

/** A pull-request row with the same hierarchy and density as WorkerListRow. */
export function PRCard({
	pr,
	session,
	summary,
}: {
	pr: DashboardPR;
	session: DashboardSession;
	summary?: SessionPRSummary;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const state = summary ? stateVisualOf(t, summary.state as PRLifecycle) : prStateVisual(t, pr);
	const title = summary?.title?.trim() || prTitle(pr, sessionTitle(session));
	const project = shortLabel(summary?.repo || session.projectId || "Standalone");
	const branches = summary
		? [summary.sourceBranch, summary.targetBranch].filter(Boolean).join(" → ")
		: session.branch || "";
	const diff = summary && (summary.changedFiles > 0 || summary.additions > 0 || summary.deletions > 0)
		? `${summary.changedFiles} ${summary.changedFiles === 1 ? "file" : "files"}  +${summary.additions} −${summary.deletions}`
		: "";
	const detail = [`#${pr.number}`, branches, diff].filter(Boolean).join("  ·  ");
	const atoms = summary ? prStatusAtoms(summary) : [prSummaryLine(pr)];
	const status = atoms[0] ?? { text: state.label, tone: "passive" as const };
	const blockers = summary ? prBlockerLine(summary) : null;

	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`${title}. Pull request ${pr.number}. ${status.text}.`}
			onPress={() => {
				haptics.tap();
				router.push({
					pathname: "/review/[sessionId]",
					params: { sessionId: session.id, prNumber: String(pr.number), prUrl: pr.url },
				});
			}}
			style={({ pressed }) => [styles.row, pressed && styles.rowPressed]}
		>
			<View style={styles.eyebrow}>
				<Feather name="git-pull-request" size={14} color={state.color} />
				<Text style={styles.project} numberOfLines={1}>{project}</Text>
				<Text style={[styles.status, { color: toneColor(t, status.tone) }]} numberOfLines={1}>{status.text}</Text>
			</View>

			<View style={styles.titleRow}>
				<View style={styles.copy}>
					<Text style={styles.title} numberOfLines={1}>{title}</Text>
					<Text style={styles.details} numberOfLines={1}>{detail}</Text>
				</View>
				<Pressable
					accessibilityRole="link"
					accessibilityLabel={`Open pull request ${pr.number} in GitHub`}
					hitSlop={8}
					onPress={(event) => {
						event.stopPropagation();
						haptics.tap();
						void openGitHub(summary?.htmlUrl || summary?.url || pr.url);
					}}
					style={({ pressed }) => [styles.external, pressed && styles.externalPressed]}
				>
					<Feather name="external-link" size={16} color={t.textTertiary} />
				</Pressable>
			</View>

			{blockers ? <Text style={styles.blockers} numberOfLines={1}>{blockers}</Text> : null}
		</Pressable>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		row: {
			minHeight: 76,
			paddingHorizontal: 18,
			paddingVertical: 10,
			gap: 3,
			borderBottomWidth: StyleSheet.hairlineWidth,
			borderBottomColor: t.borderSubtle,
		},
		rowPressed: { backgroundColor: t.bgSubtle },
		eyebrow: { flexDirection: "row", alignItems: "center", gap: 6, minHeight: 17 },
		project: { flex: 1, color: t.textSecondary, fontSize: 12, lineHeight: 16, fontWeight: "500" },
		status: { flexShrink: 0, fontSize: 12, lineHeight: 16, fontWeight: "500" },
		titleRow: { flexDirection: "row", alignItems: "center", minHeight: 40 },
		copy: { flex: 1, gap: 2 },
		title: { color: t.textPrimary, fontSize: 16, lineHeight: 21, fontWeight: "600", letterSpacing: -0.15 },
		details: { color: t.textTertiary, fontSize: 12, lineHeight: 16, fontFamily: t.fontMono },
		external: { width: 36, height: 36, marginRight: -8, alignItems: "center", justifyContent: "center", borderRadius: 12 },
		externalPressed: { backgroundColor: t.bgElevated },
		blockers: { color: t.amber, fontSize: 11, lineHeight: 15, marginTop: 2, marginLeft: 20 },
	});
