import { useEffect, useState, type FormEvent } from "react";
import { useNavigate } from "@tanstack/react-router";
import { useTranslation } from "react-i18next";
import { CalendarClock, ChevronDown, ChevronRight, Pencil, Plus, Trash2, TriangleAlert, X } from "lucide-react";
import { Button } from "./ui/button";
import { Card, CardAction, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";
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

function displayTime(value?: string, locale?: string) {
	if (!value) return "—";
	return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

export function AutomationsView() {
	const { t } = useTranslation();
	const query = useAutomations();
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

	return (
		<div className="flex min-h-0 flex-1 flex-col overflow-auto bg-background">
			<header className="flex items-center justify-between border-b border-border px-8 py-5">
				<div><h1 className="text-xl font-semibold">{t("automations.title")}</h1><p className="mt-1 text-sm text-muted-foreground">{t("automations.description")}</p></div>
				<Button onClick={() => setCreateOpen(true)}><Plus aria-hidden="true" />{t("automations.new")}</Button>
			</header>
			<main className="mx-auto flex w-full max-w-5xl flex-col gap-4 p-8">
				{actionError ? <p role="alert" className="rounded-md bg-destructive/10 px-3 py-2 text-sm text-destructive">{actionError}</p> : null}
				{query.isLoading ? <p className="text-sm text-muted-foreground">{t("automations.loading")}</p> : null}
				{query.error ? <p role="alert" className="text-sm text-destructive">{query.error.message}</p> : null}
				{!query.isLoading && !query.error && query.data?.length === 0 ? <EmptyAutomations onCreate={() => setCreateOpen(true)} /> : null}
				{query.data?.map((item) => (
					<AutomationCard key={item.id} item={item} expanded={expanded === item.id} onExpand={() => setExpanded(expanded === item.id ? null : item.id)} onEdit={() => setEditTarget(item)} onDelete={() => setDeleteTarget(item)} onToggle={async (enabled) => { setActionError(null); try { await update.mutateAsync({ id: item.id, body: { enabled } }); } catch (error) { setActionError(error instanceof Error ? error.message : t("automations.updateError")); } }} />
				))}
			</main>
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

function AutomationCard({ item, expanded, onExpand, onEdit, onDelete, onToggle }: { item: Automation; expanded: boolean; onExpand: () => void; onEdit: () => void; onDelete: () => void; onToggle: (enabled: boolean) => Promise<void> }) {
	const runs = useAutomationRuns(expanded ? item.id : null);
	const navigate = useNavigate();
	const { t, i18n } = useTranslation();
	const frequency = item.rrule.match(/FREQ=([^;\n]+)/)?.[1]?.toLowerCase();
	const schedule = t(`automations.frequency.${frequency ?? "recurring"}`, { defaultValue: frequency ?? t("automations.frequency.recurring") });
	return <Card size="sm">
		<CardHeader><CardTitle className="flex items-center gap-2"><button type="button" className="grid size-6 place-items-center rounded hover:bg-muted" aria-label={t(expanded ? "automations.runs.hide" : "automations.runs.show", { name: item.displayName })} onClick={onExpand}>{expanded ? <ChevronDown /> : <ChevronRight />}</button>{item.displayName}</CardTitle><CardDescription>{item.projectId} · {schedule} · {item.timezone}</CardDescription><CardAction className="flex items-center gap-3"><label className="flex items-center gap-2 text-xs text-muted-foreground"><span>{t(item.enabled ? "automations.enabled" : "automations.disabled")}</span><Switch checked={item.enabled} aria-label={t(item.enabled ? "automations.disable" : "automations.enable", { name: item.displayName })} onCheckedChange={(checked) => void onToggle(checked)} /></label><Button variant="ghost" size="icon-sm" aria-label={t("automations.edit.aria", { name: item.displayName })} onClick={onEdit}><Pencil /></Button><Button variant="ghost" size="icon-sm" className="text-destructive hover:bg-destructive/10 hover:text-destructive" aria-label={t("automations.delete.aria", { name: item.displayName })} onClick={onDelete}><Trash2 /></Button></CardAction></CardHeader>
		<CardContent><div className="grid gap-3 text-sm sm:grid-cols-3"><div><span className="block text-xs text-muted-foreground">{t("automations.nextRun")}</span>{item.enabled ? displayTime(item.nextRunAt, i18n.resolvedLanguage) : t("automations.paused")}</div><div><span className="block text-xs text-muted-foreground">{t("automations.latestState")}</span>{item.latestRun?.status ?? t("automations.neverRun")}</div><div><span className="block text-xs text-muted-foreground">{t("automations.agent")}</span>{item.harness || t("automations.projectDefault")} · {item.kind}</div></div>{item.latestRun?.errorMessage ? <p role="alert" className="mt-3 rounded bg-destructive/10 px-3 py-2 text-xs text-destructive">{item.latestRun.errorMessage}</p> : null}
		{expanded ? <div className="mt-4 border-t border-border pt-4"><h3 className="mb-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">{t("automations.runs.title")}</h3>{runs.isLoading ? <p className="text-sm text-muted-foreground">{t("automations.runs.loading")}</p> : runs.error ? <p role="alert" className="text-sm text-destructive">{runs.error.message}</p> : runs.data?.length ? <div className="space-y-2">{runs.data.map((run) => <div key={run.id} className="flex items-center justify-between rounded-md bg-muted/40 px-3 py-2 text-sm"><div><span className="font-medium capitalize">{run.status}</span><span className="ml-2 text-xs text-muted-foreground">{displayTime(run.scheduledFor, i18n.resolvedLanguage)}</span>{run.errorMessage ? <p className="text-xs text-destructive">{run.errorMessage}</p> : null}</div>{run.sessionId ? <Button variant="outline" size="sm" onClick={() => void navigate({ to: "/sessions/$sessionId", params: { sessionId: run.sessionId! } })}>{t("automations.runs.openSession")}</Button> : null}</div>)}</div> : <p className="text-sm text-muted-foreground">{t("automations.runs.empty")}</p>}</div> : null}</CardContent>
	</Card>;
}

type WorkspaceOption = { id: string; name: string };
type AutomationFormSubmit = {
	projectId?: string;
	displayName: string;
	prompt: string;
	kind?: "worker" | "orchestrator";
	harness?: string;
	timezone?: string;
	rrule: string;
};
type ScheduleFields = { preset: string; time: string; raw: string };
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

// Maps a persisted rule back onto the form presets; anything the presets
// cannot reproduce stays on the custom RRULE field.
function scheduleFieldsFromRRule(rruleText: string): ScheduleFields {
	const trimmed = rruleText.trim();
	const lines = trimmed.split("\n").map((line) => line.trim()).filter(Boolean);
	const rawRule = lines.length === 1 && lines[0].startsWith("RRULE:") ? lines[0].slice("RRULE:".length) : trimmed;
	if (lines.length > 1) return { preset: "raw", time: nowLocalHHMM(), raw: trimmed };
	const parts = Object.fromEntries(rawRule.split(";").map((part) => {
		const [key, ...value] = part.split("=");
		return [key, value.join("=")];
	}));
	const keys = Object.keys(parts).sort().join(",");
	const hour = parts.BYHOUR;
	const minute = parts.BYMINUTE;
	if (hour !== undefined && minute !== undefined && parts.BYSECOND === "0") {
		const time = `${hour.padStart(2, "0")}:${minute.padStart(2, "0")}`;
		if (parts.FREQ === "DAILY" && keys === "BYHOUR,BYMINUTE,BYSECOND,FREQ") return { preset: "daily", time, raw: rawRule };
		if (parts.FREQ === "WEEKLY" && parts.BYDAY === "MO" && keys === "BYDAY,BYHOUR,BYMINUTE,BYSECOND,FREQ") return { preset: "weekly", time, raw: rawRule };
	}
	return { preset: "raw", time: nowLocalHHMM(), raw: trimmed || rawRule };
}

function nowLocalHHMM() {
	const now = new Date();
	return `${String(now.getHours()).padStart(2, "0")}:${String(now.getMinutes()).padStart(2, "0")}`;
}

/** 24-hour clock values as HH:mm. Empty means unset. */
function parseTimeValue(value: string): { hour: number; minute: number } | null {
	const match = value.trim().match(/^(\d{2}):(\d{2})$/);
	if (!match) return null;
	const hour = Number(match[1]);
	const minute = Number(match[2]);
	if (hour > 23 || minute > 59) return null;
	return { hour, minute };
}

/** Digits-only draft → HH:mm, rejecting any digit that would make an invalid clock. */
function formatTimeDraft(raw: string): string {
	const out: string[] = [];
	for (const ch of raw.replace(/\D/g, "")) {
		if (out.length >= 4) break;
		const digit = Number(ch);
		const pos = out.length;
		if (pos === 0) {
			// Hour tens is 0–2; 3–9 becomes 0X so the field never holds 3x–9x.
			if (digit > 2) {
				out.push("0", ch);
			} else {
				out.push(ch);
			}
			continue;
		}
		if (pos === 1) {
			if (Number(out[0]) === 2 && digit > 3) continue;
			out.push(ch);
			continue;
		}
		if (pos === 2) {
			if (digit > 5) continue;
			out.push(ch);
			continue;
		}
		out.push(ch);
	}
	const digits = out.slice(0, 4).join("");
	if (digits.length <= 2) return digits;
	return `${digits.slice(0, 2)}:${digits.slice(2)}`;
}

type AutomationField = "projectId" | "name" | "prompt" | "raw" | "time";
type AutomationValidationErrors = Partial<Record<AutomationField, string>>;

const AUTOMATION_FIELD_IDS: Record<AutomationField, string> = {
	projectId: "automation-project",
	name: "automation-name",
	prompt: "automation-prompt",
	raw: "automation-rrule",
	time: "automation-time",
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
	const [preset, setPreset] = useState("daily");
	const [time, setTime] = useState(nowLocalHHMM);
	const [raw, setRaw] = useState("FREQ=DAILY;BYHOUR=9;BYMINUTE=0;BYSECOND=0");
	const [initialSchedule, setInitialSchedule] = useState<ScheduleFields | null>(null);
	const [validationErrors, setValidationErrors] = useState<AutomationValidationErrors>({});

	useEffect(() => {
		if (!open) return;
		setProjectId(automation?.projectId ?? "");
		setName(automation?.displayName ?? "");
		setPrompt(automation?.prompt ?? "");
		setHarness(automation?.harness ?? "");
		const defaultTime = nowLocalHHMM();
		const [defaultHour, defaultMinute] = defaultTime.split(":");
		const schedule = automation
			? scheduleFieldsFromRRule(automation.rrule)
			: {
					preset: "daily",
					time: defaultTime,
					raw: `FREQ=DAILY;BYHOUR=${Number(defaultHour)};BYMINUTE=${Number(defaultMinute)};BYSECOND=0`,
				};
		setInitialSchedule(schedule);
		setPreset(schedule.preset);
		setTime(schedule.time);
		setRaw(schedule.raw);
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
		if (preset === "raw" && !raw.trim()) nextErrors.raw = t("automations.validation.rrule");
		const parsedTime = preset === "raw" ? null : parseTimeValue(time);
		if (preset !== "raw" && !parsedTime) nextErrors.time = t("automations.validation.time");
		setValidationErrors(nextErrors);
		const firstInvalid = (["projectId", "name", "prompt", "raw", "time"] as const).find((field) => nextErrors[field]);
		if (firstInvalid) {
			document.getElementById(AUTOMATION_FIELD_IDS[firstInvalid])?.focus();
			return;
		}
		let rrule =
			preset === "daily"
				? `FREQ=DAILY;BYHOUR=${parsedTime!.hour};BYMINUTE=${parsedTime!.minute};BYSECOND=0`
				: preset === "weekly"
					? `FREQ=WEEKLY;BYDAY=MO;BYHOUR=${parsedTime!.hour};BYMINUTE=${parsedTime!.minute};BYSECOND=0`
					: raw;
		if (editing && automation && initialSchedule && preset === initialSchedule.preset && time === initialSchedule.time && raw === initialSchedule.raw) {
			rrule = automation.rrule;
		}
		await onSubmit({
			// Kind is not a form choice: automations are workers, and editing
			// leaves the stored kind untouched.
			...(editing ? {} : { projectId, timezone, kind: "worker" as const }),
			displayName: name,
			prompt,
			harness: selectedHarness || undefined,
			rrule,
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
								className="min-h-24 w-full rounded-md border border-transparent bg-input/50 px-3 py-2 text-[13px] outline-none focus-visible:outline-none aria-invalid:border-destructive"
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
									value={preset}
									onValueChange={(value) => {
										setPreset(value);
										if (value === "raw") clearValidationError("time");
										else clearValidationError("raw");
									}}
									options={[
										{ value: "daily", label: t("automations.schedule.daily") },
										{ value: "weekly", label: t("automations.schedule.weekly") },
										{ value: "raw", label: t("automations.schedule.custom") },
									]}
								/>
							</Field>
							{preset === "raw" ? (
								<Field label={t("automations.field.rrule")} id={AUTOMATION_FIELD_IDS.raw} error={validationErrors.raw}>
									<Input
										id={AUTOMATION_FIELD_IDS.raw}
										required
										value={raw}
										aria-invalid={Boolean(validationErrors.raw) || undefined}
										aria-describedby={validationErrors.raw ? `${AUTOMATION_FIELD_IDS.raw}-error` : undefined}
										onChange={(event) => {
											setRaw(event.target.value);
											if (event.target.value.trim()) clearValidationError("raw");
										}}
									/>
								</Field>
							) : (
								<Field label={t("automations.field.localTime")} id={AUTOMATION_FIELD_IDS.time} error={validationErrors.time}>
									<Input
										id={AUTOMATION_FIELD_IDS.time}
										type="text"
										inputMode="numeric"
										autoComplete="off"
										spellCheck={false}
										required
										placeholder="09:00"
										value={time}
										aria-invalid={Boolean(validationErrors.time) || undefined}
										aria-describedby={validationErrors.time ? `${AUTOMATION_FIELD_IDS.time}-error` : undefined}
										className="tabular-nums"
										onChange={(event) => {
											const next = formatTimeDraft(event.target.value);
											setTime(next);
											if (parseTimeValue(next)) clearValidationError("time");
										}}
									/>
								</Field>
							)}
						</div>
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
