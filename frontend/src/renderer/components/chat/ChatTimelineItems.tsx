/**
 * Timeline entries for the Chat surface.
 *
 * One component per durable item shape, keyed by sequence so a streaming rewrite
 * updates a message in place instead of remounting it. Every component is a pure
 * function of a domain item: no lifecycle decisions, no capability checks, no
 * re-sorting. Those belong to the daemon.
 */

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
	AlertTriangle,
	Brain,
	ChevronDown,
	ChevronRight,
	CircleAlert,
	CornerDownLeft,
	CornerDownRight,
	File as FileIcon,
	FileDiff,
	Gauge,
	KeyRound,
	Keyboard,
	ListChecks,
	Loader2,
	Pencil,
	Plug,
	Shuffle,
	ShieldCheck,
	ShieldQuestion,
	ShieldX,
	SquareTerminal,
	Undo2,
	User,
} from "lucide-react";

/** Fixed icon column, matching the prototype's row anatomy. */
const activityIcon: Record<ActivityKind, typeof SquareTerminal> = {
	command: SquareTerminal,
	file_change: FileDiff,
	plan: ListChecks,
	reasoning: Brain,
	approval: ShieldQuestion,
	usage: Gauge,
	error: AlertTriangle,
	system: CircleAlert,
	mcp_tool: Plug,
	auto_review: ShieldCheck,
	user_input: Keyboard,
};
import { cn } from "../../lib/utils";
import { caretNotation, stripAnsi } from "../../lib/ansi";
import { getApiBaseUrl } from "../../lib/api-client";
import { ChatMarkdown } from "./ChatMarkdown";
import { HighlightedCode } from "./HighlightedCode";
import { CopyButton } from "./CopyButton";
import { HumanMessageEditor } from "./HumanMessageEditor";
import { ConversationBranchNavigator } from "./ConversationBranchNavigator";
import { ConversationContentItems } from "./ConversationContentItems";
import {
	ACTIVITY_SUMMARY_BUTTON_CLASS,
	commandBinaryLabel,
	commandCategory,
	exploredFileCount,
	isNonzeroCommandExit,
} from "./activity-command";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "../ui/tooltip";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuTrigger,
} from "../ui/dropdown-menu";
import {
	fileChangeFiles,
	reviewedPaths,
	type ActivityKind,
	type ConversationActivity,
	type ConversationBranchPoint,
	type ConversationMessage,
	type DecisionOption,
	type DeliveryState,
	type DiffStatus,
	type FileChangeFile,
	type ConversationItem,
	type TurnDiff,
} from "../../types/conversation";

const timeFormatter = new Intl.DateTimeFormat(undefined, {
	hour: "2-digit",
	minute: "2-digit",
	hourCycle: "h23",
});
const dateFormatter = new Intl.DateTimeFormat(undefined, {
	month: "short",
	day: "numeric",
	year: "numeric",
});

const ORIGIN_REPORT_COLLAPSE_AT = 600;
const ORIGIN_REPORT_PREVIEW_LENGTH = 240;

// These are AO-owned prompt suffixes, not general markdown. Chat and spawn used
// slightly different wording, and older conversations used "Attached images";
// accepting every shipped form lets the transcript improve without rewriting
// its durable history.
const ATTACHMENT_REFERENCE_BLOCK =
	/(?:^|\n\n)(?:Attached files \(read these files in the workspace(?: for context)?\)|Attached images \(read these files in the workspace for visual context\)):\n((?:- [^\n]+(?:\n|$))+)$/;
const STAGED_ATTACHMENT_PATH = /^\.ao\/attachments\/(?:attachment|image)-[A-Za-z0-9][A-Za-z0-9._-]*$/;
const IMAGE_ATTACHMENT_PATH = /\.(?:png|jpe?g|gif|webp|bmp)$/i;

/** Smooth baseline, with adaptive catch-up when provider chunks outrun playback. */
const STREAM_BASE_CHARACTERS_PER_SECOND = 58;
const STREAM_TARGET_BACKLOG_CHARACTERS = 72;
const STREAM_MAX_CHARACTERS_PER_SECOND = 720;
const STREAM_MAX_FRAME_DELTA_MS = 100;
const STREAM_GRAPHEME_SEGMENTER = new Intl.Segmenter(undefined, { granularity: "grapheme" });

function streamGraphemes(text: string): string[] {
	return Array.from(STREAM_GRAPHEME_SEGMENTER.segment(text), ({ segment }) => segment);
}

function reconciledStreamPrefix(visibleText: string, targetGraphemes: string[]): string {
	let boundary = 0;
	for (const grapheme of targetGraphemes) {
		const nextBoundary = boundary + grapheme.length;
		if (nextBoundary > visibleText.length) break;
		boundary = nextBoundary;
	}
	return visibleText.slice(0, boundary);
}

function useSmoothStreamingText(message: ConversationMessage): string {
	// A snapshot can first reach the renderer after the provider has already emitted
	// text. Keep that first durable burst visible; only later deltas need smoothing.
	const [visibleText, setVisibleText] = useState(() => message.text);
	const visibleRef = useRef(visibleText);
	const targetRef = useRef(message.text);
	const visibleGraphemesRef = useRef(streamGraphemes(visibleText));
	const targetGraphemesRef = useRef(streamGraphemes(message.text));
	const messageIdRef = useRef(message.id);
	const frameRef = useRef<number | undefined>(undefined);
	const lastFrameAtRef = useRef<number | undefined>(undefined);
	const fractionalCharactersRef = useRef(0);
	const [reducedMotion, setReducedMotion] = useState(
		() => typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches,
	);

	useEffect(() => {
		const mediaQuery = window.matchMedia("(prefers-reduced-motion: reduce)");
		const update = () => setReducedMotion(mediaQuery.matches);
		mediaQuery.addEventListener("change", update);
		return () => mediaQuery.removeEventListener("change", update);
	}, []);

	const cancelDrain = useCallback(() => {
		if (frameRef.current !== undefined) {
			window.cancelAnimationFrame(frameRef.current);
			frameRef.current = undefined;
		}
		lastFrameAtRef.current = undefined;
		fractionalCharactersRef.current = 0;
	}, []);

	const scheduleDrain = useCallback(() => {
		if (frameRef.current !== undefined) return;

		const tick = (now: number) => {
			frameRef.current = undefined;
			const previousFrameAt = lastFrameAtRef.current ?? now;
			lastFrameAtRef.current = now;
			const backlog = targetGraphemesRef.current.length - visibleGraphemesRef.current.length;
			if (backlog <= 0) {
				fractionalCharactersRef.current = 0;
				return;
			}

			// Keep a small, intentional buffer for smoothness. As it grows, increase
			// throughput instead of letting a long response fall further behind.
			const catchup = Math.max(0, backlog - STREAM_TARGET_BACKLOG_CHARACTERS);
			const charactersPerSecond = Math.min(
				STREAM_MAX_CHARACTERS_PER_SECOND,
				STREAM_BASE_CHARACTERS_PER_SECOND + catchup * 2,
			);
			const elapsedMs = Math.min(STREAM_MAX_FRAME_DELTA_MS, Math.max(0, now - previousFrameAt));
			fractionalCharactersRef.current += charactersPerSecond * elapsedMs / 1000;
			const count = Math.floor(fractionalCharactersRef.current);
			if (count < 1) {
				frameRef.current = window.requestAnimationFrame(tick);
				return;
			}
			fractionalCharactersRef.current -= count;
			const current = visibleGraphemesRef.current;
			const target = targetGraphemesRef.current;
			if (current.length >= target.length) return;
			const nextGraphemes = target.slice(current.length, current.length + count);
			const next = current.concat(nextGraphemes).join("");
			visibleRef.current = next;
			visibleGraphemesRef.current = current.concat(nextGraphemes);
			setVisibleText(next);
			if (visibleGraphemesRef.current.length < targetGraphemesRef.current.length) {
				frameRef.current = window.requestAnimationFrame(tick);
			}
		};

		lastFrameAtRef.current = undefined;
		fractionalCharactersRef.current = 0;
		frameRef.current = window.requestAnimationFrame(tick);
	}, []);

	useEffect(() => {
		if (message.id !== messageIdRef.current) {
			cancelDrain();
			messageIdRef.current = message.id;
			targetRef.current = message.text;
			targetGraphemesRef.current = streamGraphemes(message.text);
			const initial = message.text;
			visibleRef.current = initial;
			visibleGraphemesRef.current = streamGraphemes(initial);
			setVisibleText(initial);
			return;
		}

		targetRef.current = message.text;
		targetGraphemesRef.current = streamGraphemes(message.text);
		if (!message.streaming || reducedMotion) {
			cancelDrain();
			visibleRef.current = message.text;
			visibleGraphemesRef.current = targetGraphemesRef.current;
			setVisibleText(message.text);
			return;
		}
		// A provider correction or rollback can replace the current prefix. In that
		// case the durable snapshot is authoritative and should be shown immediately.
		if (!message.text.startsWith(visibleRef.current)) {
			visibleRef.current = message.text;
			visibleGraphemesRef.current = targetGraphemesRef.current;
			setVisibleText(message.text);
			return;
		}
		// A later combining mark or ZWJ can merge the last visible grapheme into a
		// different target grapheme. Reconcile that trailing fragment before using
		// the old grapheme count, otherwise the drain can skip the merged suffix.
		const reconciled = reconciledStreamPrefix(visibleRef.current, targetGraphemesRef.current);
		if (reconciled !== visibleRef.current) {
			visibleRef.current = reconciled;
			visibleGraphemesRef.current = streamGraphemes(reconciled);
			setVisibleText(reconciled);
		}
		if (visibleGraphemesRef.current.length < targetGraphemesRef.current.length) scheduleDrain();
	}, [cancelDrain, message.id, message.text, message.streaming, reducedMotion, scheduleDrain]);

	useEffect(
		() => cancelDrain,
		[cancelDrain],
	);

	return visibleText;
}

function humanMessageParts(text: string): { body: string; attachments: string[] } {
	const match = ATTACHMENT_REFERENCE_BLOCK.exec(text);
	if (!match?.[1]) return { body: text, attachments: [] };

	const attachments = match[1]
		.trimEnd()
		.split("\n")
		.map((line) => line.slice(2));
	// Only reinterpret paths AO itself stages. A user can write an identically
	// worded example about docs/screenshot.png; that prose must remain untouched.
	if (attachments.length === 0 || attachments.some((path) => !STAGED_ATTACHMENT_PATH.test(path))) {
		return { body: text, attachments: [] };
	}
	// The match begins at the generated separator, so slicing at its index
	// removes only AO-owned text and preserves the authored body byte-for-byte.
	return { body: text.slice(0, match.index), attachments };
}

/** A status message followed by a full-width rule, with no text inside the rule. */
function TwoRowTimelineMarker({
	message,
	detail,
	tone = "text-muted-foreground/70",
	detailTone = "text-muted-foreground/70",
	action,
}: {
	message: string;
	detail?: string;
	tone?: string;
	detailTone?: string;
	action?: ReactNode;
}) {
	return (
		<div className="flex min-w-0 flex-col gap-1 py-1">
			<div className={cn("flex min-w-0 items-baseline gap-2 text-[11px]", tone)}>
				<span className="shrink-0">{message}</span>
				{detail ? (
					<span className={cn("min-w-0 truncate", detailTone)} title={detail}>
						{detail}
					</span>
				) : null}
				{action}
			</div>
			<span aria-hidden="true" className="h-px w-full bg-border" />
		</div>
	);
}

export function CompactionMarker({ activity }: { activity: ConversationActivity }) {
	const reclaimed = activity.detail?.tokensReclaimed;
	const after = activity.detail?.tokensAfter;
	const contextWindow = activity.detail?.contextWindow;
	const detail = reclaimed ? `−${formatTokens(reclaimed)}` : undefined;
	const fullness = after && contextWindow ? `${Math.round((after / contextWindow) * 100)}% full` : undefined;

	return (
		<TwoRowTimelineMarker
			message="The conversation history was compacted"
			detail={[detail, fullness].filter(Boolean).join(" · ") || undefined}
		/>
	);
}

export interface TurnOutcomeRetryControl {
	onRetry: () => void;
	pending?: boolean;
	error?: string;
	disabled?: boolean;
}

export function TurnOutcome({
	state,
	error,
	retry,
}: {
	state: "recovered" | "interrupted" | "failed";
	error?: string;
	retry?: TurnOutcomeRetryControl;
}) {
	const copy = {
		recovered: {
			label: "This turn was recovered from an earlier session",
			tone: "text-muted-foreground/70",
		},
		interrupted: {
			label: "The agent was interrupted by you",
			tone: "text-muted-foreground/70",
		},
		failed: { label: "The agent ran into a problem", tone: "text-destructive" },
	}[state];

	return (
		<TwoRowTimelineMarker
			message={copy.label}
			detail={error}
			tone={copy.tone}
			detailTone={state === "failed" ? "text-destructive" : undefined}
			action={
				retry ? (
					<>
						{retry.error ? (
							<span role="alert" className="max-w-[50%] text-pretty text-right text-[10px] leading-tight text-destructive">
								{retry.error}
							</span>
						) : null}
						<button
							type="button"
							onClick={retry.onRetry}
							disabled={retry.pending || retry.disabled}
							aria-label="Retry this turn"
							title={retry.error ?? (retry.disabled ? "Wait for the current turn to finish" : "Send this prompt again as a new turn")}
							data-testid="retry-turn"
							className="shrink-0 rounded px-1.5 py-0.5 text-[10px] text-muted-foreground/70 transition-colors hover:text-foreground focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring disabled:pointer-events-none disabled:opacity-50"
						>
							{retry.pending ? "Retrying…" : "Retry"}
						</button>
					</>
				) : undefined
			}
		/>
	);
}

function formatTokens(tokens: number): string {
	if (tokens < 1000) return `${tokens}`;
	return `${(tokens / 1000).toFixed(1)}k`;
}

function attachmentName(path: string): string {
	return path.slice(path.lastIndexOf("/") + 1);
}

function attachmentURL(apiBaseUrl: string, sessionId: string, path: string): string {
	const route = `/api/v1/sessions/${encodeURIComponent(sessionId)}/preview/files/${path
		.split("/")
		.map(encodeURIComponent)
		.join("/")}`;
	return apiBaseUrl ? new URL(route, apiBaseUrl).toString() : route;
}

/** Collapse the home directory so a long absolute path does not eat the row. */
function shortenPaths(text: string): string {
	return text.replace(/\/(?:Users|home)\/[^/\s]+/g, "~");
}

function formatDuration(ms: number): string {
	if (ms < 1000) return `${ms}ms`;
	if (ms < 60_000) {
		// Drop a trailing ".0" so whole seconds read as "3s", not "3.0s".
		return `${(ms / 1000).toFixed(1).replace(/\.0$/, "")}s`;
	}
	return `${Math.round(ms / 60_000)}m`;
}

function formatTime(iso: string): string {
	const parsed = new Date(iso);
	return Number.isNaN(parsed.getTime()) ? "" : timeFormatter.format(parsed);
}

function formatMessageTimestamp(iso: string, now = new Date()): string {
	const parsed = new Date(iso);
	if (Number.isNaN(parsed.getTime())) return "";

	const messageDay = new Date(parsed.getFullYear(), parsed.getMonth(), parsed.getDate()).getTime();
	const today = new Date(now.getFullYear(), now.getMonth(), now.getDate()).getTime();
	const daysAgo = Math.round((today - messageDay) / 86_400_000);
	if (daysAgo === 0) return timeFormatter.format(parsed);
	if (daysAgo === 1) return `Yesterday · ${timeFormatter.format(parsed)}`;
	return dateFormatter.format(parsed);
}

/* -------------------------------------------------------------------------- */
/* messages                                                                    */
/* -------------------------------------------------------------------------- */

/** What the user typed. Right-aligned and enclosed so it reads as theirs. */
export function HumanMessage({
	message,
	sessionId,
	apiBaseUrl = getApiBaseUrl(),
	queued,
	animateIn = false,
	onEdit,
	editing = false,
	editText,
	editReconstructedContext = false,
	onEditStart,
	onEditDraftChange,
	onEditCancel,
	editPending = false,
	editBusy = false,
	editError,
	branchPoint,
	onActivateBranch,
	activateBranchPending = false,
	activateBranchError,
}: {
	message: ConversationMessage;
	/** The staged paths are relative to this session's workspace. */
	sessionId: string;
	/** The live daemon origin; passed by the timeline so daemon restarts refresh images. */
	apiBaseUrl?: string;
	/** Typed while the agent was busy, and not sent yet. */
	queued?: boolean;
	/** True only for a human message added after the timeline first mounted. */
	animateIn?: boolean;
	onEdit?: (turnId: string, text: string) => Promise<unknown> | void;
	editing?: boolean;
	editText?: string;
	editReconstructedContext?: boolean;
	onEditStart?: () => void;
	onEditDraftChange?: (text: string) => void;
	onEditCancel?: () => void;
	editPending?: boolean;
	editBusy?: boolean;
	editError?: string;
	branchPoint?: ConversationBranchPoint;
	onActivateBranch?: (branchId: string) => Promise<unknown> | void;
	activateBranchPending?: boolean;
	activateBranchError?: string;
}) {
	const { body, attachments } = humanMessageParts(message.text);
	return (
		<div className="group/message flex flex-col items-end gap-1">
			{/* A queued message reads as not-yet-sent rather than as sent-and-ignored:
			    the agent has not seen it, and the timeline should not imply it has. */}
			{editing ? (
				<HumanMessageEditor
					text={editText ?? message.text}
					content={message.content ?? []}
					pending={editPending}
					busy={editBusy}
					reconstructedContext={editReconstructedContext}
					error={editError}
					onDraftChange={onEditDraftChange}
					onCancel={() => onEditCancel?.()}
					onSend={(text) => {
						if (!message.turnId || !onEdit) return;
						return onEdit(message.turnId, text);
					}}
				/>
			) : (
				<div
					className={cn(
						"cursor-chat-human-message w-fit max-w-[min(78%,560px)] rounded-[10px] px-3 py-2.5 text-sm leading-[1.55]",
						animateIn && "chat-human-message-enter",
						queued
							? "border border-dashed border-border-strong bg-transparent text-muted-foreground"
							: "bg-raised text-foreground",
					)}
				>
					{body ? <p className="break-words whitespace-pre-wrap text-pretty">{body}</p> : null}
					{attachments.length > 0 ? (
						<ul aria-label="Attached files" className={cn("flex max-w-full flex-wrap gap-2", body && "mt-2")}>
							{attachments.map((path) => {
								const name = attachmentName(path);
								return IMAGE_ATTACHMENT_PATH.test(path) ? (
									<li key={path} className="max-w-full overflow-hidden rounded-md border border-border bg-background">
										<img src={attachmentURL(apiBaseUrl, sessionId, path)} alt={name} loading="lazy" className="block h-auto max-h-80 max-w-full object-contain" />
									</li>
								) : (
									<li key={path} title={path} className="flex max-w-full items-center gap-1.5 rounded-md border border-border bg-background px-2 py-1.5 text-xs text-muted-foreground">
										<FileIcon aria-hidden="true" className="size-3.5 shrink-0" />
										<span className="truncate">{name}</span>
									</li>
								);
							})}
						</ul>
					) : null}
				</div>
			)}
			{editing ? null : (
				<div className="mt-1 flex h-7 items-center gap-1">
					<div className="flex items-center gap-1 opacity-0 transition-opacity duration-150 ease-out focus-within:opacity-100 group-hover/message:opacity-100 motion-reduce:transition-none">
						<span
							className="shrink-0 px-0.5 text-[11px] tabular-nums text-muted-foreground/75"
							aria-label={`Sent ${formatMessageTimestamp(message.createdAt)}`}
						>
							{formatMessageTimestamp(message.createdAt)}
						</span>
						{onEdit && onEditStart && message.turnId ? (
							<button
								type="button"
								onClick={onEditStart}
								aria-label="Edit user message"
								title="Edit user message"
								className="flex size-7 items-center justify-center rounded-md text-muted-foreground transition-[scale,background-color,color] duration-150 ease-out hover:bg-interactive-hover hover:text-foreground active:scale-[0.96] motion-reduce:transition-none motion-reduce:active:scale-100"
							>
								<Pencil aria-hidden="true" className="size-3" />
							</button>
						) : null}
						<CopyButton
							text={message.text}
							label="Copy user message"
							compact
							className="size-7 justify-center rounded-md px-0 py-0 transition-[scale,background-color,color] duration-150 ease-out hover:bg-interactive-hover hover:text-foreground active:scale-[0.96] motion-reduce:transition-none motion-reduce:active:scale-100"
						/>
					</div>
					{branchPoint && onActivateBranch ? (
						<ConversationBranchNavigator
							point={branchPoint}
							pending={activateBranchPending}
							error={activateBranchError}
							onActivate={onActivateBranch}
						/>
					) : null}
				</div>
			)}
			{queued ? (
				<div className="flex items-center gap-2 text-[11px] text-muted-foreground">
					<span>Queued · sends when the agent finishes</span>
				</div>
			) : null}
			{message.delivery && message.delivery !== "accepted" ? (
				<DeliveryNote state={message.delivery} />
			) : null}
		</div>
	);
}

/**
 * A message from a worker, automation, or the daemon. Attribution comes from the
 * durable origin field, never from a prefix parsed out of the text.
 */
export function OriginMessage({ message }: { message: ConversationMessage }) {
	const longReport = message.text.length > ORIGIN_REPORT_COLLAPSE_AT;
	const [expanded, setExpanded] = useState(false);
	const preview = longReport
		? `${message.text.slice(0, ORIGIN_REPORT_PREVIEW_LENGTH).trimEnd()}…`
		: message.text;

	return (
		<div className="cursor-chat-origin-message rounded-md border border-border border-l-2 border-l-logo-accent/60 px-3.5 py-2.5">
			<div className="mb-1.5 flex items-center gap-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
				<CircleAlert aria-hidden="true" className="size-3.5 shrink-0 text-logo-accent" />
				<span className="truncate">{message.senderLabel ?? message.origin}</span>
				<span className="ml-auto shrink-0 font-normal tabular-nums">
					{formatTime(message.createdAt)}
				</span>
			</div>
			{longReport && expanded ? (
				<ChatMarkdown text={message.text} muted />
			) : (
				<p className={cn("text-sm leading-relaxed text-muted-foreground", longReport && "line-clamp-3")}>
					{preview}
				</p>
			)}
			{longReport ? (
				<button
					type="button"
					onClick={() => setExpanded((current) => !current)}
					aria-expanded={expanded}
					className="mt-2 flex items-center gap-1 text-[11px] font-medium text-logo-accent transition-colors hover:text-markdown-link-hover"
				>
					<ChevronRight
						aria-hidden="true"
						className={cn("size-3 transition-transform", expanded && "rotate-90")}
					/>
					{expanded ? "Hide report" : "Show full report"}
				</button>
			) : null}
		</div>
	);
}

/** The agent's prose. Streaming is represented by text arriving in place. */
export function AssistantMessage({
	message,
	showCopy = false,
	onRollback,
	durationMs,
}: {
	message: ConversationMessage;
	/** Only the final answer of a finished turn owns the turn's copy action. */
	showCopy?: boolean;
	/**
	 * Discard this turn and everything after it. Lives next to copy so the finished
	 * answer owns both "keep this" and "undo from here".
	 */
	onRollback?: () => void;
	/** How long the finished turn took; sits next to rollback on the action row. */
	durationMs?: number;
}) {
	const visibleText = useSmoothStreamingText(message);
	const renderingStreaming = message.streaming || visibleText.length < message.text.length;
	const hasDuration = durationMs !== undefined && durationMs > 0;
	const showActions = !renderingStreaming && (showCopy || Boolean(onRollback) || hasDuration);
	return (
		<div className="group/message relative">
			<ChatMarkdown text={visibleText} streaming={renderingStreaming} />
			{showActions ? (
				// One action row for the completed answer, not one after every prose
				// fragment the provider emitted while working. Reveal the controls only
				// when the message is being inspected, keeping the answer visually quiet.
				<div className="mt-1 flex h-7 items-center gap-0.5 opacity-0 transition-opacity duration-150 ease-out group-hover/message:opacity-100 group-focus-within/message:opacity-100 motion-reduce:transition-none">
					{showCopy ? (
						/* The stored markdown, not a re-serialization of what was rendered:
						   pasting it into an editor has to give back what the agent wrote. */
						<CopyButton
							text={message.text}
							label="Copy message as markdown"
							compact
							className="-ml-1.5 size-7 justify-center rounded-md px-0 py-0 transition-[scale,background-color,color] duration-150 ease-out hover:bg-interactive-hover hover:text-foreground active:scale-[0.96] motion-reduce:transition-none motion-reduce:active:scale-100"
						/>
					) : null}
					{onRollback ? (
						<button
							type="button"
							onClick={onRollback}
							aria-label="Roll back to here"
							title="Roll back to here"
							className="flex size-7 items-center justify-center rounded-md text-muted-foreground transition-[scale,background-color,color] duration-150 ease-out hover:bg-interactive-hover hover:text-foreground active:scale-[0.96] motion-reduce:transition-none motion-reduce:active:scale-100"
						>
							<Undo2 aria-hidden="true" className="size-3" />
						</button>
					) : null}
					{hasDuration ? <TurnDuration durationMs={durationMs} /> : null}
					<span
						className="w-auto shrink-0 px-1 text-[11px] tabular-nums text-muted-foreground/75"
						aria-label={`Sent ${formatMessageTimestamp(message.createdAt)}`}
					>
						{formatMessageTimestamp(message.createdAt)}
					</span>
				</div>
			) : null}
		</div>
	);
}

/**
 * Delivery state, stated rather than implied. `uncertain` is its own outcome:
 * the provider may have accepted the turn while AO lost the connection, and
 * pretending otherwise in either direction would be a lie.
 */
function DeliveryNote({ state }: { state: DeliveryState }) {
	const copy: Record<DeliveryState, string> = {
		queued: "Queued — will send when the agent is idle",
		sending: "Sending…",
		accepted: "Delivered",
		uncertain: "Delivery uncertain — not retried automatically",
		failed: "Not delivered",
	};
	return (
		<span
			className={cn(
				"text-[11px] leading-none",
				state === "uncertain" || state === "failed" ? "text-warning" : "text-muted-foreground",
			)}
		>
			{copy[state]}
		</span>
	);
}

/* -------------------------------------------------------------------------- */
/* activities                                                                  */
/* -------------------------------------------------------------------------- */

/**
 * One activity, routed to the renderer for its kind.
 *
 * The dispatch lives here rather than in the timeline because a run collapses
 * activities too, and both paths must agree about what an MCP call looks like. A
 * kind this build does not recognize still renders as a generic row — dropping it
 * would hide work the agent really did.
 */
export function ActivityRow({ activity }: { activity: ConversationActivity }) {
	if (activity.activityKind === "mcp_tool") return <McpToolRow activity={activity} />;
	if (activity.activityKind === "auto_review") return <AutoReviewRow activity={activity} />;
	if (activity.activityKind === "reasoning") return <ReasoningBlock activity={activity} />;
	if (activity.activityKind === "error") return <ErrorActivityRow activity={activity} />;
	if (activity.detail?.event === "model.rerouted") return <RerouteRow activity={activity} />;
	if (activity.detail?.event === "auth.reauth_required") return <ReauthRow activity={activity} />;
	return <GenericActivityRow activity={activity} />;
}

/**
 * A collapsed activity row: icon, label, target, state. Expands to its payload.
 *
 * A `running` activity with no completion is a real terminal state here, not a
 * spinner that hangs forever — a provider can start a command and supersede it.
 */
function GenericActivityRow({ activity }: { activity: ConversationActivity }) {
	// null means "nobody has decided", which is what lets a running command open
	// itself and then close again once it settles. Once the user clicks, their
	// choice sticks: auto-collapsing a log someone is reading is worse than leaving
	// a finished row open.
	const [override, setOverride] = useState<boolean | null>(null);
	const Icon = activityIcon[activity.activityKind] ?? SquareTerminal;
	const detail = activity.detail;
	const files = fileChangeFiles(activity);
	// A single edit with no patch has nothing to expand into — the header already
	// named the file. Multi-file edits expand to a list; a lone patch expands to
	// the diff itself.
	const hasFileBody =
		files.length > 1 || (files.length === 1 && Boolean(files[0]?.patch));
	const hasBody = Boolean(
		detail?.command ||
			detail?.output || detail?.reason || detail?.text || detail?.terminalInput || hasFileBody,
	);
	const { label, path } = splitSummary(activity);
	// Commands and file edits share the explore-style summary line: muted label,
	// no icon column, always-visible chevron. Everything else keeps the denser
	// bordered activity row.
	const compactSummary =
		activity.activityKind === "command" || activity.activityKind === "file_change";
	const singleEdit = activity.activityKind === "file_change" && files.length === 1 ? files[0] : undefined;

	// Live output is only live if it is on screen, so a command that is still
	// running and already printing opens itself.
	const streamingOutput = activity.status === "running" && Boolean(detail?.output);
	const open = override ?? streamingOutput;

	return (
		<div
			className={cn(
				"min-w-0 max-w-full",
				compactSummary ? "flex flex-col" : "group/activity border-t border-border first:border-t-0",
			)}
		>
			<button
				type="button"
				onClick={() => setOverride(!open)}
				disabled={!hasBody}
				aria-expanded={hasBody ? open : undefined}
				className={cn(
					compactSummary
						? ACTIVITY_SUMMARY_BUTTON_CLASS
						: "flex min-h-[35px] w-full min-w-0 items-center gap-[9px] px-[11px] py-2 text-left text-[11px] transition-colors",
					hasBody && !compactSummary && "hover:bg-interactive-hover",
					!hasBody && "cursor-default",
				)}
			>
				{compactSummary ? null : (
					<Icon
						aria-hidden="true"
						className={cn(
							"w-[15px] shrink-0 text-center",
							activity.status === "failed" ? "text-destructive" : "text-muted-foreground/70",
						)}
						size={13}
					/>
				)}
				{singleEdit ? (
					<span className="flex min-w-0 items-center gap-1 text-[11.5px] font-normal">
						<span className="shrink-0 text-muted-foreground">
							{fileChangeVerb(singleEdit.status ?? "modified")}
						</span>
						<FileLocationLabel path={singleEdit.path} oldPath={singleEdit.oldPath} />
					</span>
				) : (
					<strong
						className={cn(
							compactSummary
								? "shrink-0 text-[11.5px] font-normal text-muted-foreground"
								: "min-w-0 truncate font-medium",
							!compactSummary &&
								(activity.status === "failed" ? "text-destructive" : "text-foreground"),
						)}
						title={compactSummary ? undefined : label}
					>
						{label}
					</strong>
				)}
				{path && !singleEdit ? (
					<span
						className="min-w-0 flex-1 truncate font-mono text-[10.5px] text-muted-foreground"
						title={path}
					>
						{path}
					</span>
				) : compactSummary ? null : (
					<span className="flex-1" />
				)}
				<ActivityState
					activity={activity}
					open={open}
					hasBody={hasBody}
					showDisclosure={!compactSummary}
				/>
				{compactSummary && hasBody ? (
					<ChevronRight
						aria-hidden="true"
						className={cn(
							"size-3 shrink-0 text-muted-foreground/40 transition-transform group-hover/run:text-muted-foreground",
							open && "rotate-90",
						)}
					/>
				) : null}
			</button>

			{open && hasBody ? (
				compactSummary &&
				activity.activityKind === "command" &&
				(detail?.command || detail?.output || detail?.terminalInput) ? (
					<CommandExploreBody activity={activity} />
				) : (
					<div className="flex flex-col gap-1.5 px-1 pb-1 pt-0.5">
						{/* One file: open straight onto its patch. Listing the same
						    basename again under "Edited name" is noise. */}
						{files.length === 1 && files[0]?.patch ? (
							<Patch patch={files[0].patch} truncated={files[0].patchTruncated} />
						) : null}
						{files.length > 1 ? <FileChangeList files={files} /> : null}
						{detail?.command ? (
							// Said explicitly rather than implied by the label: "Ran command"
							// alone never tells the reader what ran, and the collapsed row
							// deliberately keeps only the category.
							<pre className="overflow-x-auto rounded-md border border-border bg-background px-2.5 py-1.5 font-mono text-[10.5px] leading-relaxed text-foreground">
								{detail.command}
							</pre>
						) : null}
						{detail?.reason || detail?.text ? (
							<p className="whitespace-pre-wrap px-1 text-[11px] leading-relaxed text-muted-foreground">
								{detail.reason ?? detail.text}
							</p>
						) : null}
						{detail?.terminalInput ? (
							<TerminalInput
								text={detail.terminalInput}
								truncated={detail.terminalInputTruncated}
							/>
						) : null}
						{detail?.output ? <CommandOutput activity={activity} /> : null}
					</div>
				)
			) : null}
		</div>
	);
}

/**
 * Expanded command / explore body: one soft chat-surface card with the shell
 * line nested in its own chip, then muted monospace output underneath — the
 * same anatomy as the Cursor explore block, restated in AO chat tokens.
 */
function CommandExploreBody({ activity }: { activity: ConversationActivity }) {
	const detail = activity.detail;
	const command = detail?.command?.trim();
	const reason = (detail?.reason ?? detail?.text)?.trim();
	const binary = command ? commandBinaryLabel(command) : undefined;
	const showPrompt = Boolean(reason && reason !== command);

	return (
		<div className="cursor-chat-explore-box mt-1 flex min-w-0 flex-col overflow-hidden rounded-[10px] border">
			{showPrompt ? (
				<div className="flex min-w-0 items-start gap-2 border-b border-border/60 px-3 py-2">
					<span
						aria-hidden="true"
						className="shrink-0 select-none pt-px font-mono text-[11px] leading-relaxed text-muted-foreground/70"
					>
						&gt;_
					</span>
					<div className="flex min-w-0 flex-1 flex-wrap items-baseline gap-x-2 gap-y-0.5">
						<span className="min-w-0 break-words text-[12px] leading-relaxed text-foreground/90">
							{reason}
						</span>
						{binary ? (
							<span className="shrink-0 font-mono text-[10.5px] text-muted-foreground/55">
								{binary}
							</span>
						) : null}
					</div>
				</div>
			) : null}

			{command ? (
				<pre
					className={cn(
						"cursor-chat-explore-command overflow-x-auto px-3 py-2 font-mono text-[11px] leading-relaxed text-foreground/85",
						(detail?.output || detail?.terminalInput) && "border-b border-border/60",
					)}
				>
					{command}
				</pre>
			) : null}

			{detail?.terminalInput ? (
				<div className={cn("px-3 py-2", detail?.output && "border-b border-border/60")}>
					<TerminalInput
						text={detail.terminalInput}
						truncated={detail.terminalInputTruncated}
					/>
				</div>
			) : null}
			{detail?.output ? <CommandOutput activity={activity} embedded /> : null}
		</div>
	);
}

/**
 * What the agent typed into a running command's terminal.
 *
 * Shown apart from the output, and labelled as input, because it is the one thing in
 * the row the agent did rather than observed — usually `^C` on a command that was
 * never going to finish. The daemon keeps it out of `output` for the same reason it
 * is drawn separately here: the PTY echoes keystrokes, so merging them would print
 * the abort twice and leave no way to tell which was which.
 *
 * Control characters are spelled the way a terminal spells them. Stripping them
 * would leave an empty box where the interesting thing happened: an abort IS one
 * unprintable byte.
 */
function TerminalInput({ text, truncated }: { text: string; truncated?: boolean }) {
	const shown = useMemo(() => caretNotation(text), [text]);
	return (
		<div className="flex flex-col gap-1">
			<span className="flex items-center gap-1.5 text-[10px] uppercase tracking-[0.08em] text-muted-foreground/70">
				<Keyboard aria-hidden="true" className="size-3" />
				Agent typed
			</span>
			<pre className="overflow-x-auto rounded-md border border-dashed border-border-strong bg-background px-2.5 py-1.5 font-mono text-[10.5px] leading-relaxed text-accent">
				{shown}
			</pre>
			{truncated ? (
				<p className="text-[10px] text-muted-foreground/70">
					AO stopped recording keystrokes at its cap; more were sent.
				</p>
			) : null}
		</div>
	);
}

/**
 * A command's output, with an honest account of what it is.
 *
 * The pre scrolls to its own end while the command runs, because output that
 * arrives below the fold is output nobody sees. It only auto-scrolls while the
 * activity is `running` and only when the reader has not scrolled up: hijacking
 * the viewport of someone reading back through a build log is worse than making
 * them scroll down once.
 *
 * The text is what a terminal would have shown, not the bytes: output arrives with
 * its escape sequences intact — nothing in the stack strips them — so a colourized
 * test run rendered here verbatim is a wall of `[0m`, and a progress bar is a
 * hundred stacked copies of itself. See `lib/ansi.ts` for why this is a text pass
 * rather than a terminal.
 */
function CommandOutput({
	activity,
	embedded = false,
}: {
	activity: ConversationActivity;
	/** Inside the explore card: no second bordered surface. */
	embedded?: boolean;
}) {
	const pre = useRef<HTMLPreElement>(null);
	const detail = activity.detail;
	// Older ACP-backed conversations may contain the provider's structured
	// rawOutput even though AO's view model promises a string. New events are
	// normalized at the adapter boundary; this compatibility read keeps those
	// already-durable rows from taking down the entire session surface.
	const raw = commandOutputText(detail?.output as unknown);
	// Memoized per row: the timeline re-renders once a second while a turn runs, and
	// only the rows a reader has opened pay for this at all.
	const output = useMemo(() => stripAnsi(raw), [raw]);
	const streaming = activity.status === "running";

	useEffect(() => {
		const node = pre.current;
		if (!node || !streaming) return;
		// Within a line of the bottom counts as "following along". Anything further
		// up is a deliberate scroll and is left alone.
		const atBottom = node.scrollHeight - node.scrollTop - node.clientHeight < 24;
		if (atBottom) node.scrollTop = node.scrollHeight;
	}, [output, streaming]);

	return (
		<>
			<pre
				ref={pre}
				aria-live={streaming ? "polite" : undefined}
				className={cn(
					"max-h-64 overflow-auto font-mono leading-relaxed text-muted-foreground",
					embedded
						? "cursor-chat-explore-output px-3 py-2 text-[11px]"
						: "rounded-md border border-border bg-background px-2.5 py-2 text-[10.5px]",
				)}
			>
				{output}
			</pre>
			{detail?.outputTruncated ? (
				<p className="text-[10px] leading-relaxed text-warning">
					This command printed more than AO stores, so the output above stops early. Open a shell in
					the worktree to see the rest.
				</p>
			) : null}
		</>
	);
}

function commandOutputText(raw: unknown): string {
	if (typeof raw === "string") return raw;
	if (!raw || typeof raw !== "object") return "";

	const value = raw as Record<string, unknown>;
	for (const key of ["output", "text", "error", "metadata"]) {
		const text = commandOutputText(value[key]);
		if (text) return text;
	}

	try {
		return JSON.stringify(raw);
	} catch {
		return "";
	}
}

/**
 * Split a summary into a bold action and a muted target, which is how the row
 * scans: what happened, then what it happened to. A command becomes its binary
 * plus its arguments; anything without a natural split keeps its whole label.
 */
function splitSummary(activity: ConversationActivity): { label: string; path?: string } {
	if (activity.activityKind === "command") {
		const rawCommand = activity.detail?.command ?? activity.summary;
		const category = commandCategory(rawCommand);
		if (category === "read" || category === "search") {
			const count = exploredFileCount(rawCommand);
			return { label: count ? `Explored ${count} ${count === 1 ? "file" : "files"}` : "Explored files" };
		}
		return { label: category === "vcs" ? "Checked repository" : "Ran command" };
	}
	const files = fileChangeFiles(activity);
	if (activity.activityKind === "file_change" && files.length === 1) {
		// Basename sits in the chip next to the status verb; the full path is the hover.
		return {
			label: `${fileChangeVerb(files[0]!.status ?? "modified")} ${fileBasename(files[0]!.path)}`,
		};
	}
	return { label: activity.summary };
}

function ActivityState({
	activity,
	open,
	hasBody,
	showDisclosure = true,
}: {
	activity: ConversationActivity;
	open: boolean;
	hasBody: boolean;
	showDisclosure?: boolean;
}) {
	const { status, detail } = activity;
	const files = fileChangeFiles(activity);

	if (status === "running") {
		return (
			<Loader2
				aria-label="running"
				className="size-3 shrink-0 animate-spin self-center text-muted-foreground/60"
			/>
		);
	}
	if (files.length) {
		const additions = files.reduce((sum, file) => sum + file.additions, 0);
		const deletions = files.reduce((sum, file) => sum + file.deletions, 0);
		return (
			<span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground/70">
				<span className="text-success">+{additions}</span>{" "}
				<span className="text-destructive">&minus;{deletions}</span>
			</span>
		);
	}
	if (status === "failed") {
		return (
			<span
				className={cn(
					"shrink-0 font-mono text-[10px] tabular-nums",
					isNonzeroCommandExit(activity) ? "text-muted-foreground/70" : "text-destructive",
				)}
			>
				{detail?.exitCode !== undefined ? `exit ${detail.exitCode}` : "failed"}
			</span>
		);
	}
	if (status === "recovered") {
		return (
			<span className="shrink-0 font-mono text-[10px] text-muted-foreground/70">
				outcome unknown
			</span>
		);
	}
	if (status === "cancelled") {
		return (
			<span className="shrink-0 font-mono text-[10px] text-muted-foreground/70">
				stopped
			</span>
		);
	}
	// Everything else settled fine, which is the boring majority. A chevron on
	// hover is the whole affordance; a duration or timestamp on every row builds a
	// column of numbers nobody reads.
	if (hasBody && showDisclosure) {
		return (
			<ChevronRight
				aria-hidden="true"
				className={cn(
					"size-3 shrink-0 self-center text-muted-foreground/50 transition-all",
					open ? "rotate-90 opacity-100" : "opacity-0 group-hover/activity:opacity-100",
				)}
			/>
		);
	}
	return null;
}

/**
 * The files one edit touched, and what it did to them.
 *
 * Styled like the explore summary's nested lines — "Edited FAQ.tsx +1 −1" — so an
 * expanded edit reads the same as the turn-level changed-files list. Hovering the
 * basename shows the full location; a file that carries a patch can still open it.
 */
export function FileChangeList({ files }: { files: FileChangeFile[] }) {
	if (!files.length) return null;
	return (
		<ul className="flex flex-col">
			{files.map((file) => (
				<FileChangeRow key={`${file.oldPath ?? ""}→${file.path}`} file={file} />
			))}
		</ul>
	);
}

function FileChangeRow({ file }: { file: FileChangeFile }) {
	const [open, setOpen] = useState(false);
	const status = diffStatusMark[file.status ?? "modified"] ?? diffStatusMark.modified;
	const hasPatch = Boolean(file.patch);
	const accessibleName = `${status.label} ${file.path}`;

	const line = (
		<>
			<span className="sr-only">{status.label}</span>
			<span className="shrink-0 text-[11.5px] text-muted-foreground">
				{fileChangeVerb(file.status ?? "modified")}
			</span>
			<FileLocationLabel path={file.path} oldPath={file.oldPath} />
			<span className="shrink-0 font-mono text-[10px] tabular-nums text-success">
				+{file.additions}
			</span>
			<span className="shrink-0 font-mono text-[10px] tabular-nums text-destructive">
				&minus;{file.deletions}
			</span>
		</>
	);

	// A file with no patch is not a button: nothing opens, and a control that does
	// nothing when pressed is worse than plain text.
	if (!hasPatch) {
		return (
			<li className="flex items-center gap-1.5 py-0.5 pr-1" aria-label={accessibleName}>
				{line}
			</li>
		);
	}

	return (
		<li className="flex flex-col">
			<button
				type="button"
				onClick={() => setOpen((prev) => !prev)}
				aria-expanded={open}
				aria-label={accessibleName}
				className="flex items-center gap-1.5 rounded-sm py-0.5 pr-1 text-left"
			>
				{line}
				<ChevronRight
					aria-hidden="true"
					className={cn(
						"size-3 shrink-0 text-muted-foreground/40 transition-transform",
						open && "rotate-90",
					)}
				/>
			</button>
			{open ? <Patch patch={file.patch!} truncated={file.patchTruncated} /> : null}
		</li>
	);
}

/**
 * One file's unified diff.
 *
 * Highlighted by the same engine the prose fences use — `diff` is one of the shipped
 * grammars — rather than by a second highlighter that would double the payload and
 * eventually disagree with the first.
 *
 * An added file's "patch" is its whole contents with no hunk header, which the diff
 * grammar renders as plain text. That is the right outcome: there is no diff to
 * colour, only a new file to read.
 */
function Patch({ patch, truncated }: { patch: string; truncated?: boolean }) {
	return (
		// `chat-code` is what the token colours are scoped to, so a patch without it
		// tokenizes correctly and renders in one flat colour.
		<div className="chat-code mb-1 mt-0.5 overflow-hidden rounded-md border border-border bg-background">
			<pre className="max-h-72 overflow-auto px-2.5 py-2">
				<code className="font-mono text-[10.5px] leading-[1.55] text-foreground">
					<HighlightedCode code={patch} language="diff" />
				</code>
			</pre>
			{truncated ? (
				<p className="border-t border-border px-2.5 py-1.5 text-[10px] leading-relaxed text-warning">
					This patch is longer than AO stores, so it stops early. The whole change is in the
					worktree and in the turn&rsquo;s diff.
				</p>
			) : null}
		</div>
	);
}

/* -------------------------------------------------------------------------- */
/* reasoning                                                                   */
/* -------------------------------------------------------------------------- */

/**
 * The model's own account of what it is doing.
 *
 * Rendered as prose on a rail rather than as a collapsed row, because a reader only
 * ever sees this after deliberately turning reasoning on — hiding it again behind a
 * second click would make the toggle do nothing. It is markdown: the provider writes
 * bolded section headers into its summaries, and showing those as literal asterisks
 * was the visible half of this being unread.
 *
 * The text streams. Earlier builds read a field the payload does not have, so these
 * rows arrived blank even when the provider was sending; it now lands in `text`
 * while the model works and is replaced by the provider's settled summary when the
 * item completes, so one field is read either way.
 */
function ReasoningBlock({ activity }: { activity: ConversationActivity }) {
	const text = activity.detail?.text ?? activity.detail?.reason ?? "";
	if (!text) return null;
	const streaming = activity.status === "running";

	return (
		<div className="flex gap-2.5 border-l-2 border-border-strong py-0.5 pl-3">
			<Brain aria-hidden="true" className="mt-[3px] size-3.5 shrink-0 text-muted-foreground/70" />
			<div className="min-w-0 flex-1">
				<div className="flex items-center gap-2">
					<span className="text-[10px] uppercase tracking-[0.08em] text-muted-foreground/70">
						{streaming ? "Thinking" : "Thought"}
					</span>
					{streaming ? (
						<Loader2
							aria-label="still thinking"
							className="size-3 animate-spin text-muted-foreground/50"
						/>
					) : null}
				</div>
				<ChatMarkdown text={text} streaming={streaming} muted />
				{activity.detail?.textTruncated ? (
					<p className="mt-1 text-[10px] text-muted-foreground/70">
						This summary is longer than AO stores, so it stops early.
					</p>
				) : null}
			</div>
		</div>
	);
}

/* -------------------------------------------------------------------------- */
/* MCP tool calls                                                              */
/* -------------------------------------------------------------------------- */

/**
 * A call to a tool served by an MCP server.
 *
 * Deliberately not shaped like a command row. These used to render as though the
 * agent had run something in the worktree, which is a different and more alarming
 * claim than "it asked a tool server a question": nothing was executed here, and the
 * cwd it appeared to run in did not exist. So the row names the server and the tool,
 * and opens onto the arguments and the answer rather than onto terminal output.
 */
function McpToolRow({ activity }: { activity: ConversationActivity }) {
	const [open, setOpen] = useState(false);
	const detail = activity.detail;
	const tool = detail?.toolName ?? activity.summary;
	const server = detail?.server ?? detail?.namespace;
	const failed = activity.status === "failed" || detail?.success === false || Boolean(detail?.error);
	const hasBody = Boolean(
		detail?.arguments !== undefined ||
			detail?.result !== undefined ||
			detail?.error ||
			detail?.progress,
	);

	return (
		<div className="group/activity border-t border-border first:border-t-0">
			<button
				type="button"
				onClick={() => setOpen((prev) => !prev)}
				disabled={!hasBody}
				aria-expanded={hasBody ? open : undefined}
				className={cn(
					"flex min-h-[35px] w-full items-center gap-[9px] px-[11px] py-2 text-left text-[11px] transition-colors",
					hasBody && "hover:bg-interactive-hover",
					!hasBody && "cursor-default",
				)}
			>
				<Plug
					aria-hidden="true"
					className={cn(
						"w-[15px] shrink-0 text-center",
						failed ? "text-destructive" : "text-accent-dim",
					)}
					size={13}
				/>
				{/* The server is named first and in its own colour: which server answered is
				    the part a shell command row could never have said. */}
				{server ? (
					<span className="shrink-0 font-mono text-[10.5px] text-muted-foreground">
						{server}
						<span aria-hidden="true" className="px-0.5 text-muted-foreground/40">
							/
						</span>
					</span>
				) : null}
				<strong
					className={cn(
						"shrink-0 text-[10.5px] font-medium",
						failed ? "text-destructive" : "text-foreground",
					)}
				>
					{tool}
				</strong>
				<span className="min-w-0 flex-1 truncate text-[10.5px] text-muted-foreground/70">
					{detail?.progress ? lastLine(detail.progress) : "MCP tool"}
				</span>
				{activity.status === "running" ? (
					<Loader2
						aria-label="running"
						className="size-3 shrink-0 animate-spin text-muted-foreground/60"
					/>
				) : failed ? (
					<span className="shrink-0 text-[10px] text-destructive">failed</span>
				) : activity.status === "recovered" ? (
					<span className="shrink-0 text-[10px] text-muted-foreground/70">outcome unknown</span>
				) : activity.status === "cancelled" ? (
					<span className="shrink-0 text-[10px] text-muted-foreground/70">stopped</span>
				) : hasBody ? (
					<ChevronRight
						aria-hidden="true"
						className={cn(
							"size-3 shrink-0 text-muted-foreground/50 transition-all",
							open ? "rotate-90 opacity-100" : "opacity-0 group-hover/activity:opacity-100",
						)}
					/>
				) : null}
			</button>

			{open && hasBody ? (
				<div className="flex flex-col gap-2 px-[11px] pb-2.5">
					{detail?.error ? (
						<p className="rounded border border-destructive/30 bg-background px-2.5 py-1.5 text-[10.5px] leading-relaxed text-destructive">
							{detail.error}
						</p>
					) : null}
					{detail?.arguments !== undefined ? (
						<JsonPayload label="Arguments" value={detail.arguments} />
					) : null}
					{detail?.result !== undefined ? (
						<JsonPayload label="Result" value={detail.result} />
					) : null}
					{detail?.progress ? (
						<div className="flex flex-col gap-1">
							<span className="text-[10px] uppercase tracking-[0.08em] text-muted-foreground/70">
								Progress
							</span>
							<pre className="max-h-40 overflow-auto rounded-md border border-border bg-background px-2.5 py-1.5 font-mono text-[10.5px] leading-relaxed text-muted-foreground">
								{detail.progress}
							</pre>
						</div>
					) : null}
				</div>
			) : null}
		</div>
	);
}

/**
 * A tool's arguments or answer, as JSON.
 *
 * A payload the daemon could not store arrives as a stand-in object rather than as
 * the real thing, and printing that stand-in as if it were the tool's answer would
 * be a small lie with a confusing shape. It is recognized and said plainly instead.
 */
function JsonPayload({ label, value }: { label: string; value: unknown }) {
	const capped = truncationNote(value);
	const text = useMemo(() => (capped ? "" : formatJson(value)), [capped, value]);

	return (
		<div className="flex flex-col gap-1">
			<span className="text-[10px] uppercase tracking-[0.08em] text-muted-foreground/70">
				{label}
			</span>
			{capped ? (
				<p className="rounded-md border border-border bg-background px-2.5 py-1.5 text-[10.5px] leading-relaxed text-muted-foreground">
					{capped}
				</p>
			) : (
				<pre className="chat-code max-h-56 overflow-auto rounded-md border border-border bg-background px-2.5 py-1.5">
					<code className="font-mono text-[10.5px] leading-[1.55] text-foreground">
						<HighlightedCode code={text} language="json" />
					</code>
				</pre>
			)}
		</div>
	);
}

/** The daemon's stand-in for a payload past its cap, or nothing. */
function truncationNote(value: unknown): string | undefined {
	if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
	const record = value as { truncated?: unknown; bytes?: unknown; note?: unknown };
	if (record.truncated !== true) return undefined;
	const bytes = typeof record.bytes === "number" ? ` (${formatBytes(record.bytes)})` : "";
	return `This payload${bytes} was larger than AO stores, so it was not kept.`;
}

function formatBytes(bytes: number): string {
	if (bytes < 1024) return `${bytes} B`;
	if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
	return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function formatJson(value: unknown): string {
	if (typeof value === "string") return value;
	try {
		return JSON.stringify(value, null, 2) ?? String(value);
	} catch {
		return String(value);
	}
}

/** The most recent line of a progress stream, for the collapsed row. */
function lastLine(text: string): string {
	const lines = text.trimEnd().split("\n");
	return lines[lines.length - 1] ?? "";
}

/* -------------------------------------------------------------------------- */
/* auto review                                                                 */
/* -------------------------------------------------------------------------- */

const RISK_TONE: Record<string, string> = {
	low: "text-muted-foreground",
	medium: "text-warning",
	high: "text-destructive",
	critical: "text-destructive",
};

/**
 * A decision the provider made instead of asking.
 *
 * This is not an approval card and must not read like one: nobody was asked, and
 * there is nothing to answer. What the row owes the user is the fact that a decision
 * was taken on their behalf, what it allowed, and — reachable rather than shouted —
 * the reasoning that allowed it. Denials are called out more loudly than approvals,
 * because a denial is why something the user expected did not happen.
 */
function AutoReviewRow({ activity }: { activity: ConversationActivity }) {
	const [open, setOpen] = useState(false);
	const detail = activity.detail;
	const denied = (detail?.status ?? "").toLowerCase().includes("den");
	const Icon = denied ? ShieldX : ShieldCheck;
	const paths = reviewedPaths(activity);
	const hasBody = Boolean(
		detail?.rationale || detail?.command || detail?.cwd || detail?.host || paths.length,
	);

	return (
		<div className="group/activity border-t border-border first:border-t-0">
			<button
				type="button"
				onClick={() => setOpen((prev) => !prev)}
				disabled={!hasBody}
				aria-expanded={hasBody ? open : undefined}
				className={cn(
					"flex min-h-[35px] w-full items-center gap-[9px] px-[11px] py-2 text-left text-[11px] transition-colors",
					hasBody && "hover:bg-interactive-hover",
					!hasBody && "cursor-default",
				)}
			>
				<Icon
					aria-hidden="true"
					className={cn(
						"w-[15px] shrink-0 text-center",
						denied ? "text-destructive" : "text-muted-foreground/70",
					)}
					size={13}
				/>
				<strong className="shrink-0 font-medium text-foreground">
					{denied ? "Auto-declined" : "Auto-approved"}
				</strong>
				<span
					className="min-w-0 flex-1 truncate font-mono text-[10.5px] text-muted-foreground"
					title={activity.summary}
				>
					{shortenPaths(activity.summary)}
				</span>
				{detail?.riskLevel ? (
					<span
						className={cn(
							"shrink-0 text-[10px] uppercase tracking-[0.06em]",
							RISK_TONE[detail.riskLevel.toLowerCase()] ?? "text-muted-foreground",
						)}
						title={`Risk assessed as ${detail.riskLevel}`}
					>
						{detail.riskLevel}
					</span>
				) : null}
				{hasBody ? (
					<ChevronRight
						aria-hidden="true"
						className={cn(
							"size-3 shrink-0 text-muted-foreground/50 transition-all",
							open ? "rotate-90 opacity-100" : "opacity-0 group-hover/activity:opacity-100",
						)}
					/>
				) : null}
			</button>

			{open && hasBody ? (
				<div className="flex flex-col gap-2 px-[11px] pb-2.5">
					{/* Said in full rather than implied by the label: "auto-approved" alone
					    leaves it ambiguous whether the user set something up that did this. */}
					<p className="text-[11px] leading-relaxed text-muted-foreground">
						{denied
							? "The agent asked to do this and the provider declined on your behalf. You were not asked."
							: "The agent asked to do this and the provider allowed it on your behalf. You were not asked."}
					</p>
					{detail?.rationale ? (
						<p className="rounded border border-border bg-background px-2.5 py-1.5 text-[11px] leading-relaxed text-foreground">
							{detail.rationale}
						</p>
					) : null}
					<dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 font-mono text-[10.5px] leading-relaxed">
						{detail?.command ? (
							<>
								<dt className="text-muted-foreground/70">command</dt>
								<dd className="min-w-0 break-all text-foreground">{detail.command}</dd>
							</>
						) : null}
						{detail?.cwd ? (
							<>
								<dt className="text-muted-foreground/70">cwd</dt>
								<dd className="min-w-0 break-all text-muted-foreground">
									{shortenPaths(detail.cwd)}
								</dd>
							</>
						) : null}
						{detail?.host ? (
							<>
								<dt className="text-muted-foreground/70">host</dt>
								<dd className="min-w-0 break-all text-muted-foreground">{detail.host}</dd>
							</>
						) : null}
						{paths.length ? (
							<>
								<dt className="text-muted-foreground/70">files</dt>
								<dd className="min-w-0 break-all text-muted-foreground">
									{paths.map((path) => shortenPaths(path)).join(", ")}
								</dd>
							</>
						) : null}
						{detail?.decisionSource ? (
							<>
								<dt className="text-muted-foreground/70">decided by</dt>
								<dd className="text-muted-foreground">
									{/* A model making the call is a materially different thing to be
									    told than a policy rule matching, so the provider's own word
									    for it is carried rather than flattened to "automatically". */}
									{detail.decisionSource}
									{detail.durationMs !== undefined && detail.durationMs > 0
										? ` · ${formatDuration(detail.durationMs)}`
										: ""}
								</dd>
							</>
						) : null}
					</dl>
				</div>
			) : null}
		</div>
	);
}

/* -------------------------------------------------------------------------- */
/* system events                                                               */
/* -------------------------------------------------------------------------- */

/**
 * Where the provider swapped the model out.
 *
 * On the timeline as well as in the composer because it happened at a point in the
 * conversation: everything after this row was answered by a different model than
 * everything before it, and only a timeline entry can say where the line is.
 */
function RerouteRow({ activity }: { activity: ConversationActivity }) {
	const detail = activity.detail;
	return (
		<div className="flex items-start gap-2.5 rounded-md border border-border bg-surface/60 px-3 py-2">
			<Shuffle aria-hidden="true" className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
			<div className="flex min-w-0 flex-col gap-0.5">
				<span className="text-[11px] text-foreground">
					Answered by{" "}
					<strong className="font-medium">{detail?.toModel ?? "another model"}</strong>
					{detail?.fromModel ? (
						<>
							{" "}
							instead of <span className="text-muted-foreground">{detail.fromModel}</span>
						</>
					) : null}
				</span>
				{detail?.reason ? (
					<span className="text-[10.5px] leading-snug text-muted-foreground">{detail.reason}</span>
				) : null}
			</div>
		</div>
	);
}

/**
 * A provider error item — reconnect storms, credit exhaustion, and similar.
 *
 * Codex (and peers) often put the whole JSON envelope in `message` / `summary`.
 * Rendering that as a generic activity label made one long unbreakable line that
 * widened the chat column under the sidebars, and five reconnect attempts painted
 * five walls of red JSON. This row unwraps the human parts and always wraps inside
 * the column.
 *
 * Not a live region: the enclosing timeline is already a `role="log"`, and the
 * controller banner announces a terminal failure. Marking every historical
 * reconnect row as `role="alert"` would interrupt a screen reader once per attempt.
 */
function ErrorActivityRow({ activity }: { activity: ConversationActivity }) {
	const { headline } = providerErrorCopy(activity);
	return (
		<div className="flex min-w-0 max-w-full items-baseline overflow-hidden py-0.5 text-[11.5px] leading-snug text-muted-foreground">
			<span className="wrap-anywhere min-w-0">{headline}</span>
		</div>
	);
}

/**
 * Prefer the provider's short status line and `additionalDetails` over a raw JSON
 * dump. Falls back to the stored text when it is already plain language.
 */
export function providerErrorCopy(activity: ConversationActivity): {
	headline: string;
	detail?: string;
} {
	const candidates = [activity.detail?.message, activity.summary, activity.detail?.error];
	for (const candidate of candidates) {
		const raw = String(candidate ?? "").trim();
		if (!raw) continue;
		const unwrapped = unwrapProviderErrorJson(raw);
		if (unwrapped) return unwrapped;
	}

	const headline = String(activity.detail?.message ?? activity.summary ?? "").trim();
	const extra = String(activity.detail?.error ?? "").trim();
	if (!headline) return { headline: extra || "Provider error" };
	if (extra && extra !== headline) return { headline, detail: extra };
	return { headline };
}

function unwrapProviderErrorJson(raw: string): { headline: string; detail?: string } | undefined {
	const parsed = parseJsonObjectSuffix(raw);
	const err = parsed
		? parsed.error && typeof parsed.error === "object" && !Array.isArray(parsed.error)
			? (parsed.error as Record<string, unknown>)
			: parsed
		: undefined;
	const message =
		typeof err?.message === "string"
			? err.message.trim()
			: readJsonStringField(raw, "message");
	const additional =
		typeof err?.additionalDetails === "string"
			? err.additionalDetails.trim()
			: readJsonStringField(raw, "additionalDetails");
	if (!message && !additional) return undefined;
	if (message && additional && additional !== message) {
		return { headline: message, detail: additional };
	}
	return { headline: message || additional };
}

/** Read a complete string field from an envelope whose trailing JSON was truncated. */
function readJsonStringField(raw: string, field: "message" | "additionalDetails"): string {
	const match = new RegExp(`"${field}"\\s*:\\s*("(?:\\\\.|[^"\\\\])*")`).exec(raw);
	if (!match?.[1]) return "";
	try {
		const value = JSON.parse(match[1]) as unknown;
		return typeof value === "string" ? value.trim() : "";
	} catch {
		return "";
	}
}

/** AO used to persist `provider error: {…}`; parse the JSON object even when prefixed. */
function parseJsonObjectSuffix(raw: string): Record<string, unknown> | undefined {
	const start = raw.indexOf("{");
	if (start < 0) return undefined;
	const slice = raw.slice(start).trim();
	const asObject = (value: unknown): Record<string, unknown> | undefined => {
		if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
		return value as Record<string, unknown>;
	};
	try {
		return asObject(JSON.parse(slice));
	} catch {
		const end = slice.lastIndexOf("}");
		if (end <= 0) return undefined;
		try {
			return asObject(JSON.parse(slice.slice(0, end + 1)));
		} catch {
			return undefined;
		}
	}
}

/**
 * Where the provider stopped accepting work until someone signs in.
 *
 * The banner above the timeline is what the user acts on; this row is the record of
 * when it happened, so a turn that failed for this reason is not left looking like an
 * ordinary failure.
 */
function ReauthRow({ activity }: { activity: ConversationActivity }) {
	return (
		<div className="flex items-start gap-2.5 rounded-md border border-destructive/40 bg-surface px-3 py-2">
			<KeyRound aria-hidden="true" className="mt-0.5 size-3.5 shrink-0 text-destructive" />
			<div className="flex min-w-0 flex-col gap-0.5">
				<strong className="text-[11px] font-medium text-destructive">
					The provider asked you to sign in again
				</strong>
				{activity.detail?.reason ? (
					<span className="text-[10.5px] leading-snug text-muted-foreground">
						{activity.detail.reason}
					</span>
				) : null}
			</div>
		</div>
	);
}

/**
 * Guidance the user delivered into a turn that was already running.
 *
 * Drawn as the user's own words, because they are — the daemon records it as an
 * activity only because a message would have opened a second turn. It carries a
 * marker rather than looking identical to an ordinary message: a reader scrolling
 * back needs to see that this arrived mid-turn, since it explains why the agent
 * changed course without a new exchange starting.
 */
export function SteerMessage({ activity }: { activity: ConversationActivity }) {
	const text = activity.detail?.text ?? activity.summary;
	return (
		<div className="flex flex-col items-end gap-1">
			<div className="w-fit max-w-[min(78%,560px)] break-words whitespace-pre-wrap rounded-[10px] border border-accent-dim bg-raised px-3 py-2.5 text-sm leading-[1.55] text-foreground">
				{text ? <p>{text}</p> : null}
				<ConversationContentItems
					content={activity.detail?.content ?? []}
					ariaLabel="Steered attachments"
					imageLabel="Image"
					imageAlt={(position) => `Steered attachment ${position}`}
					className={cn(text && "mt-2")}
				/>
			</div>
			<span className="flex items-center gap-1 text-[11px] text-muted-foreground">
				<CornerDownRight aria-hidden="true" className="size-3" />
				Steered into the running turn
			</span>
		</div>
	);
}

/* -------------------------------------------------------------------------- */
/* approval                                                                    */
/* -------------------------------------------------------------------------- */

/**
 * A decision the agent is blocked on.
 *
 * Decisions come from `activity.decisions` — the provider's own list — never from
 * a fixed set. The UI still presents common permission choices with AO/Codex copy
 * so provider-flavored labels do not leak into the chat surface.
 */
export function ApprovalCard({
	activity,
	onDecide,
	busy,
	embedded,
}: {
	activity: ConversationActivity;
	onDecide?: (requestId: string, decisionId: string) => void;
	busy?: boolean;
	/** Render inside the shared chat composer instead of drawing another card shell. */
	embedded?: boolean;
}) {
	const resolved = activity.status !== "pending";
	const decisions = orderedApprovalDecisions(activity.decisions ?? []);
	const detail = activity.detail;
	const command = detail?.command ?? activity.summary;
	const subjectKind = approvalSubjectKind(activity);
	const rejectOnceDecision = decisions.find(
		(decision) => approvalDecisionKind(decision) === "reject_once",
	);
	const denyDecision =
		rejectOnceDecision ??
		decisions.find((decision) => approvalDecisionKind(decision) === "reject_always");
	const allowOnceDecision = decisions.find(
		(decision) => approvalDecisionKind(decision) === "allow_once",
	);
	const alternateAllowDecisions = allowOnceDecision
		? decisions.filter((decision) => approvalDecisionKind(decision) === "allow_always")
		: [];
	const otherDecisions = decisions.filter(
		(decision) =>
			decision !== denyDecision &&
			decision !== allowOnceDecision &&
			!alternateAllowDecisions.includes(decision),
	);
	const requestId = activity.requestId ?? "";
	const cardRef = useRef<HTMLDivElement>(null);

	useEffect(() => {
		if (!embedded || resolved || busy || !requestId) return;
		const card = cardRef.current;
		if (card && (document.activeElement === document.body || !document.activeElement)) card.focus();
	}, [busy, embedded, requestId, resolved]);

	if (resolved) {
		return <ResolvedApprovalRow activity={activity} command={command} />;
	}

	return (
		<div
			ref={cardRef}
			role="group"
			tabIndex={-1}
			aria-label={`Approval request ${requestId}`.trim()}
			className={cn(
				"cursor-chat-activity-panel",
				embedded ? "px-1 py-0.5" : "rounded-lg border border-border px-3 py-2.5",
			)}
			onKeyDown={(event) => {
				if (busy || event.defaultPrevented || event.altKey || event.ctrlKey || event.metaKey) return;
				if (event.key === "Escape" && rejectOnceDecision) {
					event.preventDefault();
					onDecide?.(requestId, rejectOnceDecision.id);
					return;
				}
				const target = event.target;
				const interactive =
					target instanceof HTMLElement &&
					Boolean(target.closest("button, a, input, textarea, select, [contenteditable='true']"));
				if (event.key === "Enter" && !event.shiftKey && !interactive && allowOnceDecision) {
					event.preventDefault();
					onDecide?.(requestId, allowOnceDecision.id);
				}
			}}
		>
			<div className="flex flex-col">
				<p className="whitespace-pre-wrap text-[13.5px] leading-[1.4] text-foreground/90">
					{detail?.reason ?? approvalPrompt(subjectKind)}
				</p>

				<pre className="mt-2 max-h-32 overflow-auto whitespace-pre-wrap break-words rounded-md border border-border/70 bg-background/45 px-2.5 py-1.5 font-mono text-[12px] leading-[1.45] text-muted-foreground">
					{detail?.rawCommand ?? command}
				</pre>

				<div className="mt-2 flex flex-wrap justify-end gap-1.5">
					{denyDecision ? (
						<button
							type="button"
							className="inline-flex h-7 items-center gap-1.5 rounded-full border border-border-strong bg-background/20 px-2.5 text-[12.5px] text-foreground/90 transition-colors hover:bg-interactive-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 disabled:pointer-events-none disabled:opacity-50"
							disabled={busy}
							onClick={() => onDecide?.(requestId, denyDecision.id)}
						>
							{approvalDecisionLabel(denyDecision, subjectKind)}
							{denyDecision === rejectOnceDecision ? (
								<kbd className="rounded-full bg-foreground/10 px-1.5 py-0.5 font-sans text-[10.5px] leading-none text-muted-foreground">
									Esc
								</kbd>
							) : null}
						</button>
					) : null}

					{allowOnceDecision ? (
						<div className="flex h-7 overflow-hidden rounded-full bg-logo-accent text-logo-accent-foreground shadow-sm">
							<button
								type="button"
								className="inline-flex items-center gap-1.5 px-2.5 text-[12.5px] transition-colors hover:bg-logo-accent-bright focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50"
								disabled={busy}
								onClick={() => onDecide?.(requestId, allowOnceDecision.id)}
							>
								Allow once
								<kbd className="rounded-full bg-logo-accent-foreground/15 p-0.5" aria-label="Press Return">
									<CornerDownLeft aria-hidden="true" className="size-3" />
								</kbd>
							</button>
							{alternateAllowDecisions.length > 0 ? (
								<DropdownMenu>
									<DropdownMenuTrigger asChild>
										<button
											type="button"
											aria-label="More approval options"
											className="flex w-7 items-center justify-center border-l border-logo-accent-foreground/20 transition-colors hover:bg-logo-accent-bright focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50"
											disabled={busy}
										>
											<ChevronDown aria-hidden="true" className="size-3.5" />
										</button>
									</DropdownMenuTrigger>
									<DropdownMenuContent
										align="end"
										side="bottom"
										className="min-w-52"
										data-approval-menu=""
										onEscapeKeyDown={(event) => event.stopPropagation()}
									>
										{alternateAllowDecisions.map((decision) => (
											<DropdownMenuItem
												key={decision.id}
												disabled={busy}
												onSelect={() => onDecide?.(activity.requestId ?? "", decision.id)}
											>
												{approvalDecisionLabel(decision, subjectKind)}
											</DropdownMenuItem>
										))}
									</DropdownMenuContent>
								</DropdownMenu>
							) : null}
						</div>
					) : null}
					{otherDecisions.map((decision) => (
						<button
							key={decision.id}
							type="button"
							className="inline-flex h-7 items-center rounded-full border border-border-strong bg-background/20 px-2.5 text-[12.5px] text-foreground/90 transition-colors hover:bg-interactive-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40 disabled:pointer-events-none disabled:opacity-50"
							disabled={busy}
							onClick={() => onDecide?.(requestId, decision.id)}
						>
							{approvalDecisionLabel(decision, subjectKind)}
						</button>
					))}
					{decisions.length === 0 ? (
						<p className="text-[11px] text-warning">
							The agent offered no decisions AO can present. Open diagnostics.
						</p>
					) : null}
				</div>
			</div>
		</div>
	);
}

function approvalSubjectKind(activity: ConversationActivity): ActivityKind | undefined {
	if (activity.detail?.subjectKind) return activity.detail.subjectKind;
	if (activity.detail?.method === "item/fileChange/requestApproval") return "file_change";
	if (activity.detail?.method === "item/commandExecution/requestApproval") return "command";
	return undefined;
}

function approvalPrompt(subjectKind?: ActivityKind): string {
	return subjectKind === "file_change"
		? "Do you want to allow these file changes?"
		: "Do you want to run this command?";
}

function approvalDecisionKind(decision: DecisionOption): DecisionOption["kind"] | "unknown" {
	return decision.kind ?? "unknown";
}

function approvalDecisionRank(decision: DecisionOption): number {
	switch (approvalDecisionKind(decision)) {
		case "reject_once":
			return 10;
		case "reject_always":
			return 15;
		case "allow_once":
			return 20;
		case "allow_always":
			return 30;
		default:
			return 40;
	}
}

function approvalDecisionLabel(decision: DecisionOption, subjectKind?: ActivityKind): string {
	switch (approvalDecisionKind(decision)) {
		case "allow_always":
			return subjectKind === "file_change"
				? "Always allow these file changes"
				: "Always allow this command";
		case "allow_once":
			return "Allow once";
		case "reject_once":
			return "Deny";
		case "reject_always":
			return "Always deny";
		default:
			return decision.label;
	}
}

function orderedApprovalDecisions(decisions: DecisionOption[]): DecisionOption[] {
	return decisions
		.map((decision, index) => ({ decision, index }))
		.sort((left, right) => {
			const byRank = approvalDecisionRank(left.decision) - approvalDecisionRank(right.decision);
			return byRank || left.index - right.index;
		})
		.map(({ decision }) => decision);
}

function ResolvedApprovalRow({
	activity,
	command,
}: {
	activity: ConversationActivity;
	command: string;
}) {
	const detail = activity.detail;
	const decision = typeof detail?.decision === "string" ? detail.decision : undefined;
	const decisionKind = activity.decisions?.find((option) => option.id === decision)?.kind;
	const outcome = resolvedApprovalOutcome(activity.status, decision, detail?.resolvedBy, decisionKind);
	const title = detail?.cwd ? `${command}\n${detail.cwd}` : command;

	return (
		<div className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] items-center gap-3 rounded-md border border-border/80 bg-surface/45 px-2.5 py-1.5 text-[11.5px] text-muted-foreground">
			<strong className="shrink-0 font-medium text-foreground">{outcome.label}</strong>
			<span className="min-w-0 truncate text-right font-mono" title={title}>
				{command}
			</span>
		</div>
	);
}

function resolvedApprovalOutcome(
	status: ConversationActivity["status"],
	decision?: string,
	resolvedBy?: string,
	decisionKind?: DecisionOption["kind"],
): { label: string; success: boolean } {
	const value = decision?.toLowerCase() ?? "";
	if (status === "failed") return { label: "Approval expired", success: false };
	if (
		status === "cancelled" ||
		decisionKind === "reject_once" ||
		decisionKind === "reject_always" ||
		/(deny|decline|reject|cancel)/.test(value)
	) {
		return { label: "Cancelled", success: false };
	}
	if (decisionKind === "allow_always" || /(remember|always|amendment|policy)/.test(value)) {
		return { label: "Approved and remembered", success: true };
	}
	if (decisionKind === "allow_once" || /(allow|approve|accept)/.test(value)) {
		return { label: "Approved", success: true };
	}
	if (resolvedBy === "provider") return { label: "Resolved elsewhere", success: false };
	return { label: "Resolved", success: false };
}

/* -------------------------------------------------------------------------- */
/* turn diff                                                                   */
/* -------------------------------------------------------------------------- */

/** Single letter per change kind, which is how a diff list is normally read. */
const diffStatusMark: Record<DiffStatus, { mark: string; tone: string; label: string }> = {
	added: { mark: "A", tone: "text-success", label: "added" },
	modified: { mark: "M", tone: "text-accent", label: "modified" },
	deleted: { mark: "D", tone: "text-destructive", label: "deleted" },
	renamed: { mark: "R", tone: "text-muted-foreground", label: "renamed" },
};

/** Match the daemon's file_change summary verbs (Created/Deleted/Renamed/Edited). */
function fileChangeVerb(status: DiffStatus): string {
	switch (status) {
		case "added":
			return "Created";
		case "deleted":
			return "Deleted";
		case "renamed":
			return "Renamed";
		default:
			return "Edited";
	}
}

/**
 * What a turn changed on disk.
 *
 * A bordered summary card at the end of the turn (kept near rollback), not the
 * compact explore line used for mid-turn activity. Always shows the changed
 * files; Review opens the Files rail, and clicking a row focuses that path.
 *
 * Rendered only when the daemon reported a diff. An agent that cannot report one
 * gets no empty panel implying it changed nothing.
 */
export function TurnChangedFiles({
	diff,
	live,
	onReview,
	onOpenFile,
	items,
}: {
	diff: TurnDiff;
	live?: boolean;
	/** Opens the session Files inspector for the full workspace diff. */
	onReview?: () => void;
	/** Opens the Files inspector focused on this path. */
	onOpenFile?: (path: string) => void;
	/**
	 * Timeline items from the same turn. Turn diffs often carry repo-relative
	 * basenames (`random_words.txt`); file_change rows and command cwds often
	 * carry the absolute worktree path the Edited tooltip already shows.
	 */
	items?: ConversationItem[];
}) {
	const [expanded, setExpanded] = useState(false);
	const pathHints = useMemo(() => turnPathHints(items), [items]);
	if (diff.files.length === 0) return null;

	const previewLimit = 4;
	const hidden = Math.max(0, diff.files.length - previewLimit);
	const visible = expanded ? diff.files : diff.files.slice(0, previewLimit);

	return (
		<div className="overflow-hidden rounded-lg bg-surface">
			<div className="flex items-center gap-2 px-3 py-2">
				<span className="shrink-0 text-[11px] text-muted-foreground">
					{diff.files.length === 1 ? "1 File Changed" : `${diff.files.length} Files Changed`}
				</span>
				{live ? (
					<Loader2
						aria-label="still changing"
						className="size-3 shrink-0 animate-spin text-muted-foreground/60"
					/>
				) : null}
				<span className="flex-1" />
				{onReview ? (
					<button
						type="button"
						onClick={onReview}
						className="shrink-0 text-[11px] text-muted-foreground transition-colors hover:text-foreground"
					>
						Review
					</button>
				) : null}
			</div>

			<ul className="flex flex-col px-1.5 pb-1.5">
				{visible.map((file) => {
					const status = diffStatusMark[file.status] ?? diffStatusMark.modified;
					const rowClass =
						"flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left transition-colors hover:bg-interactive-hover";
					const tooltipPath = resolveTurnFilePath(file.path, pathHints);
					const tooltipOldPath = file.oldPath
						? resolveTurnFilePath(file.oldPath, pathHints)
						: undefined;

					const body = (
						<>
							<span className="sr-only">{status.label}</span>
							<FileIcon aria-hidden="true" className="size-3.5 shrink-0 text-muted-foreground" />
							{/* Same path tooltip as mid-turn Edited rows — not a native
							    ellipsis title of the basename. */}
							<FileLocationLabel
								path={file.path}
								oldPath={file.oldPath}
								locationPath={tooltipPath}
								locationOldPath={tooltipOldPath}
								className="min-w-0 flex-1 truncate text-[12px] text-foreground/80"
							/>
							{file.additions > 0 ? (
								<span className="shrink-0 font-mono text-[11px] tabular-nums text-success">
									+{file.additions}
								</span>
							) : null}
							{file.deletions > 0 ? (
								<span className="shrink-0 font-mono text-[11px] tabular-nums text-destructive">
									&minus;{file.deletions}
								</span>
							) : null}
							{file.additions === 0 && file.deletions === 0 ? (
								<span className="shrink-0 font-mono text-[11px] tabular-nums text-muted-foreground/50">
									0
								</span>
							) : null}
						</>
					);

					return (
						<li key={`${file.status}-${file.oldPath ?? ""}-${file.path}`}>
							{onOpenFile ? (
								<button
									type="button"
									onClick={() => onOpenFile(file.path)}
									aria-label={`Open ${file.path} in Files`}
									className={rowClass}
								>
									{body}
								</button>
							) : (
								<div className={rowClass}>{body}</div>
							)}
						</li>
					);
				})}
			</ul>

			{hidden > 0 ? (
				<button
					type="button"
					onClick={() => setExpanded((prev) => !prev)}
					aria-expanded={expanded}
					className="flex w-full items-center gap-1.5 px-3 pb-2 text-left text-[11px] text-muted-foreground transition-colors hover:text-foreground"
				>
					{expanded ? "Show less" : `Show ${hidden} more`}
				</button>
			) : null}

			{diff.truncated ? (
				<p className="px-3 pb-2 text-[10px] leading-relaxed text-warning">
					This turn changed more files than AO lists here.
					{onReview ? " Use Review for the whole change." : " Open the Files tab for the whole change."}
				</p>
			) : null}
		</div>
	);
}

/**
 * Basename only — color distinguishes it from "Edited", no hover fill. Hovering
 * shows the home-shortened worktree path in a monospace tooltip.
 */
function FileLocationLabel({
	path,
	oldPath,
	locationPath,
	locationOldPath,
	className,
}: {
	path: string;
	oldPath?: string;
	/** Absolute/worktree path for the tooltip when `path` is only a basename. */
	locationPath?: string;
	locationOldPath?: string;
	className?: string;
}) {
	const location = fileLocationLabel(locationPath ?? path, locationOldPath ?? oldPath);

	return (
		<TooltipProvider delayDuration={200}>
			<Tooltip>
				<TooltipTrigger asChild>
					{/* `title=""` blocks Chromium's native ellipsis tooltip so only the
					    path tooltip below appears — otherwise hover shows the basename. */}
					<span
						className={cn(
							"min-w-0 truncate text-[11.5px] text-foreground/65 outline-none",
							className,
						)}
						title=""
					>
						{fileBasename(path)}
					</span>
				</TooltipTrigger>
				<TooltipContent
					side="top"
					className="max-w-[min(28rem,90vw)] border-border bg-popover px-2.5 py-1.5 font-mono text-[11px] font-normal text-muted-foreground shadow-none"
				>
					{location}
				</TooltipContent>
			</Tooltip>
		</TooltipProvider>
	);
}

function fileLocationLabel(path: string, oldPath?: string): string {
	return oldPath ? `${shortenPaths(oldPath)} → ${shortenPaths(path)}` : shortenPaths(path);
}

/**
 * Absolute paths and a worktree cwd gathered from the same turn's activities, so a
 * turn-diff basename can be shown like the Edited tooltip.
 */
type TurnPathHints = {
	byBase: Map<string, string | undefined>;
	cwd?: string;
};

function rememberTurnPathHint(byBase: Map<string, string | undefined>, absolutePath: string) {
	const base = fileBasename(absolutePath);
	if (!byBase.has(base)) {
		byBase.set(base, absolutePath);
		return;
	}
	if (byBase.get(base) !== absolutePath) byBase.set(base, undefined);
}

function turnPathHints(items: ConversationItem[] | undefined): TurnPathHints {
	const byBase = new Map<string, string | undefined>();
	let cwd: string | undefined;
	if (!items?.length) return { byBase, cwd };

	for (const item of items) {
		if (item.kind !== "activity") continue;
		if (!cwd && item.detail?.cwd) cwd = item.detail.cwd;
		if (item.activityKind !== "file_change") continue;
		for (const file of fileChangeFiles(item)) {
			if (looksAbsolutePath(file.path)) rememberTurnPathHint(byBase, file.path);
			if (file.oldPath && looksAbsolutePath(file.oldPath)) rememberTurnPathHint(byBase, file.oldPath);
		}
	}
	return { byBase, cwd };
}

function looksAbsolutePath(path: string): boolean {
	return path.startsWith("/") || path.startsWith("~") || /^[A-Za-z]:[\\/]/.test(path);
}

/** Prefer an absolute path from the turn; otherwise join the worktree cwd. */
function resolveTurnFilePath(path: string, hints: TurnPathHints): string {
	if (looksAbsolutePath(path)) return path;
	const fromBasename = hints.byBase.get(fileBasename(path));
	if (fromBasename) return fromBasename;
	if (hints.cwd) {
		const rel = path.replace(/^\.\//, "");
		return `${hints.cwd.replace(/\/$/, "")}/${rel}`;
	}
	return path;
}

/** Basename only — the row is too narrow for a full path; the tooltip carries that. */
function fileBasename(path: string): string {
	const slash = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
	return slash >= 0 ? path.slice(slash + 1) : path;
}

/* -------------------------------------------------------------------------- */
/* turn boundary                                                               */
/* -------------------------------------------------------------------------- */

/** Turn wall-clock duration; lives on the action row next to rollback, not the Done divider. */
export function TurnDuration({ durationMs }: { durationMs: number }) {
	if (durationMs <= 0) return null;
	return (
		<span className="shrink-0 px-1 font-sans text-[12px] leading-none tabular-nums text-muted-foreground">
			{formatDuration(durationMs)}
		</span>
	);
}

export { User };
