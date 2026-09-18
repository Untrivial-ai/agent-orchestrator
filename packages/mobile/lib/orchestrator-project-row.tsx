import Feather from "@expo/vector-icons/Feather";
import { Image, Pressable, StyleSheet, Text, View } from "react-native";
import MASCOT from "../assets/mascot.png";
import { relativeTime } from "./notificationView";
import { OrchestratorRowAction } from "./orchestrator-row-actions";
import {
	orchestratorRowAccessibilityLabel,
	orchestratorStatus,
	orchestratorWorkerAccessibilityLabel,
	orchestratorWorkerPreviews,
	type OrchestratorProjectRow,
} from "./orchestratorView";
import { statusVisual, type Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { Dot } from "./ui";

export function OrchestratorProjectRowView({
	row,
	busy,
	onOpen,
	onOpenWorker,
	onLaunch,
}: {
	row: OrchestratorProjectRow;
	busy: boolean;
	onOpen: (row: OrchestratorProjectRow) => void;
	onOpenWorker: (row: OrchestratorProjectRow, workerId: string) => void;
	onLaunch: (row: OrchestratorProjectRow) => void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const status = orchestratorStatus(t, row.link);
	const timestamp = row.activityAt ? relativeTime(row.activityAt) : "";
	const running = row.action === "open";
	const showProjectDetail = row.section !== "attention";
	const workerPreviews = orchestratorWorkerPreviews(row.workers);
	const content = (
		<>
			<View style={styles.eyebrow}>
				<Image source={MASCOT} resizeMode="contain" style={styles.orchestratorMark} accessibilityLabel="AO orchestrator" />
				<Text style={styles.project} numberOfLines={1}>
					{row.project.name}
				</Text>
				{timestamp ? <Text style={styles.timestamp}>{timestamp}</Text> : null}
				{running ? <Feather name="chevron-right" size={15} color={t.textFaint} /> : null}
			</View>
			{!running ? (
				<View style={styles.headlineRow}>
					<Text style={styles.headline} numberOfLines={1}>
						{row.headline}
					</Text>
				</View>
			) : null}
			<View style={styles.detailRow}>
				<View style={styles.leadingStatusIcon}>
					<Dot color={status.color} size={6} breathing={status.breathing} />
				</View>
				<Text style={[styles.status, { color: status.color }]}>{status.label}</Text>
				{showProjectDetail ? (
					<Text style={styles.detail} numberOfLines={2}>
						· {row.detail}
					</Text>
				) : null}
			</View>
		</>
	);

	return (
		<View style={styles.row}>
			{running ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={orchestratorRowAccessibilityLabel(row.project.name, status.label, row.action)}
					onPress={() => onOpen(row)}
					style={({ pressed }) => [styles.content, styles.runningContent, pressed && styles.pressed]}
				>
					{content}
				</Pressable>
			) : (
				<View style={[styles.content, styles.actionContent]}>{content}</View>
			)}

			{!running ? (
				<View style={styles.trailingAction}>
					<OrchestratorRowAction
						action={row.action === "resume" ? "resume" : "start"}
						projectName={row.project.name}
						busy={busy}
						onPress={() => onLaunch(row)}
					/>
				</View>
			) : null}

			{workerPreviews.length ? (
				<View style={styles.workerList} accessibilityRole="summary">
					{workerPreviews.map((worker) => {
						const workerStatus = statusVisual(t, worker.status);
						return (
							<Pressable
								key={worker.id}
								accessibilityRole="button"
								accessibilityLabel={orchestratorWorkerAccessibilityLabel(worker, workerStatus.label)}
								onPress={() => onOpenWorker(row, worker.id)}
								style={({ pressed }) => [styles.workerRow, pressed && styles.workerPressed]}
							>
								<Text style={styles.workerName} numberOfLines={1}>
									{worker.name}
								</Text>
								<View style={styles.workerStatus}>
									<Dot color={workerStatus.color} size={5} breathing={!!workerStatus.breathing} />
									<Text style={[styles.workerState, { color: workerStatus.color }]} numberOfLines={1}>
										{workerStatus.label}
									</Text>
								</View>
							</Pressable>
						);
					})}
				</View>
			) : null}
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		row: {
			minHeight: 92,
			borderBottomWidth: StyleSheet.hairlineWidth,
			borderBottomColor: t.borderSubtle,
			justifyContent: "center",
		},
		content: { paddingHorizontal: 18, paddingVertical: 6, gap: 2, justifyContent: "center" },
		runningContent: { minHeight: 56, paddingRight: 18 },
		actionContent: { minHeight: 92, paddingRight: 118 },
		pressed: { backgroundColor: t.bgSubtle },
		eyebrow: { flexDirection: "row", alignItems: "center", gap: 7, minHeight: 17 },
		orchestratorMark: { width: 18, height: 17 },
		project: { flex: 1, color: t.textPrimary, fontSize: 16, lineHeight: 21, fontWeight: "600", letterSpacing: -0.15 },
		timestamp: { color: t.textTertiary, fontSize: 12, lineHeight: 16, fontVariant: ["tabular-nums"] },
		headlineRow: { flexDirection: "row", alignItems: "center", gap: 4 },
		headline: { flexShrink: 1, color: t.textSecondary, fontSize: 13, lineHeight: 18, fontWeight: "600" },
		detailRow: { flexDirection: "row", alignItems: "flex-start", gap: 7, minHeight: 16 },
		leadingStatusIcon: { width: 14, minHeight: 16, alignItems: "center", justifyContent: "center" },
		status: { flexShrink: 0, fontSize: 12, lineHeight: 16, fontWeight: "600" },
		detail: { flex: 1, color: t.textTertiary, fontSize: 12, lineHeight: 16 },
		trailingAction: { position: "absolute", right: 14, top: 0, height: 92, minWidth: 44, justifyContent: "center" },
		workerList: { paddingLeft: 38, paddingRight: 18, paddingBottom: 9, gap: 1 },
		workerRow: { minHeight: 30, flexDirection: "row", alignItems: "center", gap: 10, paddingHorizontal: 6, borderRadius: 7, borderCurve: "continuous" },
		workerPressed: { backgroundColor: t.bgSubtle },
		workerStatus: { flexDirection: "row", alignItems: "center", gap: 6, marginLeft: "auto" },
		workerState: { flexShrink: 0, fontSize: 11, lineHeight: 15, fontWeight: "600" },
		workerName: { flex: 1, color: t.textSecondary, fontSize: 12, lineHeight: 16, fontWeight: "500" },
	});
