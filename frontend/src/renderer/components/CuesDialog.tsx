import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2, MessageSquare, Pencil, Play, Plus, Trash2, X, Zap } from "lucide-react";
import { cn } from "../lib/utils";
import { apiErrorMessage } from "../lib/api-client";
import { useNavigateToSession } from "../lib/navigate-to-session";
import { useUiStore } from "../stores/ui-store";
import {
	useCreateCueMutation,
	useDeleteCueMutation,
	useInvokeCueMutation,
	useProjectCuesQuery,
	useUpdateCueMutation,
} from "../hooks/useCuesQuery";
import type { CreateCueInput, CueDTO } from "../lib/cues";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogTitle,
	settingsDialogBodyClass,
	settingsDialogContentClass,
	settingsDialogFooterClass,
	settingsDialogHeaderClass,
} from "./ui/dialog";
import { ConfirmDialog } from "./ConfirmDialog";

type CuesDialogProps = {
	open: boolean;
	onOpenChange: (open: boolean) => void;
	projectId: string;
};

type CueType = "command" | "agent";

function cueType(cue: CueDTO): CueType {
	return cue.type === "agent" ? "agent" : "command";
}

const composerTextareaClass =
	"block w-full min-h-[3.5rem] resize-y rounded-md border border-transparent bg-input/50 px-3 py-2 text-sm text-foreground transition-[color,box-shadow,background-color] outline-none placeholder:text-muted-foreground focus-visible:outline-none disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50";

function CueTypeIcon({ type, className }: { type: CueType; className?: string }) {
	if (type === "agent") {
		return <MessageSquare aria-hidden="true" className={className} />;
	}
	return <Zap aria-hidden="true" className={className} />;
}

export function CuesDialog({ open, onOpenChange, projectId }: CuesDialogProps) {
	const { t } = useTranslation();
	const showGlobalToast = useUiStore((state) => state.showGlobalToast);
	const navigateToSession = useNavigateToSession();
	const cuesQuery = useProjectCuesQuery(projectId);
	const createMutation = useCreateCueMutation(projectId);
	const updateMutation = useUpdateCueMutation(projectId);
	const deleteMutation = useDeleteCueMutation(projectId);
	const invokeMutation = useInvokeCueMutation();

	const [formOpen, setFormOpen] = useState<"new" | CueDTO | null>(null);
	const [deletingCue, setDeletingCue] = useState<CueDTO | null>(null);
	const [name, setName] = useState("");
	const [description, setDescription] = useState("");
	const [type, setType] = useState<CueType>("command");
	const [command, setCommand] = useState("");
	const [prompt, setPrompt] = useState("");
	const [formError, setFormError] = useState<string | null>(null);
	const [invokeError, setInvokeError] = useState<string | null>(null);
	const [saving, setSaving] = useState(false);
	const [runningCueId, setRunningCueId] = useState<string | null>(null);

	useEffect(() => {
		setFormOpen(null);
		setDeletingCue(null);
		setInvokeError(null);
	}, [open, projectId]);

	const openNew = () => {
		setName("");
		setDescription("");
		setType("command");
		setCommand("");
		setPrompt("");
		setFormError(null);
		setFormOpen("new");
	};

	const openEdit = (cue: CueDTO) => {
		setName(cue.name);
		setDescription(cue.description ?? "");
		setType(cueType(cue));
		setCommand(cue.command ?? "");
		setPrompt(cue.prompt ?? "");
		setFormError(null);
		setFormOpen(cue);
	};

	const handleSave = async () => {
		if (formOpen === null) return;
		const trimmedName = name.trim();
		if (!trimmedName) {
			setFormError(t("cues.nameRequired"));
			return;
		}
		const input: CreateCueInput = {
			name: trimmedName,
			type,
			description: description.trim() || undefined,
		};
		if (type === "command") {
			input.command = command.trim() || undefined;
		} else {
			input.prompt = prompt.trim() || undefined;
		}
		setSaving(true);
		setFormError(null);
		try {
			if (formOpen === "new") {
				await createMutation.mutateAsync(input);
				showGlobalToast(t("cues.created"), t("cues.createdBody", { name: trimmedName }));
			} else {
				await updateMutation.mutateAsync({ cueId: formOpen.id, input });
				showGlobalToast(t("cues.saved"), t("cues.savedBody", { name: trimmedName }));
			}
			setFormOpen(null);
		} catch (error) {
			setFormError(apiErrorMessage(error, t("cues.saveFailed")));
		} finally {
			setSaving(false);
		}
	};

	const handleInvoke = async (cue: CueDTO) => {
		if (runningCueId) return;
		setInvokeError(null);
		setRunningCueId(cue.id);
		try {
			const sessionId = await invokeMutation.mutateAsync({ cueId: cue.id });
			showGlobalToast(t("cues.invokeSent"), t("cues.invokeSentBody", { name: cue.name }));
			onOpenChange(false);
			navigateToSession(projectId, sessionId);
		} catch (error) {
			setInvokeError(apiErrorMessage(error, t("cues.invokeFailed")));
		} finally {
			setRunningCueId(null);
		}
	};

	const handleDelete = async () => {
		if (!deletingCue) return;
		try {
			await deleteMutation.mutateAsync(deletingCue.id);
			showGlobalToast(t("cues.deleted"), t("cues.deletedBody", { name: deletingCue.name }));
			setDeletingCue(null);
		} catch (error) {
			showGlobalToast(t("cues.deleteFailed"), apiErrorMessage(error, t("cues.deleteFailed")), "error");
			setDeletingCue(null);
		}
	};

	const renderList = () => {
		if (cuesQuery.isLoading) {
			return (
				<div className="flex items-center justify-center gap-2 py-10 text-sm text-muted-foreground">
					<Loader2 className="size-4 animate-spin" aria-hidden="true" />
					{t("cues.loading")}
				</div>
			);
		}
		if (cuesQuery.isError) {
			return (
				<div className="flex flex-col items-center gap-3 py-8 text-center">
					<p role="alert" className="text-sm text-destructive">
						{apiErrorMessage(cuesQuery.error, t("cues.loadFailed"))}
					</p>
					<Button type="button" variant="outline" size="sm" onClick={() => void cuesQuery.refetch()}>
						{t("cues.retry")}
					</Button>
				</div>
			);
		}
		const cues = cuesQuery.data ?? [];
		if (cues.length === 0) {
			return (
				<div className="flex flex-col items-center gap-3 py-10 text-center">
					<Zap className="size-8 text-passive" aria-hidden="true" />
					<p className="max-w-sm text-sm leading-5 text-muted-foreground">{t("cues.empty")}</p>
				</div>
			);
		}
		return (
			<div className="flex flex-col gap-1.5">
				{cues.map((cue) => {
					const cueKind = cueType(cue);
					return (
						<div key={cue.id} className="flex items-center gap-2.5 rounded-md border border-border bg-surface px-3 py-2">
							<CueTypeIcon type={cueKind} className="size-4 shrink-0 text-muted-foreground" />
							<div className="min-w-0 flex-1">
								<div className="flex items-center gap-2 text-sm leading-5 font-medium text-foreground">
									<span className="truncate">{cue.name}</span>
									<span className="shrink-0 rounded-sm border border-border bg-background px-1.5 py-px text-[10px] leading-3 tracking-wide text-passive uppercase">
										{cueKind === "agent" ? t("cues.typeName.agent") : t("cues.typeName.command")}
									</span>
								</div>
								{cue.description ? (
									<p className="truncate text-xs leading-4 text-muted-foreground">{cue.description}</p>
								) : null}
							</div>
							<div className="flex shrink-0 items-center gap-0.5">
								<Button
									type="button"
									variant="ghost"
									size="icon-sm"
									disabled={runningCueId !== null}
									onClick={() => void handleInvoke(cue)}
									aria-label={t("cues.run")}
									title={t("cues.run")}
									className="size-7 shrink-0 rounded-full p-0 text-muted-foreground hover:text-foreground"
								>
									{runningCueId === cue.id ? (
										<Loader2 className="size-3.5 animate-spin" aria-hidden="true" />
									) : (
										<Play className="size-3.5" aria-hidden="true" />
									)}
								</Button>
								<Button
									type="button"
									variant="ghost"
									size="icon-sm"
									disabled={runningCueId !== null || saving}
									onClick={() => openEdit(cue)}
									aria-label={t("cues.edit")}
									title={t("cues.edit")}
									className="size-7 shrink-0 rounded-full p-0 text-muted-foreground hover:text-foreground"
								>
									<Pencil className="size-3.5" aria-hidden="true" />
								</Button>
								<Button
									type="button"
									variant="ghost"
									size="icon-sm"
									disabled={runningCueId !== null || saving}
									onClick={() => setDeletingCue(cue)}
									aria-label={t("cues.delete")}
									title={t("cues.delete")}
									className="size-7 shrink-0 rounded-full p-0 text-muted-foreground hover:text-destructive"
								>
									<Trash2 className="size-3.5" aria-hidden="true" />
								</Button>
							</div>
						</div>
					);
				})}
			</div>
		);
	};

	const renderForm = () => (
		<div className="flex flex-col gap-3">
			<div className="flex flex-col gap-1.5">
				<Label htmlFor="cue-name" className="text-xs font-medium text-muted-foreground">
					{t("cues.nameLabel")}
				</Label>
				<Input
					id="cue-name"
					value={name}
					onChange={(event) => setName(event.target.value)}
					placeholder={t("cues.namePlaceholder")}
					autoFocus
				/>
			</div>

			<div className="flex flex-col gap-1.5">
				<Label htmlFor="cue-description" className="text-xs font-medium text-muted-foreground">
					{t("cues.descriptionLabel")}
				</Label>
				<Input
					id="cue-description"
					value={description}
					onChange={(event) => setDescription(event.target.value)}
					placeholder={t("cues.descriptionPlaceholder")}
				/>
			</div>

			<div className="flex flex-col gap-1.5">
				<Label className="text-xs font-medium text-muted-foreground">{t("cues.typeLabel")}</Label>
				<Select
					value={type}
					onValueChange={(value) => setType(value === "agent" ? "agent" : "command")}
				>
					<SelectTrigger className="w-full">
						<SelectValue />
					</SelectTrigger>
					<SelectContent>
						<SelectItem value="command">
							<Zap className="size-3.5 text-muted-foreground" aria-hidden="true" />
							{t("cues.typeName.command")}
						</SelectItem>
						<SelectItem value="agent">
							<MessageSquare className="size-3.5 text-muted-foreground" aria-hidden="true" />
							{t("cues.typeName.agent")}
						</SelectItem>
					</SelectContent>
				</Select>
			</div>

			{type === "command" ? (
				<div className="flex flex-col gap-1.5">
					<Label htmlFor="cue-command" className="text-xs font-medium text-muted-foreground">
						{t("cues.commandLabel")}
					</Label>
					<textarea
						id="cue-command"
						value={command}
						onChange={(event) => setCommand(event.target.value)}
						placeholder={t("cues.commandPlaceholder")}
						className={composerTextareaClass}
						rows={2}
					/>
					<p className="text-xs leading-4 text-passive">{t("cues.commandHelp")}</p>
				</div>
			) : (
				<div className="flex flex-col gap-1.5">
					<Label htmlFor="cue-prompt" className="text-xs font-medium text-muted-foreground">
						{t("cues.agentLabel")}
					</Label>
					<textarea
						id="cue-prompt"
						value={prompt}
						onChange={(event) => setPrompt(event.target.value)}
						placeholder={t("cues.promptPlaceholder")}
						className={composerTextareaClass}
						rows={4}
					/>
					<p className="text-xs leading-4 text-passive">{t("cues.promptHelp")}</p>
				</div>
			)}

			{formError ? (
				<p role="alert" className="text-caption leading-4 text-error">
					{formError}
				</p>
			) : null}
		</div>
	);

	return (
		<>
			<Dialog open={open} onOpenChange={onOpenChange}>
				<DialogContent
					showCloseButton={false}
					className={cn(settingsDialogContentClass, "w-[min(640px,calc(100vw-24px))]")}
				>
					<DialogClose asChild>
						<button
							type="button"
							disabled={saving || deleteMutation.isPending}
							className="settings-dialog-close-button settings-close-button"
							aria-label={t("confirm.close")}
							title={t("confirm.closeEsc")}
						>
							<X className="size-4" aria-hidden="true" />
						</button>
					</DialogClose>

					<div className={cn(settingsDialogHeaderClass, "p-5 pr-12")}>
						<DialogTitle className="settings-dialog-title text-base">{t("cues.title")}</DialogTitle>
					</div>

					{invokeError ? (
						<div className={cn(settingsDialogBodyClass, "p-5 py-3")}>
							<p role="alert" className="text-caption leading-4 text-error">
								{invokeError}
							</p>
						</div>
					) : null}

					<div className={cn(settingsDialogBodyClass, "p-5")}>{formOpen ? renderForm() : renderList()}</div>

					<div className={cn(settingsDialogFooterClass, "gap-2 p-4")}>
						{formOpen ? (
							<>
								<Button type="button" variant="footer" disabled={saving} onClick={() => setFormOpen(null)}>
									{t("cues.cancel")}
								</Button>
								<Button type="button" variant="footer-primary" disabled={saving} onClick={() => void handleSave()}>
									{saving ? <Loader2 className="size-4 animate-spin" aria-hidden="true" /> : null}
									{formOpen === "new" ? t("cues.create") : t("cues.save")}
								</Button>
							</>
						) : (
							<Button type="button" variant="footer-primary" onClick={openNew}>
								<Plus className="size-4" aria-hidden="true" />
								{t("cues.newCue")}
							</Button>
						)}
					</div>
				</DialogContent>
			</Dialog>

			<ConfirmDialog
				open={deletingCue !== null}
				title={t("cues.deleteTitle")}
				description={deletingCue ? t("cues.deleteBody", { name: deletingCue.name }) : ""}
				confirmLabel={t("cues.delete")}
				destructive
				busy={deleteMutation.isPending}
				error={deleteMutation.isError ? apiErrorMessage(deleteMutation.error, t("cues.deleteFailed")) : null}
				onConfirm={() => void handleDelete()}
				onOpenChange={(nextOpen) => {
					if (!nextOpen) setDeletingCue(null);
				}}
			/>
		</>
	);
}