import * as Dialog from "@radix-ui/react-dialog";
import { StickyNote } from "lucide-react";
import { useTranslation } from "react-i18next";
import { useWorkspaceQuery } from "../hooks/useWorkspaceQuery";
import { TaskComposer } from "./TaskComposer";
import { STANDALONE_WORKSPACE_ID, STANDALONE_PROJECT_KIND } from "../types/workspace";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";

type NewTaskDialogProps = {
	open: boolean;
	projectId?: string;
	onProjectChange?: (projectId: string) => void;
	onCreated: (sessionId: string) => void;
	onOpenChange: (open: boolean) => void;
};

export function NewTaskDialog({ open, projectId, onProjectChange, onCreated, onOpenChange }: NewTaskDialogProps) {
	const { t } = useTranslation();
	const workspaces = useWorkspaceQuery({ subscribed: open }).data ?? [];
	const projects = workspaces.filter(
		(workspace) => workspace.id !== STANDALONE_WORKSPACE_ID && workspace.kind !== STANDALONE_PROJECT_KIND,
	);
	const selectedProjectId = projectId ?? STANDALONE_WORKSPACE_ID;
	return (
		<Dialog.Root open={open} onOpenChange={onOpenChange}>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay data-[state=open]:animate-overlay-in data-[state=closed]:animate-overlay-out" />
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay w-dialog-xl -translate-x-1/2 -translate-y-1/2 overflow-hidden rounded-lg border border-border bg-popover p-0 text-popover-foreground shadow-xl data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none">
					{/* One title line names the dialog, styled like every other settings-style
					    modal; everything else stays the composer's surface, no bordered header. */}
					<Dialog.Title className="settings-dialog-title flex flex-wrap items-baseline gap-x-1.5 px-4 pt-3">
						<span>{t("newTask.titleFor")}</span>
						<Select value={selectedProjectId} onValueChange={(value) => onProjectChange?.(value)}>
							<SelectTrigger
								size="auto"
								className="w-fit gap-1 rounded-md bg-muted/40 px-1.5 py-0 text-[length:inherit] font-inherit text-foreground outline-none transition-colors hover:bg-interactive-hover focus:outline-none focus-visible:outline-none focus-visible:ring-0 aria-expanded:bg-interactive-hover"
								aria-label={t("newTask.project")}
							>
								<SelectValue />
							</SelectTrigger>
							<SelectContent
								position="popper"
								align="start"
								showScrollButtons={false}
								className="min-w-56 max-h-[min(18rem,var(--radix-select-content-available-height))] overflow-y-auto overscroll-contain"
							>
								<SelectItem className="transition-none" value={STANDALONE_WORKSPACE_ID}>
									<span className="inline-flex items-center gap-2">
										<StickyNote aria-hidden="true" className="size-icon-sm text-muted-foreground" />
										{t("standalone.workspaceName")}
									</span>
								</SelectItem>
								{projects.map((project) => (
									<SelectItem className="transition-none" key={project.id} value={project.id}>{project.name}</SelectItem>
								))}
							</SelectContent>
						</Select>
					</Dialog.Title>
					<Dialog.Description className="sr-only">
						{t(selectedProjectId === STANDALONE_WORKSPACE_ID ? "newTask.standaloneDescription" : "newTask.description")}
					</Dialog.Description>
					<TaskComposer
						projectId={selectedProjectId}
						autoFocusTitle
						onCreated={(sessionId) => {
							onCreated(sessionId);
							onOpenChange(false);
						}}
					/>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}
