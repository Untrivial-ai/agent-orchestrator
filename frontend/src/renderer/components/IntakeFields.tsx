import { AppLink } from "./AppLink";
import { Pencil, Plus, TriangleAlert } from "lucide-react";
import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import { cn } from "../lib/utils";
import { Label } from "./ui/label";
import { SettingsRow } from "./settings/SettingsRow";
import { Button } from "./ui/button";
import { Switch } from "./ui/switch";

type TrackerIntakeConfig = components["schemas"]["TrackerIntakeConfig"];

// IntakeForm is the flat, string-backed shape both the create sheet and the
// project settings form edit. repo has no input today (it's derived from the
// git origin server-side) but is plumbed so a value set via the CLI
// (--tracker-repo) survives a UI save instead of being wiped.
export type IntakeForm = {
	enabled: boolean;
	repo: string;
	assignee: string;
};

// The provider is not set here — the daemon infers it from the project's repo
// origin URL (github.com → github, any other host → gitlab). Adding
// Linear/Jira later means: the backend enum grows, IntakeFields gains a
// provider <Select> + per-provider scope fields, and buildIntake switches the
// scope field it emits.

// intakeNeedsRule mirrors the backend guard (TrackerIntakeConfig.Validate):
// enabling intake requires an assignee so it cannot drain an entire issue
// backlog. v1 intake is assignee-only.
export function intakeNeedsRule(form: IntakeForm): boolean {
	return form.enabled && form.assignee.trim() === "";
}

// buildIntake produces the payload field, scrubbing empties so a disabled or
// blank intake serializes to `undefined` (omit) rather than an empty object the
// daemon would persist.
export function buildIntake(
	form: IntakeForm,
	existing?: TrackerIntakeConfig,
): TrackerIntakeConfig | undefined {
	const next: TrackerIntakeConfig = {
		...existing,
		enabled: form.enabled || undefined,
		repo: form.repo.trim() || undefined,
		assignee: form.assignee.trim() || undefined,
	};
	return Object.values(next).some((v) => v !== undefined) ? next : undefined;
}

// deriveRepoPath mirrors the daemon's server-side path derivation
// (observe/trackerintake/observer.go): extract the provider-native repo path
// from a git origin URL for display only. The daemon does the authoritative
// derivation server-side at poll time; this is purely so a settings card can
// show which repo intake will actually poll.
export function deriveRepoPath(remote?: string): string | undefined {
	const trimmed = remote?.trim();
	if (!trimmed) return undefined;
	let path: string | undefined;
	if (trimmed.startsWith("git@")) {
		path = trimmed.split(":")[1];
	} else {
		try {
			path = new URL(trimmed).pathname;
		} catch {
			path = trimmed;
		}
	}
	if (!path) return undefined;
	const parts = path
		.replace(/\.git$/, "")
		.replace(/^\/+|\/+$/g, "")
		.split("/");
	if (parts.length < 2) return undefined;
	// Keep all segments: GitHub uses owner/repo (2 parts), GitLab uses the full
	// namespace path (group/subgroup/repo).
	return parts.join("/");
}

// deriveRepoHost extracts the host from a git origin URL for building repo
// links. Returns undefined for SSH URLs without a parseable host.
export function deriveRepoHost(remote?: string): string | undefined {
	const trimmed = remote?.trim();
	if (!trimmed) return undefined;
	if (trimmed.startsWith("git@")) {
		const afterAt = trimmed.split("@")[1];
		return afterAt?.split(":")[0];
	}
	try {
		return new URL(trimmed).host || undefined;
	} catch {
		return undefined;
	}
}

// IntakeFields renders the shared "Tracker intake" controls: an enable checkbox
// that reveals the eligibility inputs. It is deliberately card-agnostic (no
// <Card> wrapper) so the create sheet and the settings form can frame it
// however they like.
//
// repoPreview is only meaningful once a project exists and its git origin is
// known: pass `{ value, host }` from settings to render the repo link
// row, and omit it from the create sheet (the origin URL isn't available there,
// and the daemon derives the repo regardless).
export function IntakeFields({
	form,
	onChange,
	repoPreview,
	compact = false,
	controlClassName,
	labelClassName,
	variant = "default",
}: {
	form: IntakeForm;
	onChange: (patch: Partial<IntakeForm>) => void;
	repoPreview?: { value?: string; host?: string };
	// compact drops the descriptive/help prose and folds the explanation into an
	// info-icon tooltip — used by the create-project sheet, which stays minimal.
	compact?: boolean;
	controlClassName?: string;
	labelClassName?: string;
	variant?: "default" | "settings";
}) {
	const { t } = useTranslation();
	const needsRule = intakeNeedsRule(form);
	if (variant === "settings") {
		return (
			<IntakeSettingsFields form={form} onChange={onChange} repoPreview={repoPreview} />
		);
	}
	return (
		<div className="flex flex-col gap-4">
			{!compact && (
				<p className="text-xs leading-row text-muted-foreground">
						{t("settings.project.intakeDescription")}
				</p>
			)}
			<div className={cn("flex items-center", compact ? "justify-between gap-3" : "gap-2")}>
				{compact ? (
					<>
						<label htmlFor="intakeEnabled" className="text-control text-foreground">
							{t("createProject.workOnAssignedIssues")}
						</label>
						<Switch
							id="intakeEnabled"
							aria-label={t("createProject.workOnAssignedIssues")}
							checked={form.enabled}
							onCheckedChange={(enabled) => onChange({ enabled })}
						/>
					</>
				) : (
					<label className="flex items-center gap-2.5 text-control text-foreground">
						<input
							type="checkbox"
							className="size-icon-base accent-accent"
							checked={form.enabled}
							onChange={(e) => onChange({ enabled: e.target.checked })}
						/>
						{t("settings.project.enableIssueIntake")}
					</label>
				)}
			</div>
			{form.enabled && (
				<>
					{repoPreview && (
						<IntakeField label={t("settings.project.repository")} labelClassName={labelClassName}>
							{repoPreview.value ? (
								<AppLink
									href={`https://${repoPreview.host ?? "github.com"}/${repoPreview.value}`}
									target="_blank"
									rel="noopener noreferrer"
									className="text-control text-accent hover:underline"
								>
									{repoPreview.value}
								</AppLink>
							) : (
								<span className="text-control text-muted-foreground">
									{t("settings.project.repoNotDetected")}
								</span>
							)}
						</IntakeField>
					)}
					<IntakeField label={t("settings.project.assignee")} htmlFor="intakeAssignee" labelClassName={labelClassName}>
						<input
							id="intakeAssignee"
							className={cn(
								"h-control-form w-full rounded-md border border-input bg-transparent px-2.5 text-control text-foreground placeholder:text-passive focus-visible:border-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent-weak",
								controlClassName,
							)}
							value={form.assignee}
							onChange={(e) => onChange({ assignee: e.target.value })}
							placeholder={t("settings.project.intakeAssigneePlaceholder")}
						/>
					</IntakeField>
					{!compact && needsRule && <IntakeAssigneeError />}
				</>
			)}
		</div>
	);
}

function IntakeSettingsFields({
	form,
	onChange,
	repoPreview,
}: {
	form: IntakeForm;
	onChange: (patch: Partial<IntakeForm>) => void;
	repoPreview?: { value?: string; host?: string };
}) {
	const { t } = useTranslation();
	const [assigneeEditing, setAssigneeEditing] = useState(false);
	const inputRef = useRef<HTMLInputElement>(null);
	const trimmedAssignee = form.assignee.trim();
	const hasAssignee = trimmedAssignee.length > 0;

	useEffect(() => {
		if (!assigneeEditing) return;
		const input = inputRef.current;
		if (!input) return;
		input.focus();
		input.select();
	}, [assigneeEditing]);

	const finishAssigneeEditing = () => {
		setAssigneeEditing(false);
	};

	const onAssigneeKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
		if (event.key === "Enter") {
			event.preventDefault();
			finishAssigneeEditing();
			return;
		}
		if (event.key === "Escape") {
			event.preventDefault();
			event.stopPropagation();
			setAssigneeEditing(false);
		}
	};

	return (
		<div className="flex flex-col gap-1.5">
			<SettingsRow
				description={t("settings.project.intakeDescription")}
				label={t("settings.project.enableIssueIntake")}
			>
				<Switch
					aria-label={t("settings.project.enableIssueIntake")}
					checked={form.enabled}
					onCheckedChange={(enabled) => {
						onChange({ enabled });
						if (!enabled) setAssigneeEditing(false);
					}}
				/>
			</SettingsRow>
			{form.enabled && (
				<>
					{repoPreview && (
						<SettingsRow label={t("settings.project.repository")}>
							{repoPreview.value ? (
								<AppLink
									href={`https://${repoPreview.host ?? "github.com"}/${repoPreview.value}`}
									target="_blank"
									rel="noopener noreferrer"
									className="settings-row-value text-settings-accent hover:underline"
								>
									{repoPreview.value}
								</AppLink>
							) : (
								<span className="settings-row-value">{t("settings.project.repoNotDetected")}</span>
							)}
						</SettingsRow>
					)}
					<SettingsRow label={t("settings.project.assignee")}>
						{assigneeEditing ? (
							<input
								ref={inputRef}
								id="intakeAssignee"
								aria-label={t("settings.project.assignee")}
								className="settings-inline-edit-input w-full max-w-md"
								value={form.assignee}
								onChange={(event) => onChange({ assignee: event.target.value })}
								onBlur={finishAssigneeEditing}
								onKeyDown={onAssigneeKeyDown}
								placeholder={t("settings.project.intakeAssigneePlaceholder")}
							/>
						) : hasAssignee ? (
							<div className="flex min-w-0 flex-1 items-center justify-end gap-2">
								<span className="settings-row-value truncate" title={trimmedAssignee}>
									{trimmedAssignee}
								</span>
								<Button
									type="button"
									size="sm"
									variant="outline"
									className="shrink-0"
									aria-label={t("settings.field.edit", { label: t("settings.project.assignee") })}
									onClick={() => setAssigneeEditing(true)}
								>
									<Pencil className="size-3.5" aria-hidden="true" />
									{t("settings.project.editAssignee")}
								</Button>
							</div>
						) : (
							<Button type="button" size="sm" variant="outline" onClick={() => setAssigneeEditing(true)}>
								<Plus className="size-3.5" aria-hidden="true" />
								{t("settings.project.addAssignee")}
							</Button>
						)}
					</SettingsRow>
					{!hasAssignee && !assigneeEditing && (
						<p className="px-1 text-pretty text-xs leading-4 text-settings-muted">{t("settings.project.intakeAssigneeHint")}</p>
					)}
				</>
			)}
		</div>
	);
}

function IntakeAssigneeError() {
	const { t } = useTranslation();
	return (
		<p className="flex items-center gap-1.5 px-1 text-xs leading-row text-error">
			<TriangleAlert className="size-3 shrink-0 text-error" aria-hidden="true" />
			{t("settings.project.intakeAssigneeRequired")}
		</p>
	);
}

function IntakeField({
	label,
	htmlFor,
	labelClassName,
	children,
}: {
	label: string;
	htmlFor?: string;
	labelClassName?: string;
	children: React.ReactNode;
}) {
	return (
		<div className="flex flex-col gap-1.5">
			<Label htmlFor={htmlFor} className={cn("text-xs text-muted-foreground", labelClassName)}>
				{label}
			</Label>
			{children}
		</div>
	);
}
