import { useEffect, useId, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { ExternalLink, Loader2 } from "lucide-react";
import { apiErrorMessage } from "../../lib/api-client";
import { aoBridge } from "../../lib/bridge";
import { getChatDraftSourceBoundaryKinds, setChatDraftBoundary } from "../../lib/chat-draft-boundary";
import { useChatDraftTranslation } from "../../lib/chat-draft-messages";
import {
	clearElicitationDraft,
	elicitationDraftKey,
	readElicitationDraft,
	writeElicitationDraft,
	type ElicitationDraft,
	type ElicitationDraftValue,
} from "../../lib/elicitation-drafts";
import { sameContent } from "../../lib/stable-list";
import { cn } from "../../lib/utils";
import type { ConversationActivity } from "../../types/conversation";
import { ACCENT_ACTION_PILL, QUIET_ACTION_PILL } from "./action-pill";

type InputAction = "accept" | "decline" | "cancel";
type InputValue = ElicitationDraftValue;
type PropertyEntry = [string, Record<string, unknown>];
type ElicitationDraftKey = { conversationId: string; requestId: string };

/** Delay before the first retry of a failed draft write; doubles on each further failure, up to the ceiling below. */
const ELICITATION_DRAFT_RETRY_BASE_MS = 3000;
const ELICITATION_DRAFT_RETRY_MAX_MS = 60_000;

/** One leave/quit-guard slot per pending request, so two questions on one session don't clear each other's warning. */
export function elicitationBoundarySource(requestId: string) {
	return `elicitation:${requestId}` as const;
}

/**
 * The most recent answer a write attempt failed to persist, kept in memory
 * only. A dock unmounting and remounting for an unrelated reason (a queued
 * edit starting or ending, closing the reviewer overlay) reads storage before
 * its predecessor's own unmount effect gets a chance to write — React commits
 * the new instance's render before running the old one's cleanup — so a fresh
 * instance restoring from storage alone could show an answer already
 * superseded by one that never made it to disk. This is read first.
 */
const unsavedElicitationDrafts = new Map<string, ElicitationDraft>();

function rememberUnsavedElicitationDraft(conversationId: string, requestId: string, draft: ElicitationDraft): void {
	unsavedElicitationDrafts.set(elicitationDraftKey(conversationId, requestId), draft);
}

function forgetUnsavedElicitationDraft(conversationId: string, requestId: string): void {
	unsavedElicitationDrafts.delete(elicitationDraftKey(conversationId, requestId));
}

function peekUnsavedElicitationDraft(conversationId: string, requestId: string): ElicitationDraft | undefined {
	return unsavedElicitationDrafts.get(elicitationDraftKey(conversationId, requestId));
}

/**
 * Drops the remembered answer for every request on `conversationId` that
 * `isGone` says is no longer open, and returns those request ids. Without this
 * an answer that never reached disk would stay in memory until the app quits
 * whenever its question was answered elsewhere, timed out, or was stopped.
 */
export function forgetUnsavedElicitationDraftsFor(
	conversationId: string,
	isGone: (requestId: string) => boolean,
): string[] {
	const prefix = elicitationDraftKey(conversationId, "");
	const forgotten: string[] = [];
	for (const key of unsavedElicitationDrafts.keys()) {
		if (!key.startsWith(prefix)) continue;
		const requestId = key.slice(prefix.length);
		if (!isGone(requestId)) continue;
		unsavedElicitationDrafts.delete(key);
		forgotten.push(requestId);
	}
	return forgotten;
}

/** Test seam: drops every draft this renderer remembers failed to save. */
export function resetUnsavedElicitationDraftMemory(): void {
	unsavedElicitationDrafts.clear();
}

/** Keeps a restored question index inside the bounds of the current question set. */
function clampActiveQuestion(index: number, questionGroups: PropertyEntry[][] | undefined): number {
	if (!questionGroups || questionGroups.length === 0) return 0;
	if (!Number.isFinite(index)) return 0;
	// A corrupted or hand-edited draft can carry a non-integer index; only a
	// storage bug reaches this, but truncating keeps it from picking a
	// questionGroups slot that doesn't exist.
	return Math.min(Math.max(Math.trunc(index), 0), questionGroups.length - 1);
}

/**
 * A pending question docks above the composer rather than landing in the
 * transcript: it is something to answer now, not something to read back. The
 * chrome is the queued-message dock's, so the two things that can sit on the
 * composer read as one surface, and the footer is the approval card's, because
 * both are a decision the turn is waiting on.
 */
export function ElicitationDock({
	activity,
	sessionId,
	conversationId,
	onResolve,
	initialError,
	earlierAnswerFailed,
}: {
	activity: ConversationActivity;
	/**
	 * Scopes the "you have unsaved work" leave/quit warning, the same way the
	 * Chat composer's own draft does — this is the session the human would be
	 * navigating away from, regardless of which conversation is open on it.
	 */
	sessionId?: string;
	/**
	 * Scopes the persisted answer itself. The daemon identifies a pending
	 * input by `(conversation_id, request_id)`, and a reviewer-chat overlay can
	 * report the same `sessionId` as its underlying worker chat while reading
	 * a different conversation, so the draft has to key on conversation, not
	 * session, to land back on the right question.
	 */
	conversationId?: string;
	onResolve?: (
		requestId: string,
		action: InputAction,
		content?: Record<string, unknown>,
	) => Promise<unknown> | void;
	/** A send for this question that failed while another question was shown. */
	initialError?: string;
	/** A send for a different, still-open question failed; it comes back after this one. */
	earlierAnswerFailed?: boolean;
}) {
	const requestId = activity.requestId;
	const unavailable = !requestId || !onResolve;
	const [submitting, setSubmitting] = useState(false);
	const [error, setError] = useState<string | undefined>(initialError);

	async function resolve(action: InputAction, content?: Record<string, unknown>) {
		if (!requestId || !onResolve || submitting) return;
		setSubmitting(true);
		setError(undefined);
		try {
			await onResolve(requestId, action, content);
			if (conversationId) {
				clearElicitationDraft(conversationId, requestId);
				forgetUnsavedElicitationDraft(conversationId, requestId);
			}
			if (sessionId) setChatDraftBoundary(sessionId, elicitationBoundarySource(requestId), undefined);
			// Leave the form disabled on success rather than resetting `submitting`
			// here: `onResolve`'s conversation refetch is fire-and-forget, so this
			// question can still be on screen for a beat after it resolves. A
			// re-enabled form invites a stray edit that would recreate the draft
			// just cleared above.
		} catch (reason) {
			setError(apiErrorMessage(reason, "The answer could not be sent."));
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
				// Keyed by request: a caller that reuses one ElicitationDock element
				// across requests (this component's own tests do; ChatWorkspace's own
				// outer key is a separate guarantee, not a substitute for this one)
				// otherwise keeps FormRequest's state — a typed answer, in particular
				// — across an unrelated question.
				<FormRequest
					key={requestId ?? activity.id}
					activity={activity}
					sessionId={sessionId}
					draftKey={conversationId && requestId ? { conversationId, requestId } : undefined}
					disabled={submitting || unavailable}
					onResolve={resolve}
				/>
			)}

			{error ? (
				<p role="alert" className="px-3 pb-2 text-[11px] leading-snug text-destructive">
					{error}
				</p>
			) : null}
			{earlierAnswerFailed ? (
				<p role="alert" className="px-3 pb-2 text-[11px] leading-snug text-destructive">
					Your answer to an earlier question couldn’t be sent. It will come back after this one.
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
	sessionId,
	draftKey,
	disabled,
	onResolve,
}: {
	activity: ConversationActivity;
	sessionId?: string;
	draftKey?: ElicitationDraftKey;
	disabled: boolean;
	onResolve: (action: InputAction, content?: Record<string, unknown>) => Promise<void>;
}) {
	const schema = activity.detail?.schema;
	const properties = useMemo(() => Object.entries(schema?.properties ?? {}), [schema?.properties]);
	const questionGroups = useMemo(() => claudeQuestionGroups(properties), [properties]);
	const required = useMemo(() => new Set(schema?.required ?? []), [schema?.required]);

	// A lazy initializer runs once per mount, before any effect — including a
	// just-unmounted predecessor's own last-chance write below, which commits
	// after this render. The in-memory cache is checked first for that reason:
	// it can hold a newer, not-yet-persisted answer that storage doesn't have
	// yet.
	const [draft] = useState(() => {
		if (!draftKey) return undefined;
		return (
			peekUnsavedElicitationDraft(draftKey.conversationId, draftKey.requestId) ??
			readElicitationDraft(draftKey.conversationId, draftKey.requestId)
		);
	});
	const [values, setValues] = useState<Record<string, InputValue>>(() =>
		restoreValues(initialValues(properties), draft?.values, properties),
	);
	const [missing, setMissing] = useState<Set<string>>(new Set());
	// A restored question index is clamped to the current question set: the
	// index came from storage, and an out-of-range value would otherwise fall
	// back to showing every field at once instead of the step-by-step flow.
	const [activeQuestion, setActiveQuestion] = useState(() =>
		clampActiveQuestion(draft?.activeQuestion ?? 0, questionGroups),
	);
	const translateDraft = useChatDraftTranslation();
	// Seeded from the shared boundary rather than always false: a remount that
	// restored from the in-memory cache above is picking up a still-failing
	// answer, and needs to know that immediately rather than treating the
	// restored content as already saved.
	const initiallyFailing = Boolean(
		draftKey && sessionId &&
			getChatDraftSourceBoundaryKinds(sessionId, elicitationBoundarySource(draftKey.requestId)).length > 0,
	);
	const [writeError, setWriteError] = useState<string | undefined>(
		initiallyFailing ? "chat.draft.elicitationSaveFailed" : undefined,
	);
	// What was last *successfully* written, so a write only happens when
	// something actually changed. Comparing against the initial state instead
	// would miss a real change that happens to loop back to it: restore at
	// question 0, go Next, then Back lands back on question 0, which looks
	// unchanged from the start but must still overwrite the draft Next saved
	// at question 1.
	const lastWritten = useRef({ values, activeQuestion });
	// True from a failed write until the next one that succeeds. Content
	// matching `lastWritten` normally means there's nothing to do, but not
	// while this is true — an answer can fail, then get edited back to exactly
	// what was last saved, and that revert still needs to clear the failure.
	const hasPendingFailure = useRef(initiallyFailing);
	// How many failures in a row, for the backoff below; and a tick bumped
	// when a scheduled retry fires, to re-run the write effect even though
	// nothing else changed — a ref alone can't do that, since writing the same
	// value back to it doesn't trigger a re-render.
	const consecutiveFailures = useRef(0);
	const [retryTick, setRetryTick] = useState(0);

	// The single source of truth for persisting this answer. Depending on
	// `disabled` and `retryTick` means every reason to write or retry — an
	// edit, the form disabling, re-enabling after a rejected resolve, or the
	// backoff timer — is just another run of this effect, and its own cleanup
	// cancels a stale retry without a hand-tracked timer ref.
	useEffect(() => {
		if (!draftKey || disabled) return;
		const unchanged = sameContent(lastWritten.current, { values, activeQuestion });
		if (unchanged && !hasPendingFailure.current) return;
		const result = writeElicitationDraft(draftKey.conversationId, draftKey.requestId, { values, activeQuestion });
		if (sessionId) {
			setChatDraftBoundary(
				sessionId,
				elicitationBoundarySource(draftKey.requestId),
				result.ok ? undefined : "elicitation-persistence-failed",
			);
		}
		if (result.ok) {
			lastWritten.current = { values, activeQuestion };
			hasPendingFailure.current = false;
			consecutiveFailures.current = 0;
			forgetUnsavedElicitationDraft(draftKey.conversationId, draftKey.requestId);
			setWriteError((current) => (current === undefined ? current : undefined));
			return;
		}
		hasPendingFailure.current = true;
		rememberUnsavedElicitationDraft(draftKey.conversationId, draftKey.requestId, { values, activeQuestion });
		setWriteError((current) => (current === "chat.draft.elicitationSaveFailed" ? current : "chat.draft.elicitationSaveFailed"));
		consecutiveFailures.current += 1;
		const delay = Math.min(
			ELICITATION_DRAFT_RETRY_BASE_MS * 2 ** (consecutiveFailures.current - 1),
			ELICITATION_DRAFT_RETRY_MAX_MS,
		);
		const timer = setTimeout(() => setRetryTick((tick) => tick + 1), delay);
		return () => clearTimeout(timer);
	}, [draftKey?.conversationId, draftKey?.requestId, sessionId, values, activeQuestion, disabled, retryTick]);

	// Keeps the latest answer reachable from the unmount-only effect below,
	// whose own closure would otherwise still hold whatever values existed at
	// its last run.
	const latest = useRef({ draftKey, values, activeQuestion });
	useEffect(() => {
		latest.current = { draftKey, values, activeQuestion };
	});

	useEffect(
		() => () => {
			// The leave/quit guards only cover navigating away or quitting; other
			// unmounts (ending a queued-message edit, closing the reviewer
			// overlay, a refetch error swapping the view) don't. If whatever
			// caused the failure has cleared by now, this saves the answer before
			// the warning goes away; if it hasn't, the warning stays.
			const { draftKey: key, values: finalValues, activeQuestion: finalActiveQuestion } = latest.current;
			if (!key) return;
			// The cache entry, not `hasPendingFailure`, is the gate: a successful
			// resolve forgets the entry but never resets this instance's ref, and
			// writing here after that would resurrect a draft for an answered question.
			if (!hasPendingFailure.current || !peekUnsavedElicitationDraft(key.conversationId, key.requestId)) return;
			const result = writeElicitationDraft(key.conversationId, key.requestId, {
				values: finalValues,
				activeQuestion: finalActiveQuestion,
			});
			if (result.ok) forgetUnsavedElicitationDraft(key.conversationId, key.requestId);
			if (sessionId) {
				setChatDraftBoundary(
					sessionId,
					elicitationBoundarySource(key.requestId),
					result.ok ? undefined : "elicitation-persistence-failed",
				);
			}
		},
		[sessionId],
	);
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
			{writeError && !disabled ? (
				// Hidden rather than cleared while disabled: a resolve in flight (or
				// just delivered) makes this stale either way, but the write effect
				// bails out entirely while disabled, so nothing else would clear the
				// underlying state — and it's still correct to show again if a
				// rejected resolve re-enables the form and the save is still failing.
				<p role="alert" className="px-3 pb-1 text-[11px] leading-snug text-destructive">
					{translateDraft(writeError)}
				</p>
			) : null}
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

/** Keeps a saved answer only for fields the current schema still declares. */
function restoreValues(
	defaults: Record<string, InputValue>,
	saved: Record<string, InputValue> | undefined,
	properties: PropertyEntry[],
): Record<string, InputValue> {
	if (!saved) return defaults;
	const known = new Set(properties.map(([name]) => name));
	const values = { ...defaults };
	for (const [name, value] of Object.entries(saved)) {
		if (known.has(name)) values[name] = value;
	}
	return values;
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
