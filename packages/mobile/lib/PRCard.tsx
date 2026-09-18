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
import { iconSize, space, type } from "./tokens";

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
					pathname: "/session/[id]",
					params: { id: session.id, projectId: session.projectId },
				});
			}}
			style={({ pressed }) => [styles.row, pressed && styles.rowPressed]}
		>
			<View style={styles.eyebrow}>
				<Feather name="git-pull-request" size={iconSize.sm} color={state.color} />
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
					<Feather name="external-link" size={iconSize.sm} color={t.textTertiary} />
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
			paddingHorizontal: space.lg,
			paddingVertical: space.sm,
			gap: space.hair,
			borderBottomWidth: StyleSheet.hairlineWidth,
			borderBottomColor: t.borderSubtle,
		},
		rowPressed: { backgroundColor: t.bgSubtle },
		eyebrow: { flexDirection: "row", alignItems: "center", gap: space.xs, minHeight: 17 },
		project: { flex: 1, color: t.textSecondary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "500" },
		status: { flexShrink: 0, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "500" },
		titleRow: { flexDirection: "row", alignItems: "center", minHeight: 40 },
		copy: { flex: 1, gap: space.hair },
		title: { color: t.textPrimary, fontSize: type.callout.fontSize, lineHeight: type.callout.lineHeight, fontWeight: "600", letterSpacing: -0.15 },
		details: { color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontFamily: t.fontMono },
		external: { width: 36, height: 36, marginRight: -8, alignItems: "center", justifyContent: "center", borderRadius: 12, borderCurve: "continuous"},
		externalPressed: { backgroundColor: t.bgElevated },
		blockers: { color: t.amber, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight, marginTop: space.hair, marginLeft: space.xl },
	});
