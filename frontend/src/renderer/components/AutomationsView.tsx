import { useEffect, useState, type FormEvent } from "react";
import { useNavigate } from "@tanstack/react-router";
import { ScrollArea } from "radix-ui";
import { useTranslation } from "react-i18next";
import { CalendarClock, ChevronRight, Clock3, History, Pencil, Plus, Trash2, TriangleAlert, X } from "lucide-react";
import { Button } from "./ui/button";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { TopbarButton, topbarProjectLabelClass } from "./TopbarButton";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogTitle,
} from "./ui/dialog";
import { Input } from "./ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Switch } from "./ui/switch";
import { ConfirmDialog } from "./ConfirmDialog";
import { RequiredAgentField } from "./CreateProjectAgentSheet";
import { useAgentReadinessQuery, type AgentReadinessSnapshot } from "../hooks/useAgentReadinessQuery";
import { useProjectDefaultWorker } from "../hooks/useProjectDefaultWorker";
import { useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import {
	buildRankedAgentOptions,
	DEFAULT_AGENT_PRIORITY_RANK,
	isReadyAgent,
} from "../lib/agent-select-options";
import {
	buildRRuleFromSchedule,
	clampHourDraft,
	clampMinuteDraft,
	clampMonthDayDraft,
	formatTimeDraft,
	nowLocalHHMM,
	parseHourMinute,
	parseTimeValue,
	scheduleFieldsFromRRule,
	schedulesEqual,
	WEEKDAY_CODES,
	type ScheduleFields,
	type WeekdayCode,
} from "../lib/automation-schedule";
import {
	useAutomationRuns,
	useAutomations,
	useCreateAutomation,
	useDeleteAutomation,
	useUpdateAutomation,
	type Automation,
	type CreateAutomationInput,
} from "../hooks/useAutomations";
import {
	centeredOnboardingDialogClass,
	onboardingAlertErrorClass,
	onboardingFieldErrorClass,
	onboardingFieldHintClass,
	onboardingFooterActionsEndClass,
	onboardingFormLabelClass,
} from "../lib/onboarding-ui";
import { cn } from "../lib/utils";
import { hidesShellTopbar } from "../lib/platform";

function displayTime(value?: string, locale?: string) {
	if (!value) return "—";
	return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

export function AutomationsView() {
	const { t } = useTranslation();
	const query = useAutomations();
	const showPageTitle = hidesShellTopbar();
	const workspaces = useWorkspaceQuery().data ?? [];
	const harnesses = useAgentReadinessQuery().data?.agents ?? [];
	const create = useCreateAutomation();
	const update = useUpdateAutomation();
	const remove = useDeleteAutomation();
	const [createOpen, setCreateOpen] = useState(false);
	const [editTarget, setEditTarget] = useState<Automation | null>(null);
	const [deleteTarget, setDeleteTarget] = useState<Automation | null>(null);
	const [expanded, setExpanded] = useState<string | null>(null);
	const [actionError, setActionError] = useState<string | null>(null);
	const [filter, setFilter] = useState<"all" | "enabled" | "paused">("all");
	const automations = query.data ?? [];
	const visibleAutomations = automations.filter((item) => filter === "all" || (filter === "enabled" ? item.enabled : !item.enabled));

	return (
		<div className="flex min-h-0 flex-1 flex-col bg-background">
			<header className="workspace-topbar-container center-panel-titlebar flex h-toolbar shrink-0 items-center justify-between gap-3 border-b border-border-strong pr-2">
				<div className="flex min-w-0 items-center gap-2">
					<h1 className={showPageTitle ? cn(topbarProjectLabelClass, "inline-flex items-center gap-1.5") : "sr-only"}>{showPageTitle ? <CalendarClock className="size-icon-md" aria-hidden="true" /> : null}{t("automations.title")}</h1>
					{!showPageTitle ? <p className="truncate text-xs text-muted-foreground">{t("automations.description")}</p> : null}
				</div>
				<div className="workspace-topbar-actions flex shrink-0 items-center">
					<TopbarButton variant="primary" className="topbar-control--labeled" onClick={() => setCreateOpen(true)}><Plus className="size-icon-md" aria-hidden="true" />{t("automations.new")}</TopbarButton>
				</div>
			</header>
			<ScrollArea.Root asChild type="hover" scrollHideDelay={500}>
			<main className="relative min-h-0 flex-1 overflow-hidden">
				<ScrollArea.Viewport className="h-full w-full overscroll-contain focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/60" tabIndex={0}>
				<div className="p-4">
				{actionError ? <p role="alert" className="mb-3 rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive">{actionError}</p> : null}
				{query.isLoading ? <p role="status" className="px-3 py-8 text-center text-sm text-muted-foreground">{t("automations.loading")}</p> : null}
				{query.error ? <p role="alert" className="text-sm text-destructive">{query.error.message}</p> : null}
				{!query.isLoading && !query.error && automations.length === 0 ? <EmptyAutomations onCreate={() => setCreateOpen(true)} /> : null}
				{automations.length > 0 ? <>
					<div className="mb-3 flex items-center gap-1" role="group" aria-label={t("automations.filter.label")}>
						{(["all", "enabled", "paused"] as const).map((value) => <button key={value} type="button" aria-pressed={filter === value} onClick={() => setFilter(value)} className={cn("inline-flex h-7 items-center gap-2 rounded-md px-2.5 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60", filter === value ? "bg-muted text-foreground" : "text-muted-foreground hover:bg-muted/50 hover:text-foreground")}>
							{t(`automations.filter.${value}`)}{" "}<span className="text-[10px] tabular-nums text-muted-foreground">{value === "all" ? automations.length : automations.filter((item) => value === "enabled" ? item.enabled : !item.enabled).length}</span>
						</button>)}
					</div>
					<div className="overflow-hidden rounded-lg border border-border bg-background">
						<div className="hidden grid-cols-[minmax(0,1fr)_180px_160px_132px] items-center gap-4 border-b border-border bg-muted/20 px-4 py-2 text-[11px] text-muted-foreground lg:grid" aria-hidden="true">
							<span>{t("automations.field.name")}</span><span>{t("automations.field.schedule")}</span><span>{t("automations.nextRun")}</span><span className="text-right">{t("automations.status")}</span>
						</div>
						{visibleAutomations.map((item) => (
							<AutomationCard key={item.id} item={item} projectName={workspaces.find((workspace) => workspace.id === item.projectId)?.name ?? item.projectId} busy={update.isPending} expanded={expanded === item.id} onExpand={() => setExpanded(expanded === item.id ? null : item.id)} onEdit={() => setEditTarget(item)} onDelete={() => setDeleteTarget(item)} onToggle={async (enabled) => { setActionError(null); try { await update.mutateAsync({ id: item.id, body: { enabled } }); } catch (error) { setActionError(error instanceof Error ? error.message : t("automations.updateError")); } }} />
						))}
						{visibleAutomations.length === 0 ? <p className="px-4 py-10 text-center text-xs text-muted-foreground">{t("automations.filter.empty")}</p> : null}
					</div>
				</> : null}
				</div>
				</ScrollArea.Viewport>
				<ScrollArea.Scrollbar orientation="vertical" className="z-10 flex w-2 touch-none select-none p-0.5">
					<ScrollArea.Thumb className="relative flex-1 rounded-full bg-[color-mix(in_srgb,var(--color-scrollbar)_42%,transparent)] hover:bg-[color-mix(in_srgb,var(--color-scrollbar)_56%,transparent)]" />
				</ScrollArea.Scrollbar>
			</main>
			</ScrollArea.Root>
			<AutomationFormDialog open={createOpen} workspaces={workspaces} harnesses={harnesses} busy={create.isPending} error={create.error?.message ?? null} onOpenChange={setCreateOpen} onSubmit={async (input) => { await create.mutateAsync(input as CreateAutomationInput); setCreateOpen(false); }} />
			<AutomationFormDialog open={Boolean(editTarget)} automation={editTarget ?? undefined} workspaces={workspaces} harnesses={harnesses} busy={update.isPending} error={update.error?.message ?? null} onOpenChange={(open) => { if (!open) setEditTarget(null); }} onSubmit={async (input) => { if (!editTarget) return; await update.mutateAsync({ id: editTarget.id, body: input }); setEditTarget(null); }} />
			<ConfirmDialog open={Boolean(deleteTarget)} title={t("automations.delete.title")} description={t("automations.delete.description", { name: deleteTarget?.displayName })} confirmLabel={t("automations.delete.confirm")} destructive busy={remove.isPending} error={remove.error?.message ?? null} onOpenChange={(open) => { if (!open) setDeleteTarget(null); }} onConfirm={() => { if (!deleteTarget) return; remove.mutate(deleteTarget.id, { onSuccess: () => setDeleteTarget(null) }); }} />
		</div>
	);
}

function EmptyAutomations({ onCreate }: { onCreate: () => void }) {
	const { t } = useTranslation();
	return <div className="grid min-h-64 place-items-center rounded-xl border border-dashed border-border p-8 text-center"><div><CalendarClock className="mx-auto mb-3 size-8 text-muted-foreground" /><h2 className="font-medium">{t("automations.empty.title")}</h2><p className="mt-1 text-sm text-muted-foreground">{t("automations.empty.description")}</p><Button className="mt-4" onClick={onCreate}>{t("automations.create")}</Button></div></div>;
}

function AutomationCard({ item, projectName, busy, expanded, onExpand, onEdit, onDelete, onToggle }: { item: Automation; projectName: string; busy: boolean; expanded: boolean; onExpand: () => void; onEdit: () => void; onDelete: () => void; onToggle: (enabled: boolean) => Promise<void> }) {
	const runs = useAutomationRuns(expanded ? item.id : null);
	const navigate = useNavigate();
	const { t, i18n } = useTranslation();
	const reduceMotion = useReducedMotion();
	// The daemon serializes DTSTART + RRULE, while the form intentionally
	// preserves those as legacy rules. Read the rule independently for display.
	const rule = item.rrule.split(/\r?\n/).find((line) => line.startsWith("RRULE:"))?.slice(6) ?? item.rrule.replace(/^RRULE:/, "");
	const parts: Record<string, string | undefined> = Object.fromEntries(rule.split(";").map((part) => part.split("=")));
	const frequency = parts.FREQ?.toLowerCase();
	const schedule = t(`automations.frequency.${frequency ?? "recurring"}`, { defaultValue: frequency ?? t("automations.frequency.recurring") });
	const simpleRule = !parts.INTERVAL || parts.INTERVAL === "1";
	const scheduleTime = parts.BYHOUR !== undefined && parts.BYMINUTE !== undefined ? parseHourMinute(parts.BYHOUR, parts.BYMINUTE) : null;
	const clockTime = scheduleTime ? new Intl.DateTimeFormat(i18n.resolvedLanguage, { hour: "numeric", minute: "2-digit", timeZone: "UTC" }).format(new Date(Date.UTC(2000, 0, 1, scheduleTime.hour, scheduleTime.minute))) : null;
	const rawWeekdays = parts.BYDAY?.split(",") ?? [];
	const weekdays = rawWeekdays.filter((day): day is WeekdayCode => WEEKDAY_CODES.includes(day as WeekdayCode));
	const validWeekdays = weekdays.length > 0 && weekdays.length === rawWeekdays.length;
	const shortWeekday = (day: WeekdayCode) => new Intl.DateTimeFormat(i18n.resolvedLanguage, { weekday: "short", timeZone: "UTC" }).format(new Date(Date.UTC(2026, 0, 5 + WEEKDAY_CODES.indexOf(day))));
	const weekdayLabel = weekdays.length === 1 ? t(`automations.weekday.${weekdays[0]}`) : weekdays.join(",") === "MO,TU,WE,TH,FR" ? `${shortWeekday("MO")}–${shortWeekday("FR")}` : weekdays.map(shortWeekday).join(", ");
	const scheduleLabel = simpleRule && frequency === "weekly" && validWeekdays ? weekdayLabel : simpleRule && frequency === "monthly" && /^[1-9]$|^[12]\d$|^3[01]$/.test(parts.BYMONTHDAY ?? "") ? t("automations.schedule.monthDay", { day: parts.BYMONTHDAY }) : schedule;

	const historyId = `automation-history-${item.id}`;
	return <article className="group border-b border-border last:border-b-0">
		<div className={cn("grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-4 gap-y-2 px-4 py-3 transition-colors motion-reduce:transition-none lg:grid-cols-[minmax(0,1fr)_180px_160px_132px]", expanded ? "bg-muted/30" : "hover:bg-muted/20")}>
			<button type="button" className="flex min-w-0 items-center gap-2.5 rounded-sm text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60" aria-label={t(expanded ? "automations.runs.hide" : "automations.runs.show", { name: item.displayName })} aria-expanded={expanded} aria-controls={historyId} onClick={onExpand}>
				<ChevronRight aria-hidden="true" className={cn("size-3.5 shrink-0 text-muted-foreground transition-transform duration-150 motion-reduce:transition-none", expanded && "rotate-90")} />
				<div className="min-w-0"><h2 className="truncate text-[13px] font-medium leading-5 text-foreground">{item.displayName}</h2><p className="truncate text-[11px] leading-5 text-muted-foreground">{projectName}</p></div>
			</button>
			<div className="col-start-1 flex items-start gap-2 pl-6 text-xs lg:col-start-auto lg:pl-0"><CalendarClock aria-hidden="true" className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" /><div><p className="capitalize">{scheduleLabel}{clockTime ? ` · ${clockTime}` : ""}</p></div></div>
			<div className="col-start-1 pl-6 text-xs lg:col-start-auto lg:pl-0"><span className="mr-2 text-muted-foreground lg:hidden">{t("automations.nextRun")}</span><span className={cn("tabular-nums", !item.enabled && "text-muted-foreground")}>{item.enabled ? displayTime(item.nextRunAt, i18n.resolvedLanguage) : t("automations.paused")}</span>{item.latestRun ? <p className="mt-1 text-[11px] capitalize text-muted-foreground">{item.latestRun.status}</p> : null}</div>
			<div className="col-start-2 row-start-1 row-span-3 flex items-center justify-end gap-1 lg:col-start-auto lg:row-start-auto lg:row-span-1">
				<Button variant="ghost" size="icon-sm" className="text-muted-foreground lg:opacity-0 lg:group-hover:opacity-100 lg:group-focus-within:opacity-100 focus-visible:ring-2 focus-visible:ring-ring/60" aria-label={t("automations.edit.aria", { name: item.displayName })} onClick={onEdit}><Pencil className="size-3.5" /></Button>
				<Button variant="ghost" size="icon-sm" className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive lg:opacity-0 lg:group-hover:opacity-100 lg:group-focus-within:opacity-100 focus-visible:ring-2 focus-visible:ring-ring/60" aria-label={t("automations.delete.aria", { name: item.displayName })} onClick={onDelete}><Trash2 className="size-3.5" /></Button>
				<Switch checked={item.enabled} disabled={busy} aria-label={t(item.enabled ? "automations.disable" : "automations.enable", { name: item.displayName })} onCheckedChange={(checked) => void onToggle(checked)} />
			</div>
		</div>
		{item.latestRun?.errorMessage ? <p role="alert" className="mx-4 mb-3 rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive">{item.latestRun.errorMessage}</p> : null}
		<AnimatePresence initial={false}>
			{expanded ? <motion.div id={historyId} initial={{ height: 0, opacity: 0 }} animate={{ height: "auto", opacity: 1 }} exit={{ height: 0, opacity: 0 }} transition={{ duration: reduceMotion ? 0 : 0.18, ease: "easeOut" }} className="overflow-hidden">
				<div className="border-t border-border bg-muted/10 px-4 py-4 pl-10">
					<div className="mb-4 flex flex-wrap items-center justify-between gap-2 text-[11px] text-muted-foreground"><span>{t("automations.timezone", { timezone: item.timezone })}</span><span>{t("automations.agent")}: <span className="text-foreground">{item.harness || t("automations.projectDefault")} · {item.kind}</span></span><span>{t("automations.latestState")}: <span className="capitalize">{item.latestRun?.status ?? t("automations.neverRun")}</span></span></div>
					<p className="mb-1 text-[11px] font-medium text-muted-foreground">{t("automations.field.prompt")}</p><p className="mb-4 whitespace-pre-wrap text-xs leading-5 text-foreground">{item.prompt}</p>
					<h3 className="mb-2 flex items-center gap-1.5 text-xs font-medium"><History className="size-3.5 text-muted-foreground" aria-hidden="true" />{t("automations.runs.title")}</h3>
					{runs.isLoading ? <p role="status" className="text-xs text-muted-foreground">{t("automations.runs.loading")}</p> : runs.error ? <p role="alert" className="text-xs text-destructive">{runs.error.message}</p> : runs.data?.length ? <div className="divide-y divide-border">{runs.data.map((run) => <div key={run.id} className="flex items-center justify-between gap-3 py-2 text-xs"><div><span className="font-medium capitalize">{run.status}</span><span className="ml-3 text-[11px] text-muted-foreground">{displayTime(run.scheduledFor, i18n.resolvedLanguage)}</span>{run.errorMessage ? <p className="mt-1 text-xs text-destructive">{run.errorMessage}</p> : null}</div>{run.sessionId ? <Button variant="outline" size="sm" onClick={() => void navigate({ to: "/sessions/$sessionId", params: { sessionId: run.sessionId! } })}>{t("automations.runs.openSession")}</Button> : null}</div>)}</div> : <p className="flex items-center gap-2 py-3 text-xs text-muted-foreground"><Clock3 className="size-3.5" aria-hidden="true" />{t("automations.runs.empty")}</p>}
				</div>
			</motion.div> : null}
		</AnimatePresence>
	</article>;
}

type WorkspaceOption = { id: string; name: string };
type AutomationFormSubmit = {
	projectId?: string;
	displayName: string;
	prompt: string;
	kind?: "worker" | "orchestrator";
	harness?: string;
	timezone?: string;
	rrule?: string;
};
type AutomationFormDialogProps = {
	open: boolean;
	automation?: Automation;
	workspaces: WorkspaceOption[];
	harnesses: AgentReadinessSnapshot[];
	busy: boolean;
	error: string | null;
	onOpenChange: (open: boolean) => void;
	onSubmit: (input: AutomationFormSubmit) => Promise<void>;
};

type AutomationField = "projectId" | "name" | "prompt" | "time" | "hour" | "minute" | "monthDay";
type AutomationValidationErrors = Partial<Record<AutomationField, string>>;

const AUTOMATION_FIELD_IDS: Record<AutomationField, string> = {
	projectId: "automation-project",
	name: "automation-name",
	prompt: "automation-prompt",
	time: "automation-time",
	hour: "automation-hour",
	minute: "automation-minute",
	monthDay: "automation-month-day",
};

function AutomationFormDialog({
	open,
	automation,
	workspaces,
	harnesses,
	busy,
	error,
	onOpenChange,
	onSubmit,
}: AutomationFormDialogProps) {
	const { t } = useTranslation();
	const editing = Boolean(automation);
	const timezone = automation?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
	const [projectId, setProjectId] = useState("");
	const projectDefaultWorker = useProjectDefaultWorker(open ? (automation?.projectId || projectId) : "");
	const [name, setName] = useState("");
	const [prompt, setPrompt] = useState("");
	const [harness, setHarness] = useState("");
	const [schedule, setSchedule] = useState<ScheduleFields>(() => ({
		preset: "daily",
		time: nowLocalHHMM(),
		customFrequency: "monthly",
		monthDay: "1",
		weekday: "MO",
		hour: "9",
		minute: "0",
		legacyRaw: null,
	}));
	const [initialSchedule, setInitialSchedule] = useState<ScheduleFields | null>(null);
	const [validationErrors, setValidationErrors] = useState<AutomationValidationErrors>({});

	function patchSchedule(patch: Partial<ScheduleFields>) {
		setSchedule((current) => {
			const clearsLegacy = Object.keys(patch).some((key) => key !== "legacyRaw");
			return {
				...current,
				...patch,
				legacyRaw: patch.legacyRaw !== undefined ? patch.legacyRaw : clearsLegacy ? null : current.legacyRaw,
			};
		});
	}

	useEffect(() => {
		if (!open) return;
		setProjectId(automation?.projectId ?? "");
		setName(automation?.displayName ?? "");
		setPrompt(automation?.prompt ?? "");
		setHarness(automation?.harness ?? "");
		const defaultTime = nowLocalHHMM();
		const [defaultHour, defaultMinute] = defaultTime.split(":");
		const nextSchedule = automation
			? scheduleFieldsFromRRule(automation.rrule)
			: {
					preset: "daily" as const,
					time: defaultTime,
					customFrequency: "daily" as const,
					monthDay: "1",
					weekday: "MO" as WeekdayCode,
					hour: defaultHour,
					minute: defaultMinute,
					legacyRaw: null,
				};
		setInitialSchedule(nextSchedule);
		setSchedule(nextSchedule);
		setValidationErrors({});
	}, [open, automation]);

	const projectOptions = workspaces.map((item) => ({ value: item.id, label: item.name }));
	if (automation && !projectOptions.some((option) => option.value === automation.projectId)) {
		projectOptions.unshift({ value: automation.projectId, label: automation.projectId });
	}
	// Prefer an explicit choice, then the project's resolved worker, then the
	// first ready harness so Model isn't stuck on "Select agent" before a
	// project is picked.
	const fallbackHarness =
		buildRankedAgentOptions({
			agents: harnesses,
			priorityRank: DEFAULT_AGENT_PRIORITY_RANK,
			fallbackAgents: [],
		}).find(isReadyAgent)?.id ?? "";
	const selectedHarness = harness || projectDefaultWorker || fallbackHarness;

	function clearValidationError(field: AutomationField) {
		setValidationErrors((current) => {
			if (!current[field]) return current;
			const next = { ...current };
			delete next[field];
			return next;
		});
	}

	async function submit(event: FormEvent) {
		event.preventDefault();
		const nextErrors: AutomationValidationErrors = {};
		if (!editing && !projectId) nextErrors.projectId = t("automations.validation.project");
		if (!name.trim()) nextErrors.name = t("automations.validation.name");
		if (!prompt.trim()) nextErrors.prompt = t("automations.validation.prompt");
		if (schedule.preset === "daily" || schedule.preset === "weekly") {
			if (!parseTimeValue(schedule.time)) nextErrors.time = t("automations.validation.time");
		} else {
			if (!parseHourMinute(schedule.hour, schedule.minute)) {
				nextErrors.hour = t("automations.validation.hour");
				nextErrors.minute = t("automations.validation.minute");
			}
			if (schedule.customFrequency === "monthly" && !/^[1-9]$|^[12]\d$|^3[01]$/.test(schedule.monthDay.trim())) {
				nextErrors.monthDay = t("automations.validation.date");
			}
		}
		setValidationErrors(nextErrors);
		const firstInvalid = (["projectId", "name", "prompt", "time", "hour", "minute", "monthDay"] as const).find((field) => nextErrors[field]);
		if (firstInvalid) {
			document.getElementById(AUTOMATION_FIELD_IDS[firstInvalid])?.focus();
			return;
		}
		const nextRRule = buildRRuleFromSchedule(schedule);
		const scheduleChanged = !(editing && automation && initialSchedule && schedulesEqual(schedule, initialSchedule));
		const harnessChanged = !editing || harness !== (automation?.harness ?? "");
		await onSubmit({
			// Kind is not a form choice: automations are workers, and editing
			// leaves the stored kind untouched.
			...(editing ? {} : { projectId, timezone, kind: "worker" as const }),
			displayName: name,
			prompt,
			...(harnessChanged && selectedHarness ? { harness: selectedHarness } : {}),
			...(scheduleChanged ? { rrule: nextRRule } : {}),
		});
	}

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent showCloseButton={false} className={centeredOnboardingDialogClass}>
				<DialogClose asChild>
					<button
						type="button"
						disabled={busy}
						className="settings-dialog-close-button settings-close-button"
						aria-label={t("automations.create.close")}
					>
						<X className="size-4" aria-hidden="true" />
					</button>
				</DialogClose>
				{/* Match New Task / project onboarding: title padding only, no header band or hairline. */}
				<DialogTitle className="settings-dialog-title px-4 pr-12 pt-3">
					{t(editing ? "automations.edit" : "automations.create")}
				</DialogTitle>
				<DialogDescription className="px-4 pr-12 pt-1 text-[13px] leading-5 text-muted-foreground">
					{t(editing ? "automations.edit.description" : "automations.create.description")}
				</DialogDescription>
				<form className="flex min-h-0 flex-1 flex-col" noValidate onSubmit={(event) => void submit(event)}>
					<div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 pb-1 pt-4">
						{Object.keys(validationErrors).length > 0 ? (
							<div role="alert" className={cn(onboardingAlertErrorClass, "flex items-start gap-2")}>
								<TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
								<span>{t("automations.validation.summary")}</span>
							</div>
						) : null}
						<Field label={t("automations.field.project")} id={AUTOMATION_FIELD_IDS.projectId} error={validationErrors.projectId}>
							<AutomationSelect
								id={AUTOMATION_FIELD_IDS.projectId}
								label={t("automations.field.project")}
								placeholder={t("automations.projectPlaceholder")}
								required
								disabled={editing}
								invalid={Boolean(validationErrors.projectId)}
								describedBy={validationErrors.projectId ? `${AUTOMATION_FIELD_IDS.projectId}-error` : undefined}
								value={projectId}
								onValueChange={(value) => { setProjectId(value); clearValidationError("projectId"); }}
								options={projectOptions}
							/>
						</Field>
						<Field label={t("automations.field.name")} id={AUTOMATION_FIELD_IDS.name} error={validationErrors.name}>
							<Input id={AUTOMATION_FIELD_IDS.name} required maxLength={120} value={name} aria-invalid={Boolean(validationErrors.name) || undefined} aria-describedby={validationErrors.name ? `${AUTOMATION_FIELD_IDS.name}-error` : undefined} onChange={(event) => { setName(event.target.value); if (event.target.value.trim()) clearValidationError("name"); }} />
						</Field>
						<Field label={t("automations.field.prompt")} id={AUTOMATION_FIELD_IDS.prompt} error={validationErrors.prompt}>
							<textarea
								id={AUTOMATION_FIELD_IDS.prompt}
								required
								maxLength={4096}
								value={prompt}
								aria-invalid={Boolean(validationErrors.prompt) || undefined}
								aria-describedby={validationErrors.prompt ? `${AUTOMATION_FIELD_IDS.prompt}-error` : undefined}
								onChange={(event) => { setPrompt(event.target.value); if (event.target.value.trim()) clearValidationError("prompt"); }}
								className="min-h-28 w-full resize-y rounded-md border border-transparent bg-input/50 px-3 py-2 text-[13px] leading-5 outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/30 aria-invalid:border-destructive"
							/>
						</Field>
						{/* Agent/Schedule/Time share one labeled grid and Select/Input chrome. */}
						<div className="grid grid-cols-2 gap-3">
							<RequiredAgentField
								id="automation-agent"
								label={t("automations.agent")}
								labelClassName={onboardingFormLabelClass}
								placeholder={t("automations.agentPlaceholder")}
								value={selectedHarness}
								agents={harnesses}
								disabled={busy}
								onChange={(value) => {
									setHarness(value);
								}}
							/>
							<Field label={t("automations.field.schedule")}>
								<AutomationSelect
									label={t("automations.field.schedule")}
									value={schedule.preset}
									onValueChange={(value) => {
										patchSchedule({ preset: value as ScheduleFields["preset"] });
										clearValidationError("time");
										clearValidationError("hour");
										clearValidationError("minute");
										clearValidationError("monthDay");
									}}
									options={[
										{ value: "daily", label: t("automations.schedule.daily") },
										{ value: "weekly", label: t("automations.schedule.weekly") },
										{ value: "custom", label: t("automations.schedule.custom") },
									]}
								/>
							</Field>
							{schedule.preset === "weekly" ? (
								<Field label={t("automations.field.weekday")}>
									<AutomationSelect
										label={t("automations.field.weekday")}
										value={schedule.weekday}
										onValueChange={(value) => patchSchedule({ weekday: value as WeekdayCode })}
										options={WEEKDAY_CODES.map((code) => ({
											value: code,
											label: t(`automations.weekday.${code}`),
										}))}
									/>
								</Field>
							) : null}
							{schedule.preset === "daily" || schedule.preset === "weekly" ? (
								<Field label={t("automations.field.localTime")} id={AUTOMATION_FIELD_IDS.time} error={validationErrors.time}>
									<Input
										id={AUTOMATION_FIELD_IDS.time}
										type="text"
										inputMode="numeric"
										autoComplete="off"
										spellCheck={false}
										required
										placeholder="09:00"
										value={schedule.time}
										aria-invalid={Boolean(validationErrors.time) || undefined}
										aria-describedby={validationErrors.time ? `${AUTOMATION_FIELD_IDS.time}-error` : undefined}
										className="tabular-nums"
										onChange={(event) => {
											const next = formatTimeDraft(event.target.value);
											patchSchedule({ time: next });
											if (parseTimeValue(next)) clearValidationError("time");
										}}
									/>
								</Field>
							) : null}
						</div>
						{schedule.preset === "custom" ? (
							<div className="grid grid-cols-2 gap-3">
								<Field label={t("automations.field.frequency")}>
									<AutomationSelect
										label={t("automations.field.frequency")}
										value={schedule.customFrequency}
										onValueChange={(value) => {
											patchSchedule({ customFrequency: value as ScheduleFields["customFrequency"] });
											clearValidationError("monthDay");
										}}
										options={[
											{ value: "daily", label: t("automations.customFrequency.daily") },
											{ value: "weekly", label: t("automations.customFrequency.weekly") },
											{ value: "monthly", label: t("automations.customFrequency.monthly") },
										]}
									/>
								</Field>
								{schedule.customFrequency === "monthly" ? (
									<Field label={t("automations.field.date")} id={AUTOMATION_FIELD_IDS.monthDay} error={validationErrors.monthDay}>
										<Input
											id={AUTOMATION_FIELD_IDS.monthDay}
											inputMode="numeric"
											autoComplete="off"
											required
											value={schedule.monthDay}
											aria-invalid={Boolean(validationErrors.monthDay) || undefined}
											aria-describedby={validationErrors.monthDay ? `${AUTOMATION_FIELD_IDS.monthDay}-error` : undefined}
											className="tabular-nums"
											onChange={(event) => {
												const next = clampMonthDayDraft(event.target.value);
												patchSchedule({ monthDay: next });
												if (/^[1-9]$|^[12]\d$|^3[01]$/.test(next)) clearValidationError("monthDay");
											}}
										/>
									</Field>
								) : schedule.customFrequency === "weekly" ? (
									<Field label={t("automations.field.weekday")}>
										<AutomationSelect
											label={t("automations.field.weekday")}
											value={schedule.weekday}
											onValueChange={(value) => patchSchedule({ weekday: value as WeekdayCode })}
											options={WEEKDAY_CODES.map((code) => ({
												value: code,
												label: t(`automations.weekday.${code}`),
											}))}
										/>
									</Field>
								) : (
									<div aria-hidden="true" />
								)}
								<Field label={t("automations.field.hour")} id={AUTOMATION_FIELD_IDS.hour} error={validationErrors.hour}>
									<Input
										id={AUTOMATION_FIELD_IDS.hour}
										inputMode="numeric"
										autoComplete="off"
										required
										placeholder="09"
										value={schedule.hour}
										aria-invalid={Boolean(validationErrors.hour) || undefined}
										className="tabular-nums"
										onChange={(event) => {
											const next = clampHourDraft(event.target.value);
											patchSchedule({ hour: next });
											if (parseHourMinute(next, schedule.minute)) {
												clearValidationError("hour");
												clearValidationError("minute");
											}
										}}
									/>
								</Field>
								<Field label={t("automations.field.minute")} id={AUTOMATION_FIELD_IDS.minute} error={validationErrors.minute}>
									<Input
										id={AUTOMATION_FIELD_IDS.minute}
										inputMode="numeric"
										autoComplete="off"
										required
										placeholder="00"
										value={schedule.minute}
										aria-invalid={Boolean(validationErrors.minute) || undefined}
										className="tabular-nums"
										onChange={(event) => {
											const next = clampMinuteDraft(event.target.value);
											patchSchedule({ minute: next });
											if (parseHourMinute(schedule.hour, next)) {
												clearValidationError("hour");
												clearValidationError("minute");
											}
										}}
									/>
								</Field>
							</div>
						) : null}
						<p className={onboardingFieldHintClass}>{t("automations.timezone", { timezone })}</p>
						{error ? <p role="alert" className={onboardingFieldErrorClass}>{error}</p> : null}
					</div>
					{/* Match onboarding/new-task action row: no footer hairline. */}
					<div className={cn(onboardingFooterActionsEndClass, "px-4 pb-4")}>
						<Button type="button" variant="outline" disabled={busy} onClick={() => onOpenChange(false)}>
							{t("automations.cancel")}
						</Button>
						<Button type="submit" variant="primary" disabled={busy}>
							{busy ? t(editing ? "automations.saving" : "automations.creating") : t(editing ? "automations.save" : "automations.create")}
						</Button>
					</div>
				</form>
			</DialogContent>
		</Dialog>
	);
}

function AutomationSelect({
	id,
	label,
	value,
	onValueChange,
	options,
	placeholder,
	required,
	disabled,
	invalid,
	describedBy,
}: {
	id?: string;
	label: string;
	value: string;
	onValueChange: (value: string) => void;
	options: Array<{ value: string; label: string; disabled?: boolean }>;
	placeholder?: string;
	required?: boolean;
	disabled?: boolean;
	invalid?: boolean;
	describedBy?: string;
}) {
	return (
		<Select value={value} onValueChange={onValueChange} required={required} disabled={disabled}>
			<SelectTrigger id={id} size="sm" className="w-full text-control" aria-label={label} aria-invalid={invalid || undefined} aria-describedby={describedBy}>
				<SelectValue placeholder={placeholder} />
			</SelectTrigger>
			<SelectContent position="popper" side="bottom" align="start" sideOffset={4} className="max-h-64">
				{options.map((option) => (
					<SelectItem key={option.value} value={option.value} disabled={option.disabled}>
						{option.label}
					</SelectItem>
				))}
			</SelectContent>
		</Select>
	);
}

function Field({ label, id, error, children }: { label: string; id?: string; error?: string; children: React.ReactNode }) {
	return (
		<div className="flex flex-col gap-2">
			{id ? <label htmlFor={id} className={onboardingFormLabelClass}>{label}</label> : <span className={onboardingFormLabelClass}>{label}</span>}
			{children}
			{error ? <p id={`${id}-error`} className={onboardingFieldErrorClass}>{error}</p> : null}
		</div>
	);
}
