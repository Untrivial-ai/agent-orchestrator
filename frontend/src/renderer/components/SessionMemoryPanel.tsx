import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode, type RefObject } from "react";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { Check, ChevronDown, ChevronRight, ChevronUp, Copy, X } from "lucide-react";
import {
	chipTone,
	largestSession,
	processCategories,
	processKind,
	processList,
	stableResourceOrder,
	type ChipTone,
	type PressureState,
	type ProcessRow as ProcessListRow,
	type ResourceSessionFacts,
} from "@aoagents/product-ui";
import { cn } from "@/lib/utils";
import { aoBridge } from "../lib/bridge";
import { formatEstimatedCost } from "../lib/format-cost";
import { formatTimeTerse } from "../lib/format-time";
import { formatTokenCount } from "../lib/format-token-count";
import { useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import {
	formatCPU,
	formatMemory,
	useAppMemory,
	useFastMemorySampling,
	usePressureState,
	sampleHistoryLength,
	cpuSampleOf,
	useCPUHistory,
	useSessionMemory,
	type CPUSample,
	type AppMemoryReading,
	type SessionMemoryReading,
	type SessionStepReading,
	type SystemMemoryReading,
} from "../hooks/useSessionMemory";
import { useSessionUsageSummaries, type SessionUsageSummary } from "../hooks/useSessionUsageSummaries";
import { isOrchestratorSession, type WorkspaceSession, type WorkspaceSummary } from "../types/workspace";
import { Button } from "./ui/button";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogTitle,
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

// Stable identity for the loading state: `data ?? []` would hand useMemo a
// fresh array every render, invalidating it (and the onRows effect below)
// in a loop until the workspace query resolves.
const EMPTY_WORKSPACES: readonly WorkspaceSummary[] = [];

/** True once the daemon has produced an app-wide reading; gates the archive bar. */
export function useHasAppMemory(): boolean {
	const memory = useAppMemory();
	return !memory.isError && (memory.data?.app?.rssBytes ?? 0) > 0;
}

/** What the monitor knows about a session, from the board plus its reading. */
export function toSessionFacts(session: WorkspaceSession, reading: SessionMemoryReading | undefined, now: number): ResourceSessionFacts {
	const lastActivity = session.activity?.lastActivityAt ? Date.parse(session.activity.lastActivityAt) : Number.NaN;
	return {
		id: session.id,
		title: session.title,
		rssBytes: reading?.rssBytes ?? 0,
		working: session.activity?.state === "active",
		idleSeconds: Number.isNaN(lastActivity) ? undefined : Math.max(0, (now - lastActivity) / 1000),
	};
}

const stateDot: Record<PressureState, string> = {
	fine: "bg-success",
	tight_soon: "bg-warning",
	tight: "animate-status-pulse bg-destructive",
};
const stateText: Record<PressureState, string> = {
	fine: "",
	tight_soon: "text-warning",
	tight: "text-destructive",
};

/** The pressure state and what each live session holds, for the row chips. */
function usePressureFacts(projectId?: string) {
	const workspaces = useWorkspaceQuery().data ?? EMPTY_WORKSPACES;
	const readings = useSessionMemory().data;
	const now = Date.now();
	const facts = workspaces
		.filter((workspace) => !projectId || workspace.id === projectId)
		.flatMap((workspace) => workspace.sessions)
		.filter((session) => session.isTerminated !== true && !isOrchestratorSession(session) && readings?.has(session.id))
		.map((session) => toSessionFacts(session, readings?.get(session.id), now));
	const state = usePressureState();
	return { state, facts };
}

/**
 * Archive-bar light: a dot, the state word, AO's size and the fix. Colour
 * means something needs doing; grey means nothing does. The tooltip holds
 * the machine figures, the window behind it everything else.
 */
export function AppMemoryIndicator() {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const memory = useAppMemory();
	const { state } = usePressureFacts();
	const app = memory.data?.app;
	const system = memory.data?.system;
	if (memory.isError || !app || app.rssBytes === 0) {
		return null;
	}
	const word = state ? t(`shell.memoryState.${state}`) : t("shell.memoryState.unknown");
	const detail = system
		? t("shell.memoryBarDetail", {
			free: formatMemory(system.availableBytes),
			total: formatMemory(system.totalBytes),
			used: formatMemory(app.rssBytes),
			pressure: system.pressureRaw.toFixed(1),
		})
		: t("shell.memoryAppUsageNoTotal", { used: formatMemory(app.rssBytes) });
	return (
		<>
			<Tooltip>
				<TooltipTrigger asChild>
					<button
						aria-label={`${word} · ${detail}`}
						className="inline-flex items-center gap-2 font-mono text-2xs tabular-nums text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:underline"
						data-memory-state={state ?? "unknown"}
						data-testid="app-memory-indicator"
						onClick={() => setOpen(true)}
						type="button"
					>
						<span aria-hidden="true" className={cn("size-1.5 shrink-0 rounded-full", state ? stateDot[state] : "bg-passive")} />
						<span className={state ? stateText[state] : undefined}>{formatMemory(app.rssBytes)}</span>
					</button>
				</TooltipTrigger>
				<TooltipContent side="top">{detail}</TooltipContent>
			</Tooltip>
			{open ? <SessionMemoryPanel onOpenChange={setOpen} open /> : null}
		</>
	);
}

/** One bar: AO against free, and nothing else. The bar is scaled to the two
 * of them, so there is no gap standing in for other apps. */
function MachineBar({ appBytes, system }: { appBytes: number; system: SystemMemoryReading }) {
	const { t } = useTranslation();
	const total = appBytes + system.availableBytes || 1;
	const pct = (bytes: number) => `${Math.min(100, (bytes / total) * 100)}%`;
	const legend = [
		{ key: "ao", label: t("shell.memoryLegendAO"), bytes: appBytes, className: "bg-accent-strong" },
		{ key: "free", label: t("shell.memoryLegendAvailable"), bytes: system.availableBytes, className: "bg-success/70" },
	];
	return (
		<div className="settings-row-bar h-auto flex-col items-stretch gap-2 py-3" data-testid="session-memory-stacked">
			<div className="flex h-2 w-full overflow-hidden rounded-sm bg-foreground/[0.06]">
				<div className="h-full bg-accent-strong transition-[width] duration-500" style={{ width: pct(appBytes) }} />
				<div className="h-full bg-success/70 transition-[width] duration-500" style={{ width: pct(system.availableBytes) }} />
			</div>
			<div className="flex flex-wrap gap-x-4 gap-y-1 font-mono text-xs tabular-nums text-settings-muted">
				{legend.map((part) => (
					<span className="inline-flex items-center gap-1.5" key={part.key}>
						<span aria-hidden="true" className={cn("size-1.5 rounded-full", part.className)} />
						{part.label} <span className="text-settings-label">{formatMemory(part.bytes)}</span>
					</span>
				))}
			</div>
		</div>
	);
}

/** The machine's own memory: AO against what is free. */
export function MachineSection({ action }: { action?: ReactNode }) {
	const { t } = useTranslation();
	const appMemory = useAppMemory().data;
	const app = appMemory?.app;
	const system = appMemory?.system;
	return (
		<section className="flex w-full flex-col items-stretch gap-(--size-settings-section-inner-gap)">
			<div className="flex items-center justify-between gap-3">
				<h2 className="text-xs font-medium leading-4 text-settings-muted">{t("shell.memorySectionMachine")}</h2>
				{action}
			</div>
			<div className="settings-grouped-rows flex w-full flex-col">
				{system && app ? <MachineBar appBytes={app.rssBytes} system={system} /> : null}
			</div>
		</section>
	);
}

/**
 * Each column's width, padding and alignment, in one place: the header and
 * every row take their classes from here, so the columns line up.
 */
const columnWidth = { type: "w-24", pid: "w-28", memory: "w-28", cpu: "w-16" } as const;
const cell = {
	name: "pl-4 pr-3 text-left",
	process: "pl-11 pr-3 text-left",
	type: "px-3 text-left",
	pid: "px-3 text-right",
	memory: "px-3 text-right",
	cpu: "pl-3 pr-4 text-right",
} as const;

/** Every full-width row (group label, spacer, step heading) spans all five. */
const columnCount = 5;

/**
 * Sessions by CPU alone: memory plays no part. Sessions with exactly the same
 * CPU (most often every one idle at 0) fall back to their names, A to Z, in
 * either direction, so a tie never reads as some other order turned around.
 */
function cpuOrder<T extends { id: string; session: WorkspaceSession; reading: SessionMemoryReading }>(rows: T[], ascending: boolean): T[] {
	const direction = ascending ? 1 : -1;
	return [...rows].sort(
		(a, b) =>
			direction * (a.reading.cpuPercent - b.reading.cpuPercent) ||
			a.session.title.localeCompare(b.session.title) ||
			a.id.localeCompare(b.id),
	);
}

/** A column header that orders the sessions by its own figure; a second click flips the direction. */
function SortHeader({ active, ascending, className, label, onSort }: { active: boolean; ascending: boolean; className: string; label: string; onSort: () => void }) {
	// Always visible, so the header reads as sortable before any click: faint
	// on an idle column, full strength showing the direction on the active one.
	const Arrow = active && ascending ? ChevronUp : ChevronDown;
	return (
		<th aria-sort={active ? (ascending ? "ascending" : "descending") : undefined} className={cn("bg-popover pb-2 pt-3 font-medium", className)} scope="col">
			<button
				className={cn(
					"inline-flex items-center gap-1 rounded-sm transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring",
					active && "text-settings-label",
				)}
				data-testid="session-memory-sort"
				onClick={onSort}
				type="button"
			>
				{label}
				<Arrow aria-hidden="true" className={cn("size-icon-2xs", active ? "opacity-100" : "opacity-50")} />
			</button>
		</th>
	);
}

/** Every live session and AO's own processes, largest first. */
export function SessionsTable({ onRows, projectId }: { onRows?: (rows: ReportRow[]) => void; projectId?: string }) {
	const { t } = useTranslation();
	const workspaces = useWorkspaceQuery().data ?? EMPTY_WORKSPACES;
	const readings = useSessionMemory(projectId).data;
	const app = useAppMemory().data?.app;
	// Cost belongs in a shared report even though the window never shows it.
	const usage = useSessionUsageSummaries(projectId).data;
	const { state, facts } = usePressureFacts(projectId);
	// Orchestrators are listed too: they hold memory like any session, and
	// leaving them out made the rows add up to less than AO's total.
	const sessions = useMemo(
		() =>
			workspaces
				.filter((workspace) => !projectId || workspace.id === projectId)
				.flatMap((workspace) => workspace.sessions)
				.filter((session) => session.isTerminated !== true),
		[workspaces, projectId],
	);
	// Rows with a reading are the live ones; a session without a process tree
	// is not listed, never shown as 0 MB.
	const orderRef = useRef<string[]>([]);
	// Sessions only: AO's own row stays pinned below them whatever the order.
	const [sort, setSort] = useState<{ by: "memory" | "cpu"; ascending: boolean }>({ by: "memory", ascending: false });
	// Clicking the active column flips its direction; another column starts largest first.
	const sortBy = (by: "memory" | "cpu") => setSort((current) => ({ by, ascending: current.by === by ? !current.ascending : false }));
	const live = useMemo(() => {
		const rows = sessions
			.map((session) => ({ id: session.id, session, reading: readings?.get(session.id) }))
			.filter((row): row is { id: string; session: WorkspaceSession; reading: SessionMemoryReading } => row.reading !== undefined)
			.map((row) => ({ ...row, rssBytes: row.reading.rssBytes }));
		if (sort.by === "cpu") return cpuOrder(rows, sort.ascending);
		const largestFirst = stableResourceOrder(orderRef.current, rows);
		orderRef.current = largestFirst.map((row) => row.id);
		return sort.ascending ? [...largestFirst].reverse() : largestFirst;
	}, [sessions, readings, sort]);
	const largest = largestSession(facts);
	const maxBytes = Math.max(app?.own?.rssBytes ?? 0, ...live.map((row) => row.rssBytes), 1);
	// The copy button sits above the table, so the rows it would copy travel
	// up. Same order as the screen, so the text matches what was seen.
	useEffect(() => {
		onRows?.(live.map(({ session, reading }) => ({ session, reading, usage: usage?.get(session.id) })));
	}, [live, usage, onRows]);
	// Any number of rows open at once: comparing two trees is the point.
	const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set());
	const toggleExpanded = (id: string) =>
		setExpanded((current) => {
			const next = new Set(current);
			if (!next.delete(id)) next.add(id);
			return next;
		});
	if (live.length === 0 && !app?.own) {
		return <p className="py-6 text-center text-xs text-settings-muted">{t("shell.memoryEmpty")}</p>;
	}
	return (
		<table className="w-full table-fixed border-collapse text-xs" data-testid="session-memory-table">
			<colgroup>
				<col />
				<col className={columnWidth.type} />
				<col className={columnWidth.pid} />
				<col className={columnWidth.memory} />
				<col className={columnWidth.cpu} />
			</colgroup>
			{/* The columns stay readable however far the list scrolls. */}
			<thead className="sticky top-8 z-10 bg-popover">
				<tr className="border-b border-(--color-border-settings-dialog-header) text-xs text-settings-muted">
					<th className={cn("bg-popover pb-2 pt-3 font-medium", cell.name)} scope="col">{t("shell.memoryColumnName")}</th>
					<th className={cn("bg-popover pb-2 pt-3 font-medium", cell.type)} scope="col">{t("shell.memoryColumnType")}</th>
					<th className={cn("bg-popover pb-2 pt-3 font-medium", cell.pid)} scope="col">{t("shell.memoryColumnPid")}</th>
					<SortHeader active={sort.by === "memory"} ascending={sort.ascending} className={cell.memory} label={t("shell.memoryColumnRss")} onSort={() => sortBy("memory")} />
					<SortHeader active={sort.by === "cpu"} ascending={sort.ascending} className={cell.cpu} label={t("shell.memoryColumnCpu")} onSort={() => sortBy("cpu")} />
				</tr>
			</thead>
			<tbody className="[&_tr:not(.memory-group)+tr.memory-row]:border-t [&_tr.memory-row]:border-(--color-border-settings-dialog-header)">
				{live.length > 0 ? <GroupRow label={t("shell.memoryGroupSessions")} /> : null}
				{live.map((row) => (
					<SessionRow
						chip={chipTone(state ?? "fine", facts.find((f) => f.id === row.id) ?? toSessionFacts(row.session, row.reading, Date.now()), largest)}
						isExpanded={expanded.has(row.id)}
						key={row.id}
						maxBytes={maxBytes}
						onToggle={() => toggleExpanded(row.id)}
						reading={row.reading}
						session={row.session}
					/>
				))}
				{app?.own ? (
					<>
						<GroupRow label={t("shell.memoryGroupApp")} />
						<OwnRow
							isExpanded={expanded.has("ao")}
							maxBytes={maxBytes}
							onToggle={() => toggleExpanded("ao")}
							reading={app.own}
						/>
					</>
				) : null}
			</tbody>
		</table>
	);
}

/** The CPU graph and its per-core bars. */
export function CpuSection() {
	const appMemory = useAppMemory().data;
	const system = appMemory?.system;
	const cpuHistory = useCPUHistory();
	// An unmeasured reading shows the last real point instead of a false zero.
	const current = cpuSampleOf(system, appMemory?.app) ?? cpuHistory.at(-1);
	if (!system || !current) return null;
	return <CpuGraph current={current} history={cpuHistory} system={system} />;
}

/**
 * The window and the settings page are the same thing: machine, CPU, then the
 * sessions, in one scroller. Nothing is pinned to the top or the bottom — on a
 * 680px dialog that cost ~290px of permanent chrome and left four rows of
 * list. The numbers stay reachable through the summary line, which only
 * appears once the graphs have scrolled away.
 */
export function DiagnosticsBody({ projectId, scroller }: { projectId?: string; scroller?: RefObject<HTMLElement | null> }) {
	const { t } = useTranslation();
	const appMemory = useAppMemory().data;
	const [rows, setRows] = useState<ReportRow[]>([]);
	const graphs = useRef<HTMLDivElement>(null);
	const scrolledPast = useScrolledPast(graphs, scroller);
	return (
		<>
			<div className="flex flex-col gap-(--size-settings-section-gap,1.5rem)" ref={graphs}>
				<MachineSection
					action={
						<CopyControl
							copiedLabel={t("shell.memoryReportCopied")}
							label={t("shell.memoryCopyReport")}
							testId="session-memory-copy"
							value={() => diagnosticsReport({ app: appMemory?.app, system: appMemory?.system }, rows, t)}
						>
							<span>{t("shell.memoryCopyReport")}</span>
						</CopyControl>
					}
				/>
				<CpuSection />
			</div>
			{/* Zero height, so nothing shifts when the bar appears: the bar itself
			    floats over the rows it is pinned above. */}
			<div className="sticky top-0 z-20 h-0 overflow-visible">
				<div
					aria-hidden={!scrolledPast}
					className={cn(
						"absolute inset-x-0 top-0 flex h-8 items-center gap-4 border-b border-(--color-border-settings-dialog-header) bg-popover font-mono text-xs tabular-nums text-settings-muted transition-opacity",
						scrolledPast ? "opacity-100" : "pointer-events-none opacity-0",
					)}
					data-testid="session-memory-pinned"
				>
					{appMemory?.app ? (
						<span>
							{t("shell.memoryLegendAO")} <span className="text-settings-label">{formatMemory(appMemory.app.rssBytes)}</span>
							{appMemory.system ? ` · ${formatMemory(appMemory.system.availableBytes)} ${t("shell.memoryLegendAvailable").toLowerCase()}` : null}
						</span>
					) : null}
					{appMemory?.system ? (
						<span>
							{t("shell.memoryColumnCpu")} <span className="text-settings-label">{formatCPU(appMemory.system.cpuPercent)}</span>
						</span>
					) : null}
				</div>
			</div>
			<SessionsTable onRows={setRows} projectId={projectId} />
		</>
	);
}

/**
 * True once `target` has scrolled out of the top of its scroller. An observer
 * rather than a scroll listener: no work at all while the target is on screen.
 */
function useScrolledPast(target: RefObject<HTMLElement | null>, scroller?: RefObject<HTMLElement | null>): boolean {
	const [past, setPast] = useState(false);
	useEffect(() => {
		const element = target.current;
		if (!element || typeof IntersectionObserver !== "function") return;
		const observer = new IntersectionObserver(([entry]) => setPast(!entry.isIntersecting), {
			root: scroller?.current ?? null,
			threshold: 0,
		});
		observer.observe(element);
		return () => observer.disconnect();
	}, [target, scroller]);
	return past;
}

/** Settings page: the settings body is the scroller, so this only supplies content. */
export function MemoryDiagnostics() {
	useFastMemorySampling();
	return (
		<div className="settings-dialog-body flex w-full flex-col gap-(--size-settings-section-gap,1.5rem)" data-testid="memory-diagnostics">
			<DiagnosticsBody />
		</div>
	);
}

export function SessionMemoryPanel({
	onOpenChange,
	open,
	projectId,
}: {
	onOpenChange: (open: boolean) => void;
	open: boolean;
	projectId?: string;
}) {
	const { t } = useTranslation();
	useFastMemorySampling();
	const scroller = useRef<HTMLDivElement>(null);
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className={cn(settingsDialogContentClass, "w-[min(52rem,calc(100vw-var(--space-8)))]")} showCloseButton={false}>
				<div className={cn(settingsDialogHeaderClass, "flex h-auto flex-row items-center justify-between border-b-0 pb-3")}>
					<div className="min-w-0 flex-1">
						<DialogTitle className="text-lg font-semibold leading-6 text-settings-label">{t("shell.memoryPanelTitle")}</DialogTitle>
						<DialogDescription className="sr-only">{t("shell.memoryPanelDescription")}</DialogDescription>
					</div>
					<DialogClose asChild>
						<Button aria-label={t("common.close")} size="icon" variant="ghost">
							<X className="size-icon-md" aria-hidden="true" />
						</Button>
					</DialogClose>
				</div>
				<div
					className={cn(settingsDialogBodyClass, "settings-dialog-body min-h-0 flex-1 gap-(--size-settings-section-gap,1.5rem) overscroll-contain px-(--size-modal-padding) pt-0")}
					ref={scroller}
				>
					<DiagnosticsBody projectId={projectId} scroller={scroller} />
				</div>
			</DialogContent>
		</Dialog>
	);
}

function GroupRow({ label }: { label: string }) {
	return (
		<tr className="memory-group">
			<td className="pb-2 pt-6 text-xs font-medium leading-4 text-settings-muted first:pt-0" colSpan={columnCount}>{label}</td>
		</tr>
	);
}

/** A session is not a process, so its PID cell says what is under it and
 * that the row opens: "3 processes ›". */
function ProcessCountCell({ count, isExpanded }: { count: number; isExpanded: boolean }) {
	const { t } = useTranslation();
	return (
		<td className={cn("whitespace-nowrap py-2 align-middle text-xs text-settings-muted", cell.pid)} data-testid="session-memory-process-count">
			{count > 0 && !isExpanded ? t("shell.memoryProcessCount", { count }) : null}
		</td>
	);
}

/** Memory cell: the number over a bar scaled to the biggest row, so "which one is the pig" reads at a glance. */
function MemoryCell({ bytes, maxBytes, tone }: { bytes: number; maxBytes: number; tone: ChipTone }) {
	return (
		<td className={cn("whitespace-nowrap py-2 align-middle font-mono text-xs tabular-nums", cell.memory)}>
			<span className={cn("font-medium", tone === "critical" ? "text-destructive" : tone === "warning" ? "text-warning" : "text-settings-label")}>
				{formatMemory(bytes)}
			</span>
			<div className="ml-auto mt-1 h-0.5 w-16 rounded-sm bg-foreground/[0.06]" data-testid="session-memory-share">
				<div
					className={cn("h-full rounded-sm transition-[width] duration-500", tone === "critical" ? "bg-destructive" : tone === "warning" ? "bg-warning" : "bg-accent-strong")}
					style={{ width: `${Math.min(100, (bytes / maxBytes) * 100)}%` }}
				/>
			</div>
		</td>
	);
}

/** Enter/Space activates a row the same way a click does, so an expandable
 * row is reachable without a mouse. */
function toggleOnKeyDown(onToggle: () => void) {
	return (event: KeyboardEvent<HTMLTableRowElement>) => {
		if (event.key === "Enter" || event.key === " ") {
			event.preventDefault();
			onToggle();
		}
	};
}

function SessionRow({
	chip,
	isExpanded,
	maxBytes,
	onToggle,
	reading,
	session,
}: {
	chip: ChipTone;
	isExpanded: boolean;
	maxBytes: number;
	onToggle: () => void;
	reading: SessionMemoryReading;
	session: WorkspaceSession;
}) {
	const { t } = useTranslation();
	const working = session.activity?.state === "active";
	const recent = reading.activity?.recent ?? [];
	const canExpand = reading.processes.length > 0 || recent.length > 0;
	return (
		<>
			<tr
				aria-expanded={canExpand ? isExpanded : undefined}
				className={cn("group/row memory-row", canExpand && "cursor-pointer hover:bg-interactive-hover")}
				data-chip-tone={chip}
				data-testid="session-memory-row"
				onClick={canExpand ? onToggle : undefined}
				onKeyDown={canExpand ? toggleOnKeyDown(onToggle) : undefined}
				tabIndex={canExpand ? 0 : undefined}
			>
				<td className={cn("py-2 align-middle", cell.name)}>
					<div className="flex items-center gap-1.5">
						<ChevronRight
							aria-hidden="true"
							className={cn("size-icon-2xs shrink-0 text-passive transition-transform", canExpand ? "opacity-100" : "opacity-0", isExpanded && "rotate-90")}
						/>
						<div className="min-w-0">
							<div className="truncate text-sm font-medium text-settings-label" title={session.title}>{session.title}</div>
							<StatusLine current={reading.activity?.current} session={session} working={working} />
						</div>
					</div>
				</td>
				{/* A session's type is its role; its process lines carry their own. */}
				<td className={cn("whitespace-nowrap py-2 align-middle font-mono text-xs text-passive", cell.type)} data-testid="session-memory-type">
					{isOrchestratorSession(session) ? t("settings.models.orchestratorRole") : t("settings.models.workerRole")}
				</td>
				<ProcessCountCell count={reading.processes.length} isExpanded={isExpanded} />
				<MemoryCell bytes={reading.rssBytes} maxBytes={maxBytes} tone={chip} />
				<td className={cn("whitespace-nowrap py-2 align-middle font-mono text-xs tabular-nums text-settings-muted", cell.cpu)}>
					{formatCPU(reading.cpuPercent)}
				</td>
			</tr>
			{isExpanded ? <ProcessRows processes={reading.processes} /> : null}
			{isExpanded && recent.length > 0 ? <RecentSteps steps={recent} /> : null}
			{isExpanded ? <SpacerRow /> : null}
		</>
	);
}

/** Seconds under a minute, then minutes, then hours: "38s", "4m 10s", "1h 2m". */
export function formatDuration(ms: number): string {
	const total = Math.max(0, Math.round(ms / 1000));
	if (total < 60) return `${total}s`;
	const minutes = Math.floor(total / 60);
	if (minutes < 60) return `${minutes}m ${total % 60}s`;
	return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

/**
 * The one line under a title. Working with a known step: the step and how
 * long it has run. Working otherwise: just "working". Idle: how long for.
 * The row re-renders on every sample, so the duration ticks with it.
 */
function statusText(current: SessionStepReading | undefined, session: WorkspaceSession, working: boolean, t: TFunction): string {
	if (isOrchestratorSession(session) && !current) return t("shell.memoryRowOrchestrator");
	if (current) return `${current.tool} · ${formatDuration(Date.now() - Date.parse(current.startedAt))}`;
	if (working) return t("shell.memoryRowWorking");
	const since = formatTimeTerse(session.activity?.lastActivityAt);
	return since === "now" ? t("shell.memoryRowIdle") : t("shell.memoryRowIdleFor", { time: since });
}

function StatusLine({ current, session, working }: { current?: SessionStepReading; session: WorkspaceSession; working: boolean }) {
	const { t } = useTranslation();
	// Which agent runs the session, with its name rather than in the Type column.
	return (
		<div className="truncate text-xs text-settings-muted">
			{session.provider ? <span data-testid="session-memory-agent">{session.provider} · </span> : null}
			<span data-testid="session-memory-status">{statusText(current, session, working, t)}</span>
		</div>
	);
}

/**
 * One session as plain text: who it is, what it is doing,
 * what it costs the machine, and what it has been running. Process IDs are
 * deliberately left out — they mean nothing to whoever reads the report —
 * and so are tool arguments, which can carry paths and prompts.
 */
export function sessionReport(
	session: WorkspaceSession,
	reading: SessionMemoryReading,
	usage: SessionUsageSummary | undefined,
	t: TFunction,
): string {
	const working = session.activity?.state === "active";
	const identity = [session.workspaceName, session.kind ?? "worker", session.provider, session.mode].filter(Boolean).join(" · ");
	const lines = [session.title, identity];
	// The daemon's lane ("Needs review") and what the agent is doing right now
	// ("Bash · 38s") are different facts, but on an idle session they collapse
	// to the same word; print it once.
	const lane = session.displayStatus ?? session.status;
	const doing = statusText(reading.activity?.current, session, working, t);
	lines.push(`Status   ${doing.toLowerCase().startsWith(lane.toLowerCase()) ? doing : `${lane} · ${doing}`}`);
	if (session.branch) lines.push(`Branch   ${session.branch}`);
	for (const pr of session.prs ?? []) {
		lines.push(`PR       #${pr.number} · ${pr.state}${pr.ci ? ` · CI ${pr.ci}` : ""}${pr.review ? ` · ${pr.review}` : ""}`);
		lines.push(`         ${pr.url}`);
	}
	const cost = formatEstimatedCost(usage?.estimatedCost);
	const tokens = usage?.processedTokens != null ? formatTokenCount(usage.processedTokens) : undefined;
	if (cost || tokens) lines.push(`Usage    ${[cost, tokens].filter(Boolean).join(" · ")}`);
	lines.push(`Memory   ${formatMemory(reading.rssBytes)} · CPU ${formatCPU(reading.cpuPercent)} · ${reading.sampledAt}`);

	const tree = processTree(reading.processes);
	if (tree.length > 0) {
		const names = tree.map(({ process, depth }) => `${"  ".repeat(depth)}${processKind(process.command)}`);
		const width = Math.max(...names.map((name) => name.length));
		lines.push("", t("shell.memoryGroupProcesses"));
		const categories = processCategories(reading.processes);
		tree.forEach(({ process }, i) => {
			const category = categories.get(process.pid);
			const type = category ? `  ${t(`shell.memoryCategory.${category}`)}` : "";
			lines.push(`  ${names[i].padEnd(width)}  ${formatMemory(process.rssBytes).padStart(8)}  ${formatCPU(process.cpuPercent).padStart(4)}${type}`);
		});
	}
	const recent = reading.activity?.recent ?? [];
	if (recent.length > 0) {
		lines.push("", t("shell.memoryRecent"));
		for (const step of recent) {
			const started = new Date(step.startedAt);
			const duration = step.endedAt ? formatDuration(Date.parse(step.endedAt) - started.getTime()) : "";
			lines.push(
				`  ${started.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}  ${step.tool.padEnd(12)} ${duration}${step.failed ? ` ${t("shell.memoryStepFailed")}` : ""}`.trimEnd(),
			);
		}
	}
	return `${lines.join("\n")}\n`;
}

/** One row of the report: a session with its reading, in the order shown. */
export type ReportRow = { session: WorkspaceSession; reading: SessionMemoryReading; usage?: SessionUsageSummary };

/**
 * Everything the window shows, as plain text for a bug report: the machine,
 * then every session in the order they are listed. One button copies the lot
 * — a reader needs the neighbours to judge whether one session is the
 * problem or the machine is simply full.
 */
export function diagnosticsReport(
	machine: { app?: AppMemoryReading; system?: SystemMemoryReading },
	rows: ReportRow[],
	t: TFunction,
): string {
	const { app, system } = machine;
	const lines = [t("shell.memorySectionMachine")];
	if (app && system) {
		lines.push(`Memory   AO ${formatMemory(app.rssBytes)} · ${formatMemory(system.availableBytes)} free of ${formatMemory(system.totalBytes)}`);
	} else if (app) {
		lines.push(`Memory   AO ${formatMemory(app.rssBytes)}`);
	}
	if (system) {
		// A negative load average is Windows' "not applicable" sentinel, never a real reading.
		const load = system.load1 >= 0 ? ` · load ${system.load1.toFixed(2)}` : "";
		lines.push(`CPU      ${formatCPU(system.cpuPercent)} of ${system.cpuCount} cores · AO ${formatCPU(Math.min(100, (app?.cpuPercent ?? 0) / Math.max(1, system.cpuCount)))}${load}`);
		if (system.swapBytesPerSec > 0) lines.push(`Swapping ${formatMemory(system.swapBytesPerSec)}/s`);
	}
	lines.push(`Sessions ${rows.length}`);
	return [`${lines.join("\n")}\n`, ...rows.map((row) => sessionReport(row.session, row.reading, row.usage, t))].join("\n");
}

/** The last few tool calls under an expanded row, newest first. */
function RecentSteps({ steps }: { steps: SessionStepReading[] }) {
	const { t } = useTranslation();
	return (
		<>
			<tr>
				<td className="pb-1 pl-11 pt-2 text-xs font-medium text-settings-muted" colSpan={columnCount}>{t("shell.memoryRecent")}</td>
			</tr>
			{steps.map((step) => {
				const started = new Date(step.startedAt);
				const duration = step.endedAt ? Date.parse(step.endedAt) - started.getTime() : undefined;
				return (
					<tr className="text-xs" data-testid="session-memory-step-row" key={`${step.startedAt}-${step.tool}`}>
						<td className={cn("py-1 font-mono", cell.process)}>
							<div className="flex min-w-0 items-baseline gap-3">
								<span className="shrink-0 text-passive">{started.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</span>
								<span className={cn("min-w-0 truncate", step.failed ? "text-error" : "text-settings-label")}>{step.tool}</span>
								{step.failed ? <span className="shrink-0 text-error">{t("shell.memoryStepFailed")}</span> : null}
							</div>
						</td>
						<td />
						<td />
						<td />
						<td className={cn("whitespace-nowrap py-1 font-mono tabular-nums text-passive", cell.cpu)}>
							{duration !== undefined && duration >= 1000 ? formatDuration(duration) : "·"}
						</td>
					</tr>
				);
			})}
		</>
	);
}

/** AO's daemon and desktop shell: real cost, but not a session, so no action. */
function OwnRow({ isExpanded, maxBytes, onToggle, reading }: { isExpanded: boolean; maxBytes: number; onToggle: () => void; reading: SessionMemoryReading }) {
	const { t } = useTranslation();
	const canExpand = reading.processes.length > 0;
	return (
		<>
			<tr
				aria-expanded={canExpand ? isExpanded : undefined}
				className={cn("memory-row", canExpand && "cursor-pointer hover:bg-interactive-hover")}
				data-testid="session-memory-own-row"
				onClick={canExpand ? onToggle : undefined}
				onKeyDown={canExpand ? toggleOnKeyDown(onToggle) : undefined}
				tabIndex={canExpand ? 0 : undefined}
			>
				<td className={cn("py-2 align-middle", cell.name)}>
					<div className="flex items-center gap-1.5">
						<ChevronRight
							aria-hidden="true"
							className={cn("size-icon-2xs shrink-0 text-passive transition-transform", canExpand ? "opacity-100" : "opacity-0", isExpanded && "rotate-90")}
						/>
						<div className="truncate text-sm font-medium text-settings-label">{t("shell.memoryOwnRow")}</div>
					</div>
				</td>
				<td className={cn("whitespace-nowrap py-2 align-middle font-mono text-xs text-passive", cell.type)} data-testid="session-memory-type">
					{t("shell.memoryCategory.ao")}
				</td>
				<ProcessCountCell count={reading.processes.length} isExpanded={isExpanded} />
				<MemoryCell bytes={reading.rssBytes} maxBytes={maxBytes} tone="neutral" />
				<td className={cn("whitespace-nowrap py-2 align-middle font-mono text-xs tabular-nums text-settings-muted", cell.cpu)}>{formatCPU(reading.cpuPercent)}</td>
			</tr>
			{isExpanded ? <ProcessRows own processes={reading.processes} /> : null}
			{isExpanded ? <SpacerRow /> : null}
		</>
	);
}

/** Breathing room under an expanded block, so the next rule is not glued to the last child. */
function SpacerRow() {
	return (
		<tr aria-hidden="true">
			<td className="h-2 p-0" colSpan={columnCount} />
		</tr>
	);
}

// The copied report and older imports still name processes the same way.
export { processKind };

/**
 * Each process under its parent, siblings largest first, depth as indent. A
 * child whose parent is outside the row starts at the top. Shared by the
 * rendered tree and the copied report.
 */
export function processTree(processes: SessionMemoryReading["processes"]): { process: SessionMemoryReading["processes"][number]; depth: number; last: boolean }[] {
	const pids = new Set(processes.map((p) => p.pid));
	const children = new Map<number, SessionMemoryReading["processes"]>();
	for (const p of processes) {
		const parent = pids.has(p.ppid) && p.ppid !== p.pid ? p.ppid : -1;
		children.set(parent, [...(children.get(parent) ?? []), p]);
	}
	const rows: { process: SessionMemoryReading["processes"][number]; depth: number; last: boolean }[] = [];
	const walk = (parent: number, depth: number) => {
		const kids = [...(children.get(parent) ?? [])].sort((a, b) => b.rssBytes - a.rssBytes);
		kids.forEach((kid, index) => {
			rows.push({ process: kid, depth, last: index === kids.length - 1 });
			walk(kid.pid, depth + 1);
		});
	};
	walk(-1, 0);
	return rows;
}

/**
 * The process list on screen: one flat line per program, plumbing folded in,
 * the small tail summed into "N other processes". The rows add up to the
 * session; the copied report keeps every raw process instead.
 */
function ProcessRows({ processes, own = false }: { processes: SessionMemoryReading["processes"]; own?: boolean }) {
	const { t } = useTranslation();
	const { rows, other } = useMemo(() => processList(processes, { own }), [processes, own]);
	const [showAll, setShowAll] = useState(false);
	const toggle = () => setShowAll((current) => !current);
	return (
		<>
			{[...rows, ...(showAll && other ? other.rows : [])].map((row) => (
				<ProcessRow key={row.pid} row={row} />
			))}
			{other ? (
				<tr
					aria-expanded={showAll}
					className="cursor-pointer text-xs hover:bg-interactive-hover"
					data-testid="session-memory-process-other"
					onClick={toggle}
					onKeyDown={toggleOnKeyDown(toggle)}
					tabIndex={0}
				>
					<td className={cn("py-1 font-mono text-passive", cell.process)}>
						<span className="flex min-w-0 items-center gap-1" title={showAll ? undefined : other.commands.join("\n")}>
							<span className="truncate">{showAll ? t("shell.memoryProcessFewer") : t("shell.memoryProcessOther", { count: other.count })}</span>
							<ChevronRight aria-hidden="true" className={cn("size-icon-2xs shrink-0 transition-transform", showAll ? "-rotate-90" : "rotate-90")} />
						</span>
					</td>
					<td />
					<td />
					<td className={cn("whitespace-nowrap py-1 font-mono tabular-nums text-settings-muted", cell.memory)}>{showAll ? null : formatMemory(other.bytes)}</td>
					<td className={cn("whitespace-nowrap py-1 font-mono tabular-nums text-passive", cell.cpu)}>
						{showAll ? null : other.cpu >= 1 ? formatCPU(other.cpu) : "·"}
					</td>
				</tr>
			) : null}
		</>
	);
}

function ProcessRow({ row }: { row: ProcessListRow }) {
	const { t } = useTranslation();
	return (
		<tr className="text-xs" data-testid="session-memory-process-row">
			<td className={cn("py-1 font-mono text-settings-muted", cell.process)}>
				<span className="block truncate" title={row.commands.join("\n")}>{row.kind}</span>
			</td>
			<td className={cn("whitespace-nowrap py-1 font-mono text-passive", cell.type)} data-testid="session-memory-process-type">
				<span title={t(`shell.memoryCategoryHint.${row.category}`)}>{t(`shell.memoryCategory.${row.category}`)}</span>
			</td>
			<td className={cn("whitespace-nowrap py-1 align-middle font-mono tabular-nums", cell.pid)}>
				<CopyControl
					copiedLabel={t("shell.memoryPidCopied", { pid: row.pid })}
					label={t("shell.memoryCopyPid", { pid: row.pid })}
					testId="session-memory-pid"
					value={() => String(row.pid)}
				>
					<span>{row.pid}</span>
				</CopyControl>
			</td>
			<td className={cn("whitespace-nowrap py-1 font-mono tabular-nums text-settings-muted", cell.memory)}>{formatMemory(row.bytes)}</td>
			<td className={cn("whitespace-nowrap py-1 font-mono tabular-nums text-passive", cell.cpu)}>{row.cpu >= 1 ? formatCPU(row.cpu) : "·"}</td>
		</tr>
	);
}

/**
 * Copy something from a row: the PID, or a whole session report. A tick for
 * a moment says it worked; a failure leaves the icon alone rather than
 * claiming success.
 */
function CopyControl({
	children,
	className,
	copiedLabel,
	label,
	testId,
	value,
}: {
	children?: ReactNode;
	className?: string;
	copiedLabel: string;
	label: string;
	testId: string;
	value: () => string;
}) {
	const [copied, setCopied] = useState(false);
	const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
	useEffect(() => () => {
		if (timer.current !== null) clearTimeout(timer.current);
	}, []);
	const copy = async () => {
		try {
			await aoBridge.clipboard.writeText(value());
		} catch {
			return;
		}
		setCopied(true);
		if (timer.current !== null) clearTimeout(timer.current);
		timer.current = setTimeout(() => setCopied(false), 1_500);
	};
	return (
		<button
			aria-label={copied ? copiedLabel : label}
			className={cn(
				"group inline-flex shrink-0 items-center gap-1 rounded-sm text-settings-muted transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring",
				className,
			)}
			data-testid={testId}
			onClick={(event) => {
				event.stopPropagation();
				void copy();
			}}
			type="button"
		>
			{children}
			{copied ? (
				<Check aria-hidden="true" className="size-icon-2xs text-success" />
			) : (
				<Copy
					aria-hidden="true"
					className={cn(
						"size-icon-2xs transition-opacity",
						// Beside a PID the icon is a hint that fades in; on its own it
						// is the whole control and must stay visible.
						children ? "opacity-0 group-hover:opacity-100 group-focus-visible:opacity-100" : undefined,
					)}
				/>
			)}
		</button>
	);
}

/** Graph geometry: a wide, short strip like btop's, in SVG user units. */
const graphWidth = 600;
const graphHeight = 56;

/** An SVG path along the samples, newest on the right. */
export function linePath(values: number[]): string {
	if (values.length === 0) return "";
	const step = graphWidth / (sampleHistoryLength - 1);
	const x0 = graphWidth - step * (values.length - 1);
	const points = values.map((v, i) => `${(x0 + i * step).toFixed(1)},${(graphHeight - (Math.min(100, Math.max(0, v)) / 100) * graphHeight).toFixed(1)}`);
	return points.length > 1 ? `M${points[0]} L${points.slice(1).join(" L")}` : `M${points[0]}`;
}

/**
 * Two lines over the last two minutes: how busy the machine is, and AO's
 * share of it. Newest sample on the right. No per-core detail — the number
 * that matters is whether the machine has room left.
 */
function CpuGraph({ current, history, system }: { current: CPUSample; history: CPUSample[]; system: SystemMemoryReading }) {
	const { t } = useTranslation();
	const now = current;
	return (
		<section className="flex w-full flex-col items-stretch gap-(--size-settings-section-inner-gap)" data-testid="session-cpu-graph">
			<div className="flex items-center justify-between">
				<h2 className="text-xs font-medium leading-4 text-settings-muted">{t("shell.cpuBarTitle", { cores: system.cpuCount })}</h2>
				{/* A negative load average is Windows' "not applicable" sentinel: the platform has no such concept, and printing 0.00 would read as an idle machine rather than a missing number. */}
				{system.load1 >= 0 ? (
					<span className="font-mono text-xs tabular-nums text-settings-muted">{t("shell.cpuBarLoad", { load: system.load1.toFixed(2) })}</span>
				) : null}
			</div>
			<div className="settings-grouped-rows flex w-full flex-col">
				<div className="settings-row-bar h-auto flex-col items-stretch gap-2 py-3">
					<svg aria-hidden="true" className="h-14 w-full" preserveAspectRatio="none" viewBox={`0 0 ${graphWidth} ${graphHeight}`}>
						{[25, 50, 75].map((line) => (
							<line
								className="stroke-foreground/[0.06]"
								key={line}
								strokeWidth={1}
								x1={0}
								x2={graphWidth}
								y1={graphHeight - (line / 100) * graphHeight}
								y2={graphHeight - (line / 100) * graphHeight}
							/>
						))}
						<path className="fill-none stroke-warning/80" d={linePath(history.map((s) => s.host))} strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
						<path className="fill-none stroke-accent-strong" d={linePath(history.map((s) => s.ao))} strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
					</svg>
					{/* A line's colour means nothing without a key: name each one,
					    the same way the memory bar names its parts. */}
					<div className="flex flex-wrap gap-x-4 gap-y-1 font-mono text-xs tabular-nums text-settings-muted">
						{[
							{ key: "machine", className: "bg-warning/80", label: t("shell.cpuLegendMachine"), value: now.host },
							{ key: "ao", className: "bg-accent-strong", label: t("shell.memoryLegendAO"), value: now.ao },
						].map((part) => (
							<span className="inline-flex items-center gap-1.5" key={part.key}>
								<span aria-hidden="true" className={cn("size-1.5 rounded-full", part.className)} />
								{part.label} <span className="text-settings-label">{formatCPU(part.value)}</span>
							</span>
						))}
					</div>
				</div>
			</div>
		</section>
	);
}
