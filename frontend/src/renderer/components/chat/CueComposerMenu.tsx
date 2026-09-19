import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2, MessageSquare, Play, TerminalSquare } from "lucide-react";
import { useUiStore } from "../../stores/ui-store";
import { apiErrorMessage } from "../../lib/api-client";
import { useNavigateToSession } from "../../lib/navigate-to-session";
import { useInvokeCueMutation, useProjectCuesQuery } from "../../hooks/useCuesQuery";
import type { CueDTO } from "../../lib/cues";
import { TopbarButton } from "../TopbarButton";
import { Button } from "../ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "../ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";

export function CueComposerMenu({
	projectId,
	sessionId,
}: {
	projectId: string;
	sessionId: string;
}) {
	return <CueRunMenu key={JSON.stringify([projectId, sessionId])} projectId={projectId} sessionId={sessionId} />;
}

export function ProjectCueMenu({ projectId, disabled = false }: { projectId: string; disabled?: boolean }) {
	return <CueRunMenu key={projectId} projectId={projectId} disabled={disabled} topbar />;
}

function CueRunMenu({
	projectId,
	sessionId,
	disabled = false,
	topbar = false,
}: {
	projectId: string;
	sessionId?: string;
	disabled?: boolean;
	topbar?: boolean;
}) {
	const { t } = useTranslation();
	const showGlobalToast = useUiStore((state) => state.showGlobalToast);
	const navigateToSession = useNavigateToSession();
	const [open, setOpen] = useState(false);
	const pending = useRef(false);
	const generation = useRef(0);
	useEffect(() => () => { generation.current++; }, []);
	const invokeMutation = useInvokeCueMutation();
	const [invokingId, setInvokingId] = useState<string | null>(null);


	const handleInvoke = async (cue: CueDTO) => {
		if (pending.current) return;
		pending.current = true;
		const origin = generation.current;
		setInvokingId(cue.id);
		try {
			const targetSessionId = await invokeMutation.mutateAsync({ cueId: cue.id, sessionId });
			if (origin !== generation.current) return;
			showGlobalToast(t("cues.invokeSent"), t("cues.invokeSentBody", { name: cue.name }));
			if (!sessionId) navigateToSession(projectId, targetSessionId);
		} catch (error) {
			if (origin !== generation.current) return;
			showGlobalToast(t("cues.invokeFailed"), apiErrorMessage(error, t("cues.invokeFailed")), "error");
		} finally {
			pending.current = false;
			if (origin === generation.current) setInvokingId(null);
		}
	};

	return (
		<DropdownMenu modal={false} open={open} onOpenChange={(next) => { if (next) generation.current++; setOpen(next); }}>
			<Tooltip>
				<TooltipTrigger asChild>
					<DropdownMenuTrigger asChild>
						{topbar ? (
							<TopbarButton
								type="button"
								aria-label={t("cues.run")}
								data-priority="secondary"
								disabled={disabled || invokingId !== null}
								variant="icon"
							>
								{invokingId ? <Loader2 className="size-icon-md animate-spin" aria-hidden="true" /> : <Play className="size-icon-md" aria-hidden="true" />}
							</TopbarButton>
						) : (
							<Button
								type="button"
								variant="ghost"
								size="icon-sm"
								disabled={disabled || invokingId !== null}
								aria-label={t("cues.run")}
								className="size-7 shrink-0 rounded-full p-0 text-muted-foreground hover:bg-white/5! hover:text-foreground"
							>
								{invokingId ? <Loader2 className="size-3.5 animate-spin text-muted-foreground" aria-hidden="true" /> : <Play className="size-3.5 text-muted-foreground" aria-hidden="true" />}
							</Button>
						)}
					</DropdownMenuTrigger>
				</TooltipTrigger>
				<TooltipContent side="bottom">{t("cues.run")}</TooltipContent>
			</Tooltip>

			{open ? <CueMenuItems projectId={projectId} busy={invokingId !== null} onInvoke={handleInvoke} topbar={topbar} /> : null}
		</DropdownMenu>
	);
}
function CueMenuItems({ projectId, busy, onInvoke, topbar }: { projectId: string; busy: boolean; onInvoke: (cue: CueDTO) => Promise<void>; topbar: boolean }) {
	const { t } = useTranslation();
	const query = useProjectCuesQuery(projectId);
	const ready = query.isFetchedAfterMount && !query.isFetching && !query.isError;
	return (
		<DropdownMenuContent align={topbar ? "end" : "start"} side={topbar ? "bottom" : "top"}>
			{query.isError && !query.isFetching ? <>
				<p role="alert" className="px-2 py-1 text-sm text-destructive">{t("cues.loadFailed")}</p>
				<DropdownMenuItem onSelect={(event) => { event.preventDefault(); void query.refetch(); }}>{t("cues.retry")}</DropdownMenuItem>
			</> : !ready ? <DropdownMenuItem disabled>{t("cues.loading")}</DropdownMenuItem>
				: query.data?.length ? query.data.map((cue) => (
					<DropdownMenuItem key={cue.id} disabled={busy} onSelect={() => void onInvoke(cue)} className="items-start gap-2">
						{cue.type === "agent" ? <MessageSquare className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" /> : <TerminalSquare className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />}
						<span className="min-w-0 flex-1">
							<span className="block text-sm">{cue.name}</span>
							{cue.description ? <span className="block truncate text-xs text-muted-foreground">{cue.description}</span> : null}
						</span>
					</DropdownMenuItem>
				)) : <DropdownMenuItem disabled>{t("cues.empty")}</DropdownMenuItem>}
		</DropdownMenuContent>
	);
}
