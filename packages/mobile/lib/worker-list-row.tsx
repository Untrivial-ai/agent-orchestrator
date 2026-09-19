import { Feather } from "@expo/vector-icons";
import { useRouter } from "expo-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { Keyboard, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import type { DashboardSession } from "./api";
import { AgentLogo } from "./AgentLogo";
import { haptics } from "./haptics";
import { prLine, workerRowPresentation } from "./agentsView";
import { toneColor } from "./prView";
import { statusVisual, type Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { WorkerRowActions } from "./worker-row-actions";
import { WorkerRowInteraction } from "./worker-row-interaction";
import { WORKER_ACTION_REVEAL_WIDTH } from "./worker-row-swipe-model";
import { normalizeConversationTitle } from "./chat/conversationMenuModel";

export function WorkerListRow({
	session,
	projectName,
	isRenaming,
	activeSwipeId,
	onSwipeOpen,
	onSwipeClose,
	onRenameStart,
	onRenameCancel,
	onRename,
	onSetPinned,
	onDelete,
}: {
	session: DashboardSession;
	projectName?: string;
	isRenaming: boolean;
	activeSwipeId?: string;
	onSwipeOpen(id: string, close: () => void): void;
	onSwipeClose(id: string): void;
	onRenameStart(): void;
	onRenameCancel(): void;
	onRename(title: string): Promise<void>;
	onSetPinned(pinned: boolean): Promise<void>;
	onDelete(): void;
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
			setRenameError(cause instanceof Error ? cause.message : "Could not rename this worker.");
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
			accessibilityHint="Swipe left for pin and delete actions. Long press to rename."
			onPress={openSession}
			onRenameRequest={() => {
				haptics.tap();
				setRenameTitle(row.title);
				setRenameError(undefined);
				onRenameStart();
			}}
			onSwipeOpen={onSwipeOpen}
			onSwipeClose={onSwipeClose}
			onReady={(close) => { closeActionRailRef.current = close; }}
		>
			{isRenaming ? (
				<WorkerRowContents
					row={row}
					visual={visual}
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
					details={details}
					prsTone={prs?.tone}
					harness={session.harness}
					styles={styles}
					t={t}
				/>
			)}
		</WorkerRowInteraction>
	);
}

function WorkerRowContents({
	row,
	visual,
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
						selectionColor={t.blue}
						maxLength={100}
						returnKeyType="done"
						onSubmitEditing={onRenameSave}
						style={styles.renameInput}
					/>
					<Pressable
						accessibilityRole="button"
						accessibilityLabel="Cancel rename"
						disabled={renameSaving}
						onPress={onRenameCancel}
						style={({ pressed }) => [styles.renameControl, pressed && styles.renameControlPressed, renameSaving && styles.renameControlDisabled]}
					>
						<Feather name="x" size={17} color={t.textSecondary} />
					</Pressable>
					<Pressable
						accessibilityRole="button"
						accessibilityLabel="Save worker name"
						disabled={!canSave}
						onPress={onRenameSave}
						style={({ pressed }) => [styles.renameControl, styles.renameSave, pressed && styles.renameControlPressed, !canSave && styles.renameControlDisabled]}
					>
						<Feather name={renameSaving ? "loader" : "check"} size={17} color={t.onAccent} />
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
			borderBottomWidth: StyleSheet.hairlineWidth,
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
			paddingHorizontal: 18,
			paddingVertical: 10,
			gap: 3,
		},
		rowPressed: { backgroundColor: t.bgSubtle },
		titleEditor: { minHeight: 32, flexDirection: "row", alignItems: "center", gap: 7 },
		renameInput: { flex: 1, minWidth: 0, minHeight: 32, paddingHorizontal: 0, paddingVertical: 0, borderWidth: 0, backgroundColor: "transparent", color: t.textPrimary, fontSize: 16, lineHeight: 21, fontWeight: "600", letterSpacing: -0.15, includeFontPadding: false, textAlignVertical: "center" },
		renameError: { color: t.red, fontSize: 11, lineHeight: 15, marginTop: -1 },
		renameControl: { width: 32, height: 32, borderRadius: 16, borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault, alignItems: "center", justifyContent: "center", backgroundColor: t.bgElevatedHover },
		renameSave: { borderColor: t.blue, backgroundColor: t.blue },
		renameControlPressed: { opacity: 0.68 },
		renameControlDisabled: { opacity: 0.45 },
		eyebrow: { flexDirection: "row", alignItems: "center", gap: 6, minHeight: 17 },
		project: { flex: 1, color: t.textSecondary, fontSize: 12, lineHeight: 16, fontWeight: "500" },
		trailing: { flexShrink: 0, fontSize: 12, lineHeight: 16, fontWeight: "500", fontVariant: ["tabular-nums"] },
		title: { color: t.textPrimary, fontSize: 16, lineHeight: 21, fontWeight: "600", letterSpacing: -0.15 },
		details: { color: t.textTertiary, fontSize: 12, lineHeight: 16, fontFamily: t.fontMono },
	});
