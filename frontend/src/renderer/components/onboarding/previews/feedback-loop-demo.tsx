"use client";

import { AnimatePresence, domAnimation, LazyMotion, m, motion } from "motion/react";
import {
	ArrowUpRight,
	Bell,
	ChevronDown,
	Code2,
	Files,
	GitMerge,
	GitPullRequest,
	MessageSquare,
	Network,
	PanelRight,
	Plus,
	Repeat2,
	Trash2,
} from "lucide-react";
import { useEffect, useRef, useState, useCallback, type CSSProperties, type ReactNode } from "react";
import { featurePreviewTokens } from "./preview-tokens";
import { usePreviewScale } from "./use-preview-scale";

const sessionPreviewTokens = {
	...featurePreviewTokens,
	"--preview-sidebar": "oklch(0.155 0.005 285.823)",
	"--preview-overlay": "oklch(0.28 0.008 285.885)",
	"--preview-passive": "oklch(0.442 0.017 285.786)",
	"--preview-link": "oklch(0.92 0.004 286.32)",
	"--preview-input": "oklch(1 0 0 / 4%)",
	"--preview-input-border": "oklch(1 0 0 / 3%)",
	"--preview-interactive-active": "color-mix(in oklch, oklch(0.985 0 0) 7%, transparent)",
	"--preview-terminal": "#101317",
	"--preview-terminal-fg": "#d7d7d2",
	"--preview-terminal-dim": "#7c7c7c",
} as CSSProperties;

const RAIL_WIDTH = 244;
const PREVIEW_WIDTH = 620;
const PREVIEW_HEIGHT = 408;
const PROMPT_MARKER = ">";
const ACTION_MARKER = "*";
const RESULT_MARKER = "L";

const status = {
	working: "#60a5fa",
	needsYou: "#fb923c",
	ready: "#4ade80",
	idle: "oklch(0.705 0.015 286.067)",
	exited: "oklch(0.704 0.191 22.216)",
} as const;

type PartTone = "neutral" | "passive" | "success" | "warning" | "error";

const partToneColor: Record<PartTone, string> = {
	neutral: "var(--preview-muted-foreground)",
	passive: "var(--preview-passive)",
	success: status.ready,
	warning: status.needsYou,
	error: status.exited,
};

type Pill = { label: string; tone: string; breathe: boolean };
type CardStatus = { label: string; tone: PartTone; link?: string; breathe?: boolean };

type Phase = {
	activity: Pill;
	scm: Pill[];
	primary: CardStatus;
	supporting: CardStatus[];
	diff: { files: number; additions: number; deletions: number };
	hasPullRequest?: boolean;
	mergeable?: boolean;
};

const activityPills = {
	idle: { label: "Idle", tone: status.idle, breathe: false },
	working: { label: "Working", tone: status.working, breathe: true },
} as const;

const ciFailed: Pill = { label: "CI Failed", tone: status.exited, breathe: false };

const PHASE_IDLE: Phase = {
	activity: activityPills.idle,
	scm: [],
	primary: { label: "Open", tone: "neutral" },
	supporting: [],
	diff: { files: 0, additions: 0, deletions: 0 },
	hasPullRequest: false,
};

const phaseMap: Record<string, Phase> = {
	idle: PHASE_IDLE,
	"ci-fail": {
		activity: activityPills.idle,
		scm: [ciFailed],
		primary: { label: "Checks failing", tone: "error", link: "test / web" },
		supporting: [],
		diff: { files: 3, additions: 41, deletions: 12 },
		hasPullRequest: true,
	},
	fixing: {
		activity: activityPills.working,
		scm: [ciFailed],
		primary: { label: "Checks failing", tone: "error", link: "test / web" },
		supporting: [],
		diff: { files: 3, additions: 41, deletions: 12 },
		hasPullRequest: true,
	},
	pending: {
		activity: activityPills.working,
		scm: [],
		primary: { label: "Changes requested", tone: "warning", link: "prateek" },
		supporting: [{ label: "Checks pending", tone: "neutral", breathe: true }],
		diff: { files: 4, additions: 47, deletions: 14 },
		hasPullRequest: true,
	},
	green: {
		activity: activityPills.idle,
		scm: [],
		primary: { label: "Ready to merge", tone: "success" },
		supporting: [{ label: "Checks passing", tone: "success" }],
		diff: { files: 4, additions: 47, deletions: 14 },
		hasPullRequest: true,
		mergeable: true,
	},
	merged: {
		activity: activityPills.idle,
		scm: [],
		primary: { label: "Merged", tone: "success" },
		supporting: [{ label: "Checks passing", tone: "success" }],
		diff: { files: 4, additions: 47, deletions: 14 },
		hasPullRequest: true,
	},
};

/* ── Step-based animation engine ───────────────────────────────────────────── */

type Step =
	| { type: "pause"; ms: number }
	| { type: "type"; text: string; marker?: string; highlight?: boolean }
	| { type: "stream"; text: string }
	| { type: "line"; tone: LineTone; text: string; marker?: string; indent?: number }
	| { type: "blank" }
	| { type: "phase"; key: string }
	| { type: "cursor"; target: string; click?: boolean }
	| { type: "merge"; state: "idle" | "merging" | "merged" }
	| { type: "clear" };

type LineTone = "fg" | "dim" | "error" | "success" | "working";

const TYPING_MS = 30;

const SCRIPT: Step[] = [
	// 1. Empty chat
	{ type: "pause", ms: 1800 },
	{ type: "type", text: "Add GitHub OAuth callback handling.", marker: PROMPT_MARKER },
	{ type: "pause", ms: 500 },

	// 2. Agent works — condensed
	{ type: "blank" },
	{ type: "line", tone: "fg", text: "Write(src/auth/callback.ts)", marker: ACTION_MARKER },
	{ type: "pause", ms: 300 },
	{ type: "line", tone: "dim", text: "Wrote 96 lines", marker: RESULT_MARKER },
	{ type: "pause", ms: 250 },
	{ type: "line", tone: "fg", text: "Bash(npm test -- auth)", marker: ACTION_MARKER },
	{ type: "pause", ms: 400 },
	{ type: "line", tone: "success", text: "Tests  11 passed (11)", marker: RESULT_MARKER },
	{ type: "pause", ms: 300 },
	{ type: "line", tone: "fg", text: "Bash(git push && gh pr create --fill)", marker: ACTION_MARKER },
	{ type: "pause", ms: 300 },
	{ type: "line", tone: "dim", text: "Created pull request #2481", marker: RESULT_MARKER },
	{ type: "pause", ms: 2000 },

	// 3. ★ CI failure nudge — the key moment
	{ type: "phase", key: "ci-fail" },
	{ type: "blank" },
	{ type: "line", tone: "fg", text: "CI is failing on your PR." },
	{ type: "pause", ms: 300 },
	{ type: "blank" },
	{ type: "line", tone: "fg", text: "Failed: test / web (failed)" },
	{ type: "pause", ms: 200 },
	{ type: "line", tone: "error", text: "FAIL  src/auth/callback.test.ts" },
	{ type: "pause", ms: 150 },
	{ type: "line", tone: "dim", text: "  expected 401, received 500" },
	{ type: "pause", ms: 150 },
	{ type: "line", tone: "dim", text: "Fix the issues and push again." },
	{ type: "pause", ms: 2000 },

	// 4. ★ Agent fixes
	{ type: "phase", key: "fixing" },
	{ type: "blank" },
	{ type: "line", tone: "fg", text: "Read(src/auth/callback.ts)", marker: ACTION_MARKER },
	{ type: "pause", ms: 400 },
	{ type: "stream", text: "Expiry check runs after the token exchange. Moving it before so expired params return 401." },
	{ type: "pause", ms: 400 },
	{ type: "blank" },
	{ type: "line", tone: "fg", text: "Edit(src/auth/callback.ts)", marker: ACTION_MARKER },
	{ type: "pause", ms: 300 },
	{ type: "line", tone: "dim", text: "+6 -2 lines", marker: RESULT_MARKER },
	{ type: "pause", ms: 300 },

	// 5. Tests pass, push
	{ type: "phase", key: "pending" },
	{ type: "line", tone: "fg", text: "Bash(npm test -- auth/callback)", marker: ACTION_MARKER },
	{ type: "pause", ms: 500 },
	{ type: "line", tone: "success", text: "Tests  12 passed (12)", marker: RESULT_MARKER },
	{ type: "pause", ms: 300 },
	{ type: "line", tone: "fg", text: "Bash(git push origin feat/github-auth)", marker: ACTION_MARKER },
	{ type: "pause", ms: 300 },
	{ type: "line", tone: "dim", text: "91f8c2a -> feat/github-auth", marker: RESULT_MARKER },
	{ type: "pause", ms: 2000 },

	// 6. ★ All green
	{ type: "phase", key: "green" },
	{ type: "blank" },
	{ type: "stream", text: "All checks passing. PR is approved and ready to merge." },
	{ type: "pause", ms: 2000 },

	// 7. ★ Cursor moves to Merge button, clicks it
	{ type: "cursor", target: "merge-button" },
	{ type: "pause", ms: 1500 },
	{ type: "cursor", target: "merge-button", click: true },
	{ type: "pause", ms: 200 },
	{ type: "merge", state: "merging" },
	{ type: "pause", ms: 1800 },
	{ type: "merge", state: "merged" },
	{ type: "phase", key: "merged" },
	{ type: "cursor", target: "idle-rest" },
	{ type: "pause", ms: 2500 },

	// 8. User types /clear
	{ type: "type", text: "/clear", marker: PROMPT_MARKER, highlight: true },
	{ type: "pause", ms: 800 },

	// 9. Clear and loop
	{ type: "clear" },
	{ type: "merge", state: "idle" },
	{ type: "phase", key: "idle" },
	{ type: "pause", ms: 1800 },
];

type DisplayLine = { id: string; tone: LineTone; text: string; marker?: string; indent?: number; blank?: boolean; skipAnim?: boolean };

const lineToneColor: Record<LineTone, string> = {
	fg: "var(--preview-terminal-fg)",
	dim: "var(--preview-terminal-dim)",
	error: status.exited,
	success: status.ready,
	working: status.working,
};

function normalizeMarker(marker?: string) {
	if (marker === "❯") return PROMPT_MARKER;
	if (marker === "⏺") return ACTION_MARKER;
	if (marker === "⎿") return RESULT_MARKER;
	return marker;
}

/* ── Main component ────────────────────────────────────────────────────────── */

export function FeedbackLoopDemo({ agentIcon = "/app-icons/agents/claude-code.svg" }: { agentIcon?: string }) {
	const [lines, setLines] = useState<DisplayLine[]>([]);
	const [typingText, setTypingText] = useState<{ marker?: string; chars: string; highlight?: boolean } | null>(null);
	const [streamingText, setStreamingText] = useState<string | null>(null);
	const [phaseKey, setPhaseKey] = useState("idle");
	const [cursorTarget, setCursorTarget] = useState<string | null>("idle-rest");
	const [cursorPressed, setCursorPressed] = useState(false);
	const [mergeState, setMergeState] = useState<"idle" | "merging" | "merged">("idle");
	const lineIdRef = useRef(0);
	const timerRef = useRef<number>(0);
	const rootRef = useRef<HTMLDivElement>(null);
	const { viewportRef, viewportStyle, canvasStyle } = usePreviewScale(PREVIEW_WIDTH, PREVIEW_HEIGHT);

	const phase = phaseMap[phaseKey] ?? PHASE_IDLE;

	const addLine = useCallback((l: Omit<DisplayLine, "id">) => {
		const id = `l${++lineIdRef.current}`;
		setLines((prev) => [...prev, { ...l, id }]);
	}, []);

	useEffect(() => {
		let cancelled = false;
		let stepIdx = 0;

		const runStep = () => {
			if (cancelled) return;
			if (stepIdx >= SCRIPT.length) {
				stepIdx = 0;
			}
			const step = SCRIPT[stepIdx]!;
			stepIdx++;

			switch (step.type) {
				case "pause":
					timerRef.current = window.setTimeout(runStep, step.ms);
					break;

				case "type": {
					const { text, marker, highlight } = step;
					let charIdx = 0;
					setTypingText({ marker, chars: "", highlight });
					const typeNext = () => {
						if (cancelled) return;
						if (charIdx >= text.length) {
							setTypingText(null);
							const id = `l${++lineIdRef.current}`;
							const tone: LineTone = highlight ? "working" : "fg";
							setLines((prev) => [...prev, { id, tone, text, marker, skipAnim: true }]);
							timerRef.current = window.setTimeout(runStep, 100);
							return;
						}
						charIdx++;
						setTypingText({ marker, chars: text.slice(0, charIdx), highlight });
						timerRef.current = window.setTimeout(typeNext, TYPING_MS);
					};
					timerRef.current = window.setTimeout(typeNext, TYPING_MS);
					break;
				}

				case "stream": {
					const { text } = step;
					let charIdx = 0;
					setStreamingText("");
					const streamNext = () => {
						if (cancelled) return;
						if (charIdx >= text.length) {
							setStreamingText(null);
							const id = `l${++lineIdRef.current}`;
							setLines((prev) => [...prev, { id, tone: "fg", text, skipAnim: true }]);
							timerRef.current = window.setTimeout(runStep, 100);
							return;
						}
						const burst = 2 + Math.floor(Math.abs(Math.sin(charIdx * 0.7)) * 4);
						charIdx = Math.min(charIdx + burst, text.length);
						setStreamingText(text.slice(0, charIdx));
						const delay = charIdx % 12 < 3 ? 80 : 20;
						timerRef.current = window.setTimeout(streamNext, delay);
					};
					timerRef.current = window.setTimeout(streamNext, 30);
					break;
				}

				case "line":
					addLine({ tone: step.tone, text: step.text, marker: step.marker, indent: step.indent });
					timerRef.current = window.setTimeout(runStep, 60);
					break;

				case "blank":
					addLine({ tone: "dim", text: "", blank: true });
					timerRef.current = window.setTimeout(runStep, 60);
					break;

				case "phase":
					setPhaseKey(step.key);
					timerRef.current = window.setTimeout(runStep, 0);
					break;

				case "cursor":
					if (step.target) {
						setCursorTarget(step.target);
						setCursorPressed(false);
						// Delay so React renders the target element before DemoCursor measures
						timerRef.current = window.setTimeout(() => {
							if (cancelled) return;
							if (step.click) {
								setCursorPressed(true);
								timerRef.current = window.setTimeout(() => {
									if (cancelled) return;
									setCursorPressed(false);
									runStep();
								}, 150);
							} else {
								runStep();
							}
						}, 200);
					} else {
						setCursorTarget(null);
						setCursorPressed(false);
						timerRef.current = window.setTimeout(runStep, 0);
					}
					break;

				case "merge":
					setMergeState(step.state);
					timerRef.current = window.setTimeout(runStep, 0);
					break;

				case "clear":
					setLines([]);
					setTypingText(null);
					setStreamingText(null);
					setCursorTarget("idle-rest");
					setCursorPressed(false);
					lineIdRef.current = 0;
					timerRef.current = window.setTimeout(runStep, 0);
					break;
			}
		};

		timerRef.current = window.setTimeout(runStep, 1500);

		return () => {
			cancelled = true;
			window.clearTimeout(timerRef.current);
		};
	}, [addLine]);

	return (
		<div
			ref={viewportRef}
			className="relative mx-auto w-full min-w-0 max-w-[620px]"
			style={viewportStyle}
		>
			<div
				ref={rootRef}
				className="absolute left-0 top-0 overflow-hidden rounded-[10px] bg-[var(--preview-background)] text-[var(--preview-foreground)] antialiased select-none shadow-[0_28px_74px_-22px_rgba(0,0,0,0.86)]"
				style={{ ...sessionPreviewTokens, ...canvasStyle, fontFamily: "var(--font-geist-sans), ui-sans-serif, system-ui, sans-serif" }}
			>
				<style>{`
					@keyframes ao-step-blink { 0%, 49% { opacity: 1; } 50%, 100% { opacity: 0; } }
					.ao-blink { animation: ao-step-blink 1s step-end infinite; }
					@media (prefers-reduced-motion: reduce) { .ao-blink { animation: none; } }
				`}</style>
				<div className="flex h-full min-w-0">
					<div className="flex min-h-0 min-w-0 flex-1 flex-col">
						<SessionTopbar phase={phase} agentIcon={agentIcon} />
						<TerminalPane agentIcon={agentIcon} lines={lines} typingText={typingText} streamingText={streamingText} />
					</div>
					<Inspector phase={phase} mergeState={mergeState} />
				</div>
				<DemoCursor rootRef={rootRef} target={cursorTarget ?? "idle-rest"} pressed={cursorPressed} />
			</div>
		</div>
	);
}

/* ── Demo cursor (scaled for this preview) ──────────────────────────────────── */

const IDLE_POS = { x: 28, y: 78 };

function DemoCursor({ rootRef, target, pressed }: { rootRef: React.RefObject<HTMLDivElement | null>; target: string; pressed: boolean }) {
	const [pos, setPos] = useState(IDLE_POS);

	useEffect(() => {
		if (target === "idle-rest") {
			setPos(IDLE_POS);
			return;
		}
		const root = rootRef.current;
		if (!root) return;
		let attempts = 0;
		let timer = 0;
		const measure = () => {
			const el = root.querySelector<HTMLElement>(`[data-cursor-target="${target}"]`);
			if (!el) {
				if (attempts++ < 10) { timer = window.setTimeout(measure, 50); }
				return;
			}
			const rootRect = root.getBoundingClientRect();
			const elRect = el.getBoundingClientRect();
			if (!rootRect.width || !rootRect.height) return;
			setPos({
				x: ((elRect.left + elRect.width / 2 - rootRect.left) / rootRect.width) * 100,
				y: ((elRect.top + elRect.height / 2 - rootRect.top) / rootRect.height) * 100,
			});
		};
		const frame = requestAnimationFrame(measure);
		timer = window.setTimeout(measure, 100);
		return () => { cancelAnimationFrame(frame); clearTimeout(timer); };
	}, [rootRef, target]);

	return (
		<motion.div
			className="pointer-events-none absolute z-40"
			initial={false}
			animate={{ left: `${pos.x}%`, top: `${pos.y}%` }}
			transition={{ type: "spring", stiffness: 240, damping: 30, mass: 0.8 }}
			style={{ width: 0, height: 0 }}
		>
			<motion.div animate={{ scale: pressed ? 0.86 : 1 }} transition={{ duration: 0.12 }}>
				<svg
					width="14"
					height="14"
					viewBox="0 0 24 24"
					className="-translate-x-[3px] -translate-y-[1px] drop-shadow-[0_1px_2px_rgba(0,0,0,0.7)]"
					aria-hidden="true"
				>
					<path
						d="M5 3 L5 21 L10.2 16.8 L13.4 23 L15.9 22 L12.7 15.8 L19.5 15.8 Z"
						fill="#ffffff"
						stroke="#000000"
						strokeWidth="1.4"
						strokeLinejoin="round"
					/>
				</svg>
			</motion.div>
			<AnimatePresence>
				{pressed ? (
					<motion.span
						key="click-ring"
						initial={{ opacity: 0.9, scale: 0.25 }}
						animate={{ opacity: 0, scale: 1.55 }}
						exit={{ opacity: 0 }}
						transition={{ duration: 0.7, ease: "easeOut" }}
						className="absolute -left-[10px] -top-[10px] size-5 rounded-full border border-white"
					/>
				) : null}
			</AnimatePresence>
		</motion.div>
	);
}

/* ── Session topbar ────────────────────────────────────────────────────────── */

function SessionTopbar({ phase, agentIcon }: { phase: Phase; agentIcon: string }) {
	return (
		<div className="flex h-9 w-full shrink-0 items-stretch border-b border-[var(--preview-border)] bg-[var(--preview-background)]">
			<div className="flex min-w-0 flex-1 items-center">
				<span className="relative inline-flex min-w-0 shrink self-stretch items-center gap-1.5 border-r border-[var(--preview-border)] bg-[var(--preview-overlay)] px-2.5 after:absolute after:inset-x-0 after:-bottom-px after:h-px after:bg-[var(--preview-foreground)]/80">
					<img
						src={agentIcon}
						alt=""
						aria-hidden="true"
						className="size-[13px] shrink-0 object-contain"
						draggable={false}
					/>
					<span className="inline-flex min-w-0 items-center gap-1.5 text-[10.5px] font-medium leading-none text-[var(--preview-foreground)]">
						<span className="truncate">github-auth</span>
						<span
							className={`size-[5px] shrink-0 rounded-full ${phase.activity.breathe ? "animate-pulse" : ""}`}
							style={{ background: phase.activity.tone }}
						/>
					</span>
				</span>
				<div className="ml-auto flex shrink-0 items-center gap-1 px-1.5 text-[var(--preview-muted-foreground)]">
					<SessionTopbarButton label="Switch agent">
						<Repeat2 aria-hidden="true" className="size-[13px]" />
					</SessionTopbarButton>
					<span className="inline-flex h-[22px] items-center overflow-hidden rounded-[5px] border border-[var(--preview-border)] bg-[var(--preview-input)]">
						<span className="grid size-[21px] place-items-center text-[var(--preview-foreground)]">
							<Code2 aria-hidden="true" className="size-[12px]" />
						</span>
						<span className="grid h-full w-3 place-items-center border-l border-[var(--preview-border)]">
							<ChevronDown aria-hidden="true" className="size-2.5" />
						</span>
					</span>
					<SessionTopbarButton label="New terminal">
						<Plus aria-hidden="true" className="size-[13px]" />
					</SessionTopbarButton>
					<SessionTopbarButton label="Switch to chat UI">
						<MessageSquare aria-hidden="true" className="size-[13px]" />
					</SessionTopbarButton>
					<SessionTopbarButton label="Kill session" danger>
						<Trash2 aria-hidden="true" className="size-[13px]" />
					</SessionTopbarButton>
					<SessionTopbarButton label="Open orchestrator">
						<Network aria-hidden="true" className="size-[13px]" />
					</SessionTopbarButton>
				</div>
			</div>
		</div>
	);
}

function SessionTopbarButton({
	active = false,
	children,
	danger = false,
	label,
}: {
	active?: boolean;
	children: ReactNode;
	danger?: boolean;
	label: string;
}) {
	return (
		<span
			aria-label={label}
			className={`grid size-[22px] shrink-0 place-items-center rounded-[5px] ${
				active
					? "bg-[var(--preview-interactive-active)] text-[var(--preview-foreground)]"
					: danger
						? "text-[#fb8e8a]"
						: "text-[var(--preview-muted-foreground)]"
			}`}
		>
			{children}
		</span>
	);
}

/* ── Terminal pane ──────────────────────────────────────────────────────────── */

function TerminalPane({ agentIcon, lines, typingText, streamingText }: { agentIcon: string; lines: DisplayLine[]; typingText: { marker?: string; chars: string; highlight?: boolean } | null; streamingText: string | null }) {
	const scrollRef = useRef<HTMLDivElement>(null);

	useEffect(() => {
		const terminal = scrollRef.current;
		if (!terminal) return;
		terminal.scrollTop = terminal.scrollHeight;
	}, [lines.length, typingText, streamingText]);

	return (
		<main
			ref={scrollRef}
			className="flex min-h-0 min-w-0 flex-1 flex-col justify-end overflow-hidden bg-[var(--preview-terminal)] px-3 py-2.5 font-mono"
			style={{ fontFamily: "var(--font-family-mono), 'Geist Mono Variable', ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace", fontVariantEmoji: "text" }}
		>
			{/* Claude Code header */}
			<div className="mb-2 flex items-start gap-2">
				<img
					src={agentIcon}
					alt=""
					aria-hidden="true"
					className="mt-0.5 size-[22px] shrink-0"
					draggable={false}
				/>
				<div className="min-w-0 text-[10px] leading-[1.5]">
					<div>
						<span className="font-bold text-[var(--preview-terminal-fg)]">Claude Code</span>
						<span className="text-[var(--preview-terminal-dim)]"> v2.1.204</span>
					</div>
					<div className="text-[var(--preview-terminal-dim)]">
						Opus 4.8 (1M context) · Claude Team
					</div>
					<div className="text-[var(--preview-terminal-dim)]">
						~/ao/solkit-ui/orchestrator
					</div>
				</div>
			</div>

			<LazyMotion features={domAnimation}>
				<div className="text-[10px] leading-[1.45] text-[var(--preview-terminal-fg)]">
					{lines.map((line) => (
						<m.div
							key={line.id}
							initial={line.skipAnim ? false : { opacity: 0 }}
							animate={{ opacity: 1 }}
							transition={{ duration: 0.2 }}
							className="flex min-w-0 items-start gap-1.5"
							style={{ color: lineToneColor[line.tone] }}
						>
							{line.blank ? (
								<span aria-hidden="true">&nbsp;</span>
							) : (
								<>
									<span className="w-[7px] shrink-0" style={{ marginLeft: (line.indent ?? 0) * 7 }}>
										{normalizeMarker(line.marker) ?? ""}
									</span>
									<span className="min-w-0 whitespace-pre-wrap break-words">{line.text}</span>
								</>
							)}
						</m.div>
					))}
					{/* Live typing line (user input) */}
					{typingText ? (
						<div className="flex min-w-0 items-start gap-1.5" style={{ color: typingText.highlight ? lineToneColor.working : lineToneColor.fg }}>
							<span className="w-[7px] shrink-0">{normalizeMarker(typingText.marker) ?? ""}</span>
							<span className="min-w-0 whitespace-pre-wrap break-words">
								{typingText.chars}
								<span className="inline-block h-[9px] w-[5px] translate-y-[1px] ao-blink bg-[var(--preview-terminal-fg)]" />
							</span>
						</div>
					) : null}
					{/* Streaming agent text */}
					{streamingText !== null ? (
						<div className="flex min-w-0 items-start gap-1.5" style={{ color: lineToneColor.fg }}>
							<span className="w-[7px] shrink-0" />
							<span className="min-w-0 whitespace-pre-wrap break-words">
								{streamingText}
								<span className="inline-block h-[9px] w-[5px] translate-y-[1px] ao-blink bg-[var(--preview-terminal-fg)]" />
							</span>
						</div>
					) : null}
					{/* Persistent prompt line — always visible when not typing */}
					{!typingText && streamingText === null ? (
						<div className="flex min-w-0 items-center gap-1.5" style={{ color: lineToneColor.fg }}>
							<span className="w-[7px] shrink-0">{PROMPT_MARKER}</span>
							<span className="inline-block h-[10px] w-[5.5px] ao-blink bg-[var(--preview-terminal-fg)]" />
						</div>
					) : null}
				</div>
			</LazyMotion>
		</main>
	);
}

/* ── Inspector rail ────────────────────────────────────────────────────────── */

function Inspector({ phase, mergeState }: { phase: Phase; mergeState: "idle" | "merging" | "merged" }) {
	return (
		<aside
			className="flex shrink-0 flex-col overflow-hidden border-l border-[var(--preview-border)]"
			style={{ width: RAIL_WIDTH }}
		>
			<div className="flex h-9 shrink-0 items-center border-b border-[var(--preview-border)] pl-1.5">
				<div className="flex min-w-0 flex-1 items-center gap-0.5">
					<InspectorTab active label="Summary">
						<SummaryIcon />
					</InspectorTab>
					<InspectorTab label="Reviews">
						<MessageSquare aria-hidden="true" className="size-3" />
					</InspectorTab>
					<InspectorTab label="Browser">
						<BrowserIcon />
					</InspectorTab>
					<InspectorTab label={`${phase.diff.files} Files`}>
						<Files aria-hidden="true" className="size-3" />
					</InspectorTab>
				</div>
				<div className="flex shrink-0 items-center gap-1 px-1.5">
					<SessionTopbarButton active label="Close inspector">
						<PanelRight aria-hidden="true" className="size-[13px]" />
					</SessionTopbarButton>
					<SessionTopbarButton label="Notifications">
						<Bell aria-hidden="true" className="size-[13px]" />
					</SessionTopbarButton>
				</div>
			</div>

			<div className="min-h-0 flex-1 overflow-hidden px-3 pb-3 pt-2">
				<Section title="Pull request">
					<PRSummaryCard phase={phase} mergeState={mergeState} />
				</Section>
				<Section title="Reviews">
					<ReviewsSection />
				</Section>
				<Section title="Completion">
					<CompletionControls />
				</Section>
				<Section title="Activity">
					<ActivityTimeline phase={phase} />
				</Section>
			</div>
		</aside>
	);
}

function InspectorTab({ active = false, children, label }: { active?: boolean; children: ReactNode; label: string }) {
	return (
		<span
			aria-hidden="true"
			className={`inline-flex size-[22px] shrink-0 items-center justify-center rounded-[5px] text-[var(--preview-passive)] ${
				active
					? "bg-[var(--preview-interactive-active)] text-[var(--preview-foreground)]"
					: ""
			}`}
		>
			{children}
			<span className="sr-only">{label}</span>
		</span>
	);
}

function Section({ children, title }: { children: ReactNode; title: string }) {
	return (
		<section className="mb-3 last:mb-0">
			<div className="mb-1.5 text-[8.5px] font-bold uppercase tracking-[0.06em] text-[var(--preview-muted-foreground)]">
				{title}
			</div>
			{children}
		</section>
	);
}

/* ── PR card ───────────────────────────────────────────────────────────────── */

function PRSummaryCard({ phase, mergeState }: { phase: Phase; mergeState: "idle" | "merging" | "merged" }) {
	if (!phase.hasPullRequest) {
		return (
			<div className="flex items-center gap-1.5 text-[8.5px] text-[var(--preview-passive)]">
				<span className="size-[5px] shrink-0 rounded-full" style={{ background: "var(--preview-passive)" }} />
				<span>No PR yet</span>
			</div>
		);
	}

	const prState = mergeState === "merged" ? "merged" : "open";
	const badgeStyle = mergeState === "merged"
		? { color: status.ready, borderColor: "var(--preview-border)", background: "var(--preview-overlay)" }
		: { color: "var(--preview-muted-foreground)", borderColor: "var(--preview-border)", background: "var(--preview-overlay)" };

	return (
		<article className="rounded-lg border border-[var(--preview-input-border)] bg-[var(--preview-input)] px-2.5 pt-1.5 pb-2">
			<span className="text-[10px] font-semibold leading-snug tracking-tight text-[var(--preview-foreground)]">
				Reject expired OAuth state
			</span>
			<div className="mt-1 flex min-w-0 items-center gap-1.5">
				<span className="inline-flex min-w-0 items-center gap-1 font-mono text-[9px] font-medium text-[var(--preview-foreground)]">
					<GitPullRequest aria-hidden="true" className="size-3 shrink-0" />
					<span>PR #2481</span>
					<ArrowUpRight aria-hidden="true" className="size-2 shrink-0" strokeWidth={2} />
				</span>
				<span
					className="inline-flex h-[15px] shrink-0 items-center rounded-full border px-[5px] text-[8px] font-medium leading-none"
					style={badgeStyle}
				>
					{prState === "merged" ? "Merged" : "Open"}
				</span>
			</div>
			<div className="mt-1.5 min-w-0 font-mono text-[8px] leading-[1.5]">
				<div className="flex min-w-0 items-center gap-1 overflow-hidden text-[var(--preview-muted-foreground)]">
					<span className="min-w-0 truncate">feat/github-auth → main</span>
				</div>
				<div className="flex min-w-0 flex-wrap items-center gap-x-1 text-[var(--preview-muted-foreground)]">
					<span>{phase.diff.files} files</span>
					<span className="text-[var(--preview-passive)]">·</span>
					<span style={{ color: status.ready }}>+{phase.diff.additions}</span>
					<span className="text-[var(--preview-passive)]">·</span>
					<span style={{ color: status.exited }}>-{phase.diff.deletions}</span>
				</div>
			</div>
			<div className="mt-2 border-t border-[var(--preview-border)] pt-2">
				<div className="flex min-w-0 items-center gap-2">
					<div className="flex min-w-0 flex-1 flex-col gap-1">
						<div className="flex min-w-0 items-start gap-1.5">
							<span
								aria-hidden="true"
								className={`mt-[5px] size-[5px] shrink-0 rounded-full ${phase.primary.breathe ? "animate-pulse" : ""}`}
								style={{ background: partToneColor[phase.primary.tone] }}
							/>
							<div className="min-w-0 flex-1">
								<div className="text-[9px] font-semibold leading-[1.4]" style={{ color: partToneColor[phase.primary.tone] }}>
									{phase.primary.label}
								</div>
								{phase.primary.link ? (
									<div className="mt-0.5 flex min-w-0 font-mono text-[8px]">
										<span className="inline-flex min-w-0 items-center gap-0.5" style={{ color: partToneColor[phase.primary.tone] }}>
											<span className="truncate">{phase.primary.link}</span>
											<ArrowUpRight aria-hidden="true" className="size-2 shrink-0" strokeWidth={2} />
										</span>
									</div>
								) : null}
							</div>
						</div>
						{phase.supporting.length > 0 ? (
							<div className="min-w-0 pl-3">
								<div className="flex min-w-0 flex-wrap gap-x-2.5 gap-y-0.5 font-mono text-[8px]">
									{phase.supporting.map((s) => (
										<span
											className="inline-flex items-center gap-1"
											key={s.label}
											style={{ color: partToneColor[s.tone] }}
										>
											<span
												aria-hidden="true"
												className={`size-[3px] shrink-0 rounded-full ${s.breathe ? "animate-pulse" : ""}`}
												style={{ background: partToneColor[s.tone] }}
											/>
											{s.label}
										</span>
									))}
								</div>
							</div>
						) : null}
					</div>
					{phase.mergeable && mergeState === "idle" ? (
						<span
							data-cursor-target="merge-button"
							className="inline-flex h-[20px] shrink-0 items-center gap-1 rounded-md px-1.5 text-[8px] font-medium"
							style={{ background: "var(--preview-primary)", color: "var(--preview-primary-foreground)" }}
						>
							<GitMerge aria-hidden="true" className="size-2.5 shrink-0" />
							Merge
						</span>
					) : mergeState === "merging" ? (
						<span
							data-cursor-target="merge-button"
							className="inline-flex h-[20px] shrink-0 items-center gap-1 rounded-md px-1.5 text-[8px] font-medium opacity-70"
							style={{ background: "var(--preview-primary)", color: "var(--preview-primary-foreground)" }}
						>
							<span className="size-2.5 shrink-0 animate-spin rounded-full border border-current border-t-transparent" />
							Merging…
						</span>
					) : null}
				</div>
			</div>
		</article>
	);
}

/* ── Reviews section ───────────────────────────────────────────────────────── */

function ReviewsSection() {
	return (
		<div className="flex flex-col gap-1.5">
			<div className="flex items-center justify-between">
				<span className="text-[9px] font-medium text-[var(--preview-foreground)]">AO Code Review</span>
				<span
					className="inline-flex h-[18px] items-center rounded-md px-1.5 text-[8px] font-medium text-[var(--preview-muted-foreground)]"
					style={{ background: "var(--preview-input)" }}
				>
					Run
				</span>
			</div>
			<div className="flex items-center gap-1.5 text-[8.5px] text-[var(--preview-passive)]">
				<span className="size-[5px] shrink-0 rounded-full" style={{ background: "var(--preview-passive)" }} />
				<span>Not run</span>
			</div>
		</div>
	);
}

/* ── Completion ────────────────────────────────────────────────────────────── */

function CompletionControls() {
	return (
		<div className="flex items-center justify-between gap-3 py-0.5">
			<span className="min-w-0 text-[10px] font-medium text-[var(--preview-foreground)]">Terminate on merge</span>
			<span
				aria-hidden="true"
				className="relative inline-flex h-[17px] w-[30px] shrink-0 items-center rounded-full border-[1.5px] border-transparent bg-[var(--preview-muted)]"
			>
				<span className="block h-[14px] w-[17px] rounded-full bg-[var(--preview-foreground)]" />
			</span>
		</div>
	);
}

/* ── Activity timeline ─────────────────────────────────────────────────────── */

function ActivityTimeline({ phase }: { phase: Phase }) {
	const events: { id: string; tone: "now" | "neutral"; node: ReactNode; ts: string | null }[] = [
		{
			id: "current",
			tone: "now",
			node: (
				<span className="inline-flex flex-wrap items-center gap-1.5">
					<TimelinePill {...phase.activity} />
					{phase.scm.map((pill) => (
						<TimelinePill key={pill.label} {...pill} />
					))}
				</span>
			),
			ts: null,
		},
		{
			id: "created",
			tone: "neutral",
			node: <span className="text-[var(--preview-foreground)]">Created workspace</span>,
			ts: "32m ago",
		},
	];

	if (phase.hasPullRequest) {
		events.splice(1, 0, {
			id: "opened",
			tone: "neutral",
			node: (
				<span className="inline-flex min-w-0 items-center gap-0.5 text-[var(--preview-foreground)]">
					Opened&nbsp;<b className="font-semibold">PR #2481</b>
					<ArrowUpRight aria-hidden="true" className="size-2.5 shrink-0" strokeWidth={2} />
				</span>
			),
			ts: "18m ago",
		});
	}

	return (
		<div className="relative pl-4">
			{events.map((event, index) => (
				<div key={event.id} className="relative pb-2.5 last:pb-0">
					{index < events.length - 1 ? (
						<span
							aria-hidden="true"
							className={`absolute -bottom-[7px] -left-[11px] w-px bg-[var(--preview-border)] ${
								event.tone === "now" ? "top-1/2" : "top-[8px]"
							}`}
						/>
					) : null}
					<div className="relative flex min-h-[8px] items-center">
						<span
							aria-hidden="true"
							className={`absolute -left-[14px] size-[7px] rounded-full ${
								event.tone === "now" ? "top-1/2 -translate-y-1/2" : "top-[4px]"
							}`}
							style={{
								background: event.tone === "now" ? status.working : "var(--preview-passive)",
								boxShadow:
									event.tone === "now"
										? `0 0 0 2.5px var(--preview-background), 0 0 6px ${status.working}`
										: "0 0 0 2.5px var(--preview-background)",
							}}
						/>
						<div className="min-w-0 text-[9.5px] leading-normal">{event.node}</div>
					</div>
					{event.ts ? (
						<div className="mt-0.5 font-mono text-[8px] text-[var(--preview-passive)]">{event.ts}</div>
					) : null}
				</div>
			))}
		</div>
	);
}

function TimelinePill({ label, tone, breathe }: Pill) {
	return (
		<span
			className="inline-flex shrink-0 items-center gap-1 whitespace-nowrap text-[9px] font-semibold"
			style={{ color: tone }}
		>
			<span className={`size-[5px] shrink-0 rounded-full ${breathe ? "animate-pulse" : ""}`} style={{ background: tone }} />
			{label}
		</span>
	);
}

/* ── Inspector tab icons ───────────────────────────────────────────────────── */

function SummaryIcon() {
	return (
		<svg className="size-3 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" aria-hidden="true">
			<line x1="8" y1="7" x2="20" y2="7" />
			<line x1="8" y1="12" x2="20" y2="12" />
			<line x1="8" y1="17" x2="16" y2="17" />
			<circle cx="4" cy="7" r="1" />
			<circle cx="4" cy="12" r="1" />
			<circle cx="4" cy="17" r="1" />
		</svg>
	);
}

function BrowserIcon() {
	return (
		<svg className="size-3 shrink-0" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" aria-hidden="true">
			<circle cx="12" cy="12" r="9" />
			<line x1="3" y1="12" x2="21" y2="12" />
			<path d="M12 3a14 14 0 0 1 0 18 14 14 0 0 1 0-18" />
		</svg>
	);
}
