import { useMemo, useState, type FormEvent } from "react";
import { ExternalLink, HelpCircle, Loader2 } from "lucide-react";
import { aoBridge } from "../../lib/bridge";
import { cn } from "../../lib/utils";
import type { ConversationActivity } from "../../types/conversation";
import { Button } from "../ui/button";

type InputAction = "accept" | "decline" | "cancel";
type InputValue = string | number | boolean | string[];
type PropertyEntry = [string, Record<string, unknown>];

export function ElicitationCard({
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
	const pending = activity.status === "pending";
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
		<section
			aria-label="Agent question"
			className="overflow-hidden rounded-xl border border-logo-accent/25 bg-logo-accent/[0.035]"
		>
			<div className="flex items-start gap-3 px-4 py-3.5">
				<div className="mt-0.5 flex size-7 shrink-0 items-center justify-center rounded-full bg-logo-accent/10 text-logo-accent">
					<HelpCircle aria-hidden="true" className="size-4" />
				</div>
				<div className="min-w-0 flex-1">
					<p className="text-[11px] font-semibold uppercase tracking-[0.12em] text-logo-accent">
						Agent needs your input
					</p>
					<h3 className="mt-1 text-sm font-medium leading-relaxed text-foreground">
						{activity.detail?.message || activity.summary}
					</h3>
				</div>
				{!pending ? (
					<span className="mt-1 text-[11px] font-medium text-muted-foreground">
						{activity.status === "failed" ? "Expired" : "Answered"}
					</span>
				) : null}
			</div>

			{pending && activity.detail?.inputMode === "url" ? (
				<URLRequest activity={activity} disabled={submitting || unavailable} onResolve={resolve} />
			) : pending ? (
				<FormRequest activity={activity} disabled={submitting || unavailable} onResolve={resolve} />
			) : null}

			{error ? (
				<p role="alert" className="border-t border-destructive/20 px-4 py-2 text-xs text-destructive">
					{error}
				</p>
			) : null}
		</section>
	);
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
		<div className="border-t border-logo-accent/15 px-4 py-3">
			{parsed ? (
				<div className="rounded-lg border border-border bg-background/70 px-3 py-2.5">
					<p className="text-[11px] font-medium text-foreground">{parsed.hostname}</p>
					<p className="mt-0.5 break-all font-mono text-[10px] leading-relaxed text-muted-foreground">
						{parsed.href}
					</p>
				</div>
			) : (
				<p role="alert" className="text-xs text-destructive">
					The provider supplied an unsafe or invalid URL. It was not opened.
				</p>
			)}
			{openError ? <p role="alert" className="mt-2 text-xs text-destructive">{openError}</p> : null}
			<div className="mt-3 flex items-center justify-end gap-2">
				<Button type="button" variant="ghost" size="sm" disabled={disabled} onClick={() => onResolve("cancel")}>
					Cancel
				</Button>
				<Button type="button" variant="ghost" size="sm" disabled={disabled} onClick={() => onResolve("decline")}>
					Decline
				</Button>
				<Button type="button" size="sm" disabled={disabled || !parsed} onClick={() => void consent()} className="gap-1.5">
					{opening ? <Loader2 aria-hidden="true" className="size-3.5 animate-spin" /> : <ExternalLink aria-hidden="true" className="size-3.5" />}
					Open {parsed?.hostname ?? "link"}
				</Button>
			</div>
		</div>
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
		<form onSubmit={submit} className="border-t border-logo-accent/15 px-4 pb-3.5 pt-3">
			{schema?.description ? (
				<p className="mb-3 text-xs leading-relaxed text-muted-foreground">{schema.description}</p>
			) : null}
			<div className="flex flex-col gap-3">
				{visibleProperties.map(([name, property]) => (
					<FormField
						key={name}
						name={name}
						property={property}
						value={values[name]}
						required={required.has(name)}
						invalid={missing.has(name)}
						disabled={disabled}
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
			<div className="mt-4 flex items-center justify-between gap-2">
				<div className="flex items-center gap-1">
					<Button type="button" variant="ghost" size="sm" disabled={disabled} onClick={() => onResolve("cancel")}>
						Cancel
					</Button>
					<Button type="button" variant="ghost" size="sm" disabled={disabled} onClick={() => onResolve("decline")}>
						Skip
					</Button>
				</div>
				<div className="flex items-center gap-1">
					{hasPreviousQuestion ? (
						<Button
							type="button"
							variant="ghost"
							size="sm"
							disabled={disabled}
							onClick={() => {
								setMissing(new Set());
								setActiveQuestion((current) => current - 1);
							}}
						>
							Back
						</Button>
					) : null}
					<Button type="submit" size="sm" disabled={disabled} className="min-w-20">
						{disabled ? (
							<Loader2 aria-label="Sending answer" className="size-3.5 animate-spin" />
						) : hasNextQuestion ? (
							"Next"
						) : (
							"Continue"
						)}
					</Button>
				</div>
			</div>
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
	onChange,
}: {
	name: string;
	property: Record<string, unknown>;
	value: InputValue | undefined;
	required: boolean;
	invalid: boolean;
	disabled: boolean;
	onChange: (value: InputValue) => void;
}) {
	const label = typeof property.title === "string" && property.title ? property.title : humanize(name);
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
				aria-describedby={invalid ? errorId : undefined}
			>
				<legend className="text-xs font-medium text-foreground">{label}{required ? " *" : ""}</legend>
				{description ? <p className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">{description}</p> : null}
				<div className="mt-2 grid gap-1.5">
					{options.map((option) => {
						const checked = multi
							? Array.isArray(value) && value.includes(option.value)
							: value === option.value;
						return (
							<label key={option.value} className={cn("flex cursor-pointer gap-2 rounded-lg border px-3 py-2 transition-colors", checked ? "border-logo-accent/45 bg-logo-accent/[0.06]" : "border-border bg-background/50 hover:bg-interactive-hover") }>
								<input
									type={multi ? "checkbox" : "radio"}
									name={name}
									value={option.value}
									checked={checked}
									onChange={() => onChange(multi ? toggleValue(Array.isArray(value) ? value : [], option.value) : option.value)}
									className="mt-0.5 accent-[var(--logo-accent)]"
								/>
								<span className="min-w-0">
									<span className="block text-xs text-foreground">{option.label}</span>
									{option.description ? <span className="mt-0.5 block text-[11px] leading-relaxed text-muted-foreground">{option.description}</span> : null}
								</span>
							</label>
						);
					})}
				</div>
				{invalid ? <p id={errorId} className="mt-1 text-[11px] text-destructive">Choose an answer.</p> : null}
			</fieldset>
		);
	}

	if (property.type === "boolean") {
		return (
			<div>
				<label className="flex items-start gap-2 text-xs text-foreground">
					<input
						type="checkbox"
						checked={value === true}
						disabled={disabled}
						aria-required={required || undefined}
						aria-invalid={invalid || undefined}
						aria-describedby={invalid ? errorId : undefined}
						onChange={(event) => onChange(event.target.checked)}
						className="mt-0.5 accent-[var(--logo-accent)]"
					/>
					<span><span className="font-medium">{label}</span>{description ? <span className="mt-0.5 block text-[11px] text-muted-foreground">{description}</span> : null}</span>
				</label>
				{invalid ? <p id={errorId} className="mt-1 text-[11px] text-destructive">Choose yes or no.</p> : null}
			</div>
		);
	}

	const numeric = property.type === "number" || property.type === "integer";
	return (
		<label htmlFor={id} className="block">
			<span className="text-xs font-medium text-foreground">{label}{required ? " *" : ""}</span>
			{description ? <span className="mt-0.5 block text-[11px] leading-relaxed text-muted-foreground">{description}</span> : null}
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
				className={cn("mt-1.5 h-9 w-full rounded-lg border bg-background px-3 text-xs text-foreground outline-none transition-shadow focus-visible:ring-2 focus-visible:ring-logo-accent/40", invalid ? "border-destructive" : "border-border")}
			/>
			{invalid ? <span id={errorId} className="mt-1 block text-[11px] text-destructive">This field is required.</span> : null}
		</label>
	);
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
