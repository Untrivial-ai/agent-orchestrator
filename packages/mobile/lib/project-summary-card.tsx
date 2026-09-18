import { Feather } from "@expo/vector-icons";
import { Image, Pressable, StyleSheet, Text, View } from "react-native";
import MASCOT from "../assets/mascot.png";
import { relativeTime } from "./notificationView";
import type { ProjectSummary } from "./projects-view";
import type { Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { cardShell, cardShellPressed } from "./ui";

export function ProjectSummaryCard({ summary, onPress }: { summary: ProjectSummary; onPress?: () => void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const activity = summary.lastActivityAt ? relativeTime(summary.lastActivityAt) : "No activity";
	const accessibilityLabel = `${summary.project.name}. ${summary.activeWorkers} active workers, ${summary.needsAttention} need attention, ${summary.openPRs} open pull requests.`;

	const content = (
		<>
			<View style={styles.header}>
				<View style={styles.folder}>
					<Image source={MASCOT} resizeMode="contain" style={styles.orchestratorMark} accessibilityLabel="AO orchestrator" />
				</View>
				<View style={styles.identity}>
					<Text style={styles.title} numberOfLines={1}>{summary.project.name}</Text>
					<Text style={styles.kind}>{summary.project.kind?.replace("_", " ") ?? "project"}</Text>
				</View>
				<Text style={styles.activity}>{activity}</Text>
				{onPress ? <Feather name="chevron-right" size={17} color={t.textFaint} /> : null}
			</View>
			<View style={styles.metrics}>
				<Metric value={summary.activeWorkers} label="active" color={summary.activeWorkers ? t.orange : t.textFaint} />
				<Metric value={summary.needsAttention} label="need you" color={summary.needsAttention ? t.amber : t.textFaint} />
				<Metric value={summary.openPRs} label="open PRs" color={summary.failingPRs ? t.red : summary.openPRs ? t.green : t.textFaint} />
			</View>
			{summary.failingPRs ? (
				<Text style={styles.failure}>{summary.failingPRs} {summary.failingPRs === 1 ? "PR has" : "PRs have"} failing checks</Text>
			) : null}
		</>
	);

	if (!onPress) return <View style={styles.card}>{content}</View>;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={accessibilityLabel}
			onPress={onPress}
			style={({ pressed }) => [styles.card, pressed && styles.cardPressed]}
		>
			{content}
		</Pressable>
	);
}

function Metric({ value, label, color }: { value: number; label: string; color: string }) {
	const styles = useThemedStyles(makeStyles);
	return (
		<View style={styles.metric}>
			<Text style={[styles.metricValue, { color }]}>{value}</Text>
			<Text style={styles.metricLabel}>{label}</Text>
		</View>
	);
}

const makeStyles = (t: Theme) => StyleSheet.create({
	card: { ...cardShell(t), marginBottom: 12 },
	cardPressed: cardShellPressed(t),
	header: { flexDirection: "row", alignItems: "center", gap: 10 },
	folder: { width: 34, height: 34, borderRadius: 10, backgroundColor: t.tintBlue, alignItems: "center", justifyContent: "center" },
	orchestratorMark: { width: 23, height: 22 },
	identity: { flex: 1, minWidth: 0 },
	title: { color: t.textPrimary, fontSize: 17, fontWeight: "700" },
	kind: { color: t.textTertiary, fontSize: 11, marginTop: 2, textTransform: "capitalize" },
	activity: { color: t.textFaint, fontSize: 11, fontFamily: t.fontMono },
	metrics: { flexDirection: "row", gap: 8, marginTop: 14 },
	metric: { flex: 1, borderRadius: 10, paddingVertical: 9, paddingHorizontal: 10, backgroundColor: t.bgSubtle },
	metricValue: { fontSize: 17, fontWeight: "800", fontFamily: t.fontMono },
	metricLabel: { color: t.textTertiary, fontSize: 10, marginTop: 2 },
	failure: { color: t.red, fontSize: 11, fontWeight: "600", marginTop: 10 },
});
