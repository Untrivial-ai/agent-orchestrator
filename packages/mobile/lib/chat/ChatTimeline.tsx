import { Feather } from "@expo/vector-icons";
import * as Clipboard from "expo-clipboard";
import * as Linking from "expo-linking";
import { memo, useEffect, useMemo, useRef, useState } from "react";
import {
	ActivityIndicator,
	FlatList,
	Pressable,
	StyleSheet,
	Switch,
	Text,
	TextInput,
	View,
} from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { ChatMarkdown } from "./ChatMarkdown";
import { HighlightedCodeText } from "./HighlightedCodeText";
import { caretNotation, commandOutputText } from "./ansi";
import { jumpToLatestColors } from "./chatChrome";
import {
	humanizeInputName,
	initialInputValue,
	inputOptions,
	missingRequiredInputs,
	safeHttpURL,
	toggleInputValue,
	validateInput,
} from "./elicitationModel";
import type {
	ConversationActivity,
	ConversationItem,
	ConversationSnapshot,
	ConversationTurn,
	FileChange,
	InputProperty,
} from "./types";
import {
	activityHierarchy,
	activityNodesRunning,
	activityStartsExpanded,
	canRollbackTurn,
	conversationTimelineRenderPlan,
	countActivityNodes,
	readableConversationItems,
	type ActivityNode,
	type ConversationGroup,
} from "./timelineModel";

type TimelineRow =
	| { kind: "single"; key: string; items: [ConversationItem] }
	| { kind: "activities"; key: string; items: ConversationActivity[] };

export const ChatTimeline = memo(function ChatTimeline({
	snapshot,
	loadingOlder,
	onLoadOlder,
	approvalPending,
	inputPending,
	onDecide,
	onResolveInput,
	onRollback,
	jumpToSequence,
	onJumpHandled,
}: {
	snapshot: ConversationSnapshot;
	loadingOlder: boolean;
	onLoadOlder(): void;
	approvalPending: boolean;
	inputPending: boolean;
	onDecide(requestId: string, decisionId: string): Promise<void>;
	onResolveInput(requestId: string, action: "accept" | "decline" | "cancel", content?: Record<string, unknown>): Promise<void>;
	onRollback(turnId: string): Promise<number>;
	jumpToSequence?: number;
	onJumpHandled?(): void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const listRef = useRef<FlatList<ConversationGroup>>(null);
	const followsTail = useRef(true);
	const [showJump, setShowJump] = useState(false);
	// Usage is snapshot state, not conversation. Reasoning stays available in the
	// durable record but hidden on mobile: prose and work are the primary surface.
	const items = useMemo(() => readableConversationItems(snapshot), [snapshot]);
	const plan = useMemo(() => conversationTimelineRenderPlan(snapshot, items), [items, snapshot.turns]);
	const groups = plan.groups;

	useEffect(() => {
		if (jumpToSequence === undefined) return;
		const index = groups.findIndex((group) => group.anchor === jumpToSequence);
		if (index >= 0) {
			followsTail.current = index === 0;
			setShowJump(!followsTail.current);
			requestAnimationFrame(() => listRef.current?.scrollToIndex({ index, animated: true, viewPosition: 0.82 }));
		}
		onJumpHandled?.();
	}, [groups, jumpToSequence, onJumpHandled]);

	if (plan.kind === "empty") {
		return (
			<View style={styles.timelineWrap}>
				<View style={styles.emptySurface}>
					<EmptyConversation harness={snapshot.harness} controller={snapshot.controller.state} />
				</View>
			</View>
		);
	}

	return (
		<View style={styles.timelineWrap}>
			<FlatList<ConversationGroup>
				ref={listRef}
				data={groups}
				inverted={plan.inverted}
				keyExtractor={(group) => group.key}
				style={styles.list}
				contentContainerStyle={styles.content}
				keyboardShouldPersistTaps="handled"
				initialNumToRender={4}
				maxToRenderPerBatch={4}
				updateCellsBatchingPeriod={32}
				windowSize={5}
				maintainVisibleContentPosition={{ minIndexForVisible: 0 }}
				onScroll={(event) => {
					const { contentOffset } = event.nativeEvent;
					followsTail.current = Math.abs(contentOffset.y) < 120;
					setShowJump(!followsTail.current);
				}}
				scrollEventThrottle={100}
				onContentSizeChange={() => {
					if (followsTail.current) requestAnimationFrame(() => listRef.current?.scrollToOffset({ offset: 0, animated: false }));
				}}
				onScrollToIndexFailed={({ index, averageItemLength }) => {
					listRef.current?.scrollToOffset({ offset: Math.max(0, index * averageItemLength), animated: true });
					setTimeout(() => listRef.current?.scrollToIndex({ index, animated: true, viewPosition: 0.82 }), 120);
				}}
				ListFooterComponent={
					snapshot.hasMoreBefore ? (
						<Pressable
							accessibilityRole="button"
							disabled={loadingOlder}
							onPress={() => { haptics.tap(); void onLoadOlder(); }}
							style={styles.older}
						>
							{loadingOlder ? <ActivityIndicator size="small" /> : <Feather name="clock" size={13} />}
							<Text style={styles.olderText}>{loadingOlder ? "Loading history…" : "Load earlier messages"}</Text>
						</Pressable>
					) : items.length ? <Text style={styles.beginning}>Beginning of conversation</Text> : null
				}
				renderItem={({ item: group }) => <ConversationTurnGroup
					group={group}
					snapshot={snapshot}
					approvalPending={approvalPending}
					inputPending={inputPending}
					onDecide={onDecide}
					onResolveInput={onResolveInput}
					onRollback={onRollback}
				/>}
			/>
			{showJump ? <Pressable accessibilityRole="button" accessibilityLabel="Jump to latest message" onPress={() => { haptics.tap(); followsTail.current = true; setShowJump(false); listRef.current?.scrollToOffset({ offset: 0, animated: true }); }} style={styles.jump}><Feather name="arrow-down" size={14} color={jumpToLatestColors(t).foregroundColor} /><Text style={styles.jumpText}>Latest</Text></Pressable> : null}
		</View>
	);
});

function ConversationTurnGroup({ group, snapshot, approvalPending, inputPending, onDecide, onResolveInput, onRollback }: {
	group: ConversationGroup;
	snapshot: ConversationSnapshot;
	approvalPending: boolean;
	inputPending: boolean;
	onDecide(requestId: string, decisionId: string): Promise<void>;
	onResolveInput(requestId: string, action: "accept" | "decline" | "cancel", content?: Record<string, unknown>): Promise<void>;
	onRollback(turnId: string): Promise<number>;
}) {
	const rows = activityRuns(group.items);
	return <View>{rows.map((row) => row.kind === "activities"
		? <ActivityRun key={row.key} activities={row.items} />
		: <TimelineItem key={row.key} item={row.items[0]} approvalPending={approvalPending} inputPending={inputPending} onDecide={onDecide} onResolveInput={onResolveInput} />)}
		{group.turn ? <TurnSummary turn={group.turn} onRollback={canRollbackTurn(snapshot, group.turn) ? onRollback : undefined} /> : null}
	</View>;
}

const TimelineItem = memo(function TimelineItem({
	item,
	approvalPending,
	inputPending,
	onDecide,
	onResolveInput,
}: {
	item: ConversationItem;
	approvalPending: boolean;
	inputPending: boolean;
	onDecide(requestId: string, decisionId: string): Promise<void>;
	onResolveInput(requestId: string, action: "accept" | "decline" | "cancel", content?: Record<string, unknown>): Promise<void>;
}) {
	const styles = useThemedStyles(makeStyles);
	if (item.kind === "message") {
		if (item.role === "user" && item.origin === "human") {
			const delivery = deliveryCopy(item.delivery);
			return (
				<View style={styles.userRow}>
					<View style={styles.userBubble}>
						<Text selectable style={styles.userText}>{item.text}</Text>
						{delivery ? <Text style={styles.delivery}>{delivery}</Text> : null}
					</View>
				</View>
			);
		}
		if (item.role === "user") {
			return <OriginMessage message={item} />;
		}
		return (
			<View style={styles.assistantRow}>
				{item.senderLabel ? <Text style={styles.sender}>{item.senderLabel}</Text> : null}
				<ChatMarkdown text={item.text || (item.streaming ? "…" : "")} streaming={item.streaming} />
				{item.streaming ? <View style={styles.streamingDot} /> : null}
				{!item.streaming && item.text ? <Pressable accessibilityRole="button" accessibilityLabel="Copy response" hitSlop={9} onPress={() => { void Clipboard.setStringAsync(item.text); haptics.success(); }} style={styles.copy}><Feather name="copy" size={12} /><Text style={styles.copyText}>Copy</Text></Pressable> : null}
			</View>
		);
	}
	if (item.activityKind === "approval") {
		return <ApprovalCard activity={item} busy={approvalPending} onDecide={onDecide} />;
	}
	if (item.activityKind === "user_input") {
		return <UserInputCard activity={item} busy={inputPending} onResolve={onResolveInput} />;
	}
	if (item.activityKind === "system" && item.detail?.event === "compaction") {
		return <CompactionMarker activity={item} />;
	}
	if (item.activityKind === "system" && item.detail?.event === "steer") {
		return <View style={styles.userRow}><View style={[styles.userBubble, styles.steerBubble]}><Text style={styles.steerLabel}>STEERED</Text><Text selectable style={styles.userText}>{item.detail.text || item.summary}</Text></View></View>;
	}
	if (item.detail?.event === "model.rerouted") return <SystemSignal icon="shuffle" title={`Answered by ${item.detail.toModel || "another model"}`} detail={item.detail.fromModel ? `Instead of ${item.detail.fromModel}${item.detail.reason ? ` · ${item.detail.reason}` : ""}` : item.detail.reason} />;
	if (item.detail?.event === "auth.reauth_required") return <SystemSignal icon="key" danger title="The provider asked you to sign in again" detail={item.detail.reason} />;
	if (item.activityKind === "error") return <ErrorActivity activity={item} />;
	return <ActivityRow activity={item} />;
});

function SystemSignal({ icon, title, detail, danger }: { icon: keyof typeof Feather.glyphMap; title: string; detail?: unknown; danger?: boolean }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <View style={[styles.systemSignal, danger && { borderColor: t.red }]}><Feather name={icon} size={14} color={danger ? t.red : t.textTertiary} /><View style={{ flex: 1 }}><Text style={[styles.systemTitle, danger && { color: t.red }]}>{title}</Text>{detail ? <Text style={styles.systemDetail}>{String(detail)}</Text> : null}</View></View>;
}

function deliveryCopy(state?: string): string | undefined {
	switch (state) {
		case "queued": return "Queued — sends when the agent finishes";
		case "sending": return "Sending…";
		case "uncertain": return "Delivery unconfirmed — check the conversation before retrying";
		case "failed": return "Not sent";
		default: return undefined;
	}
}

function OriginMessage({ message }: { message: Extract<ConversationItem, { kind: "message" }> }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const long = message.text.length > 600;
	const [expanded, setExpanded] = useState(false);
	return <View style={styles.originMessage}>
		<View style={styles.originHeader}><Feather name="radio" size={11} color={t.textTertiary} /><Text style={styles.originLabel}>{message.senderLabel || (message.origin === "automation" ? "Automation" : "AO")}</Text></View>
		{long && expanded ? <ChatMarkdown text={message.text} /> : <Text selectable numberOfLines={long ? 5 : undefined} style={styles.originText}>{message.text}</Text>}
		{long ? <Pressable accessibilityRole="button" accessibilityState={{ expanded }} onPress={() => { haptics.tap(); setExpanded((value) => !value); }} style={styles.originMore}><Feather name={expanded ? "chevron-up" : "chevron-right"} size={12} color={t.blue} /><Text style={styles.originMoreText}>{expanded ? "Hide report" : "Show full report"}</Text></Pressable> : null}
	</View>;
}

/** Collapse consecutive mechanics so agent prose remains the visual hierarchy. */
function activityRuns(items: ConversationItem[]): TimelineRow[] {
	const rows: TimelineRow[] = [];
	for (const item of items) {
		const runnable = item.kind === "activity" &&
			item.activityKind !== "approval" &&
			item.activityKind !== "user_input" &&
			item.activityKind !== "error" &&
			item.activityKind !== "file_change" &&
			item.activityKind !== "reasoning" &&
			item.detail?.event === undefined;
		const previous = rows[rows.length - 1];
		if (runnable && previous?.kind === "activities" && previous.items[0]?.turnId === item.turnId) {
			previous.items.push(item);
		} else if (runnable) {
			rows.push({ kind: "activities", key: `run-${item.sequence}`, items: [item] });
		} else {
			rows.push({ kind: "single", key: `${item.kind}:${item.id}`, items: [item] });
		}
	}
	return rows;
}

function ActivityRow({ activity }: { activity: ConversationActivity }) {
	if (activity.activityKind === "mcp_tool") return <McpToolRow activity={activity} />;
	if (activity.activityKind === "auto_review") return <AutoReviewRow activity={activity} />;
	if (activity.activityKind === "file_change") return <FileChangeActivity activity={activity} />;
	if (activity.activityKind === "plan") return <PlanActivity activity={activity} />;
	return <GenericActivityRow activity={activity} />;
}

function GenericActivityRow({ activity }: { activity: ConversationActivity }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const detail = activity.detail ?? {};
	const output = commandOutputText(detail.output ?? detail.result ?? detail.error ?? detail.patchOutput);
	const [openOverride, setOpenOverride] = useState<boolean | null>(null);
	const open = openOverride ?? activityStartsExpanded(activity);
	const expandable = Boolean(output || detail.cwd || detail.arguments || detail.files || detail.reason || detail.text || detail.terminalInput);
	const meta = activityMeta(activity);
	return (
		<View style={styles.activityWrap}>
			<Pressable
				accessibilityRole={expandable ? "button" : undefined}
				accessibilityState={expandable ? { expanded: open } : undefined}
				disabled={!expandable}
				onPress={() => { haptics.tap(); setOpenOverride(!open); }}
				style={styles.activityRow}
			>
				<Feather name={meta.icon} size={13} color={meta.color(t)} />
				<Text numberOfLines={open ? undefined : 2} style={[styles.activitySummary, activity.status === "failed" && { color: t.red }]}>
					{meta.prefix ? `${meta.prefix} ` : ""}{detail.command || detail.toolName || activity.summary}
				</Text>
				{activity.status === "running" ? <ActivityIndicator size="small" color={t.orange} /> : null}
				{activity.status === "cancelled" ? <Text style={styles.activityStopped}>stopped</Text> : null}
				{expandable ? <Feather name={open ? "chevron-up" : "chevron-right"} size={13} color={t.textFaint} /> : null}
			</Pressable>
			{open ? (
				<View style={styles.activityDetail}>
					{detail.cwd ? <LabelValue label="cwd" value={detail.cwd} /> : null}
					{detail.reason || detail.text ? <Text style={styles.detailCopy}>{detail.reason ?? detail.text}</Text> : null}
					{detail.arguments !== undefined ? <CodeOutput value={printable(detail.arguments)} /> : null}
					{detail.terminalInput ? <TerminalInput text={detail.terminalInput} truncated={detail.terminalInputTruncated} /> : null}
					{output ? <CodeOutput value={output} /> : null}
					{detail.outputTruncated || detail.patchOutputTruncated ? <Text style={[styles.partial, { color: t.amber }]}>This output is longer than AO stores, so it stops early. Open the worktree shell for the full run.</Text> : detail.outputMayBePartial ? <Text style={styles.partial}>{detail.outputSource === "stream" ? "Streamed live; the provider may have omitted the beginning." : "The provider's rolled-up output may omit the beginning."} Open the worktree shell for the full run.</Text> : null}
					{Array.isArray(detail.files) ? <FileList files={detail.files} /> : null}
				</View>
			) : null}
		</View>
	);
}

function TerminalInput({ text, truncated }: { text: string; truncated?: boolean }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <View style={styles.terminalInput}><View style={styles.terminalInputTitle}><Feather name="corner-down-right" size={11} color={t.textTertiary} /><Text style={styles.detailLabel}>AGENT TYPED</Text></View><CodeOutput value={caretNotation(text)} />{truncated ? <Text style={styles.partial}>AO stopped recording keystrokes at its cap; more were sent.</Text> : null}</View>;
}

function McpToolRow({ activity }: { activity: ConversationActivity }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [open, setOpen] = useState(activity.status === "failed");
	const detail = activity.detail ?? {};
	const failed = activity.status === "failed" || detail.success === false || Boolean(detail.error);
	const body = detail.arguments !== undefined || detail.result !== undefined || Boolean(detail.error || detail.progress);
	return <View style={styles.activityWrap}>
		<Pressable disabled={!body} accessibilityRole={body ? "button" : undefined} accessibilityState={body ? { expanded: open } : undefined} onPress={() => { haptics.tap(); setOpen((value) => !value); }} style={styles.activityRow}>
			<Feather name="tool" size={13} color={failed ? t.red : t.purple} />
			<Text style={[styles.server, failed && { color: t.red }]}>{detail.server ?? detail.namespace ? `${detail.server ?? detail.namespace}/` : ""}</Text>
			<Text numberOfLines={1} style={[styles.activitySummary, failed && { color: t.red }]}>{detail.toolName || activity.summary}</Text>
			{detail.progress ? <Text numberOfLines={1} style={styles.activityProgress}>{lastLine(detail.progress)}</Text> : null}
			{activity.status === "running" ? <ActivityIndicator size="small" color={t.purple} /> : activity.status === "cancelled" ? <Text style={styles.activityStopped}>stopped</Text> : body ? <Feather name={open ? "chevron-up" : "chevron-right"} size={13} color={t.textFaint} /> : null}
		</Pressable>
		{open && body ? <View style={styles.activityDetail}>{detail.error ? <Text style={[styles.detailCopy, { color: t.red }]}>{detail.error}</Text> : null}{detail.arguments !== undefined ? <JsonPayload label="Arguments" value={detail.arguments} /> : null}{detail.result !== undefined ? <JsonPayload label="Result" value={detail.result} /> : null}{detail.progress ? <View><Text style={styles.detailLabel}>PROGRESS</Text><CodeOutput value={detail.progress} />{detail.progressTruncated ? <Text style={styles.partial}>Progress was longer than AO stores.</Text> : null}</View> : null}</View> : null}
	</View>;
}

function JsonPayload({ label, value }: { label: string; value: unknown }) {
	const styles = useThemedStyles(makeStyles);
	const note = truncationNote(value);
	return <View><Text style={styles.detailLabel}>{label.toUpperCase()}</Text>{note ? <Text style={styles.detailCopy}>{note}</Text> : <CodeOutput value={printable(value)} />}</View>;
}

function AutoReviewRow({ activity }: { activity: ConversationActivity }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [open, setOpen] = useState(false);
	const detail = activity.detail ?? {};
	const denied = String(detail.status ?? "").toLowerCase().includes("den");
	const paths = reviewPaths(detail.files);
	const body = Boolean(detail.rationale || detail.command || detail.cwd || detail.host || detail.decisionSource || paths.length);
	return <View style={styles.activityWrap}><Pressable disabled={!body} accessibilityRole={body ? "button" : undefined} accessibilityState={body ? { expanded: open } : undefined} onPress={() => { haptics.tap(); setOpen((value) => !value); }} style={styles.activityRow}><Feather name={denied ? "shield-off" : "shield"} size={13} color={denied ? t.red : t.green} /><Text style={[styles.reviewDecision, denied && { color: t.red }]}>{denied ? "Auto-declined" : "Auto-approved"}</Text><Text numberOfLines={1} style={styles.activitySummary}>{activity.summary}</Text>{detail.riskLevel ? <Text style={[styles.risk, ["high", "critical"].includes(detail.riskLevel.toLowerCase()) && { color: t.red }]}>{detail.riskLevel}</Text> : null}{body ? <Feather name={open ? "chevron-up" : "chevron-right"} size={13} color={t.textFaint} /> : null}</Pressable>{open && body ? <View style={styles.activityDetail}><Text style={styles.detailCopy}>{denied ? "The provider declined this on your behalf. You were not asked." : "The provider allowed this on your behalf. You were not asked."}</Text>{detail.rationale ? <Text style={styles.reviewRationale}>{detail.rationale}</Text> : null}{detail.command ? <LabelValue label="cmd" value={detail.command} /> : null}{detail.cwd ? <LabelValue label="cwd" value={detail.cwd} /> : null}{detail.host ? <LabelValue label="host" value={detail.host} /> : null}{paths.length ? <LabelValue label="files" value={paths.join(", ")} /> : null}{detail.decisionSource ? <LabelValue label="by" value={detail.decisionSource} /> : null}</View> : null}</View>;
}

function FileChangeActivity({ activity }: { activity: ConversationActivity }) {
	const files = fileChanges(activity.detail?.files);
	return <View style={useThemedStyles(makeStyles).activityWrap}><ExpandableFileList title={activity.summary || `${files.length} changed files`} files={files} fallbackPatch={activity.detail?.patchOutput} fallbackPatchTruncated={activity.detail?.patchOutputTruncated} live={activity.status === "running"} /></View>;
}

function ExpandableFileList({ title, files, fallbackPatch, fallbackPatchTruncated, live }: { title: string; files: ReturnType<typeof fileChanges>; fallbackPatch?: string; fallbackPatchTruncated?: boolean; live?: boolean }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [openOverride, setOpenOverride] = useState<boolean | null>(null);
	const open = openOverride ?? Boolean(live && (fallbackPatch || files.some((file) => file.patch)));
	const expandable = files.length > 0 || Boolean(fallbackPatch);
	return <View><Pressable disabled={!expandable} accessibilityRole={expandable ? "button" : undefined} accessibilityState={expandable ? { expanded: open } : undefined} onPress={() => { haptics.tap(); setOpenOverride(!open); }} style={styles.activityRow}><Feather name="edit-3" size={13} color={t.blue} /><Text numberOfLines={2} style={styles.activitySummary}>{title}</Text>{expandable ? <Feather name={open ? "chevron-up" : "chevron-right"} size={13} color={t.textFaint} /> : null}</Pressable>{open ? <View style={styles.activityDetail}>{files.map((file) => <FileChangeRow key={`${file.oldPath ?? ""}:${file.path}`} file={file} live={live} />)}{fallbackPatch ? <PatchBlock patch={fallbackPatch} truncated={fallbackPatchTruncated} /> : null}</View> : null}</View>;
}

function FileChangeRow({ file, live }: { file: ReturnType<typeof fileChanges>[number]; live?: boolean }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [open, setOpen] = useState(Boolean(live && file.patch));
	const hasPatch = Boolean(file.patch);
	const mark = file.status === "added" ? "A" : file.status === "deleted" ? "D" : file.status === "renamed" ? "R" : "M";
	return <View><Pressable disabled={!hasPatch} onPress={() => { haptics.tap(); setOpen((value) => !value); }} style={styles.fileRow}><Text style={[styles.fileMark, { color: file.status === "deleted" ? t.red : file.status === "added" ? t.green : t.blue }]}>{mark}</Text><Text selectable numberOfLines={2} style={styles.filePath}>{file.oldPath ? `${file.oldPath} → ${file.path}` : file.path}</Text><Text style={styles.fileStat}>+{file.additions} −{file.deletions}</Text>{hasPatch ? <Feather name={open ? "chevron-up" : "chevron-right"} size={11} color={t.textFaint} /> : null}</Pressable>{open && file.patch ? <PatchBlock patch={file.patch} truncated={file.patchTruncated} /> : null}</View>;
}

function PatchBlock({ patch, truncated }: { patch: string; truncated?: boolean }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <View><Pressable onLongPress={() => { void Clipboard.setStringAsync(patch); haptics.success(); }}><HighlightedCodeText code={patch} language="diff" style={styles.output} /></Pressable>{truncated ? <Text style={[styles.partial, { color: t.amber }]}>This patch is longer than AO stores. The complete change remains in the worktree.</Text> : null}</View>;
}

function PlanActivity({ activity }: { activity: ConversationActivity }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [open, setOpen] = useState(activity.status === "running");
	const steps = activity.detail?.steps ?? [];
	return <View style={styles.planCard}><Pressable style={styles.planHeader} onPress={() => { haptics.tap(); setOpen((value) => !value); }}><Feather name="list" size={13} color={t.textTertiary} /><Text style={styles.planTitle}>{activity.summary || "Plan updated"}</Text><Text style={styles.planCount}>{steps.filter((step) => step.status === "completed").length}/{steps.length}</Text><Feather name={open ? "chevron-up" : "chevron-down"} size={13} color={t.textTertiary} /></Pressable>{open ? <View style={styles.planBody}>{activity.detail?.explanation ? <Text style={styles.detailCopy}>{activity.detail.explanation}</Text> : null}{steps.map((step, index) => <View key={index} style={styles.planStep}><Feather name={step.status === "completed" ? "check-circle" : "circle"} size={14} color={step.status === "completed" ? t.green : step.status === "in_progress" ? t.orange : t.textFaint} /><Text style={[styles.planStepText, step.status === "completed" && styles.planDone]}>{step.text}</Text></View>)}{!steps.length ? <Text style={styles.detailCopy}>{activity.detail?.text || activity.summary}</Text> : null}</View> : null}</View>;
}

function ActivityRun({ activities }: { activities: ConversationActivity[] }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [override, setOverride] = useState<boolean | null>(null);
	const hierarchy = activityHierarchy(activities);
	if (activities.length === 1 && hierarchy[0]?.children.length === 0) return <ActivityRow activity={activities[0]} />;
	const running = activities.some((activity) => activity.status === "running");
	const failed = activities.filter((activity) => activity.status === "failed").length;
	const cancelled = activities.filter((activity) => activity.status === "cancelled").length;
	const streaming = activities.some((activity) => activity.status === "running" && Boolean(activity.detail?.output));
	const open = override ?? streaming;
	return <View style={styles.runWrap}>
		<Pressable
			accessibilityRole="button"
			accessibilityState={{ expanded: open }}
			onPress={() => { haptics.tap(); setOverride(!open); }}
			style={styles.runSummary}
		>
			<Text style={styles.runText}>{summarizeActivities(activities)}</Text>
			{failed ? <Text style={styles.runFailed}>{failed} failed</Text> : null}
			{cancelled ? <Text style={styles.runStopped}>{cancelled} stopped</Text> : null}
			{running ? <ActivityIndicator size="small" color={t.textTertiary} /> : null}
			<Feather name={open ? "chevron-down" : "chevron-right"} size={13} color={t.textFaint} />
		</Pressable>
		{open ? <View style={styles.runDetail}>{hierarchy.map((node) => <ActivityTree key={node.activity.id} node={node} />)}</View> : null}
	</View>;
}

function ActivityTree({ node }: { node: ActivityNode }) {
	const styles = useThemedStyles(makeStyles);
	return <View><ActivityRow activity={node.activity} />{node.children.length ? <NestedAgentRun nodes={node.children} /> : null}</View>;
}

function NestedAgentRun({ nodes }: { nodes: ActivityNode[] }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [open, setOpen] = useState(false);
	const count = countActivityNodes(nodes);
	const running = activityNodesRunning(nodes);
	return <View style={styles.subagent}>
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={`${open ? "Hide" : "Show"} subagent work, ${count} ${count === 1 ? "step" : "steps"}`}
			accessibilityState={{ expanded: open }}
			onPress={() => { haptics.tap(); setOpen((value) => !value); }}
			style={styles.subagentHeader}
		>
			<Feather name="git-branch" size={12} color={t.textTertiary} />
			<Text style={styles.subagentLabel}>SUBAGENT · {count} {count === 1 ? "STEP" : "STEPS"}</Text>
			{running ? <ActivityIndicator size="small" color={t.textTertiary} /> : null}
			<Feather name={open ? "chevron-down" : "chevron-right"} size={12} color={t.textFaint} />
		</Pressable>
		{open ? nodes.map((child) => <ActivityTree key={child.activity.id} node={child} />) : null}
	</View>;
}

function summarizeActivities(activities: ConversationActivity[]): string {
	let reads = 0;
	let searches = 0;
	let vcs = 0;
	let commands = 0;
	let tools = 0;
	let reviews = 0;
	let plans = 0;
	for (const activity of activities) {
		if (activity.activityKind === "mcp_tool") { tools++; continue; }
		if (activity.activityKind === "auto_review") { reviews++; continue; }
		if (activity.activityKind === "plan") { plans++; continue; }
		switch (commandCategory(activity.detail?.command ?? activity.summary)) {
			case "read": reads++; break;
			case "search": searches++; break;
			case "vcs": vcs++; break;
			default: commands++;
		}
	}
	const parts: string[] = [];
	if (reads) parts.push(`${reads} ${reads === 1 ? "file" : "files"}`);
	if (searches) parts.push(`${searches} ${searches === 1 ? "search" : "searches"}`);
	if (vcs) parts.push(`${vcs} git ${vcs === 1 ? "check" : "checks"}`);
	if (commands) parts.push(`${commands} ${commands === 1 ? "command" : "commands"}`);
	if (tools) parts.push(`${tools} tool ${tools === 1 ? "call" : "calls"}`);
	if (reviews) parts.push(`${reviews} auto-${reviews === 1 ? "decision" : "decisions"}`);
	if (plans) parts.push("updated plan");
	return `${reads || searches ? "Explored" : "Ran"} ${parts.length ? parts.join(", ") : `${activities.length} steps`}`;
}

function commandCategory(text: string): "read" | "search" | "vcs" | "run" {
	const head = text.trim().split(/\s+/, 1)[0] ?? "";
	const binary = head.slice(head.lastIndexOf("/") + 1);
	if (READ_COMMANDS.has(binary)) return "read";
	if (SEARCH_COMMANDS.has(binary)) return "search";
	if (binary === "git" || binary === "gh") return "vcs";
	return "run";
}

const READ_COMMANDS = new Set(["cat", "sed", "nl", "head", "tail", "bat", "less", "more", "wc", "jq"]);
const SEARCH_COMMANDS = new Set(["rg", "grep", "find", "fd", "ls", "tree", "glob", "ag"]);

function TurnSummary({ turn, onRollback }: { turn: ConversationTurn; onRollback?(turnId: string): Promise<number> }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [confirming, setConfirming] = useState(false);
	const [rollingBack, setRollingBack] = useState(false);
	const [rollbackError, setRollbackError] = useState<string>();
	const duration = elapsed(turn.startedAt ?? turn.requestedAt, turn.completedAt);
	const settled = turn.state !== "running" && turn.state !== "queued";
	return (
		<View style={styles.turnWrap}>
			{turn.plan?.steps.length ? <TurnPlan turn={turn} /> : null}
			{turn.diff?.files.length ? <ChangedFiles turn={turn} /> : null}
			<View style={styles.turnLine}>
				<View style={styles.ruleHalf} />
				<Text style={[styles.turnState, turn.state === "failed" && { color: t.red }]}>{turn.rolledBack ? "ROLLED BACK" : turn.state.toUpperCase()}</Text>
				{duration ? <Text style={styles.turnDuration}>{duration}</Text> : null}
				{onRollback && settled && turn.providerTurnId && !turn.rolledBack ? (
					<Pressable accessibilityLabel="Roll back to before this turn" hitSlop={8} onPress={() => { haptics.warning(); setConfirming(true); }}>
						<Feather name="rotate-ccw" size={13} color={t.textTertiary} />
					</Pressable>
				) : null}
			</View>
			{turn.errorMessage ? <Text style={styles.turnError}>{turn.errorMessage}</Text> : null}
			{confirming ? (
				<View style={styles.rollbackConfirm}>
					<Text style={styles.rollbackTitle}>Make the agent forget this turn and everything after it?</Text>
					<Text style={styles.rollbackCopy}>Files stay changed. Only conversation memory is rolled back, and this cannot be undone.</Text>
					<View style={styles.actions}>
						<Action label="Cancel" disabled={rollingBack} onPress={() => { setRollbackError(undefined); setConfirming(false); }} />
						<Action label={rollingBack ? "Rolling back…" : "Roll back"} disabled={rollingBack} tone="danger" onPress={() => {
							if (!onRollback || rollingBack) return;
							setRollingBack(true);
							setRollbackError(undefined);
							void onRollback(turn.id).then(() => setConfirming(false)).catch((cause) => {
								setRollbackError(cause instanceof Error ? cause.message : String(cause));
							}).finally(() => setRollingBack(false));
						}} />
					</View>
					{rollbackError ? <Text style={styles.validation}>{rollbackError}</Text> : null}
				</View>
			) : null}
		</View>
	);
}

function TurnPlan({ turn }: { turn: ConversationTurn }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [open, setOpen] = useState(turn.state === "running");
	const done = turn.plan?.steps.filter((step) => step.status === "completed").length ?? 0;
	return (
		<View style={styles.planCard}>
			<Pressable style={styles.planHeader} onPress={() => { haptics.tap(); setOpen((value) => !value); }}>
				<Feather name="list" size={13} color={t.textTertiary} />
				<Text style={styles.planTitle}>Plan</Text>
				{turn.state === "running" ? <Text style={styles.planLive}>STILL CHANGING</Text> : null}
				<Text style={styles.planCount}>{done}/{turn.plan?.steps.length ?? 0}</Text>
				<Feather name={open ? "chevron-up" : "chevron-down"} size={13} color={t.textTertiary} />
			</Pressable>
			{open ? <View style={styles.planBody}>
				{turn.plan?.explanation ? <Text style={styles.detailCopy}>{turn.plan.explanation}</Text> : null}
				{turn.plan?.steps.map((step, index) => <View key={index} accessibilityLabel={`${step.status.replace("_", " ")}: ${step.text}`} style={styles.planStep}>
					<Feather name={step.status === "completed" ? "check-circle" : step.status === "in_progress" ? "circle" : "circle"} size={14} color={step.status === "completed" ? t.green : step.status === "in_progress" ? t.orange : t.textFaint} />
					<Text style={[styles.planStepText, step.status === "completed" && styles.planDone]}>{step.text}</Text>
					<Text style={styles.planStepState}>{step.status.replace("_", " ")}</Text>
				</View>)}
			</View> : null}
		</View>
	);
}

function ChangedFiles({ turn }: { turn: ConversationTurn }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [open, setOpen] = useState(false);
	const files = turn.diff?.files ?? [];
	return <View style={styles.planCard}>
		<Pressable style={styles.planHeader} onPress={() => { haptics.tap(); setOpen((value) => !value); }}>
			<Feather name="file-text" size={13} color={t.textTertiary} />
			<Text style={styles.planTitle}>{files.length} changed {files.length === 1 ? "file" : "files"}</Text>
			{turn.state === "running" ? <><Text style={styles.planLive}>GROWING</Text><ActivityIndicator size="small" color={t.textTertiary} /></> : null}
			<Text style={{ color: t.green, fontSize: 11 }}>+{files.reduce((sum, file) => sum + file.additions, 0)}</Text>
			<Text style={{ color: t.red, fontSize: 11 }}>−{files.reduce((sum, file) => sum + file.deletions, 0)}</Text>
			<Feather name={open ? "chevron-up" : "chevron-down"} size={13} color={t.textTertiary} />
		</Pressable>
		{open ? <View style={styles.fileBody}>{files.map((file) => <View key={`${file.oldPath}:${file.path}`} style={styles.fileRow}>
			<Text style={[styles.fileMark, { color: file.status === "deleted" ? t.red : file.status === "added" ? t.green : t.blue }]}>{file.status[0].toUpperCase()}</Text>
			<Text selectable style={styles.filePath}>{file.oldPath ? `${file.oldPath} → ${file.path}` : file.path}</Text>
			<Text style={styles.fileStat}>+{file.additions} −{file.deletions}</Text>
		</View>)}{turn.diff?.truncated ? <Text style={[styles.partial, { color: t.amber }]}>This turn changed more files than AO lists here. Open the worktree shell for the complete diff.</Text> : null}</View> : null}
	</View>;
}

function ApprovalCard({ activity, busy, onDecide }: { activity: ConversationActivity; busy: boolean; onDecide(requestId: string, decisionId: string): Promise<void> }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [submitting, setSubmitting] = useState<string>();
	const [submitError, setSubmitError] = useState<string>();
	const pending = activity.status === "pending";
	return <View style={[styles.requestCard, pending && { borderColor: t.amber }]}>
		<View style={styles.requestTitle}><Feather name="shield" size={15} color={pending ? t.amber : t.textTertiary} /><Text style={styles.requestHeading}>{pending ? "Approval required" : "Approval resolved"}</Text>{activity.requestId ? <Text style={styles.requestId}>req {activity.requestId}</Text> : null}</View>
		{activity.detail?.reason ? <Text style={styles.requestCopy}>{activity.detail.reason}</Text> : null}
		<Text selectable style={styles.requestCommand}>{activity.detail?.command ?? activity.summary}</Text>
		{activity.detail?.cwd ? <LabelValue label="cwd" value={activity.detail.cwd} /> : null}
		{pending ? (activity.decisions?.length ? <View style={styles.actions}>{activity.decisions.map((decision, index) => <Action key={decision.id} label={submitting === decision.id ? "Sending…" : decision.label} primary={index === 0} disabled={busy || Boolean(submitting) || !activity.requestId} onPress={() => {
			setSubmitting(decision.id);
			setSubmitError(undefined);
			void onDecide(activity.requestId ?? "", decision.id).catch((cause) => setSubmitError(cause instanceof Error ? cause.message : String(cause))).finally(() => setSubmitting(undefined));
		}} />)}</View> : <Text style={[styles.partial, { color: t.amber }]}>The agent offered no decisions AO can present. Open diagnostics from the host.</Text>) : <Text style={styles.partial}>Already answered. This card is kept for the record.</Text>}
		{submitError ? <Text accessibilityRole="alert" style={styles.validation}>{submitError}</Text> : null}
	</View>;
}

function UserInputCard({ activity, busy, onResolve }: { activity: ConversationActivity; busy: boolean; onResolve(requestId: string, action: "accept" | "decline" | "cancel", content?: Record<string, unknown>): Promise<void> }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const schema = activity.detail?.schema;
	const [values, setValues] = useState<Record<string, unknown>>(() => Object.fromEntries(Object.entries(schema?.properties ?? {}).map(([key, property]) => [key, initialInputValue(property)])));
	const [validationError, setValidationError] = useState<string>();
	const [submitting, setSubmitting] = useState(false);
	const [submitError, setSubmitError] = useState<string>();
	const pending = activity.status === "pending";
	const url = activity.detail?.inputMode === "url" ? safeHttpURL(activity.detail.url) : undefined;
	const resolve = async (action: "accept" | "decline" | "cancel", content?: Record<string, unknown>) => {
		if (submitting || !activity.requestId) return;
		setSubmitting(true);
		setSubmitError(undefined);
		try { await onResolve(activity.requestId, action, content); }
		catch (cause) { setSubmitError(cause instanceof Error ? cause.message : String(cause)); }
		finally { setSubmitting(false); }
	};
	const submit = () => {
		const missing = missingRequiredInputs(schema?.required, values);
		if (missing.length) { setValidationError(`Complete ${missing.join(", ")} before continuing.`); return; }
		for (const [name, property] of Object.entries(schema?.properties ?? {})) {
			const problem = validateInput(property, values[name]);
			if (problem) { setValidationError(`${property.title || humanizeInputName(name)} ${problem}.`); return; }
		}
		setValidationError(undefined);
		void resolve("accept", values);
	};
	return <View style={[styles.requestCard, pending && { borderColor: t.blue }]}>
		<View style={styles.requestTitle}><Feather name="message-square" size={15} color={pending ? t.blue : t.textTertiary} /><Text style={styles.requestHeading}>{schema?.title || (pending ? "Agent needs input" : "Input resolved")}</Text></View>
		<Text style={styles.requestCopy}>{activity.detail?.message || schema?.description || activity.summary}</Text>
		{pending && activity.detail?.inputMode === "url" ? <View style={styles.urlBox}><Text selectable style={styles.urlText}>{url?.href ?? "The provider supplied an unsafe or invalid URL."}</Text></View> : null}
		{pending && activity.detail?.inputMode !== "url" && schema?.properties ? <View style={styles.form}>{Object.entries(schema.properties).map(([name, property]) => <InputField key={name} name={name} property={property} required={schema.required?.includes(name) ?? false} value={values[name]} onChange={(value) => { setValidationError(undefined); setValues((old) => ({ ...old, [name]: value })); }} />)}</View> : null}
		{validationError ? <Text accessibilityRole="alert" style={styles.validation}>{validationError}</Text> : null}
		{submitError ? <Text accessibilityRole="alert" style={styles.validation}>{submitError}</Text> : null}
		{pending && activity.requestId ? <View style={styles.actions}>
			<Action label="Cancel" disabled={busy || submitting} onPress={() => void resolve("cancel")} />
			<Action label={activity.detail?.inputMode === "url" ? "Decline" : "Skip"} disabled={busy || submitting} onPress={() => void resolve("decline")} />
			{activity.detail?.inputMode === "url" ? <Action label={submitting ? "Opening…" : "Open link"} primary disabled={busy || submitting || !url} onPress={() => {
				if (!url) return;
				void Linking.openURL(url.href)
					.then(() => resolve("accept"))
					.catch(() => setValidationError("This link could not be opened on this device."));
			}} /> : <Action label={submitting ? "Sending…" : "Continue"} primary disabled={busy || submitting} onPress={submit} />}
		</View> : pending ? <Text style={[styles.partial, { color: t.amber }]}>This request has no provider identity, so AO cannot answer it safely. Open diagnostics on the host.</Text> : <Text style={styles.partial}>Already answered. This card is kept for the record.</Text>}
	</View>;
}

function InputField({ name, property, required, value, onChange }: { name: string; property: InputProperty; required: boolean; value: unknown; onChange(value: unknown): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const label = `${property.title || humanizeInputName(name)}${required ? " *" : ""}`;
	if (property.type === "boolean") return <View style={styles.switchRow}><Text style={styles.inputLabel}>{label}</Text><Switch accessibilityLabel={label} value={Boolean(value)} onValueChange={onChange} trackColor={{ true: t.blue }} /></View>;
	const options = inputOptions(property);
	if (options.length) {
		const multi = property.type === "array";
		return <View style={styles.field}>
			<Text style={styles.inputLabel}>{label}</Text>
			{property.description ? <Text style={styles.inputHint}>{property.description}</Text> : null}
			<View style={styles.choiceWrap}>{options.map((choice) => {
				const checked = multi ? Array.isArray(value) && value.includes(choice.value) : value === choice.value;
				return <Action
					key={choice.value}
					label={choice.label}
					hint={choice.description}
					primary={checked}
					onPress={() => { haptics.select(); onChange(multi ? toggleInputValue(Array.isArray(value) ? value : [], choice.value) : choice.value); }}
				/>;
			})}</View>
		</View>;
	}
	return <View style={styles.field}>
		<Text style={styles.inputLabel}>{label}</Text>
		{property.description ? <Text style={styles.inputHint}>{property.description}</Text> : null}
		<TextInput accessibilityLabel={label} value={value === undefined ? "" : String(value)} onChangeText={(text) => onChange(property.type === "number" || property.type === "integer" ? (text === "" ? "" : Number(text)) : text)} keyboardType={property.type === "number" || property.type === "integer" ? "numeric" : "default"} maxLength={property.maxLength} style={styles.formInput} placeholderTextColor={t.textFaint} />
	</View>;
}


function CompactionMarker({ activity }: { activity: ConversationActivity }) {
	const styles = useThemedStyles(makeStyles);
	const after = activity.detail?.tokensAfter;
	const window = activity.detail?.contextWindow;
	return <View style={styles.compaction}><View style={styles.ruleHalf} /><Feather name="archive" size={12} /><Text style={styles.compactionText}>HISTORY COMPACTED{activity.detail?.tokensReclaimed ? `  −${formatTokens(activity.detail.tokensReclaimed)}` : ""}{after && window ? `  ${Math.round((after / window) * 100)}% FULL` : ""}</Text><View style={styles.ruleHalf} /></View>;
}

function ErrorActivity({ activity }: { activity: ConversationActivity }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <View style={[styles.errorCard, { borderColor: t.tintRed }]}><Feather name="alert-triangle" size={14} color={t.red} /><View style={{ flex: 1 }}><Text style={styles.errorTitle}>{activity.summary || "Agent error"}</Text>{activity.detail?.error || activity.detail?.message ? <Text selectable style={styles.errorCopy}>{String(activity.detail.error ?? activity.detail.message)}</Text> : null}</View></View>;
}

function EmptyConversation({ harness, controller }: { harness: string; controller: string }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <View style={styles.empty}><View style={[styles.emptyIcon, { backgroundColor: t.tintBlue }]}><Feather name="message-square" size={20} color={t.blue} /></View><Text style={styles.emptyTitle}>{controller === "connecting" ? "Connecting to the agent…" : "Start the conversation"}</Text><Text style={styles.emptyCopy}>This {harness || "agent"} session works in its own AO worktree. Ask it to inspect, change, test, or explain anything there.</Text></View>;
}

function Action({ label, hint, onPress, primary, tone, disabled }: { label: string; hint?: string; onPress(): void; primary?: boolean; tone?: "danger"; disabled?: boolean }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const fill = tone === "danger" ? t.tintRed : primary ? t.blue : t.bgElevated;
	const ink = tone === "danger" ? t.red : primary ? t.onAccent : t.textPrimary;
	return <Pressable accessibilityRole="button" accessibilityState={{ disabled }} disabled={disabled} onPress={() => { if (tone === "danger") haptics.warning(); else haptics.tap(); onPress(); }} style={({ pressed }) => [styles.action, { backgroundColor: fill }, pressed && { opacity: 0.75 }, disabled && { opacity: 0.45 }]}><Text style={[styles.actionLabel, { color: ink }]}>{label}</Text>{hint ? <Text style={styles.actionHint}>{hint}</Text> : null}</Pressable>;
}

function LabelValue({ label, value }: { label: string; value: string }) { const styles = useThemedStyles(makeStyles); return <View style={styles.labelValue}><Text style={styles.detailLabel}>{label}</Text><Text selectable style={styles.detailValue}>{value}</Text></View>; }
function CodeOutput({ value }: { value: string }) { const styles = useThemedStyles(makeStyles); return <Pressable onLongPress={() => { void Clipboard.setStringAsync(value); haptics.success(); }}><Text selectable style={styles.output}>{value}</Text></Pressable>; }
function FileList({ files }: { files: unknown[] }) { const styles = useThemedStyles(makeStyles); return <View style={styles.fileBody}>{files.map((file, index) => <Text key={index} selectable style={styles.filePath}>• {typeof file === "string" ? file : printable(file)}</Text>)}</View>; }

function printable(value: unknown): string {
	if (value === null || value === undefined) return "";
	if (typeof value === "string") return value;
	try { return JSON.stringify(value, null, 2); } catch { return String(value); }
}

function fileChanges(value: unknown): FileChange[] {
	if (!Array.isArray(value)) return [];
	return value.flatMap((entry): FileChange[] => {
		if (!entry || typeof entry !== "object") return [];
		const file = entry as Record<string, unknown>;
		if (typeof file.path !== "string") return [];
		const status = ["added", "modified", "deleted", "renamed"].includes(String(file.status)) ? String(file.status) as FileChange["status"] : "modified";
		return [{ path: file.path, oldPath: typeof file.oldPath === "string" ? file.oldPath : undefined, status, additions: typeof file.additions === "number" ? file.additions : 0, deletions: typeof file.deletions === "number" ? file.deletions : 0, patch: typeof file.patch === "string" ? file.patch : undefined, patchTruncated: file.patchTruncated === true }];
	});
}

function reviewPaths(value: unknown): string[] {
	if (!Array.isArray(value)) return [];
	return value.flatMap((entry) => typeof entry === "string" ? [entry] : entry && typeof entry === "object" && typeof (entry as { path?: unknown }).path === "string" ? [(entry as { path: string }).path] : []);
}

function truncationNote(value: unknown): string | undefined {
	if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
	const record = value as { truncated?: unknown; bytes?: unknown };
	if (record.truncated !== true) return undefined;
	const bytes = typeof record.bytes === "number" ? ` (${formatBytes(record.bytes)})` : "";
	return `This payload${bytes} was larger than AO stores, so it was not kept.`;
}

function formatBytes(bytes: number): string {
	if (bytes < 1024) return `${bytes} B`;
	if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
	return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function lastLine(value: string): string { return value.trimEnd().split("\n").at(-1) ?? ""; }

function activityMeta(activity: ConversationActivity): { icon: keyof typeof Feather.glyphMap; prefix?: string; color(t: Theme): string } {
	switch (activity.activityKind) {
		case "command": return { icon: "terminal", color: (t) => activity.status === "failed" ? t.red : t.textTertiary };
		case "file_change": return { icon: "edit-3", prefix: "Changed", color: (t) => t.blue };
		case "mcp_tool": return { icon: "tool", prefix: activity.detail?.server ? `${activity.detail.server} ·` : "MCP ·", color: (t) => t.purple };
		case "auto_review": return { icon: "shield", prefix: "Reviewed", color: (t) => t.green };
		default: return { icon: "activity", color: (t) => t.textTertiary };
	}
}

function elapsed(start?: string, end?: string): string | undefined { if (!start || !end) return undefined; const ms = Date.parse(end) - Date.parse(start); if (!Number.isFinite(ms) || ms < 0) return undefined; const seconds = Math.round(ms / 1000); return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`; }
function formatTokens(value: number): string { return value >= 1_000 ? `${(value / 1_000).toFixed(value >= 10_000 ? 0 : 1)}k` : String(value); }
const makeStyles = (t: Theme) => StyleSheet.create({
	timelineWrap: { flex: 1, position: "relative", backgroundColor: t.bgBase },
	emptySurface: { flex: 1, justifyContent: "center" },
	list: { flex: 1, backgroundColor: t.bgBase },
	content: { paddingHorizontal: 16, paddingTop: 8, paddingBottom: 28 },
	older: { alignSelf: "center", flexDirection: "row", gap: 7, alignItems: "center", paddingHorizontal: 12, paddingVertical: 8, marginBottom: 14 },
	olderText: { color: t.textTertiary, fontSize: 12 },
	beginning: { alignSelf: "center", color: t.textFaint, fontSize: 10, letterSpacing: 1, textTransform: "uppercase", marginBottom: 16 },
	userRow: { alignItems: "flex-end", paddingVertical: 10 },
	userBubble: { maxWidth: "86%", backgroundColor: t.bgElevated, borderWidth: 1, borderColor: t.borderDefault, borderRadius: 17, borderBottomRightRadius: 5, paddingHorizontal: 14, paddingVertical: 10 },
	userText: { color: t.textPrimary, fontSize: 16, lineHeight: 22 },
	delivery: { marginTop: 5, color: t.amber, fontSize: 10 },
	originMessage: { marginVertical: 8, borderLeftWidth: 2, borderLeftColor: t.borderStrong, paddingLeft: 10, gap: 5 },
	originHeader: { flexDirection: "row", alignItems: "center", gap: 5 },
	originLabel: { color: t.textTertiary, fontSize: 10, fontWeight: "700", textTransform: "uppercase", letterSpacing: 0.7 },
	originText: { color: t.textSecondary, fontSize: 14, lineHeight: 20 },
	originMore: { alignSelf: "flex-start", flexDirection: "row", alignItems: "center", gap: 4, paddingVertical: 4 },
	originMoreText: { color: t.blue, fontSize: 11, fontWeight: "600" },
	steerBubble: { backgroundColor: t.tintBlue, borderColor: t.borderSubtle },
	steerLabel: { color: t.blue, fontSize: 8, letterSpacing: 1, fontWeight: "700", marginBottom: 3 },
	assistantRow: { paddingVertical: 14, paddingHorizontal: 1 },
	sender: { color: t.textTertiary, fontSize: 11, fontWeight: "600", marginBottom: 5 },
	streamingDot: { width: 5, height: 5, borderRadius: 3, backgroundColor: t.orange, marginTop: 8 },
	copy: { alignSelf: "flex-start", flexDirection: "row", alignItems: "center", gap: 5, paddingVertical: 7, paddingHorizontal: 2 },
	copyText: { color: t.textFaint, fontSize: 10 },
	jump: { position: "absolute", right: 14, bottom: 12, minHeight: 36, flexDirection: "row", alignItems: "center", gap: 6, paddingHorizontal: 12, borderRadius: 18, backgroundColor: jumpToLatestColors(t).backgroundColor, borderWidth: 1, borderColor: t.borderStrong },
	jumpText: { color: t.textPrimary, fontSize: 11, fontWeight: "700" },
	systemSignal: { marginVertical: 7, flexDirection: "row", alignItems: "flex-start", gap: 9, borderWidth: 1, borderColor: t.borderDefault, borderRadius: 10, backgroundColor: t.bgSurface, padding: 10 },
	systemTitle: { color: t.textPrimary, fontSize: 11, fontWeight: "600" },
	systemDetail: { color: t.textTertiary, fontSize: 10, lineHeight: 14, marginTop: 2 },
	activityWrap: { paddingVertical: 2 },
	activityRow: { minHeight: 34, flexDirection: "row", alignItems: "center", gap: 8, paddingVertical: 6 },
	activitySummary: { flex: 1, color: t.textSecondary, fontSize: 13, lineHeight: 18, fontFamily: t.fontMono },
	server: { color: t.textTertiary, fontSize: 10, fontFamily: t.fontMono },
	activityProgress: { maxWidth: "28%", color: t.textFaint, fontSize: 10 },
	activityStopped: { color: t.textFaint, fontSize: 10, fontWeight: "600" },
	activityDetail: { marginLeft: 21, marginBottom: 7, borderLeftWidth: 1, borderLeftColor: t.borderSubtle, paddingLeft: 11, gap: 7 },
	terminalInput: { gap: 4 },
	terminalInputTitle: { flexDirection: "row", alignItems: "center", gap: 5 },
	reviewDecision: { color: t.green, fontSize: 11, fontWeight: "700" },
	risk: { color: t.amber, fontSize: 9, fontWeight: "700", textTransform: "uppercase" },
	reviewRationale: { color: t.textPrimary, backgroundColor: t.bgColumn, borderRadius: 8, padding: 9, fontSize: 11, lineHeight: 16 },
	runWrap: { paddingVertical: 2 },
	runSummary: { minHeight: 34, flexDirection: "row", alignItems: "center", gap: 8, paddingVertical: 6 },
	runText: { flex: 1, color: t.textTertiary, fontSize: 12 },
	runFailed: { color: t.red, fontSize: 10, fontWeight: "600" },
	runStopped: { color: t.textFaint, fontSize: 10, fontWeight: "600" },
	runDetail: { marginBottom: 7, borderRadius: 10, borderWidth: 1, borderColor: t.borderSubtle, backgroundColor: t.bgSubtle, paddingHorizontal: 9, paddingVertical: 3 },
	subagent: { marginLeft: 20, marginBottom: 5, borderLeftWidth: 1, borderLeftColor: t.borderStrong, paddingLeft: 8 },
	subagentHeader: { minHeight: 34, flexDirection: "row", alignItems: "center", gap: 6, paddingVertical: 6 },
	subagentLabel: { flex: 1, color: t.textFaint, fontSize: 8, letterSpacing: 0.8 },
	detailCopy: { color: t.textSecondary, fontSize: 13, lineHeight: 19 },
	labelValue: { flexDirection: "row", gap: 9 },
	detailLabel: { width: 30, color: t.textFaint, fontSize: 11, fontFamily: t.fontMono },
	detailValue: { flex: 1, color: t.textSecondary, fontSize: 11, fontFamily: t.fontMono },
	output: { color: t.textSecondary, backgroundColor: t.bgColumn, borderRadius: 8, padding: 10, fontFamily: t.fontMono, fontSize: 11, lineHeight: 17 },
	partial: { color: t.textFaint, fontSize: 10 },
	turnWrap: { paddingTop: 8, paddingBottom: 16, gap: 8 },
	turnLine: { flexDirection: "row", alignItems: "center", gap: 8 },
	ruleHalf: { height: 1, flex: 1, backgroundColor: t.borderSubtle },
	turnState: { color: t.textTertiary, fontSize: 10, letterSpacing: 1.2, fontWeight: "700" },
	turnDuration: { color: t.textFaint, fontSize: 10, fontFamily: t.fontMono },
	turnError: { color: t.red, fontSize: 12, lineHeight: 17, textAlign: "right" },
	rollbackConfirm: { marginTop: 4, backgroundColor: t.bgElevated, borderWidth: 1, borderColor: t.borderDefault, borderRadius: 12, padding: 12, gap: 7 },
	rollbackTitle: { color: t.textPrimary, fontWeight: "700", fontSize: 13 },
	rollbackCopy: { color: t.textSecondary, fontSize: 12, lineHeight: 17 },
	planCard: { backgroundColor: t.bgSubtle, borderRadius: 10, overflow: "hidden" },
	planHeader: { minHeight: 36, flexDirection: "row", alignItems: "center", gap: 8, paddingHorizontal: 10 },
	planTitle: { flex: 1, color: t.textSecondary, fontSize: 12, fontWeight: "600" },
	planLive: { color: t.orange, fontSize: 8, letterSpacing: 0.7, fontWeight: "700" },
	planCount: { color: t.textFaint, fontFamily: t.fontMono, fontSize: 10 },
	planBody: { paddingHorizontal: 10, paddingBottom: 10, gap: 7 },
	planStep: { flexDirection: "row", alignItems: "flex-start", gap: 8 },
	planStepText: { flex: 1, color: t.textSecondary, fontSize: 12, lineHeight: 17 },
	planStepState: { color: t.textFaint, fontSize: 8, textTransform: "uppercase" },
	planDone: { color: t.textTertiary, textDecorationLine: "line-through" },
	fileBody: { paddingHorizontal: 10, paddingBottom: 9, gap: 5 },
	fileRow: { flexDirection: "row", alignItems: "center", gap: 7 },
	fileMark: { width: 13, fontFamily: t.fontMono, fontSize: 10, fontWeight: "700" },
	filePath: { flex: 1, color: t.textSecondary, fontFamily: t.fontMono, fontSize: 11, lineHeight: 16 },
	fileStat: { color: t.textFaint, fontFamily: t.fontMono, fontSize: 10 },
	requestCard: { marginVertical: 8, backgroundColor: t.bgElevated, borderWidth: 1, borderColor: t.borderDefault, borderRadius: 13, padding: 13, gap: 10 },
	requestTitle: { flexDirection: "row", alignItems: "center", gap: 8 },
	requestHeading: { flex: 1, color: t.textPrimary, fontSize: 13, fontWeight: "700" },
	requestId: { color: t.textFaint, fontFamily: t.fontMono, fontSize: 9 },
	requestCopy: { color: t.textSecondary, fontSize: 13, lineHeight: 19 },
	requestCommand: { color: t.textPrimary, backgroundColor: t.bgColumn, borderRadius: 8, padding: 9, fontFamily: t.fontMono, fontSize: 11, lineHeight: 16 },
	actions: { flexDirection: "row", alignItems: "center", justifyContent: "flex-end", gap: 8, flexWrap: "wrap" },
	action: { minHeight: 36, justifyContent: "center", borderRadius: 10, paddingHorizontal: 13, borderWidth: 1, borderColor: t.borderSubtle },
	actionLabel: { fontSize: 12, fontWeight: "700" },
	actionHint: { maxWidth: 180, color: t.textTertiary, fontSize: 9, lineHeight: 12, marginTop: 2 },
	form: { gap: 12 },
	field: { gap: 5 },
	inputLabel: { color: t.textPrimary, fontSize: 12, fontWeight: "600" },
	inputHint: { color: t.textTertiary, fontSize: 11, lineHeight: 15 },
	formInput: { minHeight: 42, borderRadius: 9, borderWidth: 1, borderColor: t.borderDefault, color: t.textPrimary, paddingHorizontal: 11, paddingVertical: 8, fontSize: 14, backgroundColor: t.bgColumn },
	switchRow: { flexDirection: "row", alignItems: "center", justifyContent: "space-between" },
	choiceWrap: { flexDirection: "row", flexWrap: "wrap", gap: 7 },
	urlBox: { backgroundColor: t.bgColumn, borderRadius: 8, padding: 9 },
	urlText: { color: t.textSecondary, fontFamily: t.fontMono, fontSize: 10, lineHeight: 15 },
	validation: { color: t.red, fontSize: 11, lineHeight: 15 },
	compaction: { flexDirection: "row", alignItems: "center", gap: 7, paddingVertical: 14 },
	compactionText: { color: t.textFaint, fontSize: 9, letterSpacing: 1 },
	errorCard: { marginVertical: 7, flexDirection: "row", gap: 9, backgroundColor: t.tintRed, borderRadius: 11, borderWidth: 1, padding: 11 },
	errorTitle: { color: t.red, fontSize: 12, fontWeight: "700" },
	errorCopy: { marginTop: 4, color: t.textSecondary, fontSize: 11, lineHeight: 16 },
	empty: { paddingVertical: 90, alignItems: "center", paddingHorizontal: 25 },
	emptyIcon: { width: 44, height: 44, borderRadius: 14, alignItems: "center", justifyContent: "center", marginBottom: 15 },
	emptyTitle: { color: t.textPrimary, fontSize: 17, fontWeight: "700", marginBottom: 7 },
	emptyCopy: { color: t.textTertiary, fontSize: 13, lineHeight: 19, textAlign: "center" },
});
