"use client";

import { AnimatePresence, LayoutGroup, motion } from "motion/react";
import { useCallback, useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";

export type { ActiveDemo } from "./types";

type BoardColumnId = "working" | "action" | "pending" | "merge";
type CardTone = "default" | "review" | "blocked" | "ready";
type ActivityState = "running" | "passed" | "failed" | "reviewing" | "waiting";
type TrackId = "landing" | "deploy" | "stars" | "icons" | "footer";
type ViewMode = "board" | "orchestrator";

interface PreviewCard {
	activity: string;
	activityState: ActivityState;
	agent: string;
	badge: string | null;
	branch: string;
	column: BoardColumnId;
	checks: string;
	files: string;
	id: string;
	icon: string;
	merging?: boolean;
	pr: string;
	time: string;
	title: string;
	tone: CardTone;
	// Optional metadata for hero card boxes (from landing redesign).
	lastAction?: string;
	prComments?: number;
	reviewers?: string[];
	runtime?: string;
	testResults?: { pass: number; total: number };
}

type StaticPreviewCard = Omit<PreviewCard, "column" | "id" | "merging">;

interface PreviewColumn {
	cards: StaticPreviewCard[];
	count: number;
	id: BoardColumnId;
	title: string;
}

interface TrackItem {
	id: TrackId;
	label: string;
	summary: string;
}

const repoName = "Untrivial-ai/agent-orchestrator";
const repoAvatar = "https://github.com/Untrivial-ai.png?size=64";

const previewTokenStyle = {
	// Exact dark-theme values from frontend/src/styles/tokens.css (:root).
	"--preview-background": "oklch(0.185 0.006 285.885)", // --background
	"--preview-foreground": "oklch(0.985 0 0)", // --foreground
	"--preview-card": "oklch(0.24 0.008 285.885)", // --card / --surface
	"--preview-card-foreground": "oklch(0.985 0 0)", // --card-foreground
	"--preview-primary": "oklch(0.92 0.004 286.32)", // --primary / accent-strong
	"--preview-primary-foreground": "oklch(0.21 0.006 285.885)", // --primary-foreground
	"--preview-muted": "oklch(0.274 0.006 286.033)", // --muted / --raised
	"--preview-muted-foreground": "oklch(0.705 0.015 286.067)", // --muted-foreground
	"--preview-accent": "oklch(0.274 0.006 286.033)", // --accent / --sidebar-accent
	"--preview-border": "oklch(1 0 0 / 7%)", // --border
	"--preview-border-strong": "oklch(1 0 0 / 4%)", // --input / --color-border-strong
	"--preview-divider": "oklch(1 0 0 / 4%)", // --input / --color-border-strong (column + topbar rules)
	"--preview-input": "oklch(1 0 0 / 4%)", // --input
	"--preview-ring": "oklch(0.552 0.016 285.938)", // --ring
	"--preview-sidebar": "oklch(0.155 0.005 285.823)", // --sidebar
	"--preview-sidebar-foreground": "oklch(0.985 0 0)", // --sidebar-foreground
	"--preview-sidebar-accent": "oklch(0.274 0.006 286.033)", // --sidebar-accent
	"--preview-sidebar-hover": "color-mix(in oklch, oklch(0.985 0 0) 4%, transparent)", // --color-interactive-hover
	"--preview-sidebar-border": "oklch(1 0 0 / 7%)", // --sidebar-border → --border
	"--preview-passive": "oklch(0.442 0.017 285.786)", // --chart-3 / --color-text-passive
	"--preview-raised": "oklch(0.274 0.006 286.033)", // --color-raised → --muted
} as CSSProperties;

// From --color-status-* in frontend/src/styles/tokens.css
const STATUS_COLORS = {
	idle: "oklch(0.705 0.015 286.067)", // --muted-foreground
	working: "#60a5fa",
	needsYou: "#fb923c",
	inReview: "#facc15",
	ready: "#4ade80",
	merged: "oklch(0.92 0.004 286.32)", // --primary
	unknown: "oklch(0.37 0.013 285.805)", // --chart-4
} as const;

const SIDEBAR_DEFAULT_WIDTH = 208;

const previewAgents = {
	claude: { agent: "Claude", icon: "/app-icons/agents/claude-code.svg" },
	codex: { agent: "Codex", icon: "/app-icons/agents/codex.svg" },
	cursor: { agent: "Cursor", icon: "/app-icons/agents/cursor.svg" },
	opencode: { agent: "OpenCode", icon: "/app-icons/agents/opencode.svg" },
	copilot: { agent: "Copilot", icon: "/app-icons/agents/copilot-color.svg" },
	gemini: { agent: "Gemini", icon: "/app-icons/gemini.svg" },
	aider: { agent: "Aider", icon: "/app-icons/agents/aider.png" },
	grok: { agent: "Grok", icon: "/app-icons/agents/grok.png" },
	devin: { agent: "Devin", icon: "/app-icons/agents/devin.png" },
	kimi: { agent: "Kimi", icon: "/app-icons/agents/kimi.png" },
	amp: { agent: "Amp", icon: "/app-icons/agents/amp.svg" },
	cline: { agent: "Cline", icon: "/app-icons/agents/cline.svg" },
	goose: { agent: "Goose", icon: "/app-icons/agents/goose.svg" },
	continue: { agent: "Continue", icon: "/app-icons/agents/continue.png" },
	pi: { agent: "Pi", icon: "/app-icons/agents/pi.png" },
	kilocode: { agent: "Kilo Code", icon: "/app-icons/agents/kilocode.svg" },
} as const;

type PreviewAgentKey = keyof typeof previewAgents;

// Reviewer avatars used on In Review / Ready to merge card boxes.
const REVIEWERS = {
	harshit: "https://avatars.githubusercontent.com/u/212377671?v=4&s=36",
	agent: "https://avatars.githubusercontent.com/u/11289825?v=4&s=36",
	suraj: "https://avatars.githubusercontent.com/u/96483690?v=4&s=36",
	ashish: "https://avatars.githubusercontent.com/u/73213873?v=4&s=36",
	illegal: "https://avatars.githubusercontent.com/u/44542765?v=4&s=36",
	harsh2: "https://avatars.githubusercontent.com/u/40922251?v=4&s=36",
	whoisasx: "https://avatars.githubusercontent.com/u/106678504?v=4&s=36",
	itry: "https://avatars.githubusercontent.com/u/193449657?v=4&s=36",
} as const;

const columns = [
	{
		id: "working",
		title: "Pending Work",
		count: 9,
		cards: [
			{
				title: "Tighten hero window border alignment",
				branch: "landing/window-border-pass",
				agent: previewAgents.claude.agent,
				icon: previewAgents.claude.icon,
				activity: "Editing file",
				activityState: "running",
				pr: "draft",
				checks: "editing",
				files: "1 file",
				time: "3m ago",
				badge: null,
				tone: "default",
			},
			{
				title: "Remove stale generated icon imports",
				branch: "cleanup/stale-icon-imports",
				agent: previewAgents.opencode.agent,
				icon: previewAgents.opencode.icon,
				activity: "Deleting file",
				activityState: "running",
				pr: "draft",
				checks: "cleanup",
				files: "2 files",
				time: "14m ago",
				badge: null,
				tone: "default",
			},
			{
				title: "Document preview alias ownership",
				branch: "deploy/alias-ownership",
				agent: previewAgents.gemini.agent,
				icon: previewAgents.gemini.icon,
				activity: "Writing deployment notes",
				activityState: "running",
				pr: "draft",
				checks: "editing",
				files: "1 file",
				time: "18m ago",
				badge: null,
				tone: "default",
			},
			{
				title: "Confirm whether download labels stay platform-aware",
				branch: "landing/platform-download-copy",
				agent: previewAgents.cursor.agent,
				icon: previewAgents.cursor.icon,
				activity: "Editing copy",
				activityState: "running",
				pr: "draft",
				checks: "editing",
				files: "3 files",
				time: "1h ago",
				badge: null,
				tone: "default",
			},
		],
	},
	{
		id: "action",
		title: "Iterating",
		count: 4,
		cards: [
			{
				title: "Pick final titlebar metrics for the preview",
				branch: "landing/titlebar-metrics",
				agent: previewAgents.claude.agent,
				icon: previewAgents.claude.icon,
				activity: "27/44 passed",
				activityState: "running",
				pr: "PR #322",
				checks: "checks running",
				files: "1 file",
				time: "46m ago",
				badge: null,
				tone: "default",
				testResults: { pass: 27, total: 44 },
			},
			{
				title: "Wire hero mockup progression delays",
				branch: "landing/progression-timing",
				agent: previewAgents.codex.agent,
				icon: previewAgents.codex.icon,
				activity: "Tuning interval jitter",
				activityState: "running",
				pr: "PR #331",
				checks: "checks running",
				files: "1 file",
				time: "22m ago",
				badge: null,
				tone: "default",
			},
		],
	},
	{
		id: "pending",
		title: "In Review",
		count: 5,
		cards: [
			{
				title: "Verify preview environment variables",
				branch: "deploy/preview-env",
				agent: previewAgents.opencode.agent,
				icon: previewAgents.opencode.icon,
				activity: "Deployment checks running",
				activityState: "reviewing",
				pr: "PR #415",
				checks: "checks running",
				files: "2 files",
				time: "18m ago",
				badge: "Awaiting review",
				tone: "review",
			},
			{
				title: "Preload GitHub stars before hydration",
				branch: "landing/preload-stars",
				agent: previewAgents.claude.agent,
				icon: previewAgents.claude.icon,
				activity: "Checks passed",
				activityState: "passed",
				pr: "PR #324",
				checks: "checks passed",
				files: "2 files",
				time: "1h ago",
				badge: "Awaiting review",
				tone: "review",
				testResults: { pass: 38, total: 60 },
				prComments: 3,
				reviewers: [REVIEWERS.harshit, REVIEWERS.suraj],
			},
		],
	},
	{
		id: "merge",
		title: "Ready to merge",
		count: 3,
		cards: [
			{
				title: "Ship AO logo in top navigation",
				branch: "landing/topbar-ao-logo",
				agent: previewAgents.claude.agent,
				icon: previewAgents.claude.icon,
				activity: "Approved",
				activityState: "passed",
				pr: "PR #326",
				checks: "approved",
				files: "2 files",
				time: "3h ago",
				badge: null,
				tone: "ready",
				prComments: 0,
				reviewers: [REVIEWERS.harshit, REVIEWERS.agent],
			},
		],
	},
] satisfies PreviewColumn[];

const COLUMN_COLORS: Record<BoardColumnId, string> = {
	working: "#60a5fa", // --color-status-working
	action: "#fb923c", // --color-status-needs-you
	pending: "#facc15", // --color-status-in-review
	merge: "#4ade80", // --color-status-ready
};

const projectItems: TrackItem[] = [
	{
		id: "landing",
		label: "smooth card",
		summary: "Refresh the hero board, topbar, and landing sections without losing the AO product language.",
	},
	{
		id: "deploy",
		label: "vercel",
		summary: "Keep framework detection and deploy payloads boring so every preview goes live cleanly.",
	},
	{
		id: "stars",
		label: "preload",
		summary: "Move remote counts into server-rendered data so hydration does not shift the hero controls.",
	},
	{
		id: "icons",
		label: "harness",
		summary: "Replace placeholder logos with real harness marks and keep the compatibility showcase readable.",
	},
	{
		id: "footer",
		label: "footer",
		summary: "Verify footer grids, demo video placement, and section order across the landing page.",
	},
];

function previewCard(
	card: Pick<
		StaticPreviewCard,
		"title" | "branch" | "activity" | "activityState" | "pr"
	> &
		Partial<StaticPreviewCard> & { agentKey?: PreviewAgentKey },
): StaticPreviewCard {
	const { agentKey = "codex", ...rest } = card;
	const defaults = previewAgents[agentKey];
	return {
		agent: defaults.agent,
		icon: defaults.icon,
		badge: null,
		checks: "checks running",
		files: "2 files",
		time: "18m ago",
		tone: "default",
		...rest,
	};
}

const trackCardTemplates: Record<TrackId, StaticPreviewCard[]> = {
	landing: columns.flatMap((column) =>
		column.cards.slice(0, 1).map((card) => ({ ...card }) as StaticPreviewCard),
	),
	deploy: [
		previewCard({
			title: "Document preview alias ownership",
			branch: "deploy/alias-ownership",
			activity: "Writing deployment notes",
			activityState: "running",
			pr: "draft",
			agentKey: "gemini",
			time: "18m ago",
		}),
		previewCard({
			title: "Choose production region failover",
			branch: "deploy/region-failover",
			activity: "Waiting for infra decision",
			activityState: "waiting",
			pr: "PR #414",
			agentKey: "grok",
			badge: "Needs input",
			tone: "blocked",
			time: "18m ago",
		}),
		previewCard({
			title: "Verify preview environment variables",
			branch: "deploy/preview-env",
			activity: "Deployment checks running",
			activityState: "reviewing",
			pr: "PR #415",
			agentKey: "opencode",
			badge: "Awaiting review",
			tone: "review",
			time: "18m ago",
			prComments: 0,
			reviewers: [REVIEWERS.itry, REVIEWERS.agent],
		}),
		previewCard({
			title: "Cache workspace dependencies in builds",
			branch: "deploy/workspace-cache",
			activity: "Production deploy green",
			activityState: "passed",
			pr: "PR #409",
			agentKey: "gemini",
			badge: null,
			tone: "ready",
			prComments: 0,
			reviewers: [REVIEWERS.harshit, REVIEWERS.suraj],
		}),
	],
	stars: [
		previewCard({
			title: "Fetch GitHub stars during revalidation",
			branch: "metrics/server-stars",
			activity: "Queued",
			activityState: "passed",
			pr: "draft",
			agentKey: "amp",
		}),
		previewCard({
			title: "Set the stale count fallback",
			branch: "metrics/star-fallback",
			activity: "Queued behind review",
			activityState: "passed",
			pr: "PR #430",
			agentKey: "cursor",
		}),
		previewCard({
			title: "Prevent hero metrics hydration shift",
			branch: "metrics/hydration-layout",
			activity: "Checks passed",
			activityState: "passed",
			pr: "PR #432",
			agentKey: "continue",
			badge: "Awaiting review",
			tone: "review",
			testResults: { pass: 38, total: 60 },
			prComments: 3,
			reviewers: [REVIEWERS.harshit, REVIEWERS.suraj],
		}),
		previewCard({
			title: "Preload the repository avatar",
			branch: "metrics/avatar-preload",
			activity: "Approved",
			activityState: "passed",
			pr: "PR #426",
			agentKey: "kimi",
			badge: null,
			tone: "ready",
			prComments: 0,
			reviewers: [REVIEWERS.harshit, REVIEWERS.agent],
		}),
	],
	icons: [
		previewCard({
			title: "Replace placeholder harness marks",
			branch: "icons/harness-marks",
			activity: "Queued",
			activityState: "passed",
			pr: "draft",
			agentKey: "opencode",
		}),
		previewCard({
			title: "Pick a fallback for unknown agents",
			branch: "icons/agent-fallback",
			activity: "Parked",
			activityState: "passed",
			pr: "PR #450",
			agentKey: "cline",
		}),
		previewCard({
			title: "Audit dark-mode logo contrast",
			branch: "icons/dark-contrast",
			activity: "Parked",
			activityState: "passed",
			pr: "PR #452",
			agentKey: "cursor",
		}),
		previewCard({
			title: "Remove stale generated icon imports",
			branch: "icons/remove-stale-imports",
			activity: "Approved",
			activityState: "passed",
			pr: "PR #444",
			agentKey: "kilocode",
			badge: null,
			tone: "ready",
			prComments: 0,
			reviewers: [REVIEWERS.suraj, REVIEWERS.whoisasx],
		}),
	],
	footer: [
		previewCard({
			title: "Test footer columns at mobile widths",
			branch: "qa/footer-mobile",
			activity: "Queued",
			activityState: "passed",
			pr: "draft",
			agentKey: "codex",
		}),
		previewCard({
			title: "Confirm final demo video caption",
			branch: "qa/video-caption",
			activity: "Parked",
			activityState: "passed",
			pr: "PR #471",
			agentKey: "pi",
		}),
		previewCard({
			title: "Check section order across routes",
			branch: "qa/section-order",
			activity: "Parked",
			activityState: "passed",
			pr: "PR #473",
			agentKey: "goose",
		}),
		previewCard({
			title: "Fix footer placeholder row spacing",
			branch: "qa/footer-spacing",
			activity: "Done",
			activityState: "passed",
			pr: "PR #465",
			agentKey: "copilot",
		}),
	],
};

const landingIncomingCards: StaticPreviewCard[] = [
	{
		title: "Tighten hero window border alignment",
		branch: "landing/window-border-pass",
		...previewAgents.claude,
		activity: "Editing file",
		activityState: "running",
		pr: "draft",
		checks: "editing",
		files: "1 file",
		time: "3m ago",
		badge: null,
		tone: "default",
	},
	{
		title: "Remove stale generated icon imports",
		branch: "cleanup/stale-icon-imports",
		...previewAgents.opencode,
		activity: "Deleting file",
		activityState: "running",
		pr: "draft",
		checks: "cleanup",
		files: "2 files",
		time: "14m ago",
		badge: null,
		tone: "default",
	},
	{
		title: "Document preview alias ownership",
		branch: "deploy/alias-ownership",
		...previewAgents.gemini,
		activity: "Writing deployment notes",
		activityState: "running",
		pr: "draft",
		checks: "editing",
		files: "1 file",
		time: "18m ago",
		badge: null,
		tone: "default",
	},
	{
		title: "Repair mobile overflow on landing preview",
		branch: "landing/mobile-preview-overflow",
		...previewAgents.codex,
		activity: "Debugging issue",
		activityState: "running",
		pr: "draft",
		checks: "debugging",
		files: "4 files",
		time: "now",
		badge: null,
		tone: "default",
	},
	{
		title: "Shrink card metadata copy for preview scale",
		branch: "landing/card-density",
		...previewAgents.cursor,
		activity: "Editing file",
		activityState: "running",
		pr: "draft",
		checks: "editing",
		files: "1 file",
		time: "now",
		badge: null,
		tone: "default",
	},
	{
		title: "Verify GitHub avatar fallback in project list",
		branch: "landing/repo-avatar",
		...previewAgents.aider,
		activity: "Running tests",
		activityState: "running",
		pr: "draft",
		checks: "tests",
		files: "2 files",
		time: "now",
		badge: null,
		tone: "default",
	},
	{
		title: "Replace placeholder project copy with repo-specific tasks",
		branch: "landing/organic-dummy-data",
		...previewAgents.goose,
		activity: "Writing copy",
		activityState: "running",
		pr: "draft",
		checks: "copy pass",
		files: "1 file",
		time: "now",
		badge: null,
		tone: "default",
	},
	{
		title: "Smooth card collapse when PRs merge out",
		branch: "landing/merge-collapse-motion",
		...previewAgents.cline,
		activity: "Debugging issue",
		activityState: "running",
		pr: "draft",
		checks: "motion debug",
		files: "1 file",
		time: "now",
		badge: null,
		tone: "default",
	},
];

const incomingCardsByTrack: Record<TrackId, StaticPreviewCard[]> = {
	landing: landingIncomingCards,
	deploy: [
		previewCard({
			title: "Verify preview environment variables",
			branch: "deploy/preview-env",
			activity: "Deployment checks running",
			activityState: "reviewing",
			pr: "PR #415",
			agentKey: "opencode",
			badge: "Awaiting review",
			tone: "review",
			time: "18m ago",
		}),
		previewCard({
			title: "Choose production region failover",
			branch: "deploy/region-failover",
			activity: "Waiting for infra decision",
			activityState: "waiting",
			pr: "PR #414",
			agentKey: "grok",
			badge: "Needs input",
			tone: "blocked",
			time: "18m ago",
		}),
		previewCard({
			title: "Document preview alias ownership",
			branch: "deploy/alias-ownership",
			activity: "Writing deployment notes",
			activityState: "running",
			pr: "draft",
			agentKey: "gemini",
			time: "18m ago",
		}),
	],
	stars: [
		previewCard({
			title: "Add rate-limit telemetry for star fetches",
			branch: "metrics/star-rate-limit",
			activity: "Instrumenting cache requests",
			activityState: "running",
			pr: "draft",
			agentKey: "kimi",
		}),
		previewCard({
			title: "Test zero-star fallback rendering",
			branch: "metrics/zero-state",
			activity: "Writing component tests",
			activityState: "running",
			pr: "draft",
			agentKey: "continue",
		}),
	],
	icons: [
		previewCard({
			title: "Normalize harness icon viewboxes",
			branch: "icons/viewbox-normalization",
			activity: "Editing SVG assets",
			activityState: "running",
			pr: "draft",
			agentKey: "kilocode",
		}),
		previewCard({
			title: "Add Gemini CLI authorized state",
			branch: "icons/gemini-state",
			activity: "Updating harness preview",
			activityState: "running",
			pr: "draft",
			agentKey: "gemini",
		}),
	],
	footer: [
		previewCard({
			title: "Verify video controls on iOS",
			branch: "qa/video-ios",
			activity: "Running device checks",
			activityState: "running",
			pr: "draft",
			agentKey: "devin",
		}),
		previewCard({
			title: "Audit footer links and focus order",
			branch: "qa/footer-focus",
			activity: "Testing keyboard navigation",
			activityState: "running",
			pr: "draft",
			agentKey: "pi",
		}),
	],
};

const BASE_WIDTH = 1140;
const BASE_HEIGHT = 615;
const WINDOW_ASPECT = BASE_WIDTH / BASE_HEIGHT;
const WINDOW_MARGIN = 4;
// Shell can shrink with the hero frame; inner board stays at BASE_* and CSS-scales.
// Keep the shell aspect-locked so the scaled board fills both axes (no letterbox gaps).

interface WindowState {
	x: number;
	y: number;
	width: number;
	height: number;
}

function sizeFromWidth(width: number): Pick<WindowState, "width" | "height"> {
	return { width, height: width / WINDOW_ASPECT };
}

function createInitialWindowState(
	containerWidth: number,
	containerHeight: number,
): WindowState {
	const availableWidth = containerWidth - WINDOW_MARGIN * 2;
	const availableHeight = containerHeight - WINDOW_MARGIN * 2;
	const scale = Math.min(
		1,
		availableWidth / BASE_WIDTH,
		availableHeight / BASE_HEIGHT,
	);
	const { width, height } = sizeFromWidth(BASE_WIDTH * scale);
	return {
		x: (containerWidth - width) / 2,
		y: (containerHeight - height) / 2,
		width,
		height,
	};
}

const mockupShellStyle = {
	...previewTokenStyle,
	"--mockup-design-w": `${BASE_WIDTH}px`,
	"--mockup-design-h": `${BASE_HEIGHT}px`,
	"--mockup-shell-radius": "20px",
	"--mockup-inner-radius": "17px",
	left: "50%",
	top: "50%",
	width: `min(calc(100% - ${WINDOW_MARGIN * 2}px), ${BASE_WIDTH}px)`,
	maxHeight: `calc(100% - ${WINDOW_MARGIN * 2}px)`,
	aspectRatio: `${BASE_WIDTH} / ${BASE_HEIGHT}`,
	transform: "translate(-50%, -50%)",
} as CSSProperties;

function useFloatingWindow(
	outerRef: React.RefObject<HTMLElement | null>,
) {
	const stateRef = useRef<WindowState | null>(null);

	const applyState = useCallback(() => {
		const outer = outerRef.current;
		const state = stateRef.current;
		if (!outer || !state) return;
		const scale = state.width / BASE_WIDTH;
		outer.style.left = `${state.x}px`;
		outer.style.top = `${state.y}px`;
		outer.style.width = `${state.width}px`;
		outer.style.height = `${state.height}px`;
		outer.style.setProperty("--mockup-shell-radius", `${20 * scale}px`);
		outer.style.setProperty("--mockup-inner-radius", `${17 * scale}px`);
		outer.style.transform = "none";
	}, [outerRef]);

	const updateContainer = useCallback(() => {
		const outer = outerRef.current;
		const parent = outer?.offsetParent as HTMLElement | null;
		if (!parent) return;
		const rect = parent.getBoundingClientRect();
		stateRef.current = createInitialWindowState(rect.width, rect.height);
		applyState();
	}, [applyState, outerRef]);

	useLayoutEffect(() => {
		updateContainer();
		const outer = outerRef.current;
		const parent = outer?.offsetParent as HTMLElement | null;
		if (!parent) return;
		const observer = new ResizeObserver(updateContainer);
		observer.observe(parent);
		window.addEventListener("resize", updateContainer);
		return () => {
			observer.disconnect();
			window.removeEventListener("resize", updateContainer);
		};
	}, [updateContainer, outerRef]);
}

function createInitialCards(trackId: TrackId): PreviewCard[] {
	if (trackId === "landing") {
		return columns.flatMap((column) =>
			column.cards.map((card, index) => ({
				...card,
				column: column.id,
				id: `${trackId}-${column.id}-${index}`,
			})),
		);
	}

	return columns.flatMap((column, index) => {
		const card = trackCardTemplates[trackId][index];
		return card
			? [{
					...card,
					column: column.id,
					id: `${trackId}-${column.id}`,
				}]
			: [];
	});
}

function createInitialCardsByTrack(): Record<TrackId, PreviewCard[]> {
	return Object.fromEntries(
		projectItems.map(({ id }) => [id, createInitialCards(id)]),
	) as Record<TrackId, PreviewCard[]>;
}

const REVIEWER_LIST = Object.values(REVIEWERS);

function pickRandom<T>(items: T[]): T {
	return items[Math.floor(Math.random() * items.length)] as T;
}

function pickStagingActivity(): { activity: string; testResults?: { pass: number; total: number } } {
	const total = pickRandom([28, 42, 44, 50, 54, 60]);
	const pass = Math.floor(total * (0.3 + Math.random() * 0.4));
	const labels: Array<{ activity: string; testResults?: { pass: number; total: number } }> = [
		{ activity: `${pass}/${total} passed`, testResults: { pass, total } },
		{ activity: `${pass}/${total} passed`, testResults: { pass, total } },
		{ activity: "Building...", testResults: undefined },
		{ activity: "Linting codebase", testResults: undefined },
		{ activity: "Running CI pipeline", testResults: { pass, total } },
	];
	return pickRandom(labels);
}

function pickReviewers(): string[] {
	const shuffled = [...REVIEWER_LIST].sort(() => Math.random() - 0.5);
	return shuffled.slice(0, 1 + Math.floor(Math.random() * 3));
}

const IN_REVIEW_STATES: Array<{
	activity: string;
	activityState: ActivityState;
	testResults?: { pass: number; total: number };
}> = [
	{ activity: "Checks passed", activityState: "passed", testResults: { pass: 38, total: 60 } },
	{ activity: "Awaiting review", activityState: "reviewing" },
	{ activity: "Review in progress", activityState: "reviewing", testResults: { pass: 28, total: 28 } },
	{ activity: "Checks passed", activityState: "passed", testResults: { pass: 16, total: 28 } },
];

const MERGE_ACTIVITIES = [
	"Approved",
	"Ready to land",
	"LGTM",
	"All checks passed",
];

const RELATIVE_TIMES = ["2m ago", "4m ago", "7m ago", "11m ago", "18m ago", "24m ago", "31m ago", "46m ago", "1h ago"];

function randomTime() {
	return pickRandom(RELATIVE_TIMES);
}

function advanceCard(card: PreviewCard): PreviewCard {
	if (card.column === "working") {
		const { activity, testResults } = pickStagingActivity();
		return {
			...card,
			column: "action",
			activity,
			activityState: "running",
			badge: null,
			tone: "default",
			time: randomTime(),
			testResults,
			pr: card.pr === "draft" ? `PR #${320 + Math.floor(Math.random() * 20)}` : card.pr,
		};
	}

	if (card.column === "action") {
		const review = pickRandom(IN_REVIEW_STATES);
		return {
			...card,
			column: "pending",
			activity: review.activity,
			activityState: review.activityState,
			badge: "Awaiting review",
			tone: "review",
			time: randomTime(),
			reviewers: pickReviewers(),
			prComments: Math.floor(Math.random() * 4),
			testResults: review.testResults,
			pr: card.pr === "draft" ? `PR #${320 + Math.floor(Math.random() * 20)}` : card.pr,
		};
	}

	return {
		...card,
		column: "merge",
		activity: pickRandom(MERGE_ACTIVITIES),
		activityState: "passed",
		badge: null,
		tone: "ready",
		time: randomTime(),
		reviewers: card.reviewers ?? pickReviewers(),
		prComments: card.prComments ?? 0,
		testResults: undefined,
	};
}

function cardStatusColor(card: PreviewCard): string {
	// Match kanban column dots: Pending=blue, Iterating=orange, Review=yellow, Ready=green.
	if (card.tone === "blocked" || card.activityState === "waiting" || card.column === "action") {
		return STATUS_COLORS.needsYou;
	}
	if (card.column === "pending" || card.tone === "review") return STATUS_COLORS.inReview;
	if (card.column === "merge" || card.tone === "ready") return STATUS_COLORS.ready;
	if (card.activityState === "running" || card.column === "working") return STATUS_COLORS.working;
	return STATUS_COLORS.idle;
}

function isIdleCard(card: PreviewCard): boolean {
	return card.column === "working" && card.activityState !== "running";
}

function randomDelay() {
	return 1400 + Math.random() * 1400;
}

function randomItem<T>(items: T[]): T | null {
	if (items.length === 0) return null;
	return items[Math.floor(Math.random() * items.length)] ?? null;
}

// The preview is a prop, not a real app. It exposes ~13 fake controls, so pull the
// whole subtree out of the tab order and let the root stand in for it as a single
// image node. Runs after every render because cards mount and unmount constantly.
function useDecorativeSubtree(rootRef: React.RefObject<HTMLElement | null>) {
	useEffect(() => {
		const root = rootRef.current;
		if (!root) return;

		const focusable = root.querySelectorAll<HTMLElement>(
			'a, button, input, select, textarea, [tabindex]:not([tabindex="-1"])',
		);
		focusable.forEach((element) => {
			element.tabIndex = -1;
		});
	});
}

function PanelIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<rect x="2.5" y="2.5" width="11" height="11" rx="1.5" stroke="currentColor" />
			<path d="M11 3v10" stroke="currentColor" />
		</svg>
	);
}

function SearchIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<circle cx="7" cy="7" r="3.4" stroke="currentColor" strokeWidth="1.3" />
			<path d="m9.6 9.6 2.5 2.5" stroke="currentColor" strokeLinecap="round" strokeWidth="1.3" />
		</svg>
	);
}

function PanelLeftIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<rect x="2.5" y="2.5" width="11" height="11" rx="1.5" stroke="currentColor" strokeWidth="1.3" />
			<path d="M6.5 3v10" stroke="currentColor" strokeWidth="1.3" />
		</svg>
	);
}

function ArrowLeftIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<path
				d="M12.5 8H3.5M7 4.5 3.5 8 7 11.5"
				stroke="currentColor"
				strokeLinecap="round"
				strokeLinejoin="round"
				strokeWidth="1.3"
			/>
		</svg>
	);
}

function ArrowRightIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<path
				d="M3.5 8h9M9 4.5 12.5 8 9 11.5"
				stroke="currentColor"
				strokeLinecap="round"
				strokeLinejoin="round"
				strokeWidth="1.3"
			/>
		</svg>
	);
}

function BranchIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<circle cx="4" cy="3.5" r="1.5" stroke="currentColor" strokeWidth="1.2" />
			<circle cx="4" cy="12.5" r="1.5" stroke="currentColor" strokeWidth="1.2" />
			<circle cx="12" cy="12.5" r="1.5" stroke="currentColor" strokeWidth="1.2" />
			<path d="M4 5v6M8 3.5h1.5A2.5 2.5 0 0 1 12 6v5" stroke="currentColor" strokeLinecap="round" strokeWidth="1.2" />
			<path d="m7.5 1.8 1.8 1.7-1.8 1.8" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.2" />
		</svg>
	);
}

/** Lucide LayoutDashboard — bento tiles used on the real project row. */
function LayoutGridIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<rect x="2" y="2" width="5" height="6.5" rx="1" stroke="currentColor" strokeWidth="1.25" />
			<rect x="9" y="2" width="5" height="3.5" rx="1" stroke="currentColor" strokeWidth="1.25" />
			<rect x="9" y="7.5" width="5" height="6.5" rx="1" stroke="currentColor" strokeWidth="1.25" />
			<rect x="2" y="10.5" width="5" height="3.5" rx="1" stroke="currentColor" strokeWidth="1.25" />
		</svg>
	);
}

/** Matches frontend/src/renderer/components/icons.tsx OrchestratorIcon. */
function OrchestratorIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 24 24" fill="none" aria-hidden="true">
			<circle cx="12" cy="4" r="2" stroke="currentColor" strokeWidth="2" />
			<circle cx="5" cy="20" r="2" stroke="currentColor" strokeWidth="2" />
			<circle cx="12" cy="20" r="2" stroke="currentColor" strokeWidth="2" />
			<circle cx="19" cy="20" r="2" stroke="currentColor" strokeWidth="2" />
			<path d="M12 6v12M5 11h14M5 11v7M19 11v7" stroke="currentColor" strokeLinecap="round" strokeWidth="2" />
		</svg>
	);
}

function MoreVerticalIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<circle cx="8" cy="3.5" r="1.1" fill="currentColor" />
			<circle cx="8" cy="8" r="1.1" fill="currentColor" />
			<circle cx="8" cy="12.5" r="1.1" fill="currentColor" />
		</svg>
	);
}

function PlusIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<path d="M8 3.5v9M3.5 8h9" stroke="currentColor" strokeLinecap="round" strokeWidth="1.4" />
		</svg>
	);
}

function BellIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<path
				d="M8 13.75a1.5 1.5 0 0 0 1.5-1.35H6.5A1.5 1.5 0 0 0 8 13.75ZM3.75 11.75h8.5l-.9-1.45V7.1a3.35 3.35 0 0 0-6.7 0v3.2l-.9 1.45Z"
				stroke="currentColor"
				strokeLinejoin="round"
				strokeWidth="1.25"
			/>
		</svg>
	);
}

function BeakerIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<path
				d="M6 2.5h4M7 2.5v3.1l-3.1 5.2A1.8 1.8 0 0 0 5.5 13.5h5A1.8 1.8 0 0 0 12.1 10.8L9 5.6V2.5"
				stroke="currentColor"
				strokeLinecap="round"
				strokeLinejoin="round"
				strokeWidth="1.2"
			/>
		</svg>
	);
}

function CheckIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<path d="m3.2 8.2 3.1 3.1 6.5-6.6" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.8" />
		</svg>
	);
}

function WarningIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="none" aria-hidden="true">
			<path d="M8 2.3 14 13H2L8 2.3Z" stroke="currentColor" strokeLinejoin="round" strokeWidth="1.4" />
			<path d="M8 6.2v3.2" stroke="currentColor" strokeLinecap="round" strokeWidth="1.4" />
			<path d="M8 11.7h.01" stroke="currentColor" strokeLinecap="round" strokeWidth="2" />
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

function GitHubIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
			<path d="M8 0C3.58 0 0 3.67 0 8.2c0 3.62 2.29 6.69 5.47 7.78.4.08.55-.18.55-.4 0-.2-.01-.86-.01-1.56-2.01.38-2.53-.5-2.69-.96-.09-.24-.48-.96-.82-1.16-.28-.16-.68-.56-.01-.57.63-.01 1.08.59 1.23.84.72 1.24 1.87.89 2.33.68.07-.53.28-.89.51-1.09-1.78-.21-3.64-.91-3.64-4.04 0-.89.31-1.62.82-2.19-.08-.21-.36-1.04.08-2.16 0 0 .67-.22 2.2.84A7.42 7.42 0 0 1 8 3.52c.68 0 1.36.09 1.99.27 1.53-1.06 2.2-.84 2.2-.84.44 1.12.16 1.95.08 2.16.51.57.82 1.3.82 2.19 0 3.14-1.87 3.83-3.65 4.04.29.26.54.76.54 1.53 0 1.1-.01 1.99-.01 2.26 0 .22.15.48.55.4A8.15 8.15 0 0 0 16 8.2C16 3.67 12.42 0 8 0Z" />
		</svg>
	);
}

function SettingsIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 24 24" fill="none" aria-hidden="true">
			<path
				d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.52a2 2 0 0 1-1 1.72l-.15.1a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.38a2 2 0 0 0-.73-2.73l-.15-.1a2 2 0 0 1-1-1.72v-.52a2 2 0 0 1 1-1.72l.15-.1a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2Z"
				stroke="currentColor"
				strokeLinecap="round"
				strokeLinejoin="round"
				strokeWidth="1.8"
			/>
			<circle
				cx="12"
				cy="12"
				r="3"
				stroke="currentColor"
				strokeWidth="1.2"
			/>
		</svg>
	);
}

function PinIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 24 24" fill="none" aria-hidden="true">
			<path
				d="M12 17v5M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V17h14v-1.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V6a1 1 0 0 0-1-1h-4a1 1 0 0 0-1 1z"
				stroke="currentColor"
				strokeLinecap="round"
				strokeLinejoin="round"
				strokeWidth="1.75"
			/>
		</svg>
	);
}

function FolderOpenIcon({ className = "" }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 24 24" fill="none" aria-hidden="true">
			<path
				d="m6 14 1.45-2.9A2 2 0 0 1 9.24 10H20a2 2 0 0 1 1.94 2.5l-1.55 6a2 2 0 0 1-1.94 1.5H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h3.9a2 2 0 0 1 1.69.9l.81 1.2a2 2 0 0 0 1.67.9H18a2 2 0 0 1 2 2v2"
				stroke="currentColor"
				strokeLinecap="round"
				strokeLinejoin="round"
				strokeWidth="1.75"
			/>
		</svg>
	);
}

function ChevronRightIcon({ className = "", expanded = false }: { className?: string; expanded?: boolean }) {
	return (
		<svg
			className={`${className}${expanded ? " rotate-90" : ""}`}
			viewBox="0 0 16 16"
			fill="none"
			aria-hidden="true"
		>
			<path
				d="M6 3.5 10.5 8 6 12.5"
				stroke="currentColor"
				strokeLinecap="round"
				strokeLinejoin="round"
				strokeWidth="2"
			/>
		</svg>
	);
}

function ProjectActionIcon({
	children,
	className = "",
}: {
	children: ReactNode;
	className?: string;
}) {
	return (
		<span
			aria-hidden="true"
			className={`grid size-6 shrink-0 place-items-center text-[var(--preview-passive)] ${className}`}
		>
			{children}
		</span>
	);
}

/** Compact sidebar labels for the same tasks shown on the kanban. */
function sidebarSessionLabel(card: PreviewCard): string {
	const short: Record<string, string> = {
		"Tighten hero window border alignment": "window border",
		"Remove stale generated icon imports": "stale icons",
		"Document preview alias ownership": "alias ownership",
		"Confirm whether download labels stay platform-aware": "download labels",
		"Pick final titlebar metrics for the preview": "titlebar metrics",
		"Wire hero mockup progression delays": "progression timing",
		"Verify preview environment variables": "preview env",
		"Preload GitHub stars before hydration": "preload stars",
		"Ship AO logo in top navigation": "ao logo",
		"Choose production region failover": "region failover",
		"Cache workspace dependencies in builds": "workspace cache",
		"Repair mobile overflow on landing preview": "mobile overflow",
		"Shrink card metadata copy for preview scale": "card density",
		"Verify GitHub avatar fallback in project list": "repo avatar",
		"Replace placeholder project copy with repo-specific tasks": "organic tasks",
		"Smooth card collapse when PRs merge out": "merge collapse",
		"Add rate-limit telemetry for star fetches": "star rate limit",
		"Test zero-star fallback rendering": "zero-star fallback",
		"Normalize harness icon viewboxes": "icon viewboxes",
		"Add Gemini CLI authorized state": "gemini state",
		"Verify video controls on iOS": "video ios",
		"Audit footer links and focus order": "footer focus",
	};
	return short[card.title] ?? card.title.toLowerCase();
}

function isPinnedSidebarCard(card: PreviewCard): boolean {
	return (
		card.column === "pending" ||
		card.column === "merge" ||
		card.tone === "review" ||
		card.tone === "ready"
	);
}

function SidebarSessionRow({
	active,
	dotColor,
	label,
}: {
	active?: boolean;
	dotColor: string;
	label: string;
}) {
	return (
		<div className="pl-4">
			<div
				className={`flex h-7 w-full items-center gap-2 rounded-md px-2 text-left text-[12px] ${
					active
						? "bg-[var(--preview-sidebar-accent)] font-medium text-[var(--preview-foreground)]"
						: "text-[var(--preview-muted-foreground)]"
				}`}
			>
				<span
					aria-hidden="true"
					className="mt-px h-1.5 w-1.5 shrink-0 rounded-full"
					style={{ backgroundColor: dotColor }}
				/>
				<span className="min-w-0 flex-1 truncate">{label}</span>
			</div>
		</div>
	);
}

function Sidebar({ cards }: { cards: PreviewCard[] }) {
	const activeCards = cards.filter((card) => !card.merging);
	// Pin is a shortcut: pinned sessions also stay under their project.
	const pinnedCards = activeCards.filter(isPinnedSidebarCard);
	const projectCards = activeCards;
	return (
		<aside
			aria-hidden="true"
			className="pointer-events-none relative flex shrink-0 flex-col bg-[var(--preview-sidebar)] text-[var(--preview-muted-foreground)]"
			style={{ width: SIDEBAR_DEFAULT_WIDTH }}
		>
			<div className="flex h-10 items-center gap-2 px-3">
				<div className="flex h-6 items-center gap-1.5">
					<span className="h-2.5 w-2.5 shrink-0 rounded-full bg-[#ff5f57]" />
					<span className="h-2.5 w-2.5 shrink-0 rounded-full bg-[#ffbd2e]" />
					<span className="h-2.5 w-2.5 shrink-0 rounded-full bg-[#28c840]" />
				</div>
				<span
					aria-hidden="true"
					className="grid h-6 w-6 shrink-0 place-items-center text-[var(--preview-passive)]"
				>
					<PanelLeftIcon className="h-3.5 w-3.5" />
				</span>
				<span
					aria-hidden="true"
					className="grid h-6 w-6 shrink-0 place-items-center text-[var(--preview-passive)] opacity-45"
				>
					<ArrowLeftIcon className="h-3.5 w-3.5" />
				</span>
				<span
					aria-hidden="true"
					className="grid h-6 w-6 shrink-0 place-items-center text-[var(--preview-passive)] opacity-45"
				>
					<ArrowRightIcon className="h-3.5 w-3.5" />
				</span>
			</div>

			<div className="flex shrink-0 items-center gap-1.5 px-3 pb-2 pt-0.5">
				<img
					src="/ao-logo.svg"
					alt=""
					width={22}
					height={22}
					aria-hidden="true"
					className="h-[22px] w-[22px] -translate-y-[1px] shrink-0 rounded-md"
					draggable="false"
				/>
				<div className="min-w-0 flex-1 translate-y-px truncate text-[12px] font-bold leading-tight tracking-tight text-[var(--preview-sidebar-foreground)]">
					Agent Orchestrator
				</div>
			</div>

			<div className="flex shrink-0 flex-col px-2">
				<div className="pb-3">
					<div className="flex h-7 w-full items-center gap-2 rounded-lg bg-[var(--preview-muted)] px-2.5 text-[12px] font-normal text-[var(--preview-muted-foreground)]">
						<SearchIcon className="h-3 w-3 shrink-0 opacity-80" />
						<span className="min-w-0 flex-1 truncate leading-none">Search</span>
					</div>
				</div>

				<div className="flex h-7 w-full items-center gap-1.5 rounded-md px-2 text-[12px] font-medium text-[var(--preview-passive)]">
					<PinIcon className="h-3.5 w-3.5 shrink-0" />
					<span className="min-w-0 truncate">Pinned</span>
					<ChevronRightIcon expanded className="h-3 w-3 shrink-0" />
				</div>
				<div className="mb-1 ml-2 flex flex-col gap-px">
					{pinnedCards.map((card) => (
						<SidebarSessionRow
							key={card.id}
							dotColor={cardStatusColor(card)}
							label={sidebarSessionLabel(card)}
						/>
					))}
				</div>

				<div className="mb-0.5 flex h-7 w-full items-center gap-1.5 rounded-md px-2 pr-1 text-[12px] font-medium text-[var(--preview-passive)]">
					<FolderOpenIcon className="h-3.5 w-3.5 shrink-0" />
					<span className="min-w-0 flex-1 truncate">Projects</span>
					<span
						aria-hidden="true"
						className="grid h-4 w-4 shrink-0 place-items-center text-[var(--preview-passive)]"
					>
						<PlusIcon className="h-3.5 w-3.5" />
					</span>
				</div>
			</div>

			<div className="flex min-h-0 flex-1 flex-col overflow-hidden px-2">
				<div className="relative z-20 mb-px shrink-0">
					<div className="relative flex h-8 w-full items-center gap-2 rounded-lg bg-[var(--preview-sidebar-accent)] px-2.5 pr-[84px] text-left text-[12px] font-medium text-[var(--preview-foreground)]">
						<FolderOpenIcon className="h-3.5 w-3.5 shrink-0" />
						<span className="min-w-0 flex-1 truncate">agent-orchestrator</span>
					</div>
					<div className="absolute inset-y-0 right-1.5 z-30 flex items-center gap-px">
						<ProjectActionIcon>
							<LayoutGridIcon className="h-4 w-4" />
						</ProjectActionIcon>
						<ProjectActionIcon>
							<OrchestratorIcon className="h-4 w-4" />
						</ProjectActionIcon>
						<ProjectActionIcon>
							<MoreVerticalIcon className="h-4 w-4" />
						</ProjectActionIcon>
					</div>
				</div>

				<div className="min-h-0 flex-1 overflow-y-auto py-0.5 scrollbar-hide">
					<div className="ml-2 flex flex-col gap-px">
						{projectCards.map((card) => (
							<SidebarSessionRow
								key={card.id}
								dotColor={cardStatusColor(card)}
								label={sidebarSessionLabel(card)}
							/>
						))}
					</div>
				</div>
			</div>

			<div className="mt-auto mb-[3px] flex shrink-0 items-center px-2 pb-2">
				<div className="flex h-9 w-full items-center gap-2 rounded-lg px-2.5 text-[12px] font-medium text-[var(--preview-muted-foreground)]">
					<SettingsIcon className="h-3.5 w-3.5 shrink-0" />
					<span>Settings</span>
				</div>
			</div>
		</aside>
	);
}

function ArchiveBar({ count }: { count: number }) {
	return (
		<div className="flex h-12 shrink-0 items-center border-t border-[var(--preview-divider)] px-3">
			<div className="inline-flex h-full w-full items-center gap-2 text-[10.5px] text-[var(--preview-muted-foreground)] outline-none">
				<svg
					aria-hidden="true"
					className="h-3 w-3 shrink-0 text-[var(--preview-passive)]"
					viewBox="0 0 16 16"
					fill="none"
				>
					<path
						d="M6 4l4 4-4 4"
						stroke="currentColor"
						strokeLinecap="round"
						strokeLinejoin="round"
						strokeWidth="1.5"
					/>
				</svg>
				<span className="font-mono text-[10px] font-medium uppercase tracking-[0.04em]">Archive</span>
				<span className="font-mono tabular-nums text-[var(--preview-passive)]">{count}</span>
			</div>
		</div>
	);
}

function BoardChrome({ viewMode }: { viewMode: ViewMode }) {
	return (
		<div className="flex h-10 shrink-0 items-center gap-2 border-b border-[var(--preview-border-strong)] px-4">
			<div className="min-w-0 truncate text-[16px] font-semibold tracking-tight leading-none text-[var(--preview-foreground)]">
				{viewMode === "orchestrator" ? "Orchestrator" : "agent-orchestrator"}
			</div>
			<div className="min-w-0 flex-1" />
				<span
				aria-hidden="true"
				className="inline-flex h-[32px] items-center gap-1.5 rounded-md border border-[var(--preview-border)] bg-[var(--preview-raised)] px-3 text-[12px] font-semibold leading-none text-[var(--preview-muted-foreground)]"
			>
				<PlusIcon className="h-3.5 w-3.5" />
				<span>New task</span>
			</span>
			<span
				aria-hidden="true"
				className="inline-flex h-[32px] items-center gap-1.5 rounded-md bg-[var(--preview-primary)] px-3 text-[12px] font-semibold leading-none text-[var(--preview-primary-foreground)]"
			>
				<OrchestratorIcon className="h-3.5 w-3.5" />
				Orchestrator
			</span>
			<span
				aria-hidden="true"
				className="grid size-[32px] shrink-0 place-items-center rounded-md text-[var(--preview-muted-foreground)]"
			>
				<BellIcon className="h-5 w-5 mb-2" />
			</span>
		</div>
	);
}

type ActivityIconId = "check" | "warning" | "github" | "waiting" | "spinner";

type CardVisualState = {
	prStatus: string;
	prClass: string;
	activityColor: string;
	activityIcon: ActivityIconId;
};

function getCardVisualState(card: PreviewCard): CardVisualState {
	const prStatus =
		card.tone === "ready"
			? "approved"
			: card.tone === "review"
				? "in review"
				: card.tone === "blocked"
					? "changes requested"
					: "open";
	const prClass =
		card.tone === "ready"
			? "text-[#86efac]"
			: card.tone === "blocked"
				? "text-[#fdba74]"
				: card.tone === "review"
					? "text-[#fcd34d]"
					: "text-[#9ca3af]";
	const activityColor =
		card.activityState === "passed"
			? "text-[#86efac]"
			: card.activityState === "failed"
				? "text-[#f87171]"
				: card.activityState === "reviewing"
					? "text-[#93c5fd]"
					: card.activityState === "waiting"
						? "text-[#fdba74]"
						: "text-[#9ca3af]";
	const activityIcon: ActivityIconId =
		card.activityState === "passed"
			? "check"
			: card.activityState === "failed"
				? "warning"
				: card.activityState === "reviewing"
					? "github"
					: card.activityState === "waiting"
						? "waiting"
						: "spinner";
	return { prStatus, prClass, activityColor, activityIcon };
}

function CircleProgressIcon({ pass, total }: { pass: number; total: number }) {
	const r = 5;
	const circ = 2 * Math.PI * r;
	const ratio = total > 0 ? pass / total : 0;
	const dash = ratio * circ;
	const trackColor = "#374151";
	const fillColor = ratio >= 1 ? "#4ade80" : ratio < 0.5 ? "#fb923c" : "#e5e7eb";
	return (
		<svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true" style={{ flexShrink: 0 }}>
			<circle cx="6" cy="6" r={r} stroke={trackColor} strokeWidth="1.5" />
			<circle
				cx="6"
				cy="6"
				r={r}
				stroke={fillColor}
				strokeWidth="1.5"
				strokeDasharray={`${dash} ${circ}`}
				strokeLinecap="round"
				transform="rotate(-90 6 6)"
			/>
		</svg>
	);
}

function ActivityIcon({ id, testResults }: { id: ActivityIconId; testResults?: { pass: number; total: number } }) {
	if (id === "check") return <CheckIcon className="h-3 w-3" />;
	if (id === "warning") return <WarningIcon className="h-3 w-3" />;
	if (id === "github") return <GitHubIcon className="h-3 w-3" />;
	if (id === "waiting") return <WaitingIcon className="h-3 w-3" />;
	if (testResults) return <CircleProgressIcon pass={testResults.pass} total={testResults.total} />;
	return (
		<span className="h-3 w-3 animate-spin rounded-full border border-[#4b5563] border-t-[#d1d5db]" />
	);
}

// Card boxes from PR #3496 (ronishrohan hero board redesign), adapted to
// this branch's column ids: staging→action, in_review→pending.
function BoardCard({
	card,
	isPulsing,
}: {
	card: PreviewCard;
	isPulsing: boolean;
}) {
	const isTestCard = card.column === "action" && !!card.testResults;
	const testTotal = card.testResults?.total ?? 0;
	const [animatedPass, setAnimatedPass] = useState(() =>
		isTestCard ? Math.floor(testTotal * 0.2) : 0,
	);
	useEffect(() => {
		if (!isTestCard) return;
		let timeout: number;
		const tick = () => {
			setAnimatedPass((p) => {
				const jump = Math.floor(Math.random() * 4) + 1;
				const next = p + jump;
				return next >= testTotal ? Math.floor(testTotal * 0.2) : next;
			});
			timeout = window.setTimeout(tick, 200 + Math.random() * 350);
		};
		timeout = window.setTimeout(tick, 200 + Math.random() * 350);
		return () => window.clearTimeout(timeout);
	}, [isTestCard, testTotal]);

	const prMatch = card.pr.match(/PR\s+#(\d+)/i);
	const { prStatus, prClass, activityColor, activityIcon } = getCardVisualState(card);
	const isWaiting = card.activityState === "waiting";
	const isReviewFlag = isWaiting && card.column === "pending";
	const attentionBorder = isWaiting
		? isReviewFlag
			? "border-[#f87171]/70"
			: "border-[#fb923c]/60"
		: "border-[var(--preview-border)]";
	const badgeColor = isReviewFlag ? "bg-[#f87171]" : "bg-[#fb923c]";
	const attentionAnim = isWaiting && isPulsing ? "ao-attention-pulse" : "";

	return (
		<motion.div
			layout
			layoutId={`${card.id}-${card.column}`}
			initial={{ opacity: 0, scale: 0.98, y: -8 }}
			animate={
				card.merging
					? { opacity: 0, scale: 0.96, y: -8 }
					: { opacity: 1, scale: 1, y: 0 }
			}
			exit={{ opacity: 0, scale: 0.96, y: -8 }}
			transition={{
				duration: 0.22,
				ease: [0.22, 1, 0.36, 1],
				layout: { duration: 0.25, ease: [0.22, 1, 0.36, 1] },
			}}
			className={`pointer-events-none rounded-lg border ${attentionBorder} bg-[var(--preview-card)] shadow-[0_1px_1px_rgba(0,0,0,0.05)] ${attentionAnim}`}
		>
			<div className="flex items-start gap-2.5 px-3.5 pb-2.5 pt-3">
				<div className="relative mt-0.5 h-3.5 w-3.5 shrink-0">
					<img
						src={card.icon}
						alt=""
						width={14}
						height={14}
						aria-hidden="true"
						className="h-3.5 w-3.5"
						draggable="false"
					/>
					{isWaiting ? (
						<span
							aria-hidden="true"
							className={`pointer-events-none absolute -right-1 -top-1 flex h-2.5 w-2.5 items-center justify-center rounded-full ${badgeColor} text-[7px] font-black leading-none text-white shadow-[0_0_0_1.5px_var(--preview-card)]`}
						>
							!
						</span>
					) : null}
				</div>
				<div className="min-w-0 text-[11.5px] font-semibold leading-tight tracking-tight text-[var(--preview-card-foreground)]">
					{card.title}
				</div>
			</div>
			<div className="mt-0 px-3.5 text-[9.5px] leading-4 text-[var(--preview-muted-foreground)]">
				<div className="flex items-center gap-1.5 py-1">
					<BranchIcon className="h-3 w-3 shrink-0" />
					<span className="truncate font-mono">{card.branch}</span>
				</div>
				{prMatch ? (
					<div className={`flex items-center gap-1.5 py-1 ${prClass}`}>
						<PullRequestIcon className="h-3 w-3 shrink-0" />
						<span className="font-mono">#{prMatch[1]}</span>
						<span className="truncate">{prStatus}</span>
					</div>
				) : null}
			</div>
			{(card.column === "pending" || card.column === "merge") &&
			(card.reviewers?.length || card.prComments !== undefined || card.testResults) ? (
				<div className="flex items-center gap-1.5 px-3.5 pb-2">
					{card.reviewers && card.reviewers.length > 0 ? (
						<div className="flex shrink-0 -space-x-1.5">
							{card.reviewers.slice(0, 3).map((src) => (
								<img
									key={src}
									src={src}
									alt=""
									width={18}
									height={18}
									aria-hidden="true"
									draggable="false"
									className="h-[18px] w-[18px] rounded-full ring-1 ring-[var(--preview-card)]"
								/>
							))}
						</div>
					) : null}
					{card.column === "pending" && card.testResults ? (
						<span
							className={`text-[10px] ${card.testResults.pass < card.testResults.total ? "text-[#fb923c]" : "text-[var(--preview-muted-foreground)]"}`}
						>
							{card.testResults.pass}/{card.testResults.total} tests
						</span>
					) : null}
					{card.prComments !== undefined ? (
						<span
							className={`ml-auto text-[10px] ${card.prComments > 0 ? "text-[#fb923c]" : "text-[var(--preview-muted-foreground)]"}`}
						>
							{card.prComments === 0
								? "no comments"
								: `${card.prComments} comment${card.prComments !== 1 ? "s" : ""}`}
						</span>
					) : null}
				</div>
			) : null}
			{card.tone === "ready" ? (
				<div className="flex items-center justify-between gap-2 border-t border-[var(--preview-border)] px-3.5 py-2.5">
					<span className="inline-flex h-6 items-center justify-center whitespace-nowrap rounded-md bg-[var(--preview-primary)] px-2.5 text-[10.5px] font-semibold text-[var(--preview-primary-foreground)]">
						Merge PR
					</span>
					<span className="shrink-0 text-[10.5px] text-[var(--preview-muted-foreground)]">
						{card.time}
					</span>
				</div>
			) : (
				<div className="flex items-center justify-between border-t border-[var(--preview-border)] px-3.5 py-2.5">
					<span className={`inline-flex items-center gap-1.5 text-[10.5px] ${activityColor}`}>
						<ActivityIcon
							id={activityIcon}
							testResults={isTestCard ? { pass: animatedPass, total: testTotal } : undefined}
						/>
						{isTestCard ? `${animatedPass}/${testTotal} passed` : card.activity}
					</span>
					<span className="text-[10.5px] text-[var(--preview-muted-foreground)]">
						{card.time}
					</span>
				</div>
			)}
		</motion.div>
	);
}

// Column chrome + titles from PR #3496.
function BoardColumn({
	cards,
	color,
	count,
	title,
}: {
	cards: PreviewCard[];
	color: string;
	count: number;
	title: string;
}) {
	const attentionCards = cards.filter((c) => c.activityState === "waiting");
	const normalCards = cards.filter((c) => c.activityState !== "waiting");
	const sortedCards = [...attentionCards, ...normalCards];
	const pulsingId = attentionCards[0]?.id ?? null;
	const extraWaiting = Math.max(0, attentionCards.length - 1);

	return (
		<section className="flex min-h-0 min-w-0 snap-start flex-col border-r border-[var(--preview-divider)] last:border-r-0">
			<div className="flex h-10 shrink-0 items-center gap-2.5 border-b border-[var(--preview-divider)] px-4">
				<span className="h-2 w-2 rounded-full" style={{ backgroundColor: color }} />
				<div className="text-[10.5px] font-medium tracking-tight text-[var(--preview-muted-foreground)]">
					{title}
				</div>
				<div className="ml-auto font-mono text-[10.5px] leading-none text-[var(--preview-muted-foreground)] opacity-60">
					{count}
				</div>
				{extraWaiting > 0 ? (
					<div className="inline-flex items-center gap-1 rounded-[4px] bg-[#fb923c]/10 px-1.5 py-0.5 text-[9px] font-semibold text-[#fb923c]">
						<WaitingIcon className="h-2.5 w-2.5" />
						{extraWaiting} waiting
					</div>
				) : null}
			</div>
			<div className="min-h-0 flex-1 space-y-2.5 overflow-y-auto px-3 pb-3 pt-3 scrollbar-hide">
				<AnimatePresence initial={false}>
					{sortedCards.map((card) => (
						<BoardCard
							key={`${card.id}-${card.column}`}
							card={card}
							isPulsing={card.id === pulsingId}
						/>
					))}
				</AnimatePresence>
			</div>
		</section>
	);
}

function OrchestratorView({
	cards,
	selectedTrack,
}: {
	cards: PreviewCard[];
	selectedTrack: TrackItem;
}) {
	const activeCards = cards.filter((card) => !card.merging);
	const workingCards = activeCards.filter((card) => card.column === "working");
	const waitingCards = activeCards.filter((card) => card.column === "action");
	const readyCards = activeCards.filter((card) => card.column === "merge");
	const leadWorker = workingCards[0] ?? activeCards[0];

	return (
		<div className="grid min-h-0 flex-1 grid-cols-1 overflow-hidden bg-[var(--preview-background)] sm:grid-cols-[minmax(0,1.05fr)_minmax(260px,0.65fr)]">
			<section className="flex min-h-0 flex-col p-3 sm:border-r sm:border-[var(--preview-border)] sm:p-4">
				<div className="flex items-center gap-3">
					<div className="grid h-9 w-9 place-items-center rounded-[10px] border border-[var(--preview-border)] bg-[var(--preview-muted)] text-[var(--preview-foreground)]">
						<BeakerIcon className="h-5 w-5" />
					</div>
					<div className="min-w-0">
						<div className="text-[13px] font-semibold tracking-[-0.5px] text-[var(--preview-foreground)]">
							Agent Orchestrator
						</div>
						<div className="truncate text-[10px] text-[var(--preview-muted-foreground)]">
							Planning workers for {selectedTrack.label.toLowerCase()}
						</div>
					</div>
				</div>

				<div className="mt-5 rounded-[10px] border border-[var(--preview-border)] bg-[var(--preview-card)] p-4">
					<div className="mb-3 text-[11px] font-semibold tracking-[-0.5px] text-[var(--preview-muted-foreground)]">
						Current plan
					</div>
					<div className="space-y-3 text-[12px] leading-5 text-[var(--preview-foreground)]">
						<div className="flex gap-2">
							<CheckIcon className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[#86efac]" />
							<span>Read project context and split the track into worker-sized branches.</span>
						</div>
						<div className="flex gap-2">
							<span className="mt-1 h-3 w-3 shrink-0 animate-spin rounded-full border border-[#4b5563] border-t-[#d1d5db]" />
							<span>
								Keep {workingCards.length || 1} worker{workingCards.length === 1 ? "" : "s"} moving while routing blockers back here.
							</span>
						</div>
						<div className="flex gap-2">
							<WaitingIcon className="mt-0.5 h-3.5 w-3.5 shrink-0 text-[#fcd34d]" />
							<span>
								Escalate {waitingCards.length} decision{waitingCards.length === 1 ? "" : "s"} and queue {readyCards.length} approved PR{readyCards.length === 1 ? "" : "s"} for merge.
							</span>
						</div>
					</div>
				</div>

				<div className="mt-4 min-h-0 flex-1 rounded-[10px] border border-[var(--preview-border)] bg-[var(--preview-card)] p-4 font-mono text-[11px] leading-5 text-[var(--preview-muted-foreground)]">
					<div className="text-[var(--preview-muted-foreground)]">ao orchestrator</div>
					<div className="mt-3 text-[var(--preview-foreground)]">
						<span className="text-[#60a5fa]">track</span> {selectedTrack.id}
					</div>
					<div className="mt-2 text-[var(--preview-foreground)]">
						<span className="text-[#60a5fa]">next</span>{" "}
						{leadWorker ? `watch ${leadWorker.branch}` : "spawn first worker"}
					</div>
					<div className="mt-2 text-[var(--preview-muted-foreground)]">
						Workers report back here when checks fail, reviews arrive, or a branch is ready to land.
					</div>
				</div>
			</section>

			<aside className="hidden min-h-0 flex-col bg-[var(--preview-background)] p-4 sm:flex">
				<div className="text-[11px] font-semibold tracking-[-0.5px] text-[var(--preview-muted-foreground)]">
					Worker queue
				</div>
				<div className="mt-3 space-y-2 overflow-y-auto scrollbar-hide">
					{activeCards.slice(0, 4).map((card) => (
						<div key={card.id} className="rounded-[8px] border border-[var(--preview-border)] bg-[var(--preview-card)] p-3">
							<div className="flex items-center gap-2">
								<img
									src={card.icon}
									alt=""
									width={14}
									height={14}
									aria-hidden="true"
									className="h-3.5 w-3.5"
									draggable="false"
								/>
								<div className="min-w-0 flex-1 truncate text-[11px] font-medium text-[var(--preview-card-foreground)]">
									{card.title}
								</div>
							</div>
							<div className="mt-2 truncate font-mono text-[10px] text-[var(--preview-muted-foreground)]">
								{card.branch}
							</div>
						</div>
					))}
				</div>
				<span
					aria-hidden="true"
					className="mt-4 inline-flex h-8 items-center justify-center gap-2 rounded-[8px] bg-[var(--preview-primary)] px-3 text-[12px] font-semibold text-[var(--preview-primary-foreground)]"
				>
					<PlusIcon className="h-4 w-4" />
					Spawn worker
				</span>
			</aside>
		</div>
	);
}

export function AppMockup() {
	const [cardsByTrack, setCardsByTrack] = useState(createInitialCardsByTrack);
	const [mergedCounts, setMergedCounts] = useState<Record<TrackId, number>>({
		landing: 18,
		deploy: 11,
		stars: 24,
		icons: 16,
		footer: 9,
	});
	const selectedTrackId: TrackId = "landing";
	const viewMode: ViewMode = "board";
	const incomingIndexes = useRef<Record<TrackId, number>>({
		landing: 0,
		deploy: 0,
		stars: 0,
		icons: 0,
		footer: 0,
	});
	const windowRef = useRef<HTMLDivElement>(null);
	const contentRef = useRef<HTMLDivElement>(null);
	useFloatingWindow(windowRef);
	useDecorativeSubtree(windowRef);

	// Keep the board at the design size and scale the whole chrome to the shell.
	// Shrinking the layout box itself reflows columns and clips Mergeable / Merge.
	// Shell resize is aspect-locked, so width/BASE_WIDTH fills both axes with no gaps.
	// Keep the SSR canvas hidden until this scale is applied to avoid a full-size mobile flash.
	useLayoutEffect(() => {
		const outer = windowRef.current;
		const content = contentRef.current;
		if (!outer || !content) return;

		const syncScale = () => {
			const width = outer.clientWidth;
			if (width <= 0) return;
			content.style.transform = `scale(${width / BASE_WIDTH})`;
			content.style.visibility = "visible";
		};

		syncScale();
		const observer = new ResizeObserver(syncScale);
		observer.observe(outer);
		window.addEventListener("resize", syncScale);
		return () => {
			observer.disconnect();
			window.removeEventListener("resize", syncScale);
		};
	}, []);

	const selectedTrack =
		projectItems.find((item) => item.id === selectedTrackId) ?? projectItems[0];
	const cards = cardsByTrack[selectedTrackId];
	const mergedCount = mergedCounts[selectedTrackId];

	const updateTrackCards = useCallback(
		(trackId: TrackId, update: (cards: PreviewCard[]) => PreviewCard[]) => {
			setCardsByTrack((current) => ({
				...current,
				[trackId]: update(current[trackId]),
			}));
		},
		[],
	);

	const mergeCard = useCallback((id: string) => {
		const trackId = selectedTrackId;
		updateTrackCards(trackId, (current) =>
			current.map((card) => (card.id === id ? { ...card, merging: true } : card)),
		);

		window.setTimeout(() => {
			updateTrackCards(trackId, (current) => current.filter((card) => card.id !== id));
			setMergedCounts((current) => ({
				...current,
				[trackId]: current[trackId] + 1,
			}));
		}, 240);
	}, [selectedTrackId, updateTrackCards]);

	useEffect(() => {
		let timeoutId: number;

		const scheduleNext = () => {
			timeoutId = window.setTimeout(runStep, randomDelay());
		};

		const runStep = () => {
			const trackId = selectedTrackId;
			const incomingCards = incomingCardsByTrack[trackId];
			updateTrackCards(trackId, (current) => {
				const chosen = randomItem(current.filter((card) => !card.merging));

				let next = current;
				if (chosen) {
					if (chosen.column === "merge") {
						window.setTimeout(() => mergeCard(chosen.id), 0);
					} else {
						next = current.map((card) =>
							card.id === chosen.id ? advanceCard(card) : card,
						);
					}
				}

				const workingCount = next.filter((card) => card.column === "working").length;
				const activeCount = next.filter((card) => !card.merging).length;
				if (workingCount < 2 && activeCount < 7 && Math.random() < 0.5) {
					const existingTitles = new Set(next.map((card) => card.title));
					const templateOffset = incomingCards.findIndex((_, offset) => {
						const candidate =
							incomingCards[
								(incomingIndexes.current[trackId] + offset) % incomingCards.length
							];
						return candidate ? !existingTitles.has(candidate.title) : false;
					});

					if (templateOffset >= 0) {
						const templateIndex =
							(incomingIndexes.current[trackId] + templateOffset) %
							incomingCards.length;
						const template = incomingCards[templateIndex];
						if (template) {
							incomingIndexes.current[trackId] = templateIndex + 1;
							next = [
								{
									...template,
									column: "working",
									activityState: "running",
									id: `${trackId}-incoming-${incomingIndexes.current[trackId]}`,
								},
								...next,
							];
						}
					}
				}

				return next;
			});

			scheduleNext();
		};

		scheduleNext();
		return () => window.clearTimeout(timeoutId);
	}, [mergeCard, selectedTrackId, updateTrackCards]);

	const boardColumns = columns.map((column) => {
		const columnCards = cards.filter((card) => card.column === column.id);
		return {
			...column,
			cards: columnCards,
			count: columnCards.length,
		};
	});

	return (
		<div
			ref={windowRef}
			role="img"
			aria-label="Preview of the Agent Orchestrator board: agent tasks move across Pending Work, Iterating, In Review, and Ready to merge."
			className="absolute z-10 select-none overflow-hidden rounded-[var(--mockup-shell-radius)] border border-[var(--preview-border)] bg-[var(--preview-sidebar)] font-sans tracking-tight text-[var(--preview-foreground)] antialiased shadow-[0_30px_80px_-24px_rgba(0,0,0,0.75)] [&_.font-mono]:tracking-normal"
			style={mockupShellStyle}
		>
			<style>{`
				@keyframes ao-attention-pulse-frames {
					0%, 100% { box-shadow: 0 0 0 0 rgba(251, 146, 60, 0.35); }
					50% { box-shadow: 0 0 0 4px rgba(251, 146, 60, 0); }
				}
				.ao-attention-pulse {
					animation: ao-attention-pulse-frames 1.2s ease-in-out infinite;
				}
				@media (prefers-reduced-motion: reduce) {
					.ao-attention-pulse { animation: none; }
				}
			`}</style>
			<div className="relative h-full w-full overflow-hidden">
				<div
					ref={contentRef}
					className="invisible h-(--mockup-design-h) w-(--mockup-design-w) origin-top-left"
				>
					<div className="flex h-full min-h-0">
						<Sidebar cards={cards} />
						<div className="flex min-h-0 min-w-0 flex-1 flex-col p-[2px]">
							<div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-[var(--mockup-inner-radius)] border border-[var(--preview-border-strong)] bg-[var(--preview-background)]">
								<BoardChrome viewMode={viewMode} />
								<>
									<div className="relative flex min-h-0 flex-1 flex-col overflow-hidden">
										<LayoutGroup key={selectedTrack.id}>
											<div className="grid min-h-0 flex-1 grid-cols-4 overflow-hidden">
												{boardColumns.map((column) => (
													<BoardColumn
														key={column.id}
														cards={column.cards}
														color={COLUMN_COLORS[column.id]}
														count={column.count}
														title={column.title}
													/>
												))}
											</div>
										</LayoutGroup>
									</div>
									<ArchiveBar count={mergedCount} />
								</>
							</div>
						</div>
					</div>
				</div>
			</div>
		</div>
	);
}
