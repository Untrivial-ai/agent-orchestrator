import { useId, useMemo, useState, type FormEvent, type ReactNode } from "react";
import { ExternalLink, Loader2 } from "lucide-react";
import { aoBridge } from "../../lib/bridge";
import { cn } from "../../lib/utils";
import type { ConversationActivity } from "../../types/conversation";
import { ACCENT_ACTION_PILL, QUIET_ACTION_PILL } from "./action-pill";

type InputAction = "accept" | "decline" | "cancel";
type InputValue = string | number | boolean | string[];
type PropertyEntry = [string, Record<string, unknown>];

/**
 * A pending question docks above the composer rather than landing in the
 * transcript: it is something to answer now, not something to read back. The
 * chrome is the queued-message dock's, so the two things that can sit on the
 * composer read as one surface, and the footer is the approval card's, because
 * both are a decision the turn is waiting on.
 */
export function ElicitationDock({
	activity,
	onResolve,
}: {
	activity: ConversationActivity;
	onResolve?: (
		requestId: string,
		action: InputAction,
		content?: Record<string, unknown>,
	) => Promise<unknown> | void;
}) {
	const requestId = activity.requestId;
	const unavailable = !requestId || !onResolve;
	const [submitting, setSubmitting] = useState(false);
	const [error, setError] = useState<string>();

	async function resolve(action: InputAction, content?: Record<string, unknown>) {
		if (!requestId || !onResolve || submitting) return;
		setSubmitting(true);
		setError(undefined);
		try {
			await onResolve(requestId, action, content);
		} catch (reason) {
			setError(reason instanceof Error ? reason.message : "The answer could not be sent.");
		} finally {
			setSubmitting(false);
		}
	}

	return (
		<div
			role="group"
			aria-label="Agent question"
			data-testid="elicitation-dock"
			className="elicitation-dock overflow-hidden rounded-[var(--radius-chat-composer)] border border-border-strong bg-surface shadow-sm"
		>
			{activity.detail?.inputMode === "url" ? (
				<URLRequest activity={activity} disabled={submitting || unavailable} onResolve={resolve} />
			) : (
				<FormRequest activity={activity} disabled={submitting || unavailable} onResolve={resolve} />
			)}

			{error ? (
				<p role="alert" className="px-3 pb-2 text-[11px] leading-snug text-destructive">
					{error}
				</p>
			) : null}
		</div>
	);
}

/** The dock's one-line header: what is being asked, and where you are in it. */
function DockHeader({ id, title, pager }: { id?: string; title: string; pager?: string }) {
	return (
		<div className="flex min-h-8 items-center gap-2 px-3 py-2">
			<p id={id} className="min-w-0 flex-1 text-xs font-medium leading-relaxed text-foreground line-clamp-2" title={title}>
				{title}
			</p>
			{pager ? (
				<span className="shrink-0 text-[11px] tabular-nums text-muted-foreground">{pager}</span>
			) : null}
		</div>
	);
}

function DockFooter({ children }: { children: ReactNode }) {
	return <div className="flex flex-wrap items-center justify-between gap-1.5 px-3 pb-2.5 pt-1">{children}</div>;
}

function URLRequest({
	activity,
	disabled,
	onResolve,
}: {
	activity: ConversationActivity;
	disabled: boolean;
	onResolve: (action: InputAction, content?: Record<string, unknown>) => Promise<void>;
}) {
	const rawURL = activity.detail?.url ?? "";
	const parsed = safeExternalURL(rawURL);
	const [opening, setOpening] = useState(false);
	const [openError, setOpenError] = useState<string>();

	async function consent() {
		if (!parsed || opening) return;
		setOpening(true);
		setOpenError(undefined);
		try {
			// Opening is the consented action. Tell the provider only after Electron
			// accepted it, so an OS-level refusal is never reported as success.
			await aoBridge.app.openExternal(parsed.href);
			await onResolve("accept");
		} catch {
			setOpenError("The link could not be opened. Nothing was approved.");
		} finally {
			setOpening(false);
		}
	}

	return (
		<>
			<DockHeader title={activity.detail?.message || activity.summary} />
			{parsed ? (
				<div className="flex min-h-10 min-w-0 items-center gap-2.5 px-3 py-2">
					<div className="min-w-0 flex-1">
						<p className="truncate text-xs leading-relaxed text-foreground">{parsed.hostname}</p>
						<p className="mt-0.5 break-all font-mono text-[11px] leading-relaxed text-muted-foreground">
							{parsed.href}
						</p>
					</div>
				</div>
			) : (
				<p role="alert" className="px-3 py-2 text-[11px] leading-snug text-destructive">
					The provider supplied an unsafe or invalid URL. It was not opened.
				</p>
			)}
			{openError ? (
				<p role="alert" className="px-3 pb-1 text-[11px] leading-snug text-destructive">
					{openError}
				</p>
			) : null}
			<DockFooter>
				<div className="flex items-center gap-1.5">
					<button type="button" className={QUIET_ACTION_PILL} disabled={disabled} onClick={() => onResolve("cancel")}>
						Cancel
					</button>
					<button type="button" className={QUIET_ACTION_PILL} disabled={disabled} onClick={() => onResolve("decline")}>
						Decline
					</button>
				</div>
				<button
					type="button"
					className={ACCENT_ACTION_PILL}
					disabled={disabled || !parsed}
					onClick={() => void consent()}
				>
					{opening ? (
						<Loader2 aria-hidden="true" className="size-3.5 animate-spin" />
					) : (
						<ExternalLink aria-hidden="true" className="size-3.5" />
					)}
					Open {parsed?.hostname ?? "link"}
				</button>
			</DockFooter>
		</>
	);
}

function FormRequest({
	activity,
	disabled,
	onResolve,
}: {
	activity: ConversationActivity;
	disabled: boolean;
	onResolve: (action: InputAction, content?: Record<string, unknown>) => Promise<void>;
}) {
	const schema = activity.detail?.schema;
	const properties = useMemo(() => Object.entries(schema?.properties ?? {}), [schema?.properties]);
	const questionGroups = useMemo(() => claudeQuestionGroups(properties), [properties]);
	const required = useMemo(() => new Set(schema?.required ?? []), [schema?.required]);
	const [values, setValues] = useState<Record<string, InputValue>>(() => initialValues(properties));
	const [missing, setMissing] = useState<Set<string>>(new Set());
	const [activeQuestion, setActiveQuestion] = useState(0);
	const visibleProperties = questionGroups?.[activeQuestion] ?? properties;
	const hasPreviousQuestion = questionGroups !== undefined && activeQuestion > 0;
	const hasNextQuestion = questionGroups !== undefined && activeQuestion < questionGroups.length - 1;
	const headerId = useId();

	// A Claude question carries its own prompt, so the header asks it and the
	// field below drops the legend that would otherwise repeat it verbatim.
	const askedQuestion = questionGroups ? propertyLabel(visibleProperties[0]) : undefined;
	const title = askedQuestion ?? activity.detail?.message ?? schema?.title ?? activity.summary;
	const pager =
		questionGroups && questionGroups.length > 1
			? `${activeQuestion + 1} of ${questionGroups.length}`
			: undefined;

	function submit(event: FormEvent) {
		event.preventDefault();
		const absent = new Set(
			visibleProperties
				.filter(([name]) => required.has(name) && isEmpty(values[name]))
				.map(([name]) => name),
		);
		setMissing(absent);
		if (absent.size > 0) return;
		if (hasNextQuestion) {
			setActiveQuestion((current) => current + 1);
			return;
		}
		void onResolve("accept", values);
	}

	return (
		<form onSubmit={submit}>
			<DockHeader id={headerId} title={title} pager={pager} />
			{!questionGroups && schema?.description ? (
				<p className="px-3 pb-1 text-[11px] leading-relaxed text-muted-foreground">{schema.description}</p>
			) : null}
			<div className={cn(questionGroups ? "flex flex-col" : "flex flex-col gap-3 px-3 pb-1")}>
				{visibleProperties.map(([name, property], index) => (
					<FormField
						key={name}
						name={name}
						property={property}
						value={values[name]}
						required={required.has(name)}
						invalid={missing.has(name)}
						disabled={disabled}
						// Inside a Claude question the header is the prompt, so the first
						// field borrows it as its accessible name instead of printing it.
						labelledBy={questionGroups && index === 0 ? headerId : undefined}
						rows={Boolean(questionGroups)}
						onChange={(value) => {
							setValues((current) => ({ ...current, [name]: value }));
							setMissing((current) => {
								if (!current.has(name)) return current;
								const next = new Set(current);
								next.delete(name);
								return next;
							});
						}}
					/>
				))}
			</div>
			<DockFooter>
				<div className="flex items-center gap-1.5">
					<button type="button" className={QUIET_ACTION_PILL} disabled={disabled} onClick={() => onResolve("cancel")}>
						Cancel
					</button>
					<button type="button" className={QUIET_ACTION_PILL} disabled={disabled} onClick={() => onResolve("decline")}>
						Skip
					</button>
				</div>
				<div className="flex items-center gap-1.5">
					{hasPreviousQuestion ? (
						<button
							type="button"
							className={QUIET_ACTION_PILL}
							disabled={disabled}
							onClick={() => {
								setMissing(new Set());
								setActiveQuestion((current) => current - 1);
							}}
						>
							Back
						</button>
					) : null}
					<button type="submit" className={cn(ACCENT_ACTION_PILL, "min-w-20 justify-center")} disabled={disabled}>
						{disabled ? (
							<Loader2 aria-label="Sending answer" className="size-3.5 animate-spin" />
						) : hasNextQuestion ? (
							"Next"
						) : (
							"Continue"
						)}
					</button>
				</div>
			</DockFooter>
		</form>
	);
}

function FormField({
	name,
	property,
	value,
	required,
	invalid,
	disabled,
	labelledBy,
	rows,
	onChange,
}: {
	name: string;
	property: Record<string, unknown>;
	value: InputValue | undefined;
	required: boolean;
	invalid: boolean;
	disabled: boolean;
	/** Use the dock header as this field's name instead of drawing a legend. */
	labelledBy?: string;
	/** Draw choices and free text as dock rows rather than a labelled form field. */
	rows?: boolean;
	onChange: (value: InputValue) => void;
}) {
	const label = propertyLabel([name, property]);
	const description = typeof property.description === "string" ? property.description : undefined;
	const id = `elicitation-${name}`;
	const errorId = `${id}-error`;
	const options = enumOptions(property);
	const multi = property.type === "array";

	if (options.length > 0) {
		return (
			<fieldset
				className="min-w-0"
				disabled={disabled}
				aria-required={required || undefined}
				aria-invalid={invalid || undefined}
				aria-labelledby={labelledBy}
				aria-describedby={invalid ? errorId : undefined}
			>
				{labelledBy ? null : (
					<legend className="px-3 pb-1 text-[11px] font-medium text-muted-foreground">
						{label}
						{required ? " *" : ""}
					</legend>
				)}
				{description && !labelledBy ? (
					<p className="px-3 pb-1 text-[11px] leading-relaxed text-muted-foreground">{description}</p>
				) : null}
				<div className={cn(rows ? "flex flex-col" : "flex flex-col px-1")}>
					{options.map((option) => {
						const checked = multi
							? Array.isArray(value) && value.includes(option.value)
							: value === option.value;
						return (
							<label
								key={option.value}
								className={cn(
									"flex min-h-10 min-w-0 cursor-pointer items-center gap-2.5 px-3 py-2 transition-colors",
									rows ? "" : "rounded-lg",
									checked ? "bg-logo-accent/[0.08]" : "hover:bg-interactive-hover",
								)}
							>
								<input
									type={multi ? "checkbox" : "radio"}
									name={name}
									value={option.value}
									checked={checked}
									onChange={() =>
										onChange(multi ? toggleValue(Array.isArray(value) ? value : [], option.value) : option.value)
									}
									className="size-3 shrink-0 accent-[var(--logo-accent)]"
								/>
								<span className="min-w-0 flex-1">
									<span className="block text-xs leading-relaxed text-foreground">{option.label}</span>
									{option.description ? (
										<span className="mt-0.5 block text-[11px] leading-relaxed text-muted-foreground">
											{option.description}
										</span>
									) : null}
								</span>
							</label>
						);
					})}
				</div>
				{invalid ? (
					<p id={errorId} className="px-3 pt-1 text-[11px] text-destructive">
						Choose an answer.
					</p>
				) : null}
			</fieldset>
		);
	}

	if (property.type === "boolean") {
		return (
			<>
				<label
					className={cn(
						"flex min-h-10 min-w-0 cursor-pointer items-center gap-2.5 px-3 py-2 transition-colors hover:bg-interactive-hover",
						rows ? "" : "rounded-lg",
					)}
				>
					<input
						type="checkbox"
						checked={value === true}
						disabled={disabled}
						aria-required={required || undefined}
						aria-invalid={invalid || undefined}
						aria-describedby={invalid ? errorId : undefined}
						onChange={(event) => onChange(event.target.checked)}
						className="size-3 shrink-0 accent-[var(--logo-accent)]"
					/>
					<span className="min-w-0 flex-1">
						<span className="block text-xs leading-relaxed text-foreground">{label}</span>
						{description ? (
							<span className="mt-0.5 block text-[11px] leading-relaxed text-muted-foreground">
								{description}
							</span>
						) : null}
					</span>
				</label>
				{invalid ? (
					<p id={errorId} className={cn("px-3 text-[11px] text-destructive", rows ? "pb-1" : "pt-1")}>
						This field is required.
					</p>
				) : null}
			</>
		);
	}

	const numeric = property.type === "number" || property.type === "integer";
	const control = (
		<input
			id={id}
			type={numeric ? "number" : "text"}
			aria-required={required || undefined}
			min={typeof property.minimum === "number" ? property.minimum : undefined}
			max={typeof property.maximum === "number" ? property.maximum : undefined}
			step={property.type === "integer" ? 1 : undefined}
			minLength={typeof property.minLength === "number" ? property.minLength : undefined}
			maxLength={typeof property.maxLength === "number" ? property.maxLength : undefined}
			value={typeof value === "string" || typeof value === "number" ? value : ""}
			disabled={disabled}
			aria-invalid={invalid || undefined}
			aria-describedby={invalid ? errorId : undefined}
			onChange={(event) => onChange(numeric && event.target.value !== "" ? Number(event.target.value) : event.target.value)}
			className={cn(
				"h-8 w-full min-w-0 rounded-lg border bg-background/40 px-2.5 text-xs leading-relaxed text-foreground outline-none transition-colors focus-visible:border-border-strong",
				rows ? "mt-1" : "mt-1.5",
				invalid ? "border-destructive" : "border-border",
			)}
		/>
	);

	// A Claude "Other" answer is one more way to answer the question above it, so
	// it is one more row, indented into the column the choices keep for their
	// radio. Its title is a quiet visible label above the field rather than a
	// placeholder: DESIGN.md §9 allows a placeholder as an example, never as the
	// only name a field has.
	if (rows) {
		return (
			<>
				<div className="flex min-w-0 items-start gap-2.5 px-3 py-2">
					<span aria-hidden="true" className="size-3 shrink-0" />
					<span className="flex min-w-0 flex-1 flex-col">
						<label htmlFor={id} className="text-[11px] leading-snug text-muted-foreground">
							{label}
							{required ? " *" : ""}
						</label>
						{control}
					</span>
				</div>
				{invalid ? (
					<p id={errorId} className="px-3 pb-1 text-[11px] text-destructive">
						This field is required.
					</p>
				) : null}
			</>
		);
	}

	return (
		<label htmlFor={id} className="block">
			<span className="text-xs font-medium text-foreground">
				{label}
				{required ? " *" : ""}
			</span>
			{description ? (
				<span className="mt-0.5 block text-[11px] leading-relaxed text-muted-foreground">{description}</span>
			) : null}
			{control}
			{invalid ? (
				<span id={errorId} className="mt-1 block text-[11px] text-destructive">
					This field is required.
				</span>
			) : null}
		</label>
	);
}

function propertyLabel([name, property]: PropertyEntry): string {
	return typeof property.title === "string" && property.title ? property.title : humanize(name);
}

function enumOptions(property: Record<string, unknown>): Array<{ value: string; label: string; description?: string }> {
	const source = Array.isArray(property.oneOf)
		? property.oneOf
		: property.type === "array" && isRecord(property.items) && Array.isArray(property.items.anyOf)
			? property.items.anyOf
			: Array.isArray(property.enum)
				? property.enum.map((value) => ({ const: value, title: String(value) }))
				: [];
	return source.flatMap((entry) => {
		if (!isRecord(entry) || typeof entry.const !== "string") return [];
		return [{
			value: entry.const,
			label: typeof entry.title === "string" ? entry.title : entry.const,
			description: typeof entry.description === "string" ? entry.description : undefined,
		}];
	});
}

function claudeQuestionGroups(properties: PropertyEntry[]): PropertyEntry[][] | undefined {
	if (properties.length === 0) return undefined;

	const groups = new Map<number, { question?: PropertyEntry; custom?: PropertyEntry }>();
	for (const entry of properties) {
		const match = /^question_(\d+)(_custom)?$/.exec(entry[0]);
		if (!match) return undefined;
		const index = Number(match[1]);
		const group = groups.get(index) ?? {};
		if (match[2]) {
			group.custom = entry;
		} else {
			group.question = entry;
		}
		groups.set(index, group);
	}

	const ordered: PropertyEntry[][] = [];
	for (const [, group] of [...groups.entries()].sort(([left], [right]) => left - right)) {
		if (!group.question) return undefined;
		ordered.push(group.custom ? [group.question, group.custom] : [group.question]);
	}
	return ordered;
}

function initialValues(properties: PropertyEntry[]): Record<string, InputValue> {
	const values: Record<string, InputValue> = {};
	for (const [name, property] of properties) {
		if (
			typeof property.default === "string" ||
			typeof property.default === "number" ||
			typeof property.default === "boolean"
		) {
			values[name] = property.default;
		} else if (property.type === "array") {
			values[name] = [];
		}
	}
	return values;
}

function safeExternalURL(raw: string): URL | undefined {
	try {
		const url = new URL(raw);
		return url.protocol === "https:" || url.protocol === "http:" ? url : undefined;
	} catch {
		return undefined;
	}
}

function toggleValue(values: string[], value: string): string[] {
	return values.includes(value) ? values.filter((item) => item !== value) : [...values, value];
}

function isEmpty(value: InputValue | undefined): boolean {
	return value === undefined || value === "" || (Array.isArray(value) && value.length === 0);
}

function isRecord(value: unknown): value is Record<string, unknown> {
	return typeof value === "object" && value !== null;
}

function humanize(value: string): string {
	return value.replace(/_/g, " ").replace(/^./, (letter) => letter.toUpperCase());
}
