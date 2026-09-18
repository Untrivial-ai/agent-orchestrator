"use client";

import { AnimatePresence, LayoutGroup, motion } from "motion/react";
import { useEffect, useRef, useState } from "react";
import { featurePreviewTokens } from "../FeaturePreviewShell";
import { usePreviewScale } from "../usePreviewScale";

// ── Types ─────────────────────────────────────────────────────────────────────

type ColumnId = "working" | "staging" | "in_review" | "merge";
type ActivityState = "running" | "passed" | "reviewing" | "waiting";

interface Card {
	id: string;
	title: string;
	branch: string;
	icon: string;
	column: ColumnId;
	activity: string;
	activityState: ActivityState;
	pr: string;
	time: string;
	merging?: boolean;
	testResults?: { pass: number; total: number };
	prComments?: number;
	reviewers?: string[];
}

// ── Constants ─────────────────────────────────────────────────────────────────

const COLUMN_CONFIG: Record<ColumnId, { title: string; color: string; weight: number }> = {
	working:   { title: "Pending Work",   color: "#60a5fa", weight: 4 },
	staging:   { title: "Iterating",      color: "#a78bfa", weight: 3 },
	in_review: { title: "In Review",      color: "#facc15", weight: 2 },
	merge:     { title: "Ready to merge", color: "#4ade80", weight: 1 },
};

const COLUMN_ORDER: ColumnId[] = ["working", "staging", "in_review", "merge"];

const REVIEWERS_LIST = [
	"https://avatars.githubusercontent.com/u/212377671?v=4&s=36",
	"https://avatars.githubusercontent.com/u/11289825?v=4&s=36",
	"https://avatars.githubusercontent.com/u/96483690?v=4&s=36",
	"https://avatars.githubusercontent.com/u/73213873?v=4&s=36",
	"https://avatars.githubusercontent.com/u/44542765?v=4&s=36",
	"https://avatars.githubusercontent.com/u/40922251?v=4&s=36",
];

const RELATIVE_TIMES = ["2m ago", "4m ago", "8m ago", "14m ago", "21m ago", "31m ago"];
function randomTime() { return RELATIVE_TIMES[Math.floor(Math.random() * RELATIVE_TIMES.length)] as string; }
function randomDelay() { return 5000 + Math.random() * 6000; }
function pickRandom<T>(arr: T[]): T { return arr[Math.floor(Math.random() * arr.length)] as T; }

function pickReviewers(): string[] {
	const shuffled = [...REVIEWERS_LIST].sort(() => Math.random() - 0.5);
	return shuffled.slice(0, 1 + Math.floor(Math.random() * 3));
}

function pickTestResults(): { pass: number; total: number } {
	const total = pickRandom([28, 42, 50, 54, 60]);
	const pass = Math.floor(total * (0.3 + Math.random() * 0.4));
	return { pass, total };
}

const STAGING_ACTIVITIES = [
	"Running tests", "Building...", "Type checking", "Linting codebase", "Running CI pipeline",
];
const IN_REVIEW_ACTIVITIES = [
	"Reviewer assigned", "Awaiting review", "Review in progress", "Second reviewer added",
];
const MERGE_ACTIVITIES = [
	"Ready to land", "Approved · ready to merge", "All checks passed", "LGTM", "Approved by 2 reviewers",
];

function advanceCard(card: Card): Card {
	if (card.column === "working") {
		return {
			...card,
			column: "staging",
			activity: pickRandom(STAGING_ACTIVITIES),
			activityState: "running",
			time: randomTime(),
			testResults: pickTestResults(),
			reviewers: undefined,
			prComments: undefined,
		};
	}
	if (card.column === "staging") {
		return {
			...card,
			column: "in_review",
			activity: pickRandom(IN_REVIEW_ACTIVITIES),
			activityState: "reviewing",
			time: randomTime(),
			testResults: undefined,
			reviewers: pickReviewers(),
			prComments: Math.floor(Math.random() * 4),
		};
	}
	if (card.column === "in_review") {
		return {
			...card,
			column: "merge",
			activity: pickRandom(MERGE_ACTIVITIES),
			activityState: "passed",
			time: randomTime(),
			reviewers: card.reviewers ?? pickReviewers(),
			prComments: card.prComments ?? 0,
		};
	}
	return card;
}

// ── Seed data ─────────────────────────────────────────────────────────────────

const INITIAL_CARDS: Card[] = [
	{
		id: "c1", title: "Confirm download labels are platform-aware",
		branch: "landing/platform-copy", icon: "/app-icons/agents/cursor.svg",
		column: "working", activity: "Editing copy", activityState: "running",
		pr: "draft", time: "14m ago",
	},
	{
		id: "c2", title: "Port Figma board mock into the hero preview",
		branch: "landing/figma-board", icon: "/app-icons/coverage-claude-code.svg",
		column: "working", activity: "Editing component", activityState: "running",
		pr: "PR #318", time: "8m ago",
	},
	{
		id: "c3", title: "Run integration tests on webhook handler",
		branch: "webhooks/integration-tests", icon: "/app-icons/coverage-codex.svg",
		column: "staging", activity: "Running tests", activityState: "running",
		pr: "draft", time: "22m ago",
		testResults: { pass: 18, total: 50 },
	},
	{
		id: "c4", title: "Migrate auth tokens to short-lived JWTs",
		branch: "auth/jwt-rotation", icon: "/app-icons/coverage-codex.svg",
		column: "staging", activity: "Building...", activityState: "running",
		pr: "PR #331", time: "34m ago",
		testResults: { pass: 22, total: 60 },
	},
	{
		id: "c5", title: "Preload GitHub stars before hydration",
		branch: "landing/preload-stars", icon: "/app-icons/coverage-claude-code.svg",
		column: "in_review", activity: "Awaiting review", activityState: "reviewing",
		pr: "PR #324", time: "1h ago",
		prComments: 2,
		reviewers: [REVIEWERS_LIST[0], REVIEWERS_LIST[2]],
	},
	{
		id: "c6", title: "Stabilize Vercel framework detection",
		branch: "deploy/vercel-next-config", icon: "/app-icons/opencode.svg",
		column: "merge", activity: "Ready to land", activityState: "passed",
		pr: "PR #327", time: "3h ago",
		prComments: 0,
		reviewers: [REVIEWERS_LIST[1], REVIEWERS_LIST[3]],
	},
];

// ── SVG Icons ─────────────────────────────────────────────────────────────────

function BranchIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<circle cx="4.5" cy="3.5" r="1.5" stroke="currentColor" strokeWidth="1.3" />
			<circle cx="4.5" cy="12.5" r="1.5" stroke="currentColor" strokeWidth="1.3" />
			<circle cx="11.5" cy="5.5" r="1.5" stroke="currentColor" strokeWidth="1.3" />
			<path d="M4.5 5v6" stroke="currentColor" strokeLinecap="round" strokeWidth="1.3" />
			<path d="M4.5 3.5C4.5 7 11.5 7 11.5 7" stroke="currentColor" strokeLinecap="round" strokeWidth="1.3" />
		</svg>
	);
}

function PullRequestIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<circle cx="4.5" cy="3.5" r="1.5" stroke="currentColor" strokeWidth="1.3" />
			<circle cx="4.5" cy="12.5" r="1.5" stroke="currentColor" strokeWidth="1.3" />
			<circle cx="11.5" cy="12.5" r="1.5" stroke="currentColor" strokeWidth="1.3" />
			<path d="M4.5 5v6" stroke="currentColor" strokeLinecap="round" strokeWidth="1.3" />
			<path d="M11.5 5v6" stroke="currentColor" strokeLinecap="round" strokeWidth="1.3" />
			<path d="M8.5 3.5h1A2 2 0 0 1 11.5 5.5" stroke="currentColor" strokeLinecap="round" strokeWidth="1.3" />
			<path d="m6.8 1.8 1.7 1.7-1.7 1.7" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.3" />
		</svg>
	);
}

function CheckIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<circle cx="8" cy="8" r="5.5" stroke="currentColor" strokeWidth="1.4" />
			<path d="M5.5 8l2 2 3-3" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.4" />
		</svg>
	);
}

function WaitingIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<circle cx="8" cy="8" r="5.5" stroke="currentColor" strokeWidth="1.4" />
			<path d="M6.3 5.8v4.4M9.7 5.8v4.4" stroke="currentColor" strokeLinecap="round" strokeWidth="1.5" />
		</svg>
	);
}

function ReviewingIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<circle cx="8" cy="8" r="5.5" stroke="currentColor" strokeWidth="1.4" />
			<path d="M8 5.5V8l1.8 1.8" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.4" />
		</svg>
	);
}

function CircleProgressIcon({ pass, total }: { pass: number; total: number }) {
	const r = 5;
	const circ = 2 * Math.PI * r;
	const ratio = total > 0 ? pass / total : 0;
	const dash = ratio * circ;
	const fillColor = ratio >= 1 ? "#4ade80" : ratio < 0.5 ? "#fb923c" : "#e5e7eb";
	return (
		<svg width="10" height="10" viewBox="0 0 12 12" fill="none" aria-hidden="true" style={{ flexShrink: 0 }}>
			<circle cx="6" cy="6" r={r} stroke="#374151" strokeWidth="1.5" />
			<circle cx="6" cy="6" r={r} stroke={fillColor} strokeWidth="1.5"
				strokeDasharray={`${dash} ${circ}`} strokeLinecap="round"
				transform="rotate(-90 6 6)" />
		</svg>
	);
}

function ActivityIcon({ state, testResults }: { state: ActivityState; testResults?: { pass: number; total: number } }) {
	if (state === "passed") return <CheckIcon className="h-2.5 w-2.5 shrink-0" />;
	if (state === "waiting") return <WaitingIcon className="h-2.5 w-2.5 shrink-0" />;
	if (state === "reviewing") return <ReviewingIcon className="h-2.5 w-2.5 shrink-0" />;
	if (testResults) return <CircleProgressIcon pass={testResults.pass} total={testResults.total} />;
	return <span className="h-2.5 w-2.5 shrink-0 animate-spin rounded-full border border-[#4b5563] border-t-[#d1d5db]" />;
}

// ── AnimatedTestCount ─────────────────────────────────────────────────────────

function AnimatedTestCount({ testResults }: { testResults: { pass: number; total: number } }) {
	const total = testResults.total;
	const [animPass, setAnimPass] = useState(testResults.pass);

	useEffect(() => {
		let timeout: number;
		const tick = () => {
			setAnimPass((p) => {
				const jump = Math.floor(Math.random() * 4) + 1;
				const next = p + jump;
				return next >= total ? Math.floor(total * 0.2) : next;
			});
			timeout = window.setTimeout(tick, 300 + Math.random() * 600);
		};
		timeout = window.setTimeout(tick, 300 + Math.random() * 600);
		return () => window.clearTimeout(timeout);
	}, [total]);

	return (
		<>
			<ActivityIcon state="running" testResults={{ pass: animPass, total }} />
			{`${animPass}/${total} passed`}
		</>
	);
}

// ── BoardCard ─────────────────────────────────────────────────────────────────

function BoardCard({ card, isPulsing }: { card: Card; isPulsing: boolean }) {
	const prMatch = card.pr.match(/PR\s+#(\d+)/i);
	const isWaiting = card.activityState === "waiting";
	const isReviewFlag = isWaiting && card.column === "in_review";

	const activityColor =
		card.activityState === "passed" ? "text-[#4ade80]"
		: card.activityState === "waiting" ? (isReviewFlag ? "text-[#f87171]" : "text-[#fb923c]")
		: card.activityState === "reviewing" ? "text-[#facc15]"
		: "text-[var(--preview-muted-foreground)]";

	const attentionBorder = isWaiting
		? isReviewFlag ? "border-[#f87171]/70" : "border-[#fb923c]/60"
		: "border-[var(--preview-border)]";
	const badgeColor = isReviewFlag ? "bg-[#f87171]" : "bg-[#fb923c]";
	const attentionAnim = isWaiting && isPulsing ? "ao-attention-pulse" : "";
	const isTestCard = card.column === "staging" && !!card.testResults;

	return (
		<motion.div
			layout
			layoutId={`${card.id}-${card.column}`}
			initial={{ opacity: 0, scale: 0.98, y: -8 }}
			animate={card.merging ? { opacity: 0, scale: 0.96, y: -8 } : { opacity: 1, scale: 1, y: 0 }}
			exit={{ opacity: 0, scale: 0.96, y: -8 }}
			transition={{
				duration: 0.45,
				ease: [0.22, 1, 0.36, 1],
				layout: { duration: 0.55, ease: [0.22, 1, 0.36, 1] },
			}}
			className={`rounded-[6px] border ${attentionBorder} bg-[var(--preview-card)] shadow-[0_1px_1px_rgba(0,0,0,0.05)] ${attentionAnim}`}
		>
			{/* Title row */}
			<div className="flex items-start gap-2 px-2.5 pb-2 pt-2.5">
				<div className="relative mt-0.5 h-3 w-3 shrink-0">
					<img src={card.icon} alt="" width={12} height={12}
						aria-hidden="true" draggable="false" className="h-3 w-3" />
					{isWaiting ? (
						<span aria-hidden="true"
							className={`pointer-events-none absolute -right-0.5 -top-0.5 flex h-2 w-2 items-center justify-center rounded-full ${badgeColor} text-[6px] font-black leading-none text-white shadow-[0_0_0_1px_var(--preview-card)]`}>
							!
						</span>
					) : null}
				</div>
				<div className="line-clamp-2 min-w-0 text-[8px] font-semibold leading-tight tracking-tight text-[var(--preview-card-foreground)]">
					{card.title}
				</div>
			</div>

			{/* Branch + PR rows */}
			<div className="px-2.5 text-[8px] leading-[14px] text-[var(--preview-muted-foreground)]">
				<div className="flex items-center gap-1 py-0.5">
					<BranchIcon className="h-2.5 w-2.5 shrink-0" />
					<span className="truncate font-mono">{card.branch}</span>
				</div>
				{prMatch ? (
					<div className="flex items-center gap-1 py-0.5 text-[var(--preview-muted-foreground)]">
						<PullRequestIcon className="h-2.5 w-2.5 shrink-0" />
						<span className="font-mono">#{prMatch[1]}</span>
					</div>
				) : null}
			</div>

			{/* Reviewer avatars + PR comments (in_review / merge) */}
			{(card.column === "in_review" || card.column === "merge") ? (
				<div className="flex items-center justify-between px-2.5 pb-1.5 pt-1">
					<div className="flex items-center gap-1">
						{card.reviewers && card.reviewers.length > 0 ? (
							<div className="flex -space-x-1">
								{card.reviewers.slice(0, 3).map((src) => (
									<img key={src} src={src} alt="" width={14} height={14}
										aria-hidden="true" draggable="false"
										className="h-[14px] w-[14px] rounded-full ring-1 ring-[var(--preview-card)]" />
								))}
							</div>
						) : null}
					</div>
					{card.prComments !== undefined ? (
						<span className={`text-[8px] ${card.prComments > 0 ? "text-[#fb923c]" : "text-[var(--preview-muted-foreground)]"}`}>
							{card.prComments === 0 ? "0 cmt" : `${card.prComments} cmt`}
						</span>
					) : null}
				</div>
			) : null}

			{/* Activity / action row */}
			{card.column === "merge" ? (
				<div className="flex items-center justify-between gap-2 border-t border-[var(--preview-border)] px-2.5 py-2">
					<div className="inline-flex h-5 items-center justify-center whitespace-nowrap rounded-[4px] bg-[var(--preview-primary)] px-2 text-[8.5px] font-semibold text-[var(--preview-primary-foreground)]">
						Merge PR
					</div>
					<span className="shrink-0 font-mono text-[7.5px] text-[var(--preview-muted-foreground)]">{card.time}</span>
				</div>
			) : (
				<div className={`flex items-center gap-1.5 border-t border-[var(--preview-border)] px-2.5 py-2 ${activityColor}`}>
					<span className="inline-flex min-w-0 flex-1 items-center gap-1.5 text-[8px]">
						{isTestCard ? (
							<AnimatedTestCount testResults={card.testResults!} />
						) : (
							<>
								<ActivityIcon state={card.activityState} />
								<span className="truncate">{card.activity}</span>
							</>
						)}
					</span>
					<span className="shrink-0 font-mono text-[7.5px] text-[var(--preview-muted-foreground)]">{card.time}</span>
				</div>
			)}
		</motion.div>
	);
}

// ── BoardColumn ───────────────────────────────────────────────────────────────

function BoardColumn({ cards, color, title }: { cards: Card[]; color: string; title: string }) {
	const attentionCards = cards.filter((c) => c.activityState === "waiting");
	const normalCards = cards.filter((c) => c.activityState !== "waiting");
	const sorted = [...attentionCards, ...normalCards];
	const pulsingId = attentionCards[0]?.id ?? null;
	const extraWaiting = Math.max(0, attentionCards.length - 1);

	return (
		<section className="flex min-h-0 min-w-0 snap-start flex-col border-r border-[var(--preview-border)] last:border-r-0">
			<div className="flex h-9 shrink-0 items-center gap-2 border-b border-[var(--preview-border)] px-3">
				<span className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: color }} />
				<div className="truncate text-[8px] font-medium tracking-wide text-[var(--preview-muted-foreground)]">{title}</div>
				<div className="ml-auto font-mono text-[8px] leading-none text-[var(--preview-muted-foreground)] opacity-60">{cards.length}</div>
				{extraWaiting > 0 ? (
					<div className="inline-flex items-center gap-1 rounded-[3px] bg-[#fb923c]/10 px-1 py-0.5 text-[7px] font-semibold text-[#fb923c]">
						<WaitingIcon className="h-2 w-2" />
						{extraWaiting} waiting
					</div>
				) : null}
			</div>
			<div className="min-h-0 flex-1 space-y-1.5 overflow-y-auto px-2 pb-2 pt-2 scrollbar-hide">
				<AnimatePresence initial={false}>
					{sorted.map((card) => (
						<BoardCard key={card.id} card={card} isPulsing={card.id === pulsingId} />
					))}
				</AnimatePresence>
			</div>
		</section>
	);
}

// ── FleetBoardDemo ────────────────────────────────────────────────────────────

export function FleetBoardDemo() {
	const [cards, setCards] = useState<Card[]>(INITIAL_CARDS);
	const simulation = useRef({ cards: INITIAL_CARDS, incomingIdx: 0 });
	const { viewportRef, viewportStyle, canvasStyle } = usePreviewScale(570, 318);

	useEffect(() => {
		let timeoutId: number;
		let mergeTimeoutId: number | undefined;
		const scheduleNext = () => { timeoutId = window.setTimeout(runStep, randomDelay()); };
		const mergeCard = (id: string) => {
			simulation.current.cards = simulation.current.cards.filter((card) => card.id !== id);
			setCards(simulation.current.cards);
		};

		const runStep = () => {
			// Advance the simulation once per timer tick, outside React updater callbacks.
			const current = simulation.current.cards;
			// Pick a card to advance (weighted towards earlier columns)
			let total = 0;
			for (const c of current) {
				if (!c.merging) total += COLUMN_CONFIG[c.column].weight;
			}
			let threshold = Math.random() * total;
			let chosen: Card | null = null;
			for (const c of current) {
				if (c.merging) continue;
				threshold -= COLUMN_CONFIG[c.column].weight;
				if (threshold <= 0) { chosen = c; break; }
			}
			if (!chosen) chosen = current.find((c) => !c.merging) ?? null;

			let next = current;
			if (chosen) {
				if (chosen.column === "merge") {
					mergeTimeoutId = window.setTimeout(() => mergeCard(chosen!.id), 0);
					next = next.map((c) => c.id === chosen!.id ? { ...c, merging: true } : c);
				} else {
					next = next.map((c) => c.id === chosen!.id ? advanceCard(c) : c);
				}
			}

			// Ensure no column is ever empty
			for (let i = 1; i < COLUMN_ORDER.length; i++) {
				const col = COLUMN_ORDER[i]!;
				const prev = COLUMN_ORDER[i - 1]!;
				if (next.filter((c) => c.column === col && !c.merging).length === 0) {
					const donor = next.find((c) => c.column === prev && !c.merging);
					if (donor) next = next.map((c) => c.id === donor.id ? advanceCard(c) : c);
				}
			}

			// Occasionally flip a card to waiting
			if (Math.random() < 0.1) {
				const candidates = next.filter((c) => !c.merging && c.activityState !== "waiting" && c.column !== "merge");
				const target = candidates[Math.floor(Math.random() * candidates.length)];
				if (target) {
					next = next.map((c) =>
						c.id === target.id ? {
							...c,
							activityState: "waiting" as const,
							activity: c.column === "in_review" ? "Changes requested" : "Needs your input",
							time: randomTime(),
						} : c,
					);
				}
			}

			// Spawn a new working card if column is thin
			const workingCount = next.filter((c) => c.column === "working" && !c.merging).length;
			if (workingCount < 2 && next.filter((c) => !c.merging).length < 8) {
				const newId = `spawned-${++simulation.current.incomingIdx}`;
				const templates = [
					{ title: "Throttle agent spawn rate under load",      branch: "backend/spawn-throttle",      icon: "/app-icons/coverage-claude-code.svg" },
					{ title: "Add keyboard shortcut for session focus",    branch: "feat/session-focus-shortcut", icon: "/app-icons/coverage-codex.svg"       },
					{ title: "Lazy-load session terminal on first open",   branch: "perf/lazy-terminal",          icon: "/app-icons/agents/cursor.svg"        },
					{ title: "Fix memory leak in terminal resize handler", branch: "fix/terminal-resize-leak",    icon: "/app-icons/coverage-claude-code.svg" },
					{ title: "Migrate auth tokens to short-lived JWTs",   branch: "auth/jwt-rotation",           icon: "/app-icons/opencode.svg"             },
				];
				const t = templates[simulation.current.incomingIdx % templates.length]!;
				next = [{
					id: newId,
					title: t.title,
					branch: t.branch,
					icon: t.icon,
					column: "working",
					activity: "Writing implementation",
					activityState: "running",
					pr: "draft",
					time: randomTime(),
				}, ...next];
			}

			simulation.current.cards = next;
			setCards(next);
			scheduleNext();
		};

		scheduleNext();
		return () => {
			window.clearTimeout(timeoutId);
			window.clearTimeout(mergeTimeoutId);
		};
	}, []);

	const boardColumns = COLUMN_ORDER.map((id) => ({
		id,
		...COLUMN_CONFIG[id],
		cards: cards.filter((c) => c.column === id),
	}));

	return (
		<div
			ref={viewportRef}
			className="relative mx-auto w-full min-w-0 max-w-[570px]"
			style={viewportStyle}
		>
			<div
				className="absolute left-0 top-0 overflow-hidden rounded-[10px] border border-[var(--preview-border)] bg-[var(--preview-background)] text-[9px] text-[var(--preview-foreground)] shadow-[0_24px_64px_-20px_rgba(0,0,0,0.8)]"
				style={{ ...featurePreviewTokens, ...canvasStyle, fontFamily: "var(--font-geist-sans), ui-sans-serif, system-ui, sans-serif" }}
			>
				<style>{`
					@keyframes ao-attention-pulse-frames {
						0%, 100% { box-shadow: 0 0 0 0 rgba(251, 146, 60, 0.35); }
						50%       { box-shadow: 0 0 0 4px rgba(251, 146, 60, 0); }
					}
					.ao-attention-pulse { animation: ao-attention-pulse-frames 2.2s ease-in-out infinite; }
					@media (prefers-reduced-motion: reduce) { .ao-attention-pulse { animation: none; } }
				`}</style>
				<LayoutGroup>
					<div className="grid h-full min-h-0 grid-cols-4 divide-x divide-[var(--preview-border)] overflow-hidden">
						{boardColumns.map((col) => (
							<BoardColumn key={col.id} cards={col.cards} color={col.color} title={col.title} />
						))}
					</div>
				</LayoutGroup>
			</div>
		</div>
	);
}
