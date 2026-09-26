import { AlertTriangle, Check, LoaderCircle, RotateCw, Send } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent } from "react";
import { useTranslation } from "react-i18next";
import type { WorkspaceSession } from "../types/workspace";
import {
	createCloudPendingSession,
	queueCloudPendingMessage,
	retryCloudPendingMessage,
	type CloudPendingMessage,
	type CloudPendingSession as PendingSession,
} from "../lib/cloud-pending-session";
import {
	useCloudStartupProgress,
	type CloudStartupPhaseId,
} from "../lib/cloud-startup-progress";
import { useCloudCp } from "../hooks/useCloudCp";
import { Button } from "./ui/button";
import { cn } from "../lib/utils";

type CloudPendingSessionProps = {
	attempt: PendingSession;
	session?: WorkspaceSession;
};

function rendererNow(): number {
	return globalThis.performance?.now() ?? Date.now();
}

function useElapsedMs(startedAtMs: number): number {
	const [elapsedMs, setElapsedMs] = useState(() => Math.max(0, rendererNow() - startedAtMs));
	useEffect(() => {
		const update = () => setElapsedMs(Math.max(0, rendererNow() - startedAtMs));
		update();
		const timer = window.setInterval(update, 1_000);
		return () => window.clearInterval(timer);
	}, [startedAtMs]);
	return elapsedMs;
}

function MessageRow({
	attemptId,
	message,
}: {
	attemptId: string;
	message: CloudPendingMessage;
}) {
	const { t } = useTranslation();
	const stateCopy: Record<CloudPendingMessage["state"], string> = {
		saving: t("cloud.pending.state.saving"),
		sending: t("cloud.pending.state.sending"),
		queued: t("cloud.pending.state.queued"),
		failed: t("cloud.pending.state.failed"),
	};
	return (
		<div className="rounded-lg border border-border/80 bg-background/75 px-3 py-2.5" data-testid="pending-message">
			<div className="flex items-start justify-between gap-3">
				<p className="min-w-0 whitespace-pre-wrap break-words text-sm leading-5 text-foreground">
					{message.text}
				</p>
				<div className="flex shrink-0 items-center gap-2 font-mono text-micro text-muted-foreground">
					{message.state === "queued" ? <Check aria-hidden="true" className="size-3.5 text-success" /> : null}
					{message.state === "saving" || message.state === "sending" ? (
						<LoaderCircle aria-hidden="true" className="size-3.5 animate-spin text-primary" />
					) : null}
					<span>{stateCopy[message.state]}</span>
				</div>
			</div>
			{message.state === "failed" ? (
				<div className="mt-2 flex items-center justify-between gap-3 border-t border-border/60 pt-2">
					<p className="min-w-0 truncate text-xs text-destructive" role="alert">{message.error}</p>
					<Button
						type="button"
						size="sm"
						variant="outline"
						onClick={() => retryCloudPendingMessage(attemptId, message.id)}
					>
						<RotateCw aria-hidden="true" />
						{t("cloud.pending.retryMessage")}
					</Button>
				</div>
			) : null}
		</div>
	);
}

export function CloudPendingSession({ attempt, session }: CloudPendingSessionProps) {
	const { t } = useTranslation();
	const { client } = useCloudCp();
	const [draft, setDraft] = useState("");
	const [inputError, setInputError] = useState<string>();
	const [retryError, setRetryError] = useState<string>();
	const [retrying, setRetrying] = useState(false);
	const [retryToken, setRetryToken] = useState(0);
	const textareaRef = useRef<HTMLTextAreaElement>(null);
	const progress = useCloudStartupProgress(attempt, session, retryToken);
	const elapsedMs = useElapsedMs(attempt.startedAtMs);
	const unacknowledgedMessage = attempt.messages.some((message) => message.state !== "queued");
	const canRevealTerminal = progress.phase === "ready" && draft.trim() === "" && !unacknowledgedMessage;
	const startupFailed = progress.phase === "failed" || attempt.createState === "failed";

	useEffect(() => {
		textareaRef.current?.focus({ preventScroll: true });
	}, []);

	const phaseCopy = useMemo<Record<Exclude<CloudStartupPhaseId, "failed">, { detail: string; title: string }>>(
		() => ({
			saving_session: {
				title: t("cloud.pending.phase.savingSession.title"),
				detail: t("cloud.pending.phase.savingSession.detail"),
			},
			allocating_workspace: {
				title: t("cloud.pending.phase.allocatingWorkspace.title"),
				detail: t("cloud.pending.phase.allocatingWorkspace.detail"),
			},
			starting_workspace: {
				title: t("cloud.pending.phase.startingWorkspace.title"),
				detail: t("cloud.pending.phase.startingWorkspace.detail"),
			},
			preparing_repository: {
				title: t("cloud.pending.phase.preparingRepository.title"),
				detail: t("cloud.pending.phase.preparingRepository.detail"),
			},
			starting_agent: {
				title: t("cloud.pending.phase.startingAgent.title"),
				detail: t("cloud.pending.phase.startingAgent.detail"),
			},
			ready: {
				title: t("cloud.pending.phase.ready.title"),
				detail: t("cloud.pending.phase.ready.detail"),
			},
		}),
		[t],
	);
	const copy = useMemo(() => {
		if (progress.phase === "failed") {
			return {
				title: t("cloud.pending.phaseFailed", {
					phase: phaseCopy[progress.failure?.phase ?? "saving_session"].title,
				}),
				detail: progress.failure?.message ?? attempt.createError ?? t("cloud.pending.sessionStartFailed"),
			};
		}
		if (attempt.createState === "failed") {
			return {
				title: t("cloud.pending.phaseFailed", { phase: phaseCopy.saving_session.title }),
				detail: attempt.createError ?? t("cloud.pending.sessionStartFailed"),
			};
		}
		return phaseCopy[progress.phase];
	}, [attempt.createError, phaseCopy, progress.failure, progress.phase, t]);

	if (canRevealTerminal) return null;

	const submit = (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		const text = draft.trim();
		if (!text) return;
		try {
			queueCloudPendingMessage(attempt.attemptId, text);
			setDraft("");
			setInputError(undefined);
		} catch (error) {
			setInputError(error instanceof Error ? error.message : t("cloud.pending.messageSaveFailed"));
		}
	};

	const retry = async () => {
		if (retrying) return;
		setRetrying(true);
		setRetryError(undefined);
		try {
			if (attempt.durableSessionId) {
				await client.resumeSession(attempt.orgId, attempt.durableSessionId);
				setRetryToken((current) => current + 1);
			} else {
				await createCloudPendingSession(attempt.attemptId);
			}
		} catch (error) {
			setRetryError(error instanceof Error ? error.message.slice(0, 240) : t("cloud.pending.retryFailed"));
		} finally {
			setRetrying(false);
			textareaRef.current?.focus({ preventScroll: true });
		}
	};

	const timedStatus = startupFailed
		? undefined
		: elapsedMs >= 45_000
			? t("cloud.pending.takingLonger")
			: elapsedMs >= 20_000
				? t("cloud.pending.stillWorking")
				: undefined;
	const showRetry = startupFailed || elapsedMs >= 90_000;
	const initialState = attempt.createState === "failed"
		? t("cloud.pending.state.failed")
		: attempt.createState === "saving"
			? t("cloud.pending.state.saving")
			: t("cloud.pending.state.queued");

	return (
		<div
			className="absolute inset-0 z-40 flex min-h-0 flex-col bg-background/98 text-foreground backdrop-blur-sm"
			data-testid="cloud-pending-session"
		>
			<div className="mx-auto flex min-h-0 w-full max-w-3xl flex-1 flex-col px-5 pb-5 pt-10 sm:px-8">
				<div className="flex items-start gap-3 border-b border-border/80 pb-5">
					<div
						className={cn(
							"mt-0.5 grid size-8 shrink-0 place-items-center rounded-full border",
							startupFailed
								? "border-destructive/40 bg-destructive/10 text-destructive"
								: "border-primary/35 bg-primary/10 text-primary",
						)}
					>
						{startupFailed ? (
							<AlertTriangle aria-hidden="true" className="size-4" />
						) : (
							<span className="size-2 animate-pulse rounded-full bg-current motion-reduce:animate-none" />
						)}
					</div>
					<div className="min-w-0 flex-1" aria-live="polite">
						<div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
							<h1 className="text-base font-medium">{copy.title}</h1>
							{timedStatus ? <p className="font-mono text-micro uppercase tracking-wide text-muted-foreground">{timedStatus}</p> : null}
						</div>
						<p className="mt-1 text-sm leading-5 text-muted-foreground">{copy.detail}</p>
						{progress.streamError ? <p className="mt-1 text-xs text-muted-foreground">{t("cloud.pending.reconnecting")}</p> : null}
						{showRetry ? (
							<div className="mt-3 flex flex-wrap items-center gap-3">
								<Button type="button" size="sm" variant="outline" disabled={retrying} onClick={() => void retry()}>
									{retrying ? <LoaderCircle aria-hidden="true" className="animate-spin" /> : <RotateCw aria-hidden="true" />}
									{t("cloud.pending.retryStartup")}
								</Button>
								<p className="text-xs text-muted-foreground">{t("cloud.pending.retryKeepsDraft")}</p>
							</div>
						) : null}
						{retryError ? <p className="mt-2 text-xs text-destructive" role="alert">{retryError}</p> : null}
					</div>
				</div>

				<div className="min-h-0 flex-1 space-y-2 overflow-y-auto py-5">
					<div className="rounded-lg border border-primary/25 bg-primary/5 px-3 py-2.5" data-testid="pending-initial-message">
						<div className="flex items-start justify-between gap-3">
							<p className="min-w-0 whitespace-pre-wrap break-words text-sm leading-5">{attempt.initialPrompt}</p>
							<span className={cn("shrink-0 font-mono text-micro", attempt.createState === "failed" ? "text-destructive" : "text-muted-foreground")}>{initialState}</span>
						</div>
						{attempt.createError ? <p className="mt-2 border-t border-border/60 pt-2 text-xs text-destructive" role="alert">{attempt.createError}</p> : null}
					</div>
					{attempt.messages.map((message) => (
						<MessageRow key={message.id} attemptId={attempt.attemptId} message={message} />
					))}
				</div>

				<form className="border-t border-border/80 pt-4" onSubmit={submit}>
					<div className="flex items-end gap-2 rounded-xl border border-border bg-surface p-2 shadow-sm focus-within:border-primary/55 focus-within:ring-1 focus-within:ring-primary/20">
						<textarea
							ref={textareaRef}
							aria-label={t("cloud.pending.addInstruction")}
							className="max-h-40 min-h-10 min-w-0 flex-1 resize-none bg-transparent px-2 py-2 text-sm leading-5 outline-none placeholder:text-muted-foreground"
							placeholder={t("cloud.pending.instructionPlaceholder")}
							value={draft}
							onChange={(event) => {
								setDraft(event.target.value);
								setInputError(undefined);
							}}
							onKeyDown={(event) => {
								if (event.key === "Enter" && !event.shiftKey) {
									event.preventDefault();
									event.currentTarget.form?.requestSubmit();
								}
							}}
						/>
						<Button type="submit" size="icon" aria-label={t("cloud.pending.sendInstruction")} disabled={draft.trim() === ""}>
							<Send aria-hidden="true" />
						</Button>
					</div>
					{inputError ? <p className="mt-2 text-xs text-destructive" role="alert">{inputError}</p> : null}
					<p className="mt-2 font-mono text-micro text-muted-foreground">{t("cloud.pending.messagesSavedInOrder")}</p>
				</form>
			</div>
		</div>
	);
}
