"use client";

import { AnimatePresence, domAnimation, LazyMotion, m, motion } from "motion/react";
import {
	Bell,
	ChevronRight,
	Folder,
	FolderOpen,
	Network,
	Plus,
	Search,
	Settings,
	Trash2,
} from "lucide-react";
import type { CSSProperties, ReactNode, RefObject } from "react";
import { useCallback, useEffect, useRef, useState } from "react";
import { usePreviewScale } from "../usePreviewScale";

const previewTokens = {
	"--preview-background": "oklch(0.185 0.006 285.885)",
	"--preview-foreground": "oklch(0.985 0 0)",
	"--preview-sidebar": "oklch(0.155 0.005 285.823)",
	"--preview-overlay": "oklch(0.28 0.008 285.885)",
	"--preview-muted": "oklch(0.274 0.006 286.033)",
	"--preview-muted-foreground": "oklch(0.705 0.015 286.067)",
	"--preview-passive": "oklch(0.442 0.017 285.786)",
	"--preview-border": "oklch(1 0 0 / 7%)",
	"--preview-input": "oklch(1 0 0 / 4%)",
	"--preview-input-border": "oklch(1 0 0 / 3%)",
	"--preview-primary": "oklch(0.92 0.004 286.32)",
	"--preview-primary-foreground": "oklch(0.21 0.006 285.885)",
	"--preview-terminal": "#101317",
	"--preview-terminal-fg": "#d7d7d2",
	"--preview-terminal-dim": "#7c7c7c",
	"--preview-interactive-active": "color-mix(in oklch, oklch(0.985 0 0) 7%, transparent)",
} as CSSProperties;

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

type ProjectId = "solkit-ui" | "metrics-api" | "northstar-web";
type SessionId = "orc" | "claude" | "codex" | "cursor";
type WorkerId = Exclude<SessionId, "orc">;
type LineTone = "fg" | "dim" | "error" | "success" | "working";

type DisplayLine = {
	id: string;
	tone: LineTone;
	text: string;
	marker?: string;
	indent?: number;
	blank?: boolean;
	skipAnim?: boolean;
};

type Worker = {
	id: WorkerId;
	task: string;
	provider: string;
	branch: string;
	path: string;
	statusLabel: string;
	statusTone: string;
	breathe: boolean;
	railTitle: string;
	railSubtitle: string;
};

type SessionStatusState = {
	label: string;
	tone: string;
	breathe: boolean;
};

type Project = {
	id: ProjectId;
	name: string;
	path: string;
	orchestratorPath: string;
	orchestratorPrompt: string;
};

const projects: Project[] = [
	{
		id: "solkit-ui",
		name: "solkit-ui",
		path: "~/ao/solkit-ui",
		orchestratorPath: "~/ao/solkit-ui/orchestrator",
		orchestratorPrompt: "Ship GitHub sign-in. Split route, tests, and docs into parallel tasks.",
	},
	{
		id: "metrics-api",
		name: "metrics-api",
		path: "~/ao/metrics-api",
		orchestratorPath: "~/ao/metrics-api/orchestrator",
		orchestratorPrompt: "Ship alert digests. Split delivery retries, query coverage, and the runbook into parallel tasks.",
	},
	{
		id: "northstar-web",
		name: "northstar-web",
		path: "~/ao/northstar-web",
		orchestratorPath: "~/ao/northstar-web/orchestrator",
		orchestratorPrompt: "Finish the pricing refresh. Split plan cards, mobile QA, and launch docs into parallel tasks.",
	},
];

const projectById = Object.fromEntries(projects.map((project) => [project.id, project])) as Record<ProjectId, Project>;
const workersByProject: Record<ProjectId, Record<WorkerId, Worker>> = {
	"solkit-ui": {
		claude: {
			id: "claude",
			task: "Build callback route",
			provider: "claude-code",
			branch: "ao/ao-12/auth-callback",
			path: "~/ao/ao-12/auth-callback",
			statusLabel: "Working",
			statusTone: status.working,
			breathe: true,
			railTitle: "Reject stale OAuth state",
			railSubtitle: "Callback route is green locally.",
		},
		codex: {
			id: "codex",
			task: "Add integration tests",
			provider: "codex",
			branch: "ao/ao-12/auth-flow",
			path: "~/ao/ao-12/auth-flow",
			statusLabel: "In review",
			statusTone: "#facc15",
			breathe: true,
			railTitle: "Cover callback flow end-to-end",
			railSubtitle: "PR is open and checks are running.",
		},
		cursor: {
			id: "cursor",
			task: "Update setup guide",
			provider: "cursor",
			branch: "ao/ao-12/auth-docs",
			path: "~/ao/ao-12/auth-docs",
			statusLabel: "Needs input",
			statusTone: status.needsYou,
			breathe: true,
			railTitle: "Refresh the auth setup guide",
			railSubtitle: "Needs a quick product decision.",
		},
	},
	"metrics-api": {
		claude: {
			id: "claude",
			task: "Fix digest retries",
			provider: "claude-code",
			branch: "ao/ao-14/digest-retries",
			path: "~/ao/ao-14/digest-retries",
			statusLabel: "Working",
			statusTone: status.working,
			breathe: true,
			railTitle: "Retry failed digest deliveries",
			railSubtitle: "Backoff path is being tightened.",
		},
		codex: {
			id: "codex",
			task: "Add query coverage",
			provider: "codex",
			branch: "ao/ao-14/digest-queries",
			path: "~/ao/ao-14/digest-queries",
			statusLabel: "In review",
			statusTone: "#facc15",
			breathe: true,
			railTitle: "Cover digest query edges",
			railSubtitle: "PR is up and checks are pending.",
		},
		cursor: {
			id: "cursor",
			task: "Update alert runbook",
			provider: "cursor",
			branch: "ao/ao-14/digest-runbook",
			path: "~/ao/ao-14/digest-runbook",
			statusLabel: "Working",
			statusTone: status.ready,
			breathe: true,
			railTitle: "Refresh the on-call runbook",
			railSubtitle: "Runbook is being rewritten for the new digest path.",
		},
	},
	"northstar-web": {
		claude: {
			id: "claude",
			task: "Refresh pricing cards",
			provider: "claude-code",
			branch: "ao/ao-16/pricing-cards",
			path: "~/ao/ao-16/pricing-cards",
			statusLabel: "Working",
			statusTone: status.working,
			breathe: true,
			railTitle: "Finalize pricing layout",
			railSubtitle: "Card spacing and hierarchy are in progress.",
		},
		codex: {
			id: "codex",
			task: "Run mobile QA",
			provider: "codex",
			branch: "ao/ao-16/mobile-qa",
			path: "~/ao/ao-16/mobile-qa",
			statusLabel: "Needs input",
			statusTone: status.needsYou,
			breathe: true,
			railTitle: "Check the mobile breakpoints",
			railSubtitle: "One viewport issue still needs a product call.",
		},
		cursor: {
			id: "cursor",
			task: "Polish launch docs",
			provider: "cursor",
			branch: "ao/ao-16/launch-docs",
			path: "~/ao/ao-16/launch-docs",
			statusLabel: "In review",
			statusTone: "#facc15",
			breathe: true,
			railTitle: "Update launch checklist",
			railSubtitle: "Docs branch is ready for review.",
		},
	},
};

const sessionMeta: Record<
	SessionId,
	{ icon: string; title: string; version: string; subtitle: string; path: string; tabLabel: string }
> = {
	orc: {
		icon: "/app-icons/agents/claude-code.svg",
		title: "Claude Code",
		version: "v2.1.204",
		subtitle: "Opus 4.8 (1M context) · Claude Team",
		path: "~/ao/solkit-ui/orchestrator",
		tabLabel: "orchestrator",
	},
	claude: {
		icon: "/app-icons/agents/claude-code.svg",
		title: "Claude Code",
		version: "v2.1.204",
		subtitle: "Opus 4.8 (1M context) · Claude Team",
		path: "",
		tabLabel: "",
	},
	codex: {
		icon: "/app-icons/agents/codex.svg",
		title: "Codex",
		version: "GPT-5 Code",
		subtitle: "High reasoning · Full tools",
		path: "",
		tabLabel: "",
	},
	cursor: {
		icon: "/app-icons/agents/cursor.svg",
		title: "Cursor",
		version: "Agent mode",
		subtitle: "Docs writer · Focused edit loop",
		path: "",
		tabLabel: "",
	},
};

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

type Step =
	| { type: "pause"; ms: number }
	| { type: "spawn"; session: WorkerId }
	| { type: "sessionStatus"; session: WorkerId; label: string; tone: string; breathe?: boolean }
	| { type: "project"; project: ProjectId }
	| { type: "switch"; session: SessionId }
	| { type: "type"; session: SessionId; text: string; marker?: string; highlight?: boolean }
	| { type: "line"; session: SessionId; tone: LineTone; text: string; marker?: string; indent?: number }
	| { type: "stream"; session: SessionId; text: string }
	| { type: "blank"; session: SessionId }
	| { type: "cursor"; target: string; click?: boolean }
	| { type: "reset" };

const TYPING_MS = 26;
const workerOrder: WorkerId[] = ["claude", "codex", "cursor"];

const SCRIPT: Step[] = [
	{ type: "pause", ms: 900 },
	{ type: "type", session: "orc", text: projectById["solkit-ui"].orchestratorPrompt, marker: "❯" },
	{ type: "pause", ms: 260 },
	{ type: "blank", session: "orc" },
	{ type: "line", session: "orc", tone: "fg", text: "Read(agent-orchestrator.yaml)", marker: "⏺" },
	{ type: "pause", ms: 220 },
	{ type: "stream", session: "orc", text: "Breaking this into three worker sessions so the route, the tests, and the guide move independently." },
	{ type: "pause", ms: 240 },
	{ type: "blank", session: "orc" },
	{ type: "line", session: "orc", tone: "fg", text: "Bash(ao spawn --name \"callback route\")", marker: "⏺" },
	{ type: "spawn", session: "claude" },
	{ type: "sessionStatus", session: "claude", label: "Working", tone: status.working, breathe: true },
	{ type: "pause", ms: 150 },
	{ type: "line", session: "orc", tone: "dim", text: "Started session ao-12/auth-callback", marker: "⎿" },
	{ type: "pause", ms: 180 },
	{ type: "line", session: "orc", tone: "fg", text: "Bash(ao spawn --name \"integration tests\")", marker: "⏺" },
	{ type: "spawn", session: "codex" },
	{ type: "sessionStatus", session: "codex", label: "Working", tone: status.working, breathe: true },
	{ type: "pause", ms: 150 },
	{ type: "line", session: "orc", tone: "dim", text: "Started session ao-12/auth-flow", marker: "⎿" },
	{ type: "pause", ms: 180 },
	{ type: "line", session: "orc", tone: "fg", text: "Bash(ao spawn --name \"setup guide\")", marker: "⏺" },
	{ type: "spawn", session: "cursor" },
	{ type: "sessionStatus", session: "cursor", label: "Working", tone: status.working, breathe: true },
	{ type: "pause", ms: 150 },
	{ type: "line", session: "orc", tone: "dim", text: "Started session ao-12/auth-docs", marker: "⎿" },
	{ type: "pause", ms: 250 },
	{ type: "stream", session: "orc", text: "Workers are live. Opening each terminal directly instead of waiting for any loading state." },
	{ type: "pause", ms: 180 },
	{ type: "line", session: "claude", tone: "working", text: "delegated from orchestrator: build the GitHub callback route", marker: "❯" },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "claude", tone: "fg", text: "Read(src/auth/index.ts)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "claude", tone: "dim", text: "Found existing state validation in login entrypoint", marker: "⎿" },
	{ type: "pause", ms: 130 },
	{ type: "stream", session: "claude", text: "Tracing the callback entrypoint and collecting the redirect + state validation branches first." },
	{ type: "pause", ms: 120 },
	{ type: "line", session: "claude", tone: "fg", text: "Read(src/auth/state.ts)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "claude", tone: "dim", text: "Need to reject stale state before token exchange", marker: "⎿" },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "claude", tone: "fg", text: "Edit(src/auth/callback.ts)", marker: "⏺" },
	{ type: "pause", ms: 100 },
	{ type: "line", session: "claude", tone: "dim", text: "+48 -2 lines", marker: "⎿" },
	{ type: "pause", ms: 260 },
	{ type: "line", session: "codex", tone: "working", text: "delegated from orchestrator: cover the callback flow with integration tests", marker: "❯" },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "codex", tone: "fg", text: "Write(tests/auth/callback.spec.ts)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "codex", tone: "dim", text: "Scaffolded happy-path callback case", marker: "⎿" },
	{ type: "pause", ms: 200 },
	{ type: "stream", session: "codex", text: "Building the happy path and stale-state cases before opening the PR so checks can start early." },
	{ type: "pause", ms: 100 },
	{ type: "line", session: "codex", tone: "fg", text: "Read(tests/helpers/auth-fixtures.ts)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "codex", tone: "dim", text: "Reusing existing GitHub callback fixture helpers", marker: "⎿" },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "codex", tone: "fg", text: "Bash(npm test -- auth/callback --runInBand)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "codex", tone: "dim", text: "11 passing, 1 flaky stale-state assertion", marker: "⎿" },
	{ type: "pause", ms: 170 },
	{ type: "line", session: "cursor", tone: "working", text: "delegated from orchestrator: update the auth setup guide", marker: "❯" },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "cursor", tone: "fg", text: "Edit(docs/auth/setup.md)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "cursor", tone: "dim", text: "Inserted callback URL and env var sections", marker: "⎿" },
	{ type: "pause", ms: 210 },
	{ type: "stream", session: "cursor", text: "Drafting the callback URLs and env var section while I wait on the flow choice." },
	{ type: "pause", ms: 100 },
	{ type: "line", session: "cursor", tone: "fg", text: "Read(docs/auth/troubleshooting.md)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "cursor", tone: "dim", text: "Linking common redirect mismatch failure mode", marker: "⎿" },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "cursor", tone: "fg", text: "Bash(markdownlint docs/auth/setup.md)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "cursor", tone: "dim", text: "One section title still too vague", marker: "⎿" },
	{ type: "pause", ms: 500 },
	{ type: "cursor", target: "session-claude", click: true },
	{ type: "switch", session: "claude" },
	{ type: "pause", ms: 1100 },
	{ type: "blank", session: "claude" },
	{ type: "line", session: "claude", tone: "fg", text: "Bash(npm run typecheck)", marker: "⏺" },
	{ type: "pause", ms: 160 },
	{ type: "line", session: "claude", tone: "dim", text: "typecheck clean after callback route changes", marker: "⎿" },
	{ type: "pause", ms: 120 },
	{ type: "line", session: "claude", tone: "fg", text: "Edit(src/auth/callback.ts)", marker: "⏺" },
	{ type: "pause", ms: 200 },
	{ type: "stream", session: "claude", text: "Wiring the redirect, validating the state param, and returning 401 before the token exchange runs." },
	{ type: "pause", ms: 220 },
	{ type: "line", session: "claude", tone: "fg", text: "Bash(npm test -- auth/callback)", marker: "⏺" },
	{ type: "pause", ms: 160 },
	{ type: "line", session: "claude", tone: "success", text: "Tests  6 passed (6)", marker: "⎿" },
	{ type: "sessionStatus", session: "claude", label: "Ready", tone: status.ready, breathe: false },
	{ type: "pause", ms: 520 },
	{ type: "cursor", target: "session-codex", click: true },
	{ type: "switch", session: "codex" },
	{ type: "pause", ms: 900 },
	{ type: "blank", session: "codex" },
	{ type: "line", session: "codex", tone: "fg", text: "Edit(tests/auth/callback.spec.ts)", marker: "⏺" },
	{ type: "pause", ms: 140 },
	{ type: "line", session: "codex", tone: "dim", text: "Stale-state assertion now stable across retries", marker: "⎿" },
	{ type: "pause", ms: 120 },
	{ type: "stream", session: "codex", text: "Adding the callback success path, stale-state rejection, and redirect preservation into one focused spec file." },
	{ type: "pause", ms: 200 },
	{ type: "line", session: "codex", tone: "fg", text: "Bash(npm test -- auth/callback --runInBand)", marker: "⏺" },
	{ type: "pause", ms: 140 },
	{ type: "line", session: "codex", tone: "success", text: "Tests  12 passed (12)", marker: "⎿" },
	{ type: "pause", ms: 120 },
	{ type: "line", session: "codex", tone: "fg", text: "Bash(gh pr create --fill)", marker: "⏺" },
	{ type: "pause", ms: 180 },
	{ type: "line", session: "codex", tone: "dim", text: "Opened PR #52", marker: "⎿" },
	{ type: "sessionStatus", session: "codex", label: "In review", tone: "#facc15", breathe: true },
	{ type: "pause", ms: 520 },
	{ type: "cursor", target: "session-cursor", click: true },
	{ type: "switch", session: "cursor" },
	{ type: "pause", ms: 1050 },
	{ type: "blank", session: "cursor" },
	{ type: "line", session: "cursor", tone: "fg", text: "Edit(docs/auth/setup.md)", marker: "⏺" },
	{ type: "pause", ms: 140 },
	{ type: "line", session: "cursor", tone: "dim", text: "Expanded callback setup checklist and screenshots note", marker: "⎿" },
	{ type: "pause", ms: 120 },
	{ type: "stream", session: "cursor", text: "Documenting callback URLs, required env vars, and the local GitHub app setup. I only need the PKCE vs implicit note confirmed." },
	{ type: "pause", ms: 200 },
	{ type: "line", session: "cursor", tone: "fg", text: "Bash(markdownlint docs/auth/setup.md)", marker: "⏺" },
	{ type: "pause", ms: 140 },
	{ type: "line", session: "cursor", tone: "success", text: "lint clean except for flow naming choice", marker: "⎿" },
	{ type: "pause", ms: 120 },
	{ type: "line", session: "cursor", tone: "error", text: "Decision needed: PKCE or implicit flow?", marker: "⏺" },
	{ type: "sessionStatus", session: "cursor", label: "Needs input", tone: status.needsYou, breathe: true },
	{ type: "pause", ms: 600 },
	{ type: "cursor", target: "project-metrics-api", click: true },
	{ type: "project", project: "metrics-api" },
	{ type: "pause", ms: 180 },
	{ type: "type", session: "orc", text: projectById["metrics-api"].orchestratorPrompt, marker: "❯" },
	{ type: "pause", ms: 240 },
	{ type: "blank", session: "orc" },
	{ type: "line", session: "orc", tone: "fg", text: "Read(agent-orchestrator.yaml)", marker: "⏺" },
	{ type: "pause", ms: 200 },
	{ type: "stream", session: "orc", text: "Splitting delivery retries, query coverage, and the runbook so the API work and docs can move in parallel." },
	{ type: "pause", ms: 220 },
	{ type: "line", session: "orc", tone: "fg", text: "Bash(ao spawn --name \"digest retries\")", marker: "⏺" },
	{ type: "spawn", session: "claude" },
	{ type: "sessionStatus", session: "claude", label: "Working", tone: status.working, breathe: true },
	{ type: "pause", ms: 140 },
	{ type: "line", session: "orc", tone: "dim", text: "Started session ao-14/digest-retries", marker: "⎿" },
	{ type: "pause", ms: 170 },
	{ type: "line", session: "orc", tone: "fg", text: "Bash(ao spawn --name \"query coverage\")", marker: "⏺" },
	{ type: "spawn", session: "codex" },
	{ type: "sessionStatus", session: "codex", label: "Working", tone: status.working, breathe: true },
	{ type: "pause", ms: 140 },
	{ type: "line", session: "orc", tone: "dim", text: "Started session ao-14/digest-queries", marker: "⎿" },
	{ type: "pause", ms: 170 },
	{ type: "line", session: "orc", tone: "fg", text: "Bash(ao spawn --name \"runbook\")", marker: "⏺" },
	{ type: "spawn", session: "cursor" },
	{ type: "sessionStatus", session: "cursor", label: "Working", tone: status.working, breathe: true },
	{ type: "pause", ms: 140 },
	{ type: "line", session: "orc", tone: "dim", text: "Started session ao-14/digest-runbook", marker: "⎿" },
	{ type: "pause", ms: 180 },
	{ type: "line", session: "claude", tone: "working", text: "delegated from orchestrator: fix alert digest retry behavior", marker: "❯" },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "claude", tone: "fg", text: "Edit(internal/alerts/retry.go)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "claude", tone: "dim", text: "Retry loop currently double-sends after reconnect", marker: "⎿" },
	{ type: "pause", ms: 160 },
	{ type: "stream", session: "claude", text: "Tightening the backoff path and making duplicate deliveries idempotent before I run the alert retry suite." },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "claude", tone: "fg", text: "Read(internal/alerts/delivery_store.go)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "claude", tone: "dim", text: "Need persisted attempt token for dedupe", marker: "⎿" },
	{ type: "pause", ms: 180 },
	{ type: "line", session: "codex", tone: "working", text: "delegated from orchestrator: add digest query coverage", marker: "❯" },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "codex", tone: "fg", text: "Write(internal/alerts/digest_query_test.go)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "codex", tone: "dim", text: "Added missing stale-window fixture", marker: "⎿" },
	{ type: "pause", ms: 160 },
	{ type: "stream", session: "codex", text: "Covering the empty-recipient and stale-window query cases first so the PR can open early." },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "codex", tone: "fg", text: "Bash(go test ./internal/alerts/...)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "codex", tone: "dim", text: "Query suite green, opening PR next", marker: "⎿" },
	{ type: "pause", ms: 180 },
	{ type: "line", session: "cursor", tone: "working", text: "delegated from orchestrator: refresh the alert digest runbook", marker: "❯" },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "cursor", tone: "fg", text: "Edit(docs/runbooks/alert-digests.md)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "cursor", tone: "dim", text: "Added retry escalation and rollback steps", marker: "⎿" },
	{ type: "pause", ms: 160 },
	{ type: "stream", session: "cursor", text: "Refreshing the on-call steps and rollout notes while the code branches are still running." },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "cursor", tone: "fg", text: "Read(docs/runbooks/incidents.md)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "cursor", tone: "dim", text: "Linking the generic incident drilldown page", marker: "⎿" },
	{ type: "pause", ms: 420 },
	{ type: "cursor", target: "session-claude", click: true },
	{ type: "switch", session: "claude" },
	{ type: "pause", ms: 980 },
	{ type: "blank", session: "claude" },
	{ type: "line", session: "claude", tone: "fg", text: "Bash(go test ./internal/alerts/...)", marker: "⏺" },
	{ type: "pause", ms: 180 },
	{ type: "line", session: "claude", tone: "success", text: "ok  internal/alerts  0.41s", marker: "⎿" },
	{ type: "sessionStatus", session: "claude", label: "Ready", tone: status.ready, breathe: false },
	{ type: "pause", ms: 480 },
	{ type: "cursor", target: "session-codex", click: true },
	{ type: "switch", session: "codex" },
	{ type: "pause", ms: 900 },
	{ type: "blank", session: "codex" },
	{ type: "line", session: "codex", tone: "fg", text: "Bash(gh pr create --fill)", marker: "⏺" },
	{ type: "pause", ms: 160 },
	{ type: "line", session: "codex", tone: "dim", text: "Opened PR #67", marker: "⎿" },
	{ type: "sessionStatus", session: "codex", label: "In review", tone: "#facc15", breathe: true },
	{ type: "pause", ms: 480 },
	{ type: "cursor", target: "session-cursor", click: true },
	{ type: "switch", session: "cursor" },
	{ type: "pause", ms: 1040 },
	{ type: "blank", session: "cursor" },
	{ type: "line", session: "cursor", tone: "fg", text: "Bash(markdownlint docs/runbooks/alert-digests.md)", marker: "⏺" },
	{ type: "pause", ms: 180 },
	{ type: "line", session: "cursor", tone: "success", text: "1 file linted, 0 issues", marker: "⎿" },
	{ type: "sessionStatus", session: "cursor", label: "Ready", tone: status.ready, breathe: false },
	{ type: "pause", ms: 500 },
	{ type: "cursor", target: "project-northstar-web", click: true },
	{ type: "project", project: "northstar-web" },
	{ type: "pause", ms: 180 },
	{ type: "type", session: "orc", text: projectById["northstar-web"].orchestratorPrompt, marker: "❯" },
	{ type: "pause", ms: 240 },
	{ type: "blank", session: "orc" },
	{ type: "line", session: "orc", tone: "fg", text: "Read(agent-orchestrator.yaml)", marker: "⏺" },
	{ type: "pause", ms: 200 },
	{ type: "stream", session: "orc", text: "Splitting the pricing launch into cards, mobile QA, and launch docs so the final polish lands faster." },
	{ type: "pause", ms: 220 },
	{ type: "line", session: "orc", tone: "fg", text: "Bash(ao spawn --name \"pricing cards\")", marker: "⏺" },
	{ type: "spawn", session: "claude" },
	{ type: "sessionStatus", session: "claude", label: "Working", tone: status.working, breathe: true },
	{ type: "pause", ms: 140 },
	{ type: "line", session: "orc", tone: "dim", text: "Started session ao-16/pricing-cards", marker: "⎿" },
	{ type: "pause", ms: 170 },
	{ type: "line", session: "orc", tone: "fg", text: "Bash(ao spawn --name \"mobile qa\")", marker: "⏺" },
	{ type: "spawn", session: "codex" },
	{ type: "sessionStatus", session: "codex", label: "Working", tone: status.working, breathe: true },
	{ type: "pause", ms: 140 },
	{ type: "line", session: "orc", tone: "dim", text: "Started session ao-16/mobile-qa", marker: "⎿" },
	{ type: "pause", ms: 170 },
	{ type: "line", session: "orc", tone: "fg", text: "Bash(ao spawn --name \"launch docs\")", marker: "⏺" },
	{ type: "spawn", session: "cursor" },
	{ type: "sessionStatus", session: "cursor", label: "Working", tone: status.working, breathe: true },
	{ type: "pause", ms: 140 },
	{ type: "line", session: "orc", tone: "dim", text: "Started session ao-16/launch-docs", marker: "⎿" },
	{ type: "pause", ms: 180 },
	{ type: "line", session: "claude", tone: "working", text: "delegated from orchestrator: refresh the pricing cards", marker: "❯" },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "claude", tone: "fg", text: "Edit(app/pricing/cards.tsx)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "claude", tone: "dim", text: "Rebalancing enterprise card hierarchy and CTA weights", marker: "⎿" },
	{ type: "pause", ms: 160 },
	{ type: "stream", session: "claude", text: "Retuning the tier hierarchy and spacing so the comparison table reads cleanly on first scan." },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "claude", tone: "fg", text: "Read(app/pricing/comparison-table.tsx)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "claude", tone: "dim", text: "Need consistent vertical rhythm with new cards", marker: "⎿" },
	{ type: "pause", ms: 180 },
	{ type: "line", session: "codex", tone: "working", text: "delegated from orchestrator: run mobile QA for pricing", marker: "❯" },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "codex", tone: "fg", text: "Bash(pnpm test mobile-pricing -- --viewport=390)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "codex", tone: "dim", text: "390px footer overlap reproduced", marker: "⎿" },
	{ type: "pause", ms: 160 },
	{ type: "stream", session: "codex", text: "Checking the narrow viewports first. One footer overlap still needs a quick product call." },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "codex", tone: "fg", text: "Bash(pnpm test mobile-pricing -- --viewport=430)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "codex", tone: "dim", text: "430px and 768px both clean", marker: "⎿" },
	{ type: "pause", ms: 180 },
	{ type: "line", session: "cursor", tone: "working", text: "delegated from orchestrator: polish the launch docs", marker: "❯" },
	{ type: "pause", ms: 90 },
	{ type: "line", session: "cursor", tone: "fg", text: "Edit(docs/launch/pricing-refresh.md)", marker: "⏺" },
	{ type: "pause", ms: 80 },
	{ type: "line", session: "cursor", tone: "dim", text: "Added rollout checklist and screenshot handoff", marker: "⎿" },
	{ type: "pause", ms: 160 },
	{ type: "stream", session: "cursor", text: "Updating the launch checklist, rollout note, and screenshot handoff while QA keeps running." },
	{ type: "pause", ms: 420 },
	{ type: "cursor", target: "session-claude", click: true },
	{ type: "switch", session: "claude" },
	{ type: "pause", ms: 1120 },
	{ type: "blank", session: "claude" },
	{ type: "line", session: "claude", tone: "success", text: "Visual diff looks good at desktop + tablet", marker: "⎿" },
	{ type: "sessionStatus", session: "claude", label: "Ready", tone: status.ready, breathe: false },
	{ type: "pause", ms: 480 },
	{ type: "cursor", target: "session-codex", click: true },
	{ type: "switch", session: "codex" },
	{ type: "pause", ms: 930 },
	{ type: "blank", session: "codex" },
	{ type: "line", session: "codex", tone: "error", text: "Decision needed: stack CTA buttons at 390px?", marker: "⏺" },
	{ type: "sessionStatus", session: "codex", label: "Needs input", tone: status.needsYou, breathe: true },
	{ type: "pause", ms: 480 },
	{ type: "cursor", target: "session-cursor", click: true },
	{ type: "switch", session: "cursor" },
	{ type: "pause", ms: 1010 },
	{ type: "blank", session: "cursor" },
	{ type: "line", session: "cursor", tone: "dim", text: "Docs branch ready for review", marker: "⎿" },
	{ type: "sessionStatus", session: "cursor", label: "In review", tone: "#facc15", breathe: true },
	{ type: "pause", ms: 1200 },
	{ type: "reset" },
];

const sessionKey = (projectId: ProjectId, sessionId: SessionId) => `${projectId}:${sessionId}` as const;
const workerFor = (projectId: ProjectId, workerId: WorkerId) => workersByProject[projectId][workerId];

const initialLines = (): Record<string, DisplayLine[]> => {
	const lines: Record<string, DisplayLine[]> = {};
	for (const project of projects) {
		lines[sessionKey(project.id, "orc")] = [];
		for (const workerId of workerOrder) lines[sessionKey(project.id, workerId)] = [];
	}
	return lines;
};

const initialStatuses = (): Record<ProjectId, Record<WorkerId, SessionStatusState>> => ({
	"solkit-ui": {
		claude: { label: "Idle", tone: status.idle, breathe: false },
		codex: { label: "Idle", tone: status.idle, breathe: false },
		cursor: { label: "Idle", tone: status.idle, breathe: false },
	},
	"metrics-api": {
		claude: { label: "Idle", tone: status.idle, breathe: false },
		codex: { label: "Idle", tone: status.idle, breathe: false },
		cursor: { label: "Idle", tone: status.idle, breathe: false },
	},
	"northstar-web": {
		claude: { label: "Idle", tone: status.idle, breathe: false },
		codex: { label: "Idle", tone: status.idle, breathe: false },
		cursor: { label: "Idle", tone: status.idle, breathe: false },
	},
});

const IDLE_POS = { x: 28, y: 78 };

export function DelegationDemo() {
	const [activeProject, setActiveProject] = useState<ProjectId>("solkit-ui");
	const [spawned, setSpawned] = useState<SessionId[]>(["orc"]);
	const [active, setActive] = useState<SessionId>("orc");
	const [linesBySession, setLinesBySession] = useState<Record<string, DisplayLine[]>>(initialLines);
	const [statusesByProject, setStatusesByProject] = useState<Record<ProjectId, Record<WorkerId, SessionStatusState>>>(initialStatuses);
	const [typing, setTyping] = useState<{ project: ProjectId; session: SessionId; marker?: string; chars: string; highlight?: boolean } | null>(null);
	const [streaming, setStreaming] = useState<{ project: ProjectId; session: SessionId; text: string } | null>(null);
	const [cursorTarget, setCursorTarget] = useState<string>("idle-rest");
	const [cursorPressed, setCursorPressed] = useState(false);
	const [pinnedOpen, setPinnedOpen] = useState(false);
	const rootRef = useRef<HTMLDivElement>(null);
	const timerRef = useRef<number>(0);
	const startedRef = useRef(false);
	const stepRef = useRef(0);
	const lineIdRef = useRef(0);
	const { viewportRef, viewportStyle, canvasStyle } = usePreviewScale(PREVIEW_WIDTH, PREVIEW_HEIGHT);

	const addLine = useCallback((project: ProjectId, session: SessionId, line: Omit<DisplayLine, "id">) => {
		const id = `l${++lineIdRef.current}`;
		setLinesBySession((prev) => ({
			...prev,
			[sessionKey(project, session)]: [...(prev[sessionKey(project, session)] ?? []), { ...line, id }],
		}));
	}, []);

	const resetDemo = useCallback(() => {
		setActiveProject("solkit-ui");
		setSpawned(["orc"]);
		setActive("orc");
		setLinesBySession(initialLines());
		setStatusesByProject(initialStatuses());
		setTyping(null);
		setStreaming(null);
		setCursorTarget("idle-rest");
		setCursorPressed(false);
		lineIdRef.current = 0;
		stepRef.current = 0;
	}, []);

	const runStep = useCallback(() => {
		if (stepRef.current >= SCRIPT.length) {
			stepRef.current = 0;
		}
		const step = SCRIPT[stepRef.current++];
		if (!step) return;

		switch (step.type) {
			case "pause":
				timerRef.current = window.setTimeout(runStep, step.ms);
				break;

			case "spawn":
				setSpawned((prev) => (prev.includes(step.session) ? prev : [...prev, step.session]));
				timerRef.current = window.setTimeout(runStep, 0);
				break;

			case "sessionStatus":
				setStatusesByProject((prev) => ({
					...prev,
					[activeProject]: {
						...prev[activeProject],
						[step.session]: {
							label: step.label,
							tone: step.tone,
							breathe: step.breathe ?? false,
						},
					},
				}));
				timerRef.current = window.setTimeout(runStep, 0);
				break;

			case "project":
				setActiveProject(step.project);
				setActive("orc");
				setSpawned(["orc"]);
				timerRef.current = window.setTimeout(runStep, 0);
				break;

			case "switch":
				setActive(step.session);
				timerRef.current = window.setTimeout(runStep, 0);
				break;

			case "line":
				addLine(activeProject, step.session, {
					tone: step.tone,
					text: step.text,
					marker: step.marker,
					indent: step.indent,
				});
				timerRef.current = window.setTimeout(runStep, 70);
				break;

			case "blank":
				addLine(activeProject, step.session, { tone: "dim", text: "", blank: true });
				timerRef.current = window.setTimeout(runStep, 70);
				break;

			case "type": {
				let charIdx = 0;
				const project = activeProject;
				setTyping({ project, session: step.session, marker: step.marker, chars: "", highlight: step.highlight });
				const typeNext = () => {
					if (charIdx >= step.text.length) {
						setTyping(null);
						addLine(project, step.session, {
							tone: step.highlight ? "working" : "fg",
							text: step.text,
							marker: step.marker,
							skipAnim: true,
						});
						timerRef.current = window.setTimeout(runStep, 100);
						return;
					}
					charIdx += 1;
					setTyping({
						project,
						session: step.session,
						marker: step.marker,
						chars: step.text.slice(0, charIdx),
						highlight: step.highlight,
					});
					timerRef.current = window.setTimeout(typeNext, TYPING_MS);
				};
				timerRef.current = window.setTimeout(typeNext, TYPING_MS);
				break;
			}

			case "stream": {
				let charIdx = 0;
				const project = activeProject;
				setStreaming({ project, session: step.session, text: "" });
				const streamNext = () => {
					if (charIdx >= step.text.length) {
						setStreaming(null);
						addLine(project, step.session, { tone: "fg", text: step.text, skipAnim: true });
						timerRef.current = window.setTimeout(runStep, 100);
						return;
					}
					const burst = 2 + Math.floor(Math.abs(Math.sin(charIdx * 0.7)) * 4);
					charIdx = Math.min(charIdx + burst, step.text.length);
					setStreaming({ project, session: step.session, text: step.text.slice(0, charIdx) });
					const delay = charIdx % 12 < 3 ? 80 : 20;
					timerRef.current = window.setTimeout(streamNext, delay);
				};
				timerRef.current = window.setTimeout(streamNext, 30);
				break;
			}

			case "cursor":
				setCursorTarget(step.target);
				setCursorPressed(false);
				timerRef.current = window.setTimeout(() => {
					if (!step.click) {
						runStep();
						return;
					}
					setCursorPressed(true);
					timerRef.current = window.setTimeout(() => {
						setCursorPressed(false);
						runStep();
					}, 150);
				}, 180);
				break;

			case "reset":
				resetDemo();
				timerRef.current = window.setTimeout(runStep, 900);
				break;
		}
	}, [activeProject, addLine, resetDemo]);

	const start = useCallback(() => {
		if (startedRef.current) return;
		startedRef.current = true;
		const reduce = typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
		if (reduce) {
			setActiveProject("solkit-ui");
			setSpawned(["orc", "claude", "codex", "cursor"]);
			setActive("claude");
			setStatusesByProject({
				"solkit-ui": {
					claude: { label: "Ready", tone: status.ready, breathe: false },
					codex: { label: "In review", tone: "#facc15", breathe: true },
					cursor: { label: "Needs input", tone: status.needsYou, breathe: true },
				},
				"metrics-api": {
					claude: { label: "Ready", tone: status.ready, breathe: false },
					codex: { label: "In review", tone: "#facc15", breathe: true },
					cursor: { label: "Ready", tone: status.ready, breathe: false },
				},
				"northstar-web": {
					claude: { label: "Ready", tone: status.ready, breathe: false },
					codex: { label: "Needs input", tone: status.needsYou, breathe: true },
					cursor: { label: "In review", tone: "#facc15", breathe: true },
				},
			});
			setLinesBySession({
				[sessionKey("solkit-ui", "orc")]: [
					{ id: "r1", tone: "fg", text: "Ship GitHub sign-in. Split route, tests, and docs into parallel tasks.", marker: "❯" },
					{ id: "r2", tone: "fg", text: "Bash(ao spawn --name \"callback route\")", marker: "⏺" },
					{ id: "r3", tone: "fg", text: "Bash(ao spawn --name \"integration tests\")", marker: "⏺" },
					{ id: "r4", tone: "fg", text: "Bash(ao spawn --name \"setup guide\")", marker: "⏺" },
				],
				[sessionKey("solkit-ui", "claude")]: [
					{ id: "r5", tone: "fg", text: "Build the GitHub callback route.", marker: "❯" },
					{ id: "r6", tone: "fg", text: "Edit(src/auth/callback.ts)", marker: "⏺" },
					{ id: "r7", tone: "success", text: "Tests  6 passed (6)", marker: "⎿" },
				],
				[sessionKey("solkit-ui", "codex")]: [
					{ id: "r8", tone: "fg", text: "Cover the callback flow with integration tests.", marker: "❯" },
					{ id: "r9", tone: "dim", text: "Opened PR #52", marker: "⎿" },
				],
				[sessionKey("solkit-ui", "cursor")]: [
					{ id: "r10", tone: "fg", text: "Update the auth setup guide.", marker: "❯" },
					{ id: "r11", tone: "error", text: "Decision needed: PKCE or implicit flow?", marker: "⏺" },
				],
				[sessionKey("metrics-api", "orc")]: [{ id: "r12", tone: "fg", text: projectById["metrics-api"].orchestratorPrompt, marker: "❯" }],
				[sessionKey("metrics-api", "claude")]: [{ id: "r13", tone: "fg", text: "Fix alert digest retry behavior.", marker: "❯" }],
				[sessionKey("metrics-api", "codex")]: [{ id: "r14", tone: "fg", text: "Add digest query coverage.", marker: "❯" }],
				[sessionKey("metrics-api", "cursor")]: [{ id: "r15", tone: "fg", text: "Update the alert digest runbook.", marker: "❯" }],
				[sessionKey("northstar-web", "orc")]: [{ id: "r16", tone: "fg", text: projectById["northstar-web"].orchestratorPrompt, marker: "❯" }],
				[sessionKey("northstar-web", "claude")]: [{ id: "r17", tone: "fg", text: "Refresh the pricing cards.", marker: "❯" }],
				[sessionKey("northstar-web", "codex")]: [{ id: "r18", tone: "error", text: "Decision needed: stack CTA buttons at 390px?", marker: "⏺" }],
				[sessionKey("northstar-web", "cursor")]: [{ id: "r19", tone: "dim", text: "Docs branch ready for review", marker: "⎿" }],
			});
			return;
		}
		timerRef.current = window.setTimeout(runStep, 250);
	}, [runStep]);

	useEffect(() => {
		const root = rootRef.current;
		if (!root) return;
		const onVisible = () => {
			const rect = root.getBoundingClientRect();
			const vh = window.innerHeight || 800;
			if (rect.top < vh * 0.78 && rect.bottom > vh * 0.22) {
				start();
			}
		};
		let observer: IntersectionObserver | undefined;
		try {
			observer = new IntersectionObserver(
				(entries) => {
					entries.forEach((entry) => {
						if (entry.isIntersecting && entry.intersectionRatio > 0.4) start();
					});
				},
				{ threshold: [0, 0.4, 0.7] },
			);
			observer.observe(root);
		} catch {
			// No-op.
		}
		window.addEventListener("scroll", onVisible, { passive: true });
		window.addEventListener("resize", onVisible, { passive: true });
		const visibilityTimer = window.setTimeout(onVisible, 250);
		return () => {
			observer?.disconnect();
			window.removeEventListener("scroll", onVisible);
			window.removeEventListener("resize", onVisible);
			window.clearTimeout(visibilityTimer);
			window.clearTimeout(timerRef.current);
			startedRef.current = false;
		};
	}, [start]);

	const visibleWorkers = spawned.filter((session): session is WorkerId => session !== "orc");
	const terminalLines = linesBySession[sessionKey(activeProject, active)] ?? [];
	const typingText = typing?.session === active && typing.project === activeProject ? typing : null;
	const streamingText = streaming?.session === active && streaming.project === activeProject ? streaming.text : null;

	return (
		<LazyMotion features={domAnimation}>
			<div
				ref={viewportRef}
				className="relative mx-auto w-full min-w-0 max-w-[620px]"
				style={viewportStyle}
			>
				<div
					ref={rootRef}
					className="absolute left-0 top-0 select-none overflow-hidden rounded-[10px] antialiased shadow-[0_28px_74px_-22px_rgba(0,0,0,0.86)]"
					style={{ ...previewTokens, ...canvasStyle, fontFamily: "var(--font-geist-sans), ui-sans-serif, system-ui, sans-serif" }}
				>
					<style>{`
						@keyframes ao-step-blink { 0%, 49% { opacity: 1; } 50%, 100% { opacity: 0; } }
						.ao-blink { animation: ao-step-blink 1s step-end infinite; }
						@media (prefers-reduced-motion: reduce) { .ao-blink { animation: none; } }
					`}</style>
					<div className="grid h-full min-w-0 grid-cols-[200px_minmax(0,1fr)] overflow-hidden bg-[var(--preview-sidebar)] text-[var(--preview-foreground)]">
						<div className="flex min-h-0">
							<PreviewSidebar
								activeProject={activeProject}
								active={active}
								pinnedOpen={pinnedOpen}
								setPinnedOpen={setPinnedOpen}
								statusesByProject={statusesByProject}
								visibleWorkers={visibleWorkers}
								onSelectSession={setActive}
								onSelectProject={setActiveProject}
							/>
						</div>
						<div className="flex min-h-0 min-w-0 flex-col bg-[var(--preview-background)] p-[2px]">
							<div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden rounded-[8px] border border-[var(--preview-border)] bg-[var(--preview-background)]">
								<PreviewTopbar project={projectById[activeProject]} active={active} statuses={statusesByProject[activeProject]} />
								<div className="flex min-h-0 min-w-0 flex-1">
									<TerminalPane project={projectById[activeProject]} active={active} lines={terminalLines} typingText={typingText} streamingText={streamingText} />
								</div>
							</div>
						</div>
					</div>
					<DemoCursor rootRef={rootRef} target={cursorTarget} pressed={cursorPressed} />
				</div>
			</div>
		</LazyMotion>
	);
}

function PreviewSidebar({
	activeProject,
	active,
	pinnedOpen,
	setPinnedOpen,
	statusesByProject,
	visibleWorkers,
	onSelectSession,
	onSelectProject,
}: {
	activeProject: ProjectId;
	active: SessionId;
	pinnedOpen: boolean;
	setPinnedOpen: (value: boolean | ((value: boolean) => boolean)) => void;
	statusesByProject: Record<ProjectId, Record<WorkerId, SessionStatusState>>;
	visibleWorkers: WorkerId[];
	onSelectSession: (session: SessionId) => void;
	onSelectProject: (project: ProjectId) => void;
}) {
	return (
		<div className="flex w-full min-w-0 flex-col bg-[var(--preview-sidebar)]">
			<div className="flex h-10 shrink-0 items-center gap-2 px-3">
				<div className="flex h-6 items-center gap-1.5">
					<span className="size-2.5 shrink-0 rounded-full bg-[#ff5f57]" />
					<span className="size-2.5 shrink-0 rounded-full bg-[#ffbd2e]" />
					<span className="size-2.5 shrink-0 rounded-full bg-[#28c840]" />
				</div>
			</div>
			<div className="flex shrink-0 items-center gap-1.5 px-3 pb-2">
				<img src="/ao-logo.svg" alt="" className="size-[18px] shrink-0 rounded-md" draggable={false} loading="lazy" decoding="async" />
				<span className="truncate text-sm font-semibold tracking-tight text-[var(--preview-foreground)]">Agent Orchestrator</span>
			</div>
			<div className="flex shrink-0 flex-col px-2">
				<div className="mb-3">
					<div className="flex h-8 items-center gap-2 rounded-md bg-[var(--preview-muted)] px-2.5">
						<Search size={14} strokeWidth={1.75} className="shrink-0 text-[var(--preview-muted-foreground)]" />
						<span className="text-sm leading-none text-[var(--preview-muted-foreground)]">Search</span>
					</div>
				</div>
				<button
					type="button"
					onClick={() => setPinnedOpen((value) => !value)}
					className="flex h-8 w-full items-center gap-2 rounded-md px-2.5 text-left text-sm font-medium text-[var(--preview-passive)] hover:bg-white/5"
				>
					<ChevronRight
						size={14}
						className="transition-transform"
						style={{ transform: pinnedOpen ? "rotate(90deg)" : undefined }}
					/>
					<span>Pinned</span>
				</button>
				{pinnedOpen ? (
					<div className="mb-1 ml-4 text-xs text-[var(--preview-passive)]">No pinned sessions</div>
				) : null}
				<div className="mb-0.5 flex h-8 items-center gap-2 px-2.5 text-sm font-medium text-[var(--preview-passive)]">
					<Folder size={14} />
					<span className="flex-1 truncate">Projects</span>
					<Plus size={14} />
				</div>
			</div>
			<div className="min-h-0 flex-1 overflow-y-auto px-2 scrollbar-hide">
				<div className="w-full">
					{projects.map((project) => {
						const isSelected = activeProject === project.id;
						const showSessions = project.id === activeProject && visibleWorkers.length > 0;
						return (
							<div key={project.id}>
								<ProjectRow
									projectId={project.id}
									selected={isSelected}
									onClick={() => {
										onSelectProject(project.id);
										onSelectSession("orc");
									}}
								>
									{project.name}
								</ProjectRow>
								<AnimatePresence initial={false}>
									{showSessions ? (
										<m.div
											key={`${project.id}-sessions`}
											initial={{ height: 0 }}
											animate={{ height: "auto" }}
											exit={{ height: 0 }}
											transition={{ duration: 0.14, ease: [0.25, 0.46, 0.45, 0.94] }}
											style={{ overflow: "hidden" }}
										>
											<m.div
												initial={{ y: -12, opacity: 0 }}
												animate={{ y: 0, opacity: 1 }}
												exit={{ y: -12, opacity: 0 }}
												transition={{ duration: 0.14, ease: [0.25, 0.46, 0.45, 0.94] }}
												className="ml-3.5 pt-1"
											>
												{visibleWorkers.map((sessionId) => {
													const worker = workerFor(project.id, sessionId);
													const sessionStatus = statusesByProject[project.id][sessionId];
													return (
														<SessionRow
															key={worker.id}
															worker={worker}
															sessionStatus={sessionStatus}
															active={active === worker.id}
															onClick={() => onSelectSession(worker.id)}
														/>
													);
												})}
											</m.div>
										</m.div>
									) : null}
								</AnimatePresence>
							</div>
						);
					})}
				</div>
			</div>
			<div className="mb-[3px] mt-auto flex shrink-0 items-center px-2 pb-2">
				<div className="flex h-9 w-full items-center gap-2.5 rounded-lg px-2.5 text-sm font-medium text-[var(--preview-muted-foreground)]">
					<Settings size={14} />
					<span>Settings</span>
				</div>
			</div>
		</div>
	);
}

function ProjectRow({
	children,
	projectId,
	selected,
	onClick,
}: {
	children: ReactNode;
	projectId: ProjectId;
	selected?: boolean;
	onClick: () => void;
}) {
	return (
		<button
			type="button"
			onClick={onClick}
			data-cursor-target={`project-${projectId}`}
			className="group flex h-9 w-full items-center gap-2.5 rounded-lg px-2.5 text-left text-sm font-medium"
			style={{
				color: selected ? "var(--preview-foreground)" : "var(--preview-muted-foreground)",
				background: selected ? "var(--preview-overlay)" : "transparent",
			}}
		>
			{selected ? <FolderOpen size={16} className="shrink-0 text-[var(--preview-passive)]" /> : <Folder size={16} className="shrink-0 text-[var(--preview-passive)]" />}
			<span className="min-w-0 flex-1 truncate">{children}</span>
			<span className="opacity-0 transition-opacity group-hover:opacity-100" style={{ color: "var(--preview-passive)" }}>
				<Network size={14} />
			</span>
		</button>
	);
}

function SessionRow({
	worker,
	sessionStatus,
	active,
	onClick,
}: {
	worker: Worker;
	sessionStatus: SessionStatusState;
	active: boolean;
	onClick: () => void;
}) {
	return (
		<button
			type="button"
			onClick={onClick}
			data-cursor-target={`session-${worker.id}`}
			className="flex h-8 w-full items-center gap-1.5 rounded-lg px-2.5 text-left text-sm"
			style={{
				color: active ? "var(--preview-foreground)" : "var(--preview-muted-foreground)",
				background: active ? "var(--preview-overlay)" : "transparent",
			}}
		>
			<span
				className={`size-2 shrink-0 rounded-full ${sessionStatus.breathe ? "animate-pulse" : ""}`}
				style={{ background: sessionStatus.tone }}
			/>
			<span className="min-w-0 flex-1 truncate">{worker.task}</span>
		</button>
	);
}

function PreviewTopbar({
	project,
	active,
	statuses,
}: {
	project: Project;
	active: SessionId;
	statuses: Record<WorkerId, SessionStatusState>;
}) {
	const activeWorker = active === "orc" ? null : workerFor(project.id, active);
	const meta =
		active === "orc"
			? { ...sessionMeta.orc, path: project.orchestratorPath, tabLabel: project.name }
			: { ...sessionMeta[active], path: activeWorker!.path, tabLabel: activeWorker!.task };
	const dotColor = active === "orc" ? status.working : statuses[active].tone;
	const shouldPulse = active === "orc" || statuses[active].breathe;
	return (
		<div className="flex h-9 shrink-0 items-stretch border-b border-[var(--preview-border)] bg-[var(--preview-background)]">
			<div className="flex min-w-0 flex-1 items-center">
				<span className="relative inline-flex min-w-0 shrink-0 self-stretch items-center gap-1.5 border-r border-[var(--preview-border)] bg-[var(--preview-overlay)] px-2.5 after:absolute after:inset-x-0 after:-bottom-px after:h-px after:bg-[#fafafa]">
					<img src={meta.icon} alt="" aria-hidden="true" className="size-[13px] shrink-0 object-contain" draggable={false} loading="lazy" decoding="async" />
					<span className="inline-flex min-w-0 items-center gap-1.5 text-[10.5px] font-medium leading-none text-[var(--preview-foreground)]">
						<span className="truncate">{meta.tabLabel}</span>
						<span
							className={`size-[5px] shrink-0 rounded-full ${shouldPulse ? "animate-pulse" : ""}`}
							style={{ background: dotColor }}
						/>
					</span>
				</span>
				<span className="ml-auto mr-2 grid size-[21px] shrink-0 place-items-center rounded-[5px] border border-[var(--preview-border)] text-[var(--preview-muted-foreground)]">
					<Plus aria-hidden="true" className="size-3" />
				</span>
			</div>
			<div className="flex shrink-0 items-center justify-end gap-1 px-1.5">
				<span className="grid size-[22px] place-items-center rounded-[7px]" style={{ color: `color-mix(in srgb, ${status.exited} 80%, transparent)` }}>
					<Trash2 aria-hidden="true" className="size-[13px]" />
				</span>
				<span className="grid size-[22px] place-items-center rounded-[7px] bg-[var(--preview-primary)] text-[var(--preview-primary-foreground)]">
					<Network aria-hidden="true" className="size-[13px]" />
				</span>
				<span className="grid size-[22px] place-items-center rounded-[7px] text-[var(--preview-muted-foreground)]">
					<Bell aria-hidden="true" className="size-[15px]" />
				</span>
			</div>
		</div>
	);
}

function TerminalPane({
	project,
	active,
	lines,
	typingText,
	streamingText,
}: {
	project: Project;
	active: SessionId;
	lines: DisplayLine[];
	typingText: { project: ProjectId; session: SessionId; marker?: string; chars: string; highlight?: boolean } | null;
	streamingText: string | null;
}) {
	const meta =
		active === "orc"
			? { ...sessionMeta.orc, path: project.orchestratorPath, tabLabel: project.name }
			: { ...sessionMeta[active], path: workerFor(project.id, active).path, tabLabel: workerFor(project.id, active).task };
	const scrollRef = useRef<HTMLDivElement>(null);

	useEffect(() => {
		scrollRef.current?.scrollTo({ top: scrollRef.current.scrollHeight, behavior: "smooth" });
	}, [lines.length, typingText, streamingText]);

	return (
		<main
			ref={scrollRef}
			className="flex min-h-0 min-w-0 flex-1 flex-col justify-end overflow-hidden bg-[var(--preview-terminal)] px-3 py-2.5 font-mono"
			style={{ fontFamily: "var(--font-geist-mono), ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, Liberation Mono, monospace", fontVariantEmoji: "text" }}
		>
			<div className="mb-2 flex items-start gap-2">
				<img src={meta.icon} alt="" aria-hidden="true" className="mt-0.5 size-[22px] shrink-0" draggable={false} loading="lazy" decoding="async" />
				<div className="min-w-0 text-[10px] leading-[1.5]">
					<div>
						<span className="font-bold text-[var(--preview-terminal-fg)]">{meta.title}</span>
						<span className="text-[var(--preview-terminal-dim)]"> {meta.version}</span>
					</div>
					<div className="text-[var(--preview-terminal-dim)]">{meta.subtitle}</div>
					<div className="text-[var(--preview-terminal-dim)]">{meta.path}</div>
				</div>
			</div>

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
				{typingText ? (
					<div className="flex min-w-0 items-start gap-1.5" style={{ color: typingText.highlight ? lineToneColor.working : lineToneColor.fg }}>
							<span className="w-[7px] shrink-0">{normalizeMarker(typingText.marker) ?? ""}</span>
						<span className="min-w-0 whitespace-pre-wrap break-words">
							{typingText.chars}
							<span className="inline-block h-[9px] w-[5px] translate-y-[1px] ao-blink bg-[var(--preview-terminal-fg)]" />
						</span>
					</div>
				) : null}
				{streamingText !== null ? (
					<div className="flex min-w-0 items-start gap-1.5" style={{ color: lineToneColor.fg }}>
						<span className="w-[7px] shrink-0" />
						<span className="min-w-0 whitespace-pre-wrap break-words">
							{streamingText}
							<span className="inline-block h-[9px] w-[5px] translate-y-[1px] ao-blink bg-[var(--preview-terminal-fg)]" />
						</span>
					</div>
				) : null}
				{!typingText && streamingText === null ? (
					<div className="flex min-w-0 items-center gap-1.5" style={{ color: lineToneColor.fg }}>
						<span className="w-[7px] shrink-0">{PROMPT_MARKER}</span>
						<span className="inline-block h-[10px] w-[5.5px] ao-blink bg-[var(--preview-terminal-fg)]" />
					</div>
				) : null}
			</div>
		</main>
	);
}

function DemoCursor({
	rootRef,
	target,
	pressed,
}: {
	rootRef: RefObject<HTMLDivElement | null>;
	target: string;
	pressed: boolean;
}) {
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
				if (attempts++ < 10) timer = window.setTimeout(measure, 50);
				return;
			}
			const rootRect = root.getBoundingClientRect();
			const rect = el.getBoundingClientRect();
			if (!rootRect.width || !rootRect.height) return;
			setPos({
				x: ((rect.left + rect.width / 2 - rootRect.left) / rootRect.width) * 100,
				y: ((rect.top + rect.height / 2 - rootRect.top) / rootRect.height) * 100,
			});
		};
		const frame = requestAnimationFrame(measure);
		timer = window.setTimeout(measure, 100);
		return () => {
			cancelAnimationFrame(frame);
			clearTimeout(timer);
		};
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
