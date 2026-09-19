import { Feather } from "@expo/vector-icons";
import { useRouter } from "expo-router";
import { memo, useCallback, useEffect, useRef, useState } from "react";
import { Keyboard, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import type { DashboardSession } from "./api";
import { AgentLogo } from "./AgentLogo";
import { haptics } from "./haptics";
import { prLine, workerRowPresentation, workerStatusGlyph } from "./agentsView";
import { toneColor } from "./prView";
import { statusVisual, type Theme } from "./theme";
import { rowDividerWidth } from "./divider";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { openGitHub } from "./openGitHub";
import { workerContextActions, type WorkerActionId } from "./worker-action-model";
import { WorkerRowActions } from "./worker-row-actions";
import { WorkerRowInteraction } from "./worker-row-interaction";
import { WORKER_ACTION_REVEAL_WIDTH } from "./worker-row-swipe-model";
import { normalizeConversationTitle } from "./chat/conversationMenuModel";
import { iconSize, press, space, type } from "./tokens";

export const WorkerListRow = memo(
	function WorkerListRow({
	session,
	projectName,
	isRenaming,
	activeSwipeId,
	nowBucket,
	onSwipeOpen,
	onSwipeClose,
	onRenameStart,
	onRenameCancel,
	onRename,
	onSetPinned,
	onDelete,
	onResume,
	onRestore,
}: {
	session: DashboardSession;
	projectName?: string;
	isRenaming: boolean;
	activeSwipeId?: string;
	/**
	 * The current minute, passed in only so a row re-renders when its relative
	 * timestamp would read differently — the memo below compares session *values*,
	 * so nothing else would wake a row whose data has not changed.
	 */
	nowBucket: number;
	onSwipeOpen(id: string, close: () => void): void;
	onSwipeClose(id: string): void;
	onRenameStart(): void;
	onRenameCancel(): void;
	onRename(title: string): Promise<void>;
	onSetPinned(pinned: boolean): Promise<void>;
	onDelete(): void;
	/** Restart a stopped agent without resurrecting a terminated session. */
	onResume(): void;
	/** Bring a terminated session back. */
	onRestore(): void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const closeActionRailRef = useRef<() => void>(() => {});
	const [renameTitle, setRenameTitle] = useState("");
	const [renameSaving, setRenameSaving] = useState(false);
	const [renameError, setRenameError] = useState<string>();
	const row = workerRowPresentation(t, session, projectName);
	const visual = statusVisual(t, session.status);
	const glyph = workerStatusGlyph(session.status);
	const prs = prLine(session);
	const details = [row.branch, prs?.text].filter(Boolean).join("  ·  ");
	useEffect(() => {
		if (isRenaming) return;
		setRenameTitle(row.title);
		setRenameError(undefined);
		setRenameSaving(false);
	}, [isRenaming, row.title]);
	const cancelRename = () => {
		Keyboard.dismiss();
		setRenameError(undefined);
		onRenameCancel();
	};
	const saveRename = useCallback(async () => {
		const nextTitle = normalizeConversationTitle(renameTitle);
		if (!nextTitle || renameSaving) return;
		setRenameSaving(true);
		setRenameError(undefined);
		try {
			await onRename(nextTitle);
			haptics.success();
			Keyboard.dismiss();
			onRenameCancel();
		} catch (cause) {
			haptics.error();
			setRenameError(cause instanceof Error ? cause.message : "Couldn't rename this worker.");
			setRenameSaving(false);
		}
	}, [onRename, onRenameCancel, renameSaving, renameTitle]);
	const renderRightActions = useCallback(
		() => (
			<View style={styles.actionRail}>
				<WorkerRowActions
					title={row.title}
					pinned={Boolean(session.isPinned)}
					onSetPinned={(pinned) => {
						closeActionRailRef.current();
						haptics.tap();
						void onSetPinned(pinned);
					}}
					onDelete={() => { closeActionRailRef.current(); haptics.warning(); onDelete(); }}
				/>
			</View>
		),
		[onDelete, onSetPinned, row.title, session.isPinned, styles.actionRail],
	);
	const openSession = () => {
		haptics.tap();
		router.push({
			pathname: "/session/[id]",
			params: { id: session.id, projectId: session.projectId },
		});
	};

	// prLine returns display text, not a link, so the url comes off the session.
	const prUrl = (session.prs?.length ? session.prs[0] : session.pr)?.url ?? null;
	const terminated = session.isTerminated === true || session.status === "terminated";
	const contextActions = workerContextActions({
		pinned: Boolean(session.isPinned),
		terminated,
		// A live session whose agent has stopped: exited or crashed, but the AO
		// session around it is still intact, so resuming is the lighter fix.
		stopped: !terminated && (session.status === "exited" || session.status === "errored"),
		hasPr: Boolean(prUrl),
	});

	const runAction = useCallback((id: WorkerActionId) => {
		switch (id) {
			case "open":
				return openSession();
			case "pin":
				return void onSetPinned(true);
			case "unpin":
				return void onSetPinned(false);
			case "rename":
				haptics.tap();
				setRenameTitle(row.title);
				setRenameError(undefined);
				return onRenameStart();
			case "resume":
				return onResume();
			case "restore":
				return onRestore();
			case "openPr":
				if (prUrl) void openGitHub(prUrl);
				return;
			default:
				return onDelete();
		}
	// openSession closes over router and session, both stable enough for a row.
	}, [onDelete, onRenameStart, onResume, onRestore, onSetPinned, prUrl, row.title]);

	return (
		<WorkerRowInteraction
			sessionId={session.id}
			enabled={!isRenaming}
			activeSwipeId={activeSwipeId}
			rightActions={renderRightActions()}
			shellStyle={styles.shell}
			foregroundStyle={styles.foreground}
			rowStyle={styles.row}
			pressedStyle={styles.rowPressed}
			accessibilityLabel={`${row.title}. ${visual.label}. ${row.project}.`}
			accessibilityHint="Swipe left for pin and delete actions. Long press for more."
			onPress={openSession}
			actions={contextActions}
			onAction={runAction}
			onSwipeOpen={onSwipeOpen}
			onSwipeClose={onSwipeClose}
			onReady={(close) => { closeActionRailRef.current = close; }}
		>
			{isRenaming ? (
				<WorkerRowContents
					row={row}
					visual={visual}
					glyph={glyph}
					details={details}
					prsTone={prs?.tone}
					harness={session.harness}
					isRenaming
					renameTitle={renameTitle}
					renameSaving={renameSaving}
					renameError={renameError}
					onRenameTitleChange={setRenameTitle}
					onRenameCancel={cancelRename}
					onRenameSave={() => void saveRename()}
					styles={styles}
					t={t}
				/>
			) : (
				<WorkerRowContents
					row={row}
					visual={visual}
					glyph={glyph}
					details={details}
					prsTone={prs?.tone}
					harness={session.harness}
					styles={styles}
					t={t}
				/>
			)}
		</WorkerRowInteraction>
	);
},
	/**
	 * Ignores the handler props on purpose. Every one of them is created inline by
	 * the board and closes only over this row's own `session` (and store actions
	 * that never change identity), so a row whose session, name and swipe state are
	 * the same cannot be holding a stale handler. Comparing them would defeat the
	 * memo — and re-rendering every row of a long board is what made folding heavy.
	 */
	(prev, next) =>
		prev.nowBucket === next.nowBucket &&
		prev.projectName === next.projectName &&
		prev.isRenaming === next.isRenaming &&
		prev.activeSwipeId === next.activeSwipeId &&
		// By value, not identity: the store polls and publishes freshly parsed
		// session objects each tick, so identity would fail for every row and put
		// the whole board through a re-render on every poll.
		(prev.session === next.session || JSON.stringify(prev.session) === JSON.stringify(next.session)),
);

function WorkerRowContents({
	row,
	visual,
	glyph,
	details,
	prsTone,
	harness,
	isRenaming = false,
	renameTitle = "",
	renameSaving = false,
	renameError,
	onRenameTitleChange,
	onRenameCancel,
	onRenameSave,
	styles,
	t,
}: {
	row: ReturnType<typeof workerRowPresentation>;
	visual: ReturnType<typeof statusVisual>;
	glyph: ReturnType<typeof workerStatusGlyph>;
	details: string;
	prsTone?: Parameters<typeof toneColor>[1];
	harness: DashboardSession["harness"];
	isRenaming?: boolean;
	renameTitle?: string;
	renameSaving?: boolean;
	renameError?: string;
	onRenameTitleChange?(title: string): void;
	onRenameCancel?(): void;
	onRenameSave?(): void;
	styles: ReturnType<typeof makeStyles>;
	t: Theme;
}) {
	const canSave = Boolean(normalizeConversationTitle(renameTitle)) && !renameSaving;
	return (
		<>
			<View style={styles.eyebrow}>
				<AgentLogo harness={harness} size={14} />
				<Text style={styles.project} numberOfLines={1}>
					{row.project}
				</Text>
				{/* Paired with the tinted label so status reads by shape as well as
				    colour. Only shown alongside a real status — when the row is
				    showing an elapsed time instead, there is no state to depict. */}
				{glyph && row.trailingKind === "status" ? (
					<Feather name={glyph} size={12} color={visual.color} />
				) : null}
				<Text
					style={[styles.trailing, { color: row.trailingKind === "status" ? visual.color : t.textTertiary }]}
					numberOfLines={1}
				>
					{row.trailing}
				</Text>
			</View>

			{isRenaming ? (
				<View style={styles.titleEditor}>
					<TextInput
						autoFocus
						value={renameTitle}
						onChangeText={onRenameTitleChange}
						placeholder="Worker name"
						placeholderTextColor={t.textFaint}
						selectionColor={t.accent}
						maxLength={120}
						returnKeyType="done"
						onSubmitEditing={onRenameSave}
						style={styles.renameInput}
					/>
					<Pressable
						accessibilityRole="button"
						hitSlop={{ top: 6, bottom: 6, left: 3, right: 3 }} accessibilityLabel="Cancel rename"
						disabled={renameSaving}
						onPress={onRenameCancel}
						style={({ pressed }) => [styles.renameControl, pressed && styles.renameControlPressed, renameSaving && styles.renameControlDisabled]}
					>
						<Feather name="x" size={iconSize.md} color={t.textSecondary} />
					</Pressable>
					<Pressable
						accessibilityRole="button"
						hitSlop={{ top: 6, bottom: 6, left: 3, right: 3 }} accessibilityLabel="Save worker name"
						disabled={!canSave}
						onPress={onRenameSave}
						style={({ pressed }) => [styles.renameControl, styles.renameSave, pressed && styles.renameControlPressed, !canSave && styles.renameControlDisabled]}
					>
						<Feather name={renameSaving ? "loader" : "check"} size={iconSize.md} color={t.onAccent} />
					</Pressable>
				</View>
			) : (
				<Text style={styles.title} numberOfLines={1}>
					{row.title}
				</Text>
			)}

			{renameError ? <Text accessibilityRole="alert" style={styles.renameError}>{renameError}</Text> : null}
			{details ? (
				<Text style={[styles.details, prsTone && !row.branch && { color: toneColor(t, prsTone) }]} numberOfLines={1}>
					{details}
				</Text>
			) : null}
		</>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		shell: {
			minHeight: 76,
			overflow: "hidden",
			borderBottomWidth: rowDividerWidth,
			borderBottomColor: t.borderSubtle,
		},
		actionRail: {
			width: WORKER_ACTION_REVEAL_WIDTH,
			backgroundColor: t.bgElevated,
			borderLeftWidth: StyleSheet.hairlineWidth,
			borderLeftColor: t.borderSubtle,
		},
		foreground: { backgroundColor: t.bgBase },
		row: {
			minHeight: 76,
			paddingHorizontal: space.lg,
			paddingVertical: space.sm,
			gap: space.hair,
		},
		rowPressed: { backgroundColor: t.bgSubtle },
		titleEditor: { minHeight: 32, flexDirection: "row", alignItems: "center", gap: space.xs },
		renameInput: { flex: 1, minWidth: 0, minHeight: 32, paddingHorizontal: space.none, paddingVertical: space.none, borderWidth: 0, backgroundColor: "transparent", color: t.textPrimary, fontSize: type.callout.fontSize, lineHeight: type.callout.lineHeight, fontWeight: "600", letterSpacing: -0.15, includeFontPadding: false, textAlignVertical: "center" },
		renameError: { color: t.red, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight, marginTop: -1 },
		renameControl: { width: 32, height: 32, borderRadius: 16, borderCurve: "continuous", borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault, alignItems: "center", justifyContent: "center", backgroundColor: t.bgElevatedHover },
		renameSave: { borderColor: t.accent, backgroundColor: t.accent },
		renameControlPressed: { opacity: press.opacity },
		renameControlDisabled: { opacity: 0.45 },
		eyebrow: { flexDirection: "row", alignItems: "center", gap: space.xs, minHeight: 17 },
		project: { flex: 1, color: t.textSecondary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "500" },
		trailing: { flexShrink: 0, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "500", fontVariant: ["tabular-nums"] },
		title: { color: t.textPrimary, fontSize: type.callout.fontSize, lineHeight: type.callout.lineHeight, fontWeight: "600", letterSpacing: -0.15 },
		details: { color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontFamily: t.fontMono },
	});
