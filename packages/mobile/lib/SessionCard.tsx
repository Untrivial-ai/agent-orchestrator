import { Feather } from "@expo/vector-icons";
import { useRouter } from "expo-router";
import { Pressable, StyleSheet, Text, View } from "react-native";
import { AgentLogo } from "./AgentLogo";
import { sessionTitle, shortLabel, type DashboardSession } from "./api";
import { haptics } from "./haptics";
import { prLine, showBranch, trackerIssueId } from "./agentsView";
import { relativeTime } from "./notificationView";
import { toneColor } from "./prView";
import { statusVisual, type Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { cardShell, cardShellPressed, Dot } from "./ui";

// One session on the board, following the desktop board card's anatomy: agent
// mark and title, a branch line, a hairline, then a footer of status + relative
// time, PR line, and identifiers.
//
// The whole card is the tap target. A session card has exactly one destination
// — its mode-aware session surface — so the card being the button is honest;
// PRCard uses explicit IconButtons because it has two. The previous version drew a `terminal` glyph
// that looked like a control and did nothing.
export function SessionCard({
	session,
	showProject = false,
	now,
}: {
	session: DashboardSession;
	showProject?: boolean;
	/** The board's clock, so the list owns one interval instead of one per card. */
	now: number;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();

	const v = statusVisual(t, session.status);
	const title = sessionTitle(session);
	const branch = showBranch(session.branch, title) ? session.branch : null;
	const prs = prLine(session);
	const when = relativeTime(session.lastActivityAt, now);
	// Only a real tracker reference earns a chip; a hand-typed task name does
	// not, matching desktop.
	const issue = trackerIssueId(session.issueId);

	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`${title}. ${v.label}.`}
			onPress={() => {
				haptics.tap();
				router.push({
					pathname: "/session/[id]",
					params: { id: session.id, projectId: session.projectId },
				});
			}}
			style={({ pressed }) => [styles.card, pressed && styles.cardPressed]}
		>
			{/* Identity above the divider, state below. The project rides on the
			    title row rather than a row of its own: right-aligned and alone, it
			    was an orphan line on every card without an issue chip. */}
			<View style={styles.head}>
				<AgentLogo harness={session.harness} size={20} />
				<Text style={styles.title} numberOfLines={2}>
					{title}
				</Text>
				{showProject ? (
					<Text style={styles.project} numberOfLines={1}>
						{shortLabel(session.projectId)}
					</Text>
				) : null}
			</View>

			{branch || issue ? (
				<View style={styles.metaRow}>
					{branch ? (
						<>
							<Feather name="git-branch" size={11} color={t.textFaint} />
							<Text style={styles.branch} numberOfLines={1}>
								{branch}
							</Text>
						</>
					) : null}
					{issue ? (
						// Wrapped in a View: a bare Text in a flex row stretches to the
						// row's width, which turned the chip into a full-width bar. This
						// is why `Chip` in ui.tsx is built the same way.
						<View style={styles.issue}>
							<Text style={styles.issueText} numberOfLines={1}>
								{shortLabel(issue, 24)}
							</Text>
						</View>
					) : null}
				</View>
			) : null}

			<View style={styles.divider} />

			<View style={styles.statusRow}>
				<Dot color={v.color} breathing={v.breathing} size={7} />
				<Text style={[styles.status, { color: v.color }]} numberOfLines={1}>
					{v.label}
				</Text>
				<View style={{ flex: 1 }} />
				{when ? <Text style={styles.time}>{when}</Text> : null}
			</View>

			{prs ? (
				<Text style={[styles.prs, { color: toneColor(t, prs.tone) }]} numberOfLines={1}>
					{prs.text}
				</Text>
			) : null}
		</Pressable>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		card: cardShell(t),
		cardPressed: cardShellPressed(t),
		head: { flexDirection: "row", alignItems: "flex-start", gap: 9 },
		title: { flex: 1, color: t.textPrimary, fontSize: 15, fontWeight: "600", lineHeight: 20 },
		// Nudged down to sit on the title's first baseline rather than its box top,
		// and never shrinks — the title truncates instead.
		project: {
			color: t.textTertiary,
			fontSize: 11,
			fontFamily: t.fontMono,
			marginTop: 3,
			flexShrink: 0,
			maxWidth: "40%",
		},
		// Branch and issue share one row, indented to the title's x-origin (the
		// 20pt mark plus its 9pt gap). Either may be absent; the row only appears
		// when at least one is present, so it can never render empty.
		metaRow: { flexDirection: "row", alignItems: "center", gap: 6, marginTop: 6, marginLeft: 29 },
		branch: { color: t.textFaint, fontSize: 11, fontFamily: t.fontMono, flexShrink: 1 },
		issue: {
			backgroundColor: t.tintBlue,
			borderRadius: 5,
			paddingHorizontal: 6,
			paddingVertical: 2,
			flexShrink: 1,
		},
		issueText: { color: t.blue, fontSize: 10, fontFamily: t.fontMono },
		// Desktop separates the identity block from the status block with a
		// hairline; without it the two runs of small text read as one paragraph.
		divider: { height: StyleSheet.hairlineWidth, backgroundColor: t.borderSubtle, marginTop: 10 },
		statusRow: { flexDirection: "row", alignItems: "center", gap: 6, marginTop: 8 },
		status: { fontSize: 12, fontWeight: "600", flexShrink: 1 },
		time: { color: t.textFaint, fontSize: 11, fontFamily: t.fontMono },
		prs: { fontSize: 11, fontFamily: t.fontMono, marginTop: 5 },
	});
