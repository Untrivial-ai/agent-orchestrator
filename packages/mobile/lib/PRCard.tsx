import { Feather } from "@expo/vector-icons";
import { useRouter } from "expo-router";
import { StyleSheet, Text, View } from "react-native";
import { sessionTitle, shortLabel, type DashboardPR, type DashboardSession, type SessionPRSummary } from "./api";
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
import { cardShell, IconButton } from "./ui";

// One PR, complete. Everything the daemon knows is on the card — there is no
// detail screen behind it, because for most PRs the detail was four rows and two
// links, which does not earn a navigation.
//
// The rich fields (real title, branches, author, diff stats, blockers) come from
// GET /sessions/{id}/pr via usePRSummaries and arrive a moment after the card
// first paints. Until then the card renders from the thin board facts, so it is
// never blank and never jumps between two layouts — the extra lines only append.
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
	// The rich summary reports `draft` as a state of its own; the board facts fold
	// it into "open" and leave only the isDraft flag for prLifecycle to recover.
	const state = summary ? stateVisualOf(t, summary.state as PRLifecycle) : prStateVisual(t, pr);

	const title = summary?.title?.trim() || prTitle(pr, sessionTitle(session));
	const branches = summary ? [summary.sourceBranch, summary.targetBranch].filter(Boolean).join(" → ") : "";
	const meta = [branches, summary?.author].filter(Boolean).join(" · ");
	const hasDiff = !!summary && (summary.changedFiles > 0 || summary.additions > 0 || summary.deletions > 0);

	// With the rich summary we can say all three things; without it, fall back to
	// the single worst-thing line the board facts support.
	const atoms = summary ? prStatusAtoms(summary) : [prSummaryLine(pr)];
	const blockers = summary ? prBlockerLine(summary) : null;

	return (
		<View style={styles.card}>
			<View style={styles.top}>
				<Feather name="git-pull-request" size={13} color={state.color} />
				<Text style={styles.number}>#{pr.number}</Text>
				<Text style={[styles.state, { color: state.color }]}>{state.label}</Text>
				<View style={{ flex: 1 }} />
				<Text style={styles.project} numberOfLines={1}>
					{shortLabel(summary?.repo || session.projectId || "Standalone")}
				</Text>
			</View>

			<Text style={styles.title} numberOfLines={2}>
				{title}
			</Text>

			{meta ? (
				<Text style={styles.meta} numberOfLines={1}>
					{meta}
				</Text>
			) : null}

			{hasDiff && summary ? (
				<View style={styles.diff}>
					<Text style={styles.diffFiles}>
						{summary.changedFiles} {summary.changedFiles === 1 ? "file" : "files"}
					</Text>
					<Text style={[styles.diffNum, { color: t.green }]}>+{summary.additions}</Text>
					<Text style={[styles.diffNum, { color: t.red }]}>−{summary.deletions}</Text>
				</View>
			) : null}

			<View style={styles.footer}>
				<View style={styles.status}>
					{atoms.map((a, i) => (
						<View key={a.text} style={styles.atom}>
							{i > 0 ? <Text style={styles.sep}>·</Text> : null}
							<Text style={[styles.atomText, { color: toneColor(t, a.tone) }]}>{a.text}</Text>
						</View>
					))}
				</View>

				<View style={styles.actions}>
					<IconButton
						icon="terminal"
						label="Open session"
						onPress={() =>
							router.push({
								pathname: "/session/[id]",
								params: { id: session.id, projectId: session.projectId },
							})
						}
					/>
					<IconButton
						icon="external-link"
						label="Open in GitHub"
						onPress={() => {
							void openGitHub(summary?.htmlUrl || summary?.url || pr.url);
						}}
					/>
				</View>
			</View>

			{blockers ? (
				<Text style={styles.blockers} numberOfLines={2}>
					{blockers}
				</Text>
			) : null}
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
	card: cardShell(t),
	top: { flexDirection: "row", alignItems: "center", gap: 6, marginBottom: 8 },
	number: { color: t.textSecondary, fontSize: 12, fontWeight: "700", fontFamily: t.fontMono },
	state: { fontSize: 12, fontWeight: "600" },
	project: { color: t.textTertiary, fontSize: 11, fontFamily: t.fontMono, flexShrink: 1 },
	title: { color: t.textPrimary, fontSize: 15, fontWeight: "500", lineHeight: 20 },
	meta: { color: t.textTertiary, fontSize: 11, fontFamily: t.fontMono, marginTop: 5 },
	diff: { flexDirection: "row", alignItems: "center", gap: 8, marginTop: 5 },
	diffFiles: { color: t.textTertiary, fontSize: 11, fontFamily: t.fontMono },
	diffNum: { fontSize: 11, fontWeight: "700", fontFamily: t.fontMono },
	// The status line and the actions share a row: the actions sit bottom-right
	// against the card's own padding, and the status wraps beside them. Centred,
	// not bottom-aligned — the buttons are 32pt and a one-line status pinned to
	// their baseline left an obvious gap above it.
	footer: { flexDirection: "row", alignItems: "center", gap: 10, marginTop: 10 },
	status: { flex: 1, flexDirection: "row", flexWrap: "wrap", alignItems: "center" },
	atom: { flexDirection: "row", alignItems: "center" },
	sep: { color: t.textFaint, fontSize: 12, marginHorizontal: 6 },
	atomText: { fontSize: 12, fontWeight: "600" },
	actions: { flexDirection: "row", gap: 6 },
	blockers: { color: t.textTertiary, fontSize: 11, lineHeight: 16, marginTop: 8 },
});
