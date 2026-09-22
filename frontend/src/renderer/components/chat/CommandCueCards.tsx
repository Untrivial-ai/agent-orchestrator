import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Eye, Keyboard, Loader2, Square } from "lucide-react";
import { apiErrorMessage } from "../../lib/api-client";
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
		<div className="mx-auto flex w-full max-w-3xl flex-col gap-2 px-4 pb-2" data-testid="command-cue-cards">
			{cards.map((card) => <CommandCueCardView key={card.handleId} card={card} onViewTerminal={onViewTerminal} />)}
		</div>
	) : null;
}

function CommandCueCardView({ card, onViewTerminal }: { card: CommandCueCard; onViewTerminal: (handleId: string) => void }) {
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
		const poll = async () => {
			try {
				const result = await getCommandCueTerminalStatus(card.handleId);
				if (!cancelled) setState(card.handleId, result.state);
			} catch (error) {
				if (!cancelled) setState(card.handleId, "failed", apiErrorMessage(error));
			}
		};
		void poll();
		const timer = window.setInterval(() => void poll(), 1_000);
		return () => { cancelled = true; window.clearInterval(timer); };
	}, [card.handleId, card.state, setState]);

	const stop = async () => {
		if (stopPending.current) return;
		stopPending.current = true;
		setStopping(true);
		setStopError(undefined);
		try {
			const result = await stopCommandCueTerminal(card.handleId);
			setState(card.handleId, result.state);
			setConfirmStop(false);
		} catch (error) {
			setStopError(apiErrorMessage(error));
		} finally {
			stopPending.current = false;
			setStopping(false);
		}
	};

	return (
		<article className="rounded-lg border border-border bg-surface px-4 py-3 shadow-sm" data-state={card.state}>
			<div className="flex items-start gap-3">
				{ACTIVE_STATES.has(card.state) ? <Loader2 className="mt-0.5 size-4 animate-spin text-status-working" aria-hidden="true" /> : <Square className="mt-0.5 size-4 text-muted-foreground" aria-hidden="true" />}
				<div className="min-w-0 flex-1">
					<strong className="text-sm text-foreground">{card.name}</strong>
					<code className="mt-1 block whitespace-pre-wrap break-words text-xs text-muted-foreground">{card.command}</code>
					<p className="mt-1 text-xs text-muted-foreground" role="status">{t(`cues.commandState.${card.state}`)}</p>
					{card.error ? <p className="mt-1 text-xs text-destructive" role="alert">{card.error}</p> : null}
				</div>
				<div className="flex shrink-0 flex-wrap gap-2">
					<Button size="sm" variant="outline" onClick={() => onViewTerminal(card.handleId)}><Eye aria-hidden="true" />{t("cues.viewTerminal")}</Button>
					{!card.inputEnabled ? <Button size="sm" variant="outline" onClick={() => enableInput(card.handleId)}><Keyboard aria-hidden="true" />{t("cues.enableInput")}</Button> : null}
					{ACTIVE_STATES.has(card.state) ? <Button size="sm" variant="outline" onClick={() => setConfirmStop(true)}>{t("cues.stop")}</Button> : null}
				</div>
			</div>
			<ConfirmDialog open={confirmStop} title={t("cues.stopTitle")} description={t("cues.stopBody")} confirmLabel={t("cues.stopCommand")} destructive busy={stopping} error={stopError} onConfirm={() => void stop()} onOpenChange={(open) => { if (!stopping) setConfirmStop(open); }} />
		</article>
	);
}
