/**
 * Timeline entries for the Chat surface.
 *
 * One component per durable item shape, keyed by sequence so a streaming rewrite
 * updates a message in place instead of remounting it. Every component is a pure
 * function of a domain item: no lifecycle decisions, no capability checks, no
 * re-sorting. Those belong to the daemon.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import {
	AlertTriangle,
	Archive,
	Brain,
	ChevronRight,
	CircleAlert,
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
	commandCategory,
	exploredFileCount,
} from "./activity-command";
import { Button } from "../ui/button";
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
	type TurnDiff,
} from "../../types/conversation";

const timeFormatter = new Intl.DateTimeFormat(undefined, {
	hour: "numeric",
	minute: "2-digit",
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
	if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
	return `${Math.round(ms / 60_000)}m`;
}

function formatTime(iso: string): string {
	const parsed = new Date(iso);
	return Number.isNaN(parsed.getTime()) ? "" : timeFormatter.format(parsed);
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
	onEdit,
	editing = false,
	editText,
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
	onEdit?: (turnId: string, text: string) => Promise<unknown> | void;
	editing?: boolean;
	editText?: string;
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
						"cursor-chat-human-message w-fit max-w-[min(78%,560px)] rounded-[10px] border px-3 py-2.5 text-sm leading-[1.55]",
						queued
							? "border-dashed border-border-strong bg-transparent text-muted-foreground"
							: "border-border bg-raised text-foreground",
					)}
				>
					{body ? <p className="whitespace-pre-wrap text-pretty">{body}</p> : null}
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
				<div className="flex h-[18px] items-center gap-0.5">
					<div className="flex items-center opacity-0 transition-opacity duration-150 focus-within:opacity-100 group-hover/message:opacity-100">
						<CopyButton text={message.text} label="Copy user message" compact className="-mr-1" />
						{onEdit && onEditStart && message.turnId ? (
							<button
								type="button"
								onClick={onEditStart}
								aria-label="Edit user message"
								title="Edit user message"
								className="flex items-center gap-1 rounded px-1.5 py-0.5 text-[10.5px] text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground"
							>
								<Pencil aria-hidden="true" className="size-3" />
							</button>
						) : null}
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

/** The agent's prose. A trailing caret marks text still arriving. */
export function AssistantMessage({
	message,
	showCopy = false,
	showStreamingIndicator = message.streaming,
}: {
	message: ConversationMessage;
	/** Only the final answer of a finished turn owns the turn's copy action. */
	showCopy?: boolean;
	/** Only the newest item can still be visibly writing; older streaming fragments
	 * are waiting on a tool rather than missing content. */
	showStreamingIndicator?: boolean;
}) {
	const visiblyStreaming = message.streaming && showStreamingIndicator;
	const hasText = message.text.trim().length > 0;
	return (
		<div className={cn("group/message relative", visiblyStreaming && hasText && "chat-assistant-streaming")}>
			<ChatMarkdown text={message.text} streaming={message.streaming} />
			{visiblyStreaming ? (
				hasText ? (
					<span aria-label="still writing" className="sr-only" />
				) : (
					<span
						role="status"
						aria-label="still writing"
						className="inline-flex items-center gap-1.5 text-[11px] text-muted-foreground"
					>
						<Loader2 aria-hidden="true" className="size-3 animate-spin" />
						Writing…
					</span>
				)
			) : showCopy ? (
				// One action for the completed answer, not one after every prose fragment
				// the provider emitted while working.
				<div className="flex h-[18px] items-center opacity-0 transition-opacity duration-150 focus-within:opacity-100 group-hover/message:opacity-100">
					{/* The stored markdown, not a re-serialization of what was rendered:
					    pasting it into an editor has to give back what the agent wrote. */}
					<CopyButton
						text={message.text}
						label="Copy message as markdown"
						compact
						className="-ml-1.5"
					/>
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
	const hasBody = Boolean(
		detail?.output || detail?.reason || detail?.text || detail?.terminalInput || files.length,
	);
	const { label, path } = splitSummary(activity);
	const compactCommand = activity.activityKind === "command";

	// Live output is only live if it is on screen, so a command that is still
	// running and already printing opens itself.
	const streamingOutput = activity.status === "running" && Boolean(detail?.output);
	const open = override ?? streamingOutput;

	return (
		<div
			className={cn(
				"min-w-0 max-w-full",
				compactCommand ? "flex flex-col" : "group/activity border-t border-border first:border-t-0",
			)}
		>
			<button
				type="button"
				onClick={() => setOverride(!open)}
				disabled={!hasBody}
				aria-expanded={hasBody ? open : undefined}
				className={cn(
					compactCommand
						? ACTIVITY_SUMMARY_BUTTON_CLASS
						: "flex min-h-[35px] w-full min-w-0 items-center gap-[9px] px-[11px] py-2 text-left text-[11px] transition-colors",
					hasBody && !compactCommand && "hover:bg-interactive-hover",
					!hasBody && "cursor-default",
				)}
			>
				{compactCommand ? null : (
					<Icon
						aria-hidden="true"
						className={cn(
							"w-[15px] shrink-0 text-center",
							activity.status === "failed" ? "text-destructive" : "text-muted-foreground/70",
						)}
						size={13}
					/>
				)}
				<strong
					className={cn(
						compactCommand
							? "shrink-0 text-[11.5px] font-normal text-muted-foreground"
							: "min-w-0 truncate font-medium",
						!compactCommand &&
							(activity.status === "failed" ? "text-destructive" : "text-foreground"),
					)}
					title={compactCommand ? undefined : label}
				>
					{label}
				</strong>
				{path ? (
					<span
						className="min-w-0 flex-1 truncate font-mono text-[10.5px] text-muted-foreground"
						title={path}
					>
						{path}
					</span>
				) : compactCommand ? null : (
					<span className="flex-1" />
				)}
				<ActivityState
					activity={activity}
					open={open}
					hasBody={hasBody}
					showDisclosure={!compactCommand}
				/>
				{compactCommand && hasBody ? (
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
				<div className="flex flex-col gap-1.5 px-[11px] pb-2.5">
					{files.length ? <FileChangeList files={files} /> : null}
					{detail?.reason || detail?.text ? (
						<p className="whitespace-pre-wrap text-[11px] leading-relaxed text-muted-foreground">
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
			) : null}
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
 * The caveat below it is not boilerplate. The provider drops output, and it does
 * so differently per source, so the note says which source this is and why it may
 * be incomplete rather than hedging the same way about both.
 *
 * The text is what a terminal would have shown, not the bytes: output arrives with
 * its escape sequences intact — nothing in the stack strips them — so a colourized
 * test run rendered here verbatim is a wall of `[0m`, and a progress bar is a
 * hundred stacked copies of itself. See `lib/ansi.ts` for why this is a text pass
 * rather than a terminal.
 */
function CommandOutput({ activity }: { activity: ConversationActivity }) {
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
				className="max-h-64 overflow-auto rounded-md border border-border bg-background px-2.5 py-2 font-mono text-[10.5px] leading-relaxed text-muted-foreground"
			>
				{output}
			</pre>
			{detail?.outputTruncated ? (
				<p className="text-[10px] leading-relaxed text-warning">
					This command printed more than AO stores, so the output above stops early. Open a shell in
					the worktree to see the rest.
				</p>
			) : detail?.outputMayBePartial ? (
				<p className="text-[10px] leading-relaxed text-muted-foreground/70">
					{detail.outputSource === "stream"
						? "Streamed live as the command runs. The provider drops the first chunk, so the beginning may be missing."
						: "Rolled up by the provider after the command finished, and observed to drop the beginning."}{" "}
					Open a shell in the worktree for the full run.
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
		return { label: "Edited", path: shortenPaths(files[0]!.path) };
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
			<span className="shrink-0 font-mono text-[10px] tabular-nums text-destructive">
				{detail?.exitCode !== undefined ? `exit ${detail.exitCode}` : "failed"}
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
 * Every field here is new signal. The daemon normalizes the provider's change kind
 * — which arrives as an object — into a plain status, so a row can finally say
 * whether a file was added, deleted or renamed instead of only counting lines. And
 * each file now carries its own patch, which is the difference between being told
 * something changed and being able to read the change without leaving the
 * conversation.
 */
export function FileChangeList({ files }: { files: FileChangeFile[] }) {
	if (!files.length) return null;
	return (
		<ul className="flex flex-col gap-0.5">
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

	const line = (
		<>
			<span
				aria-label={status.label}
				className={cn("w-3 shrink-0 text-center font-mono text-[10px] font-semibold", status.tone)}
				title={status.label}
			>
				{status.mark}
			</span>
			<span className="min-w-0 flex-1 truncate font-mono text-[11px] text-muted-foreground" title={file.path}>
				{file.oldPath ? (
					<>
						<span className="text-muted-foreground/60">{shortenPaths(file.oldPath)}</span>
						<span aria-hidden="true" className="px-1 text-muted-foreground/40">
							&rarr;
						</span>
					</>
				) : null}
				{shortenPaths(file.path)}
			</span>
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
		return <li className="flex items-center gap-2.5 px-0.5 py-1">{line}</li>;
	}

	return (
		<li className="flex flex-col">
			<button
				type="button"
				onClick={() => setOpen((prev) => !prev)}
				aria-expanded={open}
				className="flex items-center gap-2.5 rounded-sm px-0.5 py-1 text-left transition-colors hover:bg-interactive-hover"
			>
				{line}
				<ChevronRight
					aria-hidden="true"
					className={cn(
						"size-3 shrink-0 text-muted-foreground/50 transition-transform",
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
	const { headline, detail } = providerErrorCopy(activity);
	return (
		<div className="flex min-w-0 max-w-full items-start gap-2.5 overflow-hidden rounded-md border border-destructive/40 bg-surface px-3 py-2">
			<AlertTriangle aria-hidden="true" className="mt-0.5 size-3.5 shrink-0 text-destructive" />
			<div className="flex min-w-0 flex-1 flex-col gap-0.5">
				<strong className="wrap-anywhere text-[11px] font-medium leading-snug text-destructive">
					{headline}
				</strong>
				{detail ? (
					<p className="wrap-anywhere text-[10.5px] leading-snug text-muted-foreground">{detail}</p>
				) : null}
			</div>
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
			<div className="w-fit max-w-[min(78%,560px)] whitespace-pre-wrap rounded-[10px] border border-accent-dim bg-raised px-3 py-2.5 text-sm leading-[1.55] text-foreground">
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
 * Buttons come from `activity.decisions` — the provider's own list — never from a
 * fixed set. A real captured approval offered `accept`, an object-shaped
 * `acceptWithExecpolicyAmendment`, and `cancel`, and offered **no decline**: a
 * hardcoded three-button row would have drawn a control that cannot be honored.
 */
export function ApprovalCard({
	activity,
	onDecide,
	busy,
}: {
	activity: ConversationActivity;
	onDecide?: (requestId: string, decisionId: string) => void;
	busy?: boolean;
}) {
	const resolved = activity.status !== "pending";
	const decisions: DecisionOption[] = activity.decisions ?? [];
	const detail = activity.detail;

	return (
		<div
			className={cn(
				"rounded-lg border bg-surface",
				resolved ? "border-border" : "border-warning/40 ring-1 ring-warning/10",
			)}
		>
			<div className="flex items-center gap-2 border-b border-border px-3.5 py-2.5">
				<ShieldQuestion
					aria-hidden="true"
					className={cn("size-4 shrink-0", resolved ? "text-muted-foreground" : "text-warning")}
				/>
				<strong className="text-xs font-semibold text-foreground">
					{resolved ? "Approval resolved" : "Approval required"}
				</strong>
				<span className="ml-auto shrink-0 font-mono text-[11px] text-muted-foreground">
					req {activity.requestId}
				</span>
			</div>

			<div className="flex flex-col gap-2.5 px-3.5 py-3">
				{detail?.reason ? (
					<p className="text-sm leading-relaxed text-muted-foreground">{detail.reason}</p>
				) : null}

				<dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 rounded bg-background px-2.5 py-2 font-mono text-[11px] leading-relaxed">
					<dt className="text-muted-foreground">command</dt>
					<dd className="min-w-0 break-all text-foreground">{detail?.command ?? activity.summary}</dd>
					{detail?.cwd ? (
						<>
							<dt className="text-muted-foreground">cwd</dt>
							<dd className="min-w-0 break-all text-muted-foreground">{detail.cwd}</dd>
						</>
					) : null}
				</dl>

				{resolved ? (
					<p className="text-[11px] text-muted-foreground">
						Already answered. This card is kept for the record.
					</p>
				) : (
					<div className="flex flex-wrap gap-2 pt-0.5">
						{decisions.map((decision, index) => (
							<Button
								key={decision.id}
								type="button"
								size="sm"
								variant={index === 0 ? "primary" : "outline"}
								disabled={busy}
								onClick={() => onDecide?.(activity.requestId ?? "", decision.id)}
							>
								{decision.label}
							</Button>
						))}
						{decisions.length === 0 ? (
							<p className="text-[11px] text-warning">
								The agent offered no decisions AO can present. Open diagnostics.
							</p>
						) : null}
					</div>
				)}
			</div>
		</div>
	);
}

/* -------------------------------------------------------------------------- */
/* compaction                                                                  */
/* -------------------------------------------------------------------------- */

/**
 * Where the conversation's earlier history was summarized to reclaim context.
 *
 * It renders as a divider rather than an activity row because that is what it is:
 * everything above it is no longer what the agent sees verbatim. Without the
 * marker, a conversation that quietly lost half its history reads as if the agent
 * simply forgot — the user would have no way to tell a compaction from a bug.
 *
 * Figures are shown only when the provider's reports allowed them to be computed.
 * A compaction right after a daemon restart genuinely does not know what it saved,
 * and a "0 tokens freed" label would be a lie rather than a gap.
 */
export function CompactionMarker({ activity }: { activity: ConversationActivity }) {
	const reclaimed = activity.detail?.tokensReclaimed;
	const after = activity.detail?.tokensAfter;
	const window = activity.detail?.contextWindow;

	return (
		<div className="flex items-center gap-2 py-1" data-compaction="true">
			<span aria-hidden="true" className="h-px flex-1 bg-border" />
			<Archive aria-hidden="true" className="size-3 shrink-0 text-muted-foreground/70" />
			<span className="shrink-0 text-[10px] uppercase tracking-[0.08em] text-muted-foreground/70">
				History compacted
			</span>
			{reclaimed ? (
				<span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground/70">
					&minus;{formatTokens(reclaimed)}
				</span>
			) : null}
			{after && window ? (
				<span
					className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground/70"
					title={`${after.toLocaleString()} of ${window.toLocaleString()} context tokens in use`}
				>
					{Math.round((after / window) * 100)}% full
				</span>
			) : null}
			<span aria-hidden="true" className="h-px flex-1 bg-border" />
		</div>
	);
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

/**
 * What a turn changed on disk.
 *
 * One panel per turn rather than a row per update: the provider re-sends the whole
 * diff as the turn progresses, and the daemon overwrites it, so this is current
 * state and not history. It grows while the turn runs, which is the point — seeing
 * a file appear as the agent touches it is the difference between watching work and
 * waiting for it.
 *
 * Rendered only when the daemon reported a diff. An agent that cannot report one
 * gets no empty panel implying it changed nothing.
 */
export function TurnChangedFiles({ diff, live }: { diff: TurnDiff; live?: boolean }) {
	const [open, setOpen] = useState(false);
	if (diff.files.length === 0) return null;

	const additions = diff.files.reduce((sum, file) => sum + file.additions, 0);
	const deletions = diff.files.reduce((sum, file) => sum + file.deletions, 0);

	return (
		<div className="rounded-lg border border-border bg-surface">
			<button
				type="button"
				onClick={() => setOpen((prev) => !prev)}
				aria-expanded={open}
				className="flex w-full items-center gap-2 px-3.5 py-2.5 text-left transition-colors hover:bg-interactive-hover"
			>
				<FileDiff aria-hidden="true" className="size-4 shrink-0 text-muted-foreground" />
				<strong className="shrink-0 text-xs font-semibold text-foreground">
					{diff.files.length === 1 ? "1 file changed" : `${diff.files.length} files changed`}
				</strong>
				{live ? (
					<Loader2
						aria-label="still changing"
						className="size-3 shrink-0 animate-spin text-muted-foreground/60"
					/>
				) : null}
				<span className="flex-1" />
				<span className="shrink-0 font-mono text-[10.5px] tabular-nums">
					<span className="text-success">+{additions}</span>{" "}
					<span className="text-destructive">&minus;{deletions}</span>
				</span>
				<ChevronRight
					aria-hidden="true"
					className={cn(
						"size-3.5 shrink-0 text-muted-foreground/50 transition-transform",
						open && "rotate-90",
					)}
				/>
			</button>

			{open ? (
				<ul className="flex flex-col border-t border-border">
					{diff.files.map((file) => {
						const status = diffStatusMark[file.status] ?? diffStatusMark.modified;
						return (
							<li
								key={`${file.status}-${file.oldPath ?? ""}-${file.path}`}
								className="flex items-center gap-2.5 px-3.5 py-1.5 text-[11px]"
							>
								<span
									aria-label={status.label}
									className={cn("w-3 shrink-0 text-center font-mono font-semibold", status.tone)}
									title={status.label}
								>
									{status.mark}
								</span>
								<span className="min-w-0 flex-1 truncate font-mono text-muted-foreground" title={file.path}>
									{/* A rename shows both ends. Only the new path would read as an addition
									    and lose the fact that something moved. */}
									{file.oldPath ? (
										<>
											<span className="text-muted-foreground/60">
												{shortenPaths(file.oldPath)}
											</span>
											<span aria-hidden="true" className="px-1 text-muted-foreground/40">
												&rarr;
											</span>
											{shortenPaths(file.path)}
										</>
									) : (
										shortenPaths(file.path)
									)}
								</span>
								<span className="shrink-0 font-mono tabular-nums text-success">
									+{file.additions}
								</span>
								<span className="shrink-0 font-mono tabular-nums text-destructive">
									&minus;{file.deletions}
								</span>
							</li>
						);
					})}
					{diff.truncated ? (
						<li className="px-3.5 py-2 text-[10px] leading-relaxed text-warning">
							This turn changed more files than AO lists here. Use the Diff tab for the whole change.
						</li>
					) : null}
				</ul>
			) : null}
		</div>
	);
}

/** Exact below a thousand, because that is where the digits still mean something. */
function formatTokens(tokens: number): string {
	if (tokens < 1000) return `${tokens}`;
	return `${(tokens / 1000).toFixed(1)}k`;
}

/* -------------------------------------------------------------------------- */
/* turn boundary                                                               */
/* -------------------------------------------------------------------------- */

/**
 * How a turn ended. `interrupted` is reported as its own outcome because the
 * provider reports it that way — relabelling it as failed would misattribute a
 * deliberate cancellation.
 */
export function TurnOutcome({
	state,
	durationMs,
	error,
	onRollback,
}: {
	state: "completed" | "interrupted" | "failed";
	durationMs?: number;
	error?: string;
	/**
	 * Discard this turn and everything after it. Absent means the operation is not
	 * available right now — the agent cannot undo, or it is mid-turn — and the
	 * control is not drawn rather than drawn and then refused.
	 */
	onRollback?: () => void;
}) {
	const copy = {
		completed: { label: "Done", tone: "text-muted-foreground/70" },
		interrupted: { label: "Stopped", tone: "text-muted-foreground/70" },
		failed: { label: "Failed", tone: "text-destructive" },
	}[state];

	return (
		<div className="group/turn flex items-center gap-2 pt-1">
			<span aria-hidden="true" className="h-px flex-1 bg-border" />
			{onRollback ? (
				// Revealed on hover or keyboard focus. Undo belongs on the turn it undoes,
				// but a permanent button on every turn would compete with the conversation
				// for attention.
				<button
					type="button"
					onClick={onRollback}
					className="shrink-0 rounded px-1 text-[10px] uppercase tracking-[0.08em] text-muted-foreground/70 opacity-0 transition-opacity hover:text-foreground focus-visible:opacity-100 group-hover/turn:opacity-100"
				>
					Roll back to here
				</button>
			) : null}
			<span className={cn("shrink-0 text-[10px] uppercase tracking-[0.08em]", copy.tone)}>
				{copy.label}
			</span>
			{durationMs !== undefined && durationMs > 0 ? (
				<span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground/70">
					{formatDuration(durationMs)}
				</span>
			) : null}
			{error ? (
				<span className="shrink-0 text-[10px] text-destructive" title={error}>
					{error.slice(0, 60)}
				</span>
			) : null}
		</div>
	);
}

export { User };
