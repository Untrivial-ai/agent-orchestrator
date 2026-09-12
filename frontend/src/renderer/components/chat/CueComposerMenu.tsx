import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2, MessageSquare, Zap } from "lucide-react";
import { useUiStore } from "../../stores/ui-store";
import { apiErrorMessage } from "../../lib/api-client";
import { useInvokeCueMutation, useProjectCuesQuery } from "../../hooks/useCuesQuery";
import type { CueDTO } from "../../lib/cues";
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
	const { t } = useTranslation();
	const showGlobalToast = useUiStore((state) => state.showGlobalToast);
	const cuesQuery = useProjectCuesQuery(projectId);
	const invokeMutation = useInvokeCueMutation();
	const [invokingId, setInvokingId] = useState<string | null>(null);

	const cues = cuesQuery.data ?? [];
	if (cues.length === 0) return null;

	const handleInvoke = async (cue: CueDTO) => {
		if (invokingId) return;
		setInvokingId(cue.id);
		try {
			await invokeMutation.mutateAsync({ cueId: cue.id, sessionId });
			showGlobalToast(t("cues.invokeSent"), t("cues.invokeSentBody", { name: cue.name }));
		} catch (error) {
			showGlobalToast(t("cues.invokeFailed"), apiErrorMessage(error, t("cues.invokeFailed")), "error");
		} finally {
			setInvokingId(null);
		}
	};

	return (
		<DropdownMenu modal={false}>
			<Tooltip>
				<TooltipTrigger asChild>
					<DropdownMenuTrigger asChild>
						<Button
							type="button"
							variant="ghost"
							size="icon-sm"
							disabled={invokingId !== null}
							aria-label={t("cues.run")}
							className="size-7 shrink-0 rounded-full p-0 text-muted-foreground hover:bg-white/5! hover:text-foreground"
						>
							{invokingId ? (
								<Loader2 className="size-3.5 animate-spin text-muted-foreground" aria-hidden="true" />
							) : (
								<Zap className="size-3.5 text-muted-foreground" aria-hidden="true" />
							)}
						</Button>
					</DropdownMenuTrigger>
				</TooltipTrigger>
				<TooltipContent side="bottom">{t("cues.run")}</TooltipContent>
			</Tooltip>

			<DropdownMenuContent align="start" side="top">
				{cues.map((cue) => (
					<DropdownMenuItem
						key={cue.id}
						disabled={invokingId !== null}
						onSelect={() => void handleInvoke(cue)}
						className="items-start gap-2"
					>
						{cue.type === "agent" ? (
							<MessageSquare className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
						) : (
							<Zap className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
						)}
						<span className="min-w-0 flex-1">
							<span className="block text-sm text-foreground">{cue.name}</span>
							{cue.description ? (
								<span className="block truncate text-xs text-muted-foreground">{cue.description}</span>
							) : null}
						</span>
					</DropdownMenuItem>
				))}
			</DropdownMenuContent>
		</DropdownMenu>
	);
}