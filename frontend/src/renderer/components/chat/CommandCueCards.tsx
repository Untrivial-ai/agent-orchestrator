import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Eye, Keyboard, Loader2, SquareTerminal } from "lucide-react";
import { apiErrorCode, apiErrorMessage } from "../../lib/api-client";
import { getCommandCueTerminalStatus, stopCommandCueTerminal } from "../../hooks/useShellTerminals";
import { useCommandCueStore, type CommandCueCard } from "../../stores/command-cue-store";
import { Button } from "../ui/button";
import { ConfirmDialog } from "../ConfirmDialog";

const ACTIVE_STATES = new Set(["starting", "running"]);

export function CommandCueCards({
	projectId,
	sessionId,
	onViewTerminal,
}: {
	projectId: string;
	sessionId: string;
	onViewTerminal: (handleId: string) => void;
}) {
	const cardsByHandle = useCommandCueStore((state) => state.cards);
	const cards = useMemo(
		() => Object.values(cardsByHandle).filter((card) => card.projectId === projectId && card.sessionId === sessionId),
		[cardsByHandle, projectId, sessionId],
	);
	return cards.length ? (
		<div className="flex w-full min-w-0 flex-col gap-4" data-testid="command-cue-cards">
			{cards.map((card) => <CommandCueCardView key={card.handleId} card={card} onViewTerminal={onViewTerminal} />)}
		</div>
	) : null;
}

export function CommandCueCardView({ card, onViewTerminal }: { card: CommandCueCard; onViewTerminal: (handleId: string) => void }) {
	const { t } = useTranslation();
	const setState = useCommandCueStore((state) => state.setState);
	const enableInput = useCommandCueStore((state) => state.enableInput);
	const [confirmStop, setConfirmStop] = useState(false);
	const [stopping, setStopping] = useState(false);
	const [stopError, setStopError] = useState<string>();
	const stopPending = useRef(false);

	useEffect(() => {
		if (!ACTIVE_STATES.has(card.state)) return;
		let cancelled = false;
		let timer: number | undefined;
		const poll = async () => {
			try {
				const result = await getCommandCueTerminalStatus(card.handleId);
				if (!cancelled) setState(card.handleId, result.state, undefined, result.output);
			} catch (error) {
				if (cancelled) return;
				if (apiErrorCode(error) === "CUE_COMMAND_TERMINAL_NOT_FOUND") useCommandCueStore.getState().close(card.handleId);
				else setState(card.handleId, "failed", apiErrorMessage(error));
			} finally {
				if (!cancelled && ACTIVE_STATES.has(useCommandCueStore.getState().cards[card.handleId]?.state ?? "")) {
					timer = window.setTimeout(() => void poll(), 1_000);
				}
			}
		};
		void poll();
		return () => { cancelled = true; window.clearTimeout(timer); };
	}, [card.handleId, setState]);

	const stop = async () => {
		if (stopPending.current) return;
		stopPending.current = true;
		setStopping(true);
		setStopError(undefined);
		try {
			const result = await stopCommandCueTerminal(card.handleId);
			setState(card.handleId, result.state, undefined, result.output);
			setConfirmStop(false);
		} catch (error) {
			setStopError(apiErrorMessage(error));
		} finally {
			stopPending.current = false;
			setStopping(false);
		}
	};

	return (
		<article className="cursor-chat-origin-message rounded-md border border-border border-l-2 border-l-logo-accent/60 px-3.5 py-2.5" data-state={card.state}>
			<div className="mb-1.5 flex items-center gap-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
				{ACTIVE_STATES.has(card.state) ? <Loader2 className="size-3.5 shrink-0 animate-spin text-logo-accent" aria-hidden="true" /> : <SquareTerminal className="size-3.5 shrink-0 text-logo-accent" aria-hidden="true" />}
				<span>{t("cues.commandLabel")}</span>
				<span className="ml-auto" role="status">{t(`cues.commandState.${card.state}`)}</span>
			</div>
			<div className="min-w-0">
				<strong className="text-sm text-foreground">{card.name}</strong>
				<code className="mt-1 block whitespace-pre-wrap break-words text-xs text-muted-foreground">{card.command}</code>
				{card.output ? <pre className="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded bg-terminal px-3 py-2 font-mono text-xs text-terminal-foreground" data-testid="command-cue-output">{card.output}</pre> : null}
				{card.error ? <p className="mt-1 text-xs text-destructive" role="alert">{card.error}</p> : null}
				<div className="mt-2 flex flex-wrap gap-2">
					<Button size="sm" variant="outline" disabled={card.state === "closed"} onClick={() => onViewTerminal(card.handleId)}><Eye aria-hidden="true" />{t("cues.viewTerminal")}</Button>
					{!card.inputEnabled ? <Button size="sm" variant="outline" disabled={card.state === "closed"} onClick={() => enableInput(card.handleId)}><Keyboard aria-hidden="true" />{t("cues.enableInput")}</Button> : null}
					{ACTIVE_STATES.has(card.state) ? <Button size="sm" variant="outline" onClick={() => setConfirmStop(true)}>{t("cues.stop")}</Button> : null}
				</div>
			</div>
			<ConfirmDialog open={confirmStop} title={t("cues.stopTitle")} description={t("cues.stopBody")} confirmLabel={t("cues.stopCommand")} destructive busy={stopping} error={stopError} onConfirm={() => void stop()} onOpenChange={(open) => { if (!stopping) setConfirmStop(open); }} />
		</article>
	);
}
