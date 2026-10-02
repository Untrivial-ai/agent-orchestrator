import { Feather } from "../icons";
import { Pressable, StyleSheet, Text, View } from "react-native";
import type { DashboardPR, SessionPRSummary } from "../api";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { prReviewPromptHeadline, prReviewPromptStatuses, toneColor, type Tone } from "../prView";
import { space, type } from "../tokens";

export function PRReviewPrompt({ pr, summary, branch, onPress, onCollapse }: { pr: DashboardPR; summary?: SessionPRSummary; branch?: string; onPress(): void; onCollapse(): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const title = summary?.title.trim() || pr.title?.trim() || `PR #${pr.number}`;
	const branchName = summary?.sourceBranch?.trim() || pr.branch?.trim() || branch?.trim();
	const headline = prReviewPromptHeadline(pr, summary);
	const statuses = prReviewPromptStatuses(pr, summary).filter((status) => status.text !== "CI passing" && status.text !== headline.text);
	const additions = summary?.additions ?? pr.additions;
	const deletions = summary?.deletions ?? pr.deletions;

	return <View style={styles.wrapper}>
		<Pressable accessibilityRole="button" accessibilityLabel="Collapse pull request card" hitSlop={{ top: 8, bottom: 8 }} onPress={onCollapse} style={styles.dragHandleTarget}>
			<View style={styles.dragHandle} />
		</Pressable>
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`Pull request ${pr.number}, ${title}. ${headline.text}.${additions !== undefined && deletions !== undefined ? ` ${additions} additions, ${deletions} deletions.` : ""}`}
			onPress={() => { haptics.tap(); onPress(); }}
			style={({ pressed }) => [styles.prompt, pressed && styles.pressed]}
		>
		<View style={styles.header}>
			<View style={styles.iconTile}><Feather name="git-pull-request" size={19} color={t.accent} /></View>
			<View style={styles.copy}>
				<View style={styles.headline}>
					<Feather name={statusIcon(headline.text)} size={12} color={toneColor(t, headline.tone)} />
					<Text style={[styles.headlineText, { color: toneColor(t, headline.tone) }]}>{headline.text}</Text>
				</View>
				<Text numberOfLines={1} ellipsizeMode="tail" style={styles.title}>{title}</Text>
				{branchName ? <Text numberOfLines={1} ellipsizeMode="tail" style={styles.meta}>{branchName}</Text> : null}
			</View>
			<View style={styles.trailing}>
				{additions !== undefined && deletions !== undefined ? <View style={styles.diff}>
					<Text style={styles.additions}>+{additions}</Text>
					<Text style={styles.deletions}>−{deletions}</Text>
				</View> : null}
				<Feather name="chevron-right" size={18} color={t.textTertiary} />
			</View>
		</View>
		<View style={styles.statuses}>
			{statuses.map((status) => <StatusFact key={`${status.text}-${status.tone}`} text={status.text} tone={status.tone} />)}
		</View>
		</Pressable>
	</View>;
}

function StatusFact({ text, tone }: { text: string; tone: Tone }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const color = toneColor(t, tone);
	return <View style={styles.statusFact}>
		<Feather name={statusIcon(text)} size={12} color={color} />
		<Text style={[styles.statusText, { color }]}>{text}</Text>
	</View>;
}

function statusIcon(status: string): keyof typeof Feather.glyphMap {
	if (status.startsWith("CI passing") || status === "Approved") return "check-circle";
	if (status.startsWith("CI failing") || status.includes("Conflict") || status === "Blocked" || status === "Changes requested" || status.includes("Unresolved")) return "alert-circle";
	if (status.startsWith("CI running") || status.includes("pending") || status === "Unstable") return "clock";
	if (status === "Mergeable" || status === "Ready to merge" || status === "Merged") return "git-merge";
	return "git-pull-request";
}

const makeStyles = (t: Theme) => StyleSheet.create({
	wrapper: { width: "100%" },
	dragHandleTarget: { width: "100%", height: 16, alignItems: "center", justifyContent: "center" },
	dragHandle: { width: 30, height: 3, borderRadius: 2, backgroundColor: t.borderStrong },
	prompt: { gap: space.xs, paddingHorizontal: space.xs, paddingVertical: space.xs },
	header: { minHeight: 54, flexDirection: "row", alignItems: "center", gap: space.md },
	iconTile: { width: 40, height: 40, alignItems: "center", justifyContent: "center", borderRadius: 20, backgroundColor: t.accentTint },
	copy: { flex: 1, minWidth: 0, gap: 2 },
	headline: { flexDirection: "row", alignItems: "center", gap: space.xxs },
	headlineText: { fontFamily: "Geist_500Medium", fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "500" },
	title: { minWidth: 0, flexShrink: 1, fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight, fontWeight: "600" },
	meta: { minWidth: 0, flexShrink: 1, fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight },
	trailing: { flexDirection: "row", alignItems: "center", gap: space.xs },
	statuses: { flexDirection: "row", flexWrap: "wrap", alignItems: "center", columnGap: space.md, rowGap: space.xs, paddingLeft: 40 + space.md },
	statusFact: { minHeight: 20, flexDirection: "row", alignItems: "center", gap: space.xxs },
	statusText: { fontFamily: "Geist_500Medium", fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "500" },
	diff: { flexDirection: "row", alignItems: "center", gap: space.xxs },
	additions: { fontFamily: "Geist_600SemiBold", color: t.green, fontSize: type.caption2.fontSize, fontWeight: "700" },
	deletions: { fontFamily: "Geist_600SemiBold", color: t.red, fontSize: type.caption2.fontSize, fontWeight: "700" },
	pressed: { opacity: 0.7 },
});
