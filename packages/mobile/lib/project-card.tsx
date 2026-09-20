import { Feather } from "./icons";
import { ActivityIndicator, Pressable, StyleSheet, Text, View } from "react-native";
import { OrchestratorIcon } from "./orchestrator-icon";
import {
	orchestratorButtonCopy,
	orchestratorStatus,
	projectBlockerLine,
	projectCardSummary,
	type ProjectPageStats,
	type OrchestratorProjectRow,
} from "./orchestratorView";
import type { Theme } from "./theme";
import { rowDividerWidth } from "./divider";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { fontScaleCap, space, type } from "./tokens";
import { Dot } from "./ui";

/**
 * One project on the Projects page: who it is, how its fleet is doing, and the
 * way into its orchestrator.
 *
 * A flat row with a divider, like a worker row, rather than a card: the two
 * lists read as one app. The body opens the project page; a footer strip names
 * the orchestrator's state on the left and carries a compact pill on the right
 * that opens, starts or resumes it. The pill is the only solid control on the
 * row, so it outranks the counts without dominating the name.
 */
export function ProjectCard({
	row,
	busy,
	onOpenProject,
	onOrchestrator,
}: {
	row: OrchestratorProjectRow;
	busy: boolean;
	onOpenProject: (row: OrchestratorProjectRow) => void;
	onOrchestrator: (row: OrchestratorProjectRow) => void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const status = orchestratorStatus(t, row.link);
	const summary = projectCardSummary(row);
	const blocker = projectBlockerLine(row);

	return (
		<View style={styles.row}>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={`${row.project.name}, ${status.label}, ${summary.workers}`}
				accessibilityHint="Opens the project"
				onPress={() => onOpenProject(row)}
				style={({ pressed }) => [styles.body, pressed && styles.pressed]}
			>
				<View style={styles.titleRow}>
					<Text style={styles.project} numberOfLines={1}>
						{row.project.name}
					</Text>
					<Feather name="chevron-right" size={15} color={t.textFaint} />
				</View>

				<View style={styles.summaryRow}>
					<Dot color={status.color} size={6} breathing={status.breathing} />
					<Text style={[styles.summaryStrong, styles.statusLabel, { color: status.color }]} numberOfLines={1}>
						{status.label}
					</Text>
				</View>

				{blocker ? (
					// Two texts so the ellipsis lands on the worker name, not the age.
					<View style={styles.blockerRow}>
						<Text style={styles.blockerWorker} numberOfLines={1}>
							{blocker.worker}
						</Text>
						<Text style={styles.blocker} numberOfLines={1}>
							{` · ${blocker.reason}`}
							{blocker.age ? ` · ${blocker.age}` : ""}
						</Text>
					</View>
				) : null}

				{/* Footer strip. The right edge is left clear for the pill, which sits
				    over it rather than inside this Pressable so the two never nest. */}
				<View style={styles.footer}>
					<Text style={styles.footerLabel} numberOfLines={1}>
						{summary.workers}
					</Text>
				</View>
			</Pressable>

			<View style={styles.pillSlot} pointerEvents="box-none">
				<OrchestratorPill row={row} busy={busy} onPress={onOrchestrator} />
			</View>
		</View>
	);
}

/** The compact orchestrator action on a project row. */
function OrchestratorPill({
	row,
	busy,
	onPress,
	openLabel,
}: {
	row: OrchestratorProjectRow;
	busy: boolean;
	onPress: (row: OrchestratorProjectRow) => void;
	/** Overrides the running label where "Orchestrator" is already on screen. */
	openLabel?: string;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const copy = orchestratorButtonCopy(row, busy);
	// Solid when it opens something live; outlined when it would start one, so a
	// stopped orchestrator does not look ready.
	const solid = copy.running;
	const ink = solid ? t.bgBase : t.textPrimary;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`${copy.label}, ${row.project.name}`}
			accessibilityState={{ busy, disabled: busy }}
			disabled={busy}
			hitSlop={8}
			onPress={() => onPress(row)}
			style={({ pressed }) => [styles.pill, solid ? styles.pillSolid : styles.pillOutline, pressed && styles.buttonPressed]}
		>
			{busy ? <ActivityIndicator size="small" color={ink} /> : <OrchestratorIcon size={15} color={ink} />}
			<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={[styles.pillLabel, { color: ink }]} numberOfLines={1}>
				{copy.running && openLabel ? openLabel : copy.short}
			</Text>
		</Pressable>
	);
}

/**
 * The top of a project page: a strip of counts for the overview, then the
 * orchestrator as one line — its state and what it reports — with the pill that
 * opens it. Flat like the rows beneath it, so the page reads as one list.
 */
export function ProjectPageHeader({
	row,
	stats,
	busy,
	onPress,
}: {
	row: OrchestratorProjectRow;
	stats: ProjectPageStats;
	busy: boolean;
	onPress: (row: OrchestratorProjectRow) => void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const status = orchestratorStatus(t, row.link);
	const tiles: { label: string; value: number; color?: string }[] = [
		{ label: "Workers", value: stats.workers },
		{ label: "Needs you", value: stats.needsYou, color: stats.needsYou ? t.amber : undefined },
		{ label: "Ready", value: stats.ready },
		{ label: "Archived", value: stats.archived },
	];
	return (
		<View>
			<View style={styles.stats} accessible accessibilityLabel={tiles.map((tile) => `${tile.value} ${tile.label}`).join(", ")}>
				{tiles.map((tile) => (
					<View key={tile.label} style={styles.stat}>
						<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={[styles.statValue, tile.color ? { color: tile.color } : null]}>
							{tile.value}
						</Text>
						<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={styles.statLabel} numberOfLines={1}>
							{tile.label}
						</Text>
					</View>
				))}
			</View>

			{/* The whole row opens the orchestrator, and so does the pill. The pill
			    is laid over the row rather than inside its Pressable, so the two
			    never nest and each stays its own accessible control. */}
			<View>
				<Pressable
					accessibilityRole="button"
					accessibilityLabel={`${orchestratorButtonCopy(row, busy).label}, ${status.label}, ${row.detail}`}
					disabled={busy}
					onPress={() => onPress(row)}
					style={({ pressed }) => [styles.orchestratorRow, pressed && styles.pressed]}
				>
					{/* The worker row's three lines — eyebrow, title, detail — at its exact
					    metrics, so the orchestrator is the same height as the rows below. */}
					<View style={[styles.main, styles.orchestratorMain]}>
						<View style={styles.orchestratorEyebrow}>
							<OrchestratorIcon size={14} color={t.textSecondary} />
							<Text style={styles.orchestratorEyebrowText} numberOfLines={1}>
								{row.link?.harness || "Orchestrator"}
							</Text>
						</View>
						<Text style={styles.orchestratorTitle} numberOfLines={1}>
							Orchestrator
						</Text>
						<View style={styles.summaryRow}>
							<Dot color={status.color} size={6} breathing={status.breathing} />
							<Text style={[styles.summaryStrong, styles.statusLabel, { color: status.color }]} numberOfLines={1}>
								{status.label}
							</Text>
							<Text style={styles.summary} numberOfLines={1}>
								{` · ${row.detail}`}
							</Text>
						</View>
					</View>
				</Pressable>
				<View style={styles.orchestratorPillSlot} pointerEvents="box-none">
					<OrchestratorPill row={row} busy={busy} onPress={onPress} openLabel="Open" />
				</View>
			</View>
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		// A flat row like a worker row: no card, just the divider.
		row: { borderBottomWidth: rowDividerWidth, borderBottomColor: t.borderSubtle, backgroundColor: t.bgBase },
		body: { paddingHorizontal: space.lg, paddingTop: space.md, paddingBottom: space.md, gap: space.xxs },
		pressed: { backgroundColor: t.bgSubtle },
		main: { flex: 1, minWidth: 0, gap: space.xs },

		titleRow: { flexDirection: "row", alignItems: "center", gap: space.sm },
		project: { fontFamily: "Geist_600SemiBold", flex: 1, color: t.textPrimary, fontSize: type.callout.fontSize, lineHeight: type.callout.lineHeight, fontWeight: "600", letterSpacing: -0.15 },
		timestamp: { color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontVariant: ["tabular-nums"], fontFamily: t.fontMono },

		summaryRow: { flexDirection: "row", alignItems: "center", minWidth: 0 },
		summaryStrong: { fontFamily: "Geist_600SemiBold", flexShrink: 0, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "600" },
		statusLabel: { marginLeft: space.xs },
		summary: { fontFamily: "Geist_400Regular", flexShrink: 1, color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight },

		blockerRow: { flexDirection: "row", alignItems: "center", minWidth: 0 },
		blocker: { fontFamily: "Geist_400Regular", flexShrink: 0, color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight },
		blockerWorker: { fontFamily: "Geist_600SemiBold", flexShrink: 1, color: t.textSecondary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "600" },

		// Height matches the pill so the strip's text centres on it; the right
		// padding keeps the status clear of the pill laid over this edge.
		footer: { flexDirection: "row", alignItems: "center", minHeight: 28, marginTop: space.sm, paddingRight: 150 },
		footerLabel: { fontFamily: "Geist_400Regular", flexShrink: 1, color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight },
		pillSlot: { position: "absolute", right: 18, bottom: 12, height: 28, justifyContent: "center" },
		pill: { flexDirection: "row", alignItems: "center", gap: space.xs, height: 28, paddingHorizontal: space.md, borderRadius: 12 },
		pillSolid: { backgroundColor: t.textPrimary },
		pillOutline: { borderWidth: 1, borderColor: t.borderStrong },
		buttonPressed: { opacity: 0.8 },
		pillLabel: { fontFamily: "Geist_600SemiBold", fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, fontWeight: "600" },

		stats: { flexDirection: "row", marginHorizontal: space.lg, marginTop: space.xxs, borderTopWidth: rowDividerWidth, borderBottomWidth: rowDividerWidth, borderColor: t.borderSubtle },
		stat: { flex: 1, alignItems: "center", paddingVertical: space.sm, gap: space.none },
		statValue: { color: t.textPrimary, fontSize: type.body.fontSize, lineHeight: type.body.lineHeight, fontWeight: "600", fontFamily: t.fontMono, fontVariant: ["tabular-nums"] },
		statLabel: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight },
		orchestratorRow: { minHeight: 76, flexDirection: "row", alignItems: "center", gap: space.md, paddingLeft: space.lg, paddingRight: 120, paddingVertical: space.sm, borderBottomWidth: rowDividerWidth, borderBottomColor: t.borderSubtle },
		orchestratorPillSlot: { position: "absolute", right: 18, top: 0, bottom: 0, justifyContent: "center" },
		orchestratorMain: { gap: space.hair },
		orchestratorEyebrow: { flexDirection: "row", alignItems: "center", gap: space.xs, minHeight: 17 },
		orchestratorEyebrowText: { fontFamily: "Geist_500Medium", flex: 1, color: t.textSecondary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "500" },
		orchestratorTitle: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.callout.fontSize, lineHeight: type.callout.lineHeight, fontWeight: "600", letterSpacing: -0.15 },
	});
