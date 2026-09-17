import * as Dialog from "@radix-ui/react-dialog";
import { useNavigate } from "@tanstack/react-router";
import { AlertTriangle, RotateCw, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { isChatPreflightCode } from "../lib/spawn-orchestrator";
import type { OrchestratorReplacementFailure } from "../stores/ui-store";
import { findProjectOrchestrator, type WorkspaceSummary } from "../types/workspace";
import { Button } from "./ui/button";
import {
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";

type OrchestratorReplacementDialogProps = {
	projectId: string | null;
	pending?: boolean;
	error?: OrchestratorReplacementFailure;
	workspaces: WorkspaceSummary[];
	onOpenChange: (open: boolean) => void;
	onRetry: (projectId: string) => void;
	onRetryAsTui: (projectId: string) => void;
};

export function OrchestratorReplacementDialog({
	projectId,
	pending = false,
	error,
	workspaces,
	onOpenChange,
	onRetry,
	onRetryAsTui,
}: OrchestratorReplacementDialogProps) {
	const { t } = useTranslation();
	const navigate = useNavigate();
	const open = Boolean(projectId && error);
	const orchestrator = projectId ? findProjectOrchestrator(workspaces, projectId) : undefined;

	const openCurrent = () => {
		if (!projectId || !orchestrator) return;
		onOpenChange(false);
		void navigate({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId, sessionId: orchestrator.id },
		});
	};

	return (
		<Dialog.Root
			open={open}
			onOpenChange={(open) => {
				if (!pending) onOpenChange(open);
			}}
		>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay data-[state=open]:animate-overlay-in" />
				<Dialog.Content
					aria-busy={pending}
					className={`${settingsDialogContentClass} fixed left-1/2 top-1/2 w-dialog-orchestrator -translate-x-1/2 -translate-y-1/2 data-[state=open]:animate-modal-in`}
				>
					<Dialog.Close asChild>
						<button
							type="button"
							className="settings-dialog-close-button settings-close-button"
							disabled={pending}
							aria-label={t("orchestratorReplacement.close")}
						>
							<X className="size-5" aria-hidden="true" />
						</button>
					</Dialog.Close>
					<div className={settingsDialogHeaderClass}>
						<div className="flex items-start gap-3">
							<div className="grid size-8 shrink-0 place-items-center rounded-md border border-border bg-muted text-warning">
								<AlertTriangle className="size-icon-base" aria-hidden="true" />
							</div>
							<div className="min-w-0 flex-1">
								<Dialog.Title className="settings-dialog-title">{t("orchestratorReplacement.title")}</Dialog.Title>
								<Dialog.Description className="mt-1 text-control leading-5 text-settings-muted">
									{error?.message ?? t("orchestratorReplacement.fallback")}
								</Dialog.Description>
							</div>
						</div>
					</div>
					<div className={settingsDialogFooterClass}>
						{error && isChatPreflightCode(error.code) ? (
							<Button
								type="button"
								variant="footer"
								aria-disabled={pending}
								onClick={() => !pending && projectId && onRetryAsTui(projectId)}
							>
								{t("newTask.createAsTui")}
							</Button>
						) : null}
						{orchestrator ? (
							<Button type="button" variant="footer" disabled={pending} onClick={openCurrent}>
								{t("orchestratorReplacement.openCurrent")}
							</Button>
						) : null}
						<Button
							type="button"
							variant="footer-primary"
							aria-disabled={pending}
							onClick={() => !pending && projectId && onRetry(projectId)}
						>
							<RotateCw className="size-3.5" aria-hidden="true" />
							{t("orchestratorReplacement.retry")}
						</Button>
					</div>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}
