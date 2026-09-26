"use client";

import { AnimatePresence, LayoutGroup, motion } from "motion/react";
import { createContext, useContext, useEffect, useRef, useState, type ImgHTMLAttributes } from "react";
import { featurePreviewTokens } from "./preview-tokens";
import { usePreviewScale } from "./use-preview-scale";

type ColumnId = "building" | "validating" | "needs_review" | "ready";

type PreviewPullRequest = {
	number: number;
	commentCount?: number;
	reviewers?: string[];
};

interface Card {
	id: string;
	title: string;
	branch: string;
	icon: string;
	column: ColumnId;
	displayStatus: string;
	needsAttention?: boolean;
	showSpinner?: boolean;
	pr?: PreviewPullRequest;
	time: string;
	merging?: boolean;
}

export type FleetBoardAssets = Readonly<Record<string, string>>;
const FleetBoardAssetsContext = createContext<FleetBoardAssets>({});

function FleetBoardImage({ src, ...props }: ImgHTMLAttributes<HTMLImageElement>) {
	const assets = useContext(FleetBoardAssetsContext);
	return <img {...props} src={typeof src === "string" ? (assets[src] ?? src) : src} />;
}

const COLUMN_CONFIG: Record<
	ColumnId,
	{ title: string; color: string; statusClassName: string; weight: number }
> = {
	building: {
		title: "Building",
		color: "#60a5fa",
		statusClassName: "text-[#60a5fa]",
		weight: 4,
	},
	validating: {
		title: "Validating",
		color: "#facc15",
		statusClassName: "text-[#facc15]",
		weight: 3,
	},
	needs_review: {
		title: "In review",
		color: "#f59e0b",
		statusClassName: "text-[#f59e0b]",
		weight: 2,
	},
	ready: {
		title: "Ready",
		color: "#4ade80",
		statusClassName: "text-[#4ade80]",
		weight: 1,
	},
};

const COLUMN_ORDER: ColumnId[] = ["building", "validating", "needs_review", "ready"];

const IN_PROGRESS_STATUSES = new Set(["Working", "Fixing CI failures", "Addressing comments", "Review pending", "Reviewing"]);

const REVIEWERS_LIST = [
	"https://avatars.githubusercontent.com/u/212377671?v=4&s=36",
	"https://avatars.githubusercontent.com/u/11289825?v=4&s=36",
	"https://avatars.githubusercontent.com/u/96483690?v=4&s=36",
	"https://avatars.githubusercontent.com/u/73213873?v=4&s=36",
];

const RELATIVE_TIMES = ["2m ago", "4m ago", "8m ago", "14m ago", "21m ago", "31m ago"];
function randomTime() {
	return RELATIVE_TIMES[Math.floor(Math.random() * RELATIVE_TIMES.length)] as string;
}
function randomDelay() {
	return 5000 + Math.random() * 6000;
}
function pickRandom<T>(arr: T[]): T {
	return arr[Math.floor(Math.random() * arr.length)] as T;
}

function pickReviewers(): string[] {
	const shuffled = [...REVIEWERS_LIST].sort(() => Math.random() - 0.5);
	return shuffled.slice(0, 1 + Math.floor(Math.random() * 2));
}

function randomPullRequestNumber() {
	return 300 + Math.floor(Math.random() * 40);
}

function advanceCard(card: Card): Card {
	if (card.column === "building") {
		const displayStatus = pickRandom(["Fixing CI failures", "Addressing comments"]);
		return {
			...card,
			column: "validating",
			displayStatus,
			showSpinner: IN_PROGRESS_STATUSES.has(displayStatus),
			needsAttention: false,
			pr: { number: randomPullRequestNumber() },
			time: randomTime(),
		};
	}
	if (card.column === "validating") {
		const displayStatus = pickRandom(["Review pending", "Needs human review"]);
		return {
			...card,
			column: "needs_review",
			displayStatus,
			showSpinner: displayStatus === "Review pending",
			needsAttention: displayStatus === "Needs human review",
			pr: card.pr ?? { number: randomPullRequestNumber() },
			time: randomTime(),
		};
	}
	if (card.column === "needs_review") {
		return {
			...card,
			column: "ready",
			displayStatus: "Mergeable",
			showSpinner: false,
			needsAttention: false,
			pr: card.pr ?? { number: randomPullRequestNumber() },
			time: randomTime(),
		};
	}
	return card;
}

const INITIAL_CARDS: Card[] = [
	{
		id: "c1",
		title: "Build screenshot-ready dashboard data",
		branch: "demo/dashboard-screenshot",
		icon: "/app-icons/cursor.svg",
		column: "building",
		displayStatus: "Working",
		showSpinner: true,
		time: "2m ago",
	},
	{
		id: "c2",
		title: "Tighten hero window border alignment",
		branch: "landing/window-border-pass",
		icon: "/app-icons/coverage-claude-code.svg",
		column: "building",
		displayStatus: "Working",
		showSpinner: true,
		time: "14m ago",
	},
	{
		id: "c3",
		title: "Fix flaky NewTaskDialog smoke test",
		branch: "demo/new-task-flake",
		icon: "/app-icons/coverage-codex.svg",
		column: "validating",
		displayStatus: "Fixing CI failures",
		showSpinner: true,
		pr: { number: 324 },
		time: "46m ago",
	},
	{
		id: "c4",
		title: "Resolve reviewer feedback on terminal polish",
		branch: "demo/terminal-polish",
		icon: "/app-icons/coverage-claude-code.svg",
		column: "validating",
		displayStatus: "Addressing comments",
		showSpinner: true,
		needsAttention: true,
		pr: { number: 318, commentCount: 2, reviewers: [REVIEWERS_LIST[0]!, REVIEWERS_LIST[2]!] },
		time: "18m ago",
	},
	{
		id: "c5",
		title: "Wait for CI on project settings copy",
		branch: "demo/project-settings-copy",
		icon: "/app-icons/opencode.svg",
		column: "needs_review",
		displayStatus: "Review pending",
		showSpinner: true,
		pr: { number: 322 },
		time: "31m ago",
	},
	{
		id: "c6",
		title: "Merge README screenshot asset update",
		branch: "demo/readme-assets",
		icon: "/app-icons/coverage-codex.svg",
		column: "ready",
		displayStatus: "Mergeable",
		pr: { number: 323 },
		time: "5m ago",
	},
];

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

function MessageSquareIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<path
				d="M3.5 3.5h9v6.5H8.2L5.5 13V10H3.5V3.5Z"
				stroke="currentColor"
				strokeLinejoin="round"
				strokeWidth="1.3"
			/>
		</svg>
	);
}

function StatusSpinner({ className = "" }: { className?: string }) {
	return (
		<span
			aria-hidden="true"
			className={`inline-block animate-spin rounded-full border border-[#4b5563] border-t-[#d1d5db] ${className}`}
		/>
	);
}

function statusClassName(card: Card): string {
	if (card.needsAttention) return "text-[#fb923c]";
	if (card.displayStatus === "Mergeable") return "text-[#4ade80]";
	return COLUMN_CONFIG[card.column].statusClassName;
}

function BoardCard({ card }: { card: Card }) {
	const showBranch = card.branch !== "" && card.branch !== card.title && card.branch !== card.id;
	const commentCount = card.pr?.commentCount ?? 0;

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
			className={`rounded-[6px] border bg-[var(--preview-card)] shadow-[0_1px_1px_rgba(0,0,0,0.05)] ${
				card.needsAttention
					? "ao-attention-pulse border-[#fb923c]/60 bg-[color-mix(in_srgb,#fb923c_8%,var(--preview-card))]"
					: "border-[var(--preview-border)]"
			}`}
		>
			<div className="px-2.5 pb-2 pt-2.5">
				<div className="flex min-w-0 items-center gap-2">
					<FleetBoardImage
						src={card.icon}
						alt=""
						width={12}
						height={12}
						aria-hidden="true"
						draggable="false"
						className="size-3 shrink-0 object-contain"
					/>
					<div className="line-clamp-2 min-w-0 flex-1 text-[8px] font-semibold leading-tight tracking-tight text-[var(--preview-card-foreground)]">
						{card.title}
					</div>
				</div>
				{showBranch ? (
					<div className="mt-1 flex min-w-0 items-center gap-1 font-mono text-[7px] text-[var(--preview-muted-foreground)]">
						<BranchIcon className="size-2.5 shrink-0" />
						<span className="truncate">{card.branch}</span>
					</div>
				) : null}
			</div>

			{card.pr ? (
				<div className="flex min-w-0 items-center gap-1 px-2.5 pb-1 font-mono text-[7px] text-[var(--preview-muted-foreground)]">
					<PullRequestIcon className="size-2.5 shrink-0 text-[#4ade80]" />
					<span className="font-medium text-[var(--preview-card-foreground)]">#{card.pr.number}</span>
					{commentCount > 0 && card.pr.reviewers && card.pr.reviewers.length > 0 ? (
						<div className="flex -space-x-1 pl-0.5">
							{card.pr.reviewers.slice(0, 3).map((src) => (
								<img
									key={src}
									src={src}
									alt=""
									width={10}
									height={10}
									aria-hidden="true"
									draggable="false"
									className="size-2.5 rounded-full ring-1 ring-[var(--preview-card)]"
								/>
							))}
						</div>
					) : null}
					{commentCount > 0 ? (
						<span className="ml-auto inline-flex shrink-0 items-center gap-0.5 tabular-nums">
							<MessageSquareIcon className="size-2 shrink-0" />
							{commentCount}
						</span>
					) : null}
				</div>
			) : null}

			<div className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-x-2 border-t border-[var(--preview-border)] px-2.5 py-2">
				<span className={`inline-flex min-w-0 items-center text-[7.5px] font-medium ${statusClassName(card)}`}>
					{card.showSpinner && !card.needsAttention ? (
						<StatusSpinner className="mr-1 size-2 shrink-0" />
					) : null}
					<span className="truncate">{card.displayStatus}</span>
				</span>
				<span className="shrink-0 font-mono text-[7px] tabular-nums text-[var(--preview-muted-foreground)]">
					{card.time}
				</span>
			</div>
		</motion.div>
	);
}

function BoardColumn({ cards, color, title }: { cards: Card[]; color: string; title: string }) {
	const ordered = [...cards].sort(
		(left, right) => Number(Boolean(right.needsAttention)) - Number(Boolean(left.needsAttention)),
	);

	return (
		<section className="flex min-h-0 min-w-0 snap-start flex-col border-r border-[var(--preview-border)] last:border-r-0">
			<div className="flex h-9 shrink-0 items-center gap-2 border-b border-[var(--preview-border)] px-3">
				<span className="size-1.5 rounded-full" style={{ backgroundColor: color }} />
				<div className="truncate text-[8px] font-medium tracking-wide text-[var(--preview-muted-foreground)]">{title}</div>
				<div className="ml-auto font-mono text-[8px] leading-none tabular-nums text-[var(--preview-muted-foreground)] opacity-60">
					{cards.length}
				</div>
			</div>
			<div className="min-h-0 flex-1 space-y-1.5 overflow-y-auto px-2 pb-2 pt-2 scrollbar-hide">
				<AnimatePresence initial={false}>
					{ordered.map((card) => (
						<BoardCard key={card.id} card={card} />
					))}
				</AnimatePresence>
			</div>
		</section>
	);
}

export function FleetBoardDemo({ assets = {} }: { assets?: FleetBoardAssets }) {
	const [cards, setCards] = useState<Card[]>(INITIAL_CARDS);
	const incomingIdx = useRef(0);
	const { viewportRef, viewportStyle, canvasStyle } = usePreviewScale(570, 318);

	const mergeCard = (id: string) => {
		setCards((current) => current.filter((card) => card.id !== id));
	};

	useEffect(() => {
		let timeoutId: number;
		const scheduleNext = () => {
			timeoutId = window.setTimeout(runStep, randomDelay());
		};

		const runStep = () => {
			setCards((current) => {
				let total = 0;
				for (const card of current) {
					if (!card.merging) total += COLUMN_CONFIG[card.column].weight;
				}
				let threshold = Math.random() * total;
				let chosen: Card | null = null;
				for (const card of current) {
					if (card.merging) continue;
					threshold -= COLUMN_CONFIG[card.column].weight;
					if (threshold <= 0) {
						chosen = card;
						break;
					}
				}
				if (!chosen) chosen = current.find((card) => !card.merging) ?? null;

				let next = current;
				if (chosen) {
					if (chosen.column === "ready") {
						window.setTimeout(() => mergeCard(chosen!.id), 0);
						next = next.map((card) => (card.id === chosen!.id ? { ...card, merging: true } : card));
					} else {
						next = next.map((card) => (card.id === chosen!.id ? advanceCard(card) : card));
					}
				}

				for (let index = 1; index < COLUMN_ORDER.length; index++) {
					const column = COLUMN_ORDER[index]!;
					const previous = COLUMN_ORDER[index - 1]!;
					if (next.filter((card) => card.column === column && !card.merging).length === 0) {
						const donor = next.find((card) => card.column === previous && !card.merging);
						if (donor) next = next.map((card) => (card.id === donor.id ? advanceCard(card) : card));
					}
				}

				if (Math.random() < 0.1) {
					const candidates = next.filter(
						(card) => !card.merging && !card.needsAttention && card.column !== "ready" && card.column !== "building",
					);
					const target = candidates[Math.floor(Math.random() * candidates.length)];
					if (target) {
						next = next.map((card) =>
							card.id === target.id
								? {
										...card,
										displayStatus: pickRandom(["Changes requested", "CI failing", "Blocked"]),
										needsAttention: true,
										showSpinner: false,
										pr: {
											number: card.pr?.number ?? randomPullRequestNumber(),
											commentCount: 1 + Math.floor(Math.random() * 3),
											reviewers: pickReviewers(),
										},
										time: randomTime(),
									}
								: card,
						);
					}
				}

				const buildingCount = next.filter((card) => card.column === "building" && !card.merging).length;
				if (buildingCount < 2 && next.filter((card) => !card.merging).length < 8) {
					const newId = `spawned-${++incomingIdx.current}`;
					const templates = [
						{
							title: "Throttle agent spawn rate under load",
							branch: "backend/spawn-throttle",
							icon: "/app-icons/coverage-claude-code.svg",
						},
						{
							title: "Add keyboard shortcut for session focus",
							branch: "feat/session-focus-shortcut",
							icon: "/app-icons/coverage-codex.svg",
						},
						{
							title: "Lazy-load session terminal on first open",
							branch: "perf/lazy-terminal",
							icon: "/app-icons/cursor.svg",
						},
					];
					const template = templates[incomingIdx.current % templates.length]!;
					next = [
						{
							id: newId,
							title: template.title,
							branch: template.branch,
							icon: template.icon,
							column: "building",
							displayStatus: "Working",
							showSpinner: true,
							time: randomTime(),
						},
						...next,
					];
				}

				return next;
			});
			scheduleNext();
		};

		scheduleNext();
		return () => window.clearTimeout(timeoutId);
	}, []);

	const boardColumns = COLUMN_ORDER.map((id) => ({
		id,
		...COLUMN_CONFIG[id],
		cards: cards.filter((card) => card.column === id),
	}));

	return (
		<FleetBoardAssetsContext.Provider value={assets}>
			<div ref={viewportRef} className="relative mx-auto w-full min-w-0 max-w-[570px]" style={viewportStyle}>
				<div
					className="absolute left-0 top-0 overflow-hidden rounded-[10px] border border-[var(--preview-border)] bg-[var(--preview-background)] text-[9px] text-[var(--preview-foreground)] shadow-[0_24px_64px_-20px_rgba(0,0,0,0.8)]"
					style={{
						...featurePreviewTokens,
						...canvasStyle,
						fontFamily: "var(--font-geist-sans), ui-sans-serif, system-ui, sans-serif",
					}}
				>
					<style>{`
						@keyframes ao-attention-pulse-frames {
							0%, 100% { box-shadow: 0 0 0 0 rgba(251, 146, 60, 0.35); }
							50% { box-shadow: 0 0 0 4px rgba(251, 146, 60, 0); }
						}
						.ao-attention-pulse { animation: ao-attention-pulse-frames 2.2s ease-in-out infinite; }
						@media (prefers-reduced-motion: reduce) { .ao-attention-pulse { animation: none; } }
					`}</style>
					<LayoutGroup>
						<div className="grid h-full min-h-0 grid-cols-4 divide-x divide-[var(--preview-border)] overflow-hidden">
							{boardColumns.map((column) => (
								<BoardColumn key={column.id} cards={column.cards} color={column.color} title={column.title} />
							))}
						</div>
					</LayoutGroup>
				</div>
			</div>
		</FleetBoardAssetsContext.Provider>
	);
}
