import {
	type ClipboardEvent,
	type DragEvent,
	type FormEvent,
	type ReactNode,
	useId,
	useRef,
	useState,
} from "react";
import {
	FileTextIcon as FileText,
	LoaderCircleIcon as Loader2,
	PlusIcon as Plus,
	XIcon as X,
} from "./icons";

export type TaskComposerAgentOption = {
	authStatus?: "authorized" | "unauthorized" | "unknown";
	id: string;
	label: string;
};

export type TaskComposerAgentControl = {
	authorized?: TaskComposerAgentOption[];
	disabled: boolean;
	id: string;
	installed?: TaskComposerAgentOption[];
	label: string;
	onChange: (value: string) => void;
	placeholder: string;
	supported?: TaskComposerAgentOption[];
	value: string;
};

export type TaskComposerModelOption = {
	id: string;
	isDefault?: boolean;
	label: string;
	provider?: string;
};

export type TaskComposerModelCatalog = {
	allowCustom: boolean;
	models: TaskComposerModelOption[];
	selectionMode: "catalog" | "text" | "mode";
};

export type TaskComposerModelControl = {
	agentId: string;
	agentLabel: string;
	catalog?: TaskComposerModelCatalog;
	fetching: boolean;
	id: string;
	loading: boolean;
	mode: string;
	onModeChange: (value: string) => void;
	onModelChange: (value: string) => void;
	onRefresh?: () => void;
	projectId: string;
	refreshing: boolean;
	value: string;
};

export type TaskComposerAttachment = {
	id: string;
	name: string;
	previewUrl?: string;
};

export type TaskComposerAttachments = {
	error?: string | null;
	items: TaskComposerAttachment[];
	onAddFiles: (files: File[]) => void;
	onRemove: (id: string) => void;
};

export type TaskComposerSubmission = {
	canCreateAsTui: boolean;
	error?: string;
	isSubmitting: boolean;
	modelWarning?: string;
	onSubmit: () => void;
	onSubmitAsTui: () => void;
};

export type TaskComposerLabels = {
	addFile: string;
	createAsTui: string;
	removeFile: (name: string) => string;
	runsWith: string;
	start: string;
	starting: string;
	task: string;
	taskPlaceholder: string;
};

export type TaskComposerViewProps = {
	agent: Omit<TaskComposerAgentControl, "id">;
	attachments: TaskComposerAttachments;
	autoFocusPrompt?: boolean;
	canSubmit: boolean;
	labels: TaskComposerLabels;
	model: Omit<TaskComposerModelControl, "id">;
	onPromptChange: (value: string) => void;
	prompt: string;
	renderAgentControl: (control: TaskComposerAgentControl) => ReactNode;
	renderModelControl: (control: TaskComposerModelControl) => ReactNode;
	submission: TaskComposerSubmission;
};

export function TaskComposerView({
	agent,
	attachments,
	autoFocusPrompt,
	canSubmit,
	labels,
	model,
	onPromptChange,
	prompt,
	renderAgentControl,
	renderModelControl,
	submission,
}: TaskComposerViewProps) {
	const promptId = useId();
	const modelId = useId();
	const agentId = useId();
	const fileInputRef = useRef<HTMLInputElement>(null);
	const [isDragging, setIsDragging] = useState(false);

	const submit = (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		submission.onSubmit();
	};

	const handlePaste = (event: ClipboardEvent<HTMLTextAreaElement>) => {
		const files = Array.from(event.clipboardData?.files ?? []);
		if (files.length === 0) return;
		event.preventDefault();
		attachments.onAddFiles(files);
	};

	const handleDrop = (event: DragEvent<HTMLFormElement>) => {
		event.preventDefault();
		setIsDragging(false);
		const files = Array.from(event.dataTransfer?.files ?? []);
		if (files.length > 0) attachments.onAddFiles(files);
	};

	const handleDragOver = (event: DragEvent<HTMLFormElement>) => {
		if (Array.from(event.dataTransfer?.items ?? []).some((item) => item.kind === "file")) {
			event.preventDefault();
			setIsDragging(true);
		}
	};

	return (
		<form
			onSubmit={submit}
			className="composer-prompt-surface flex flex-col transition-[background-color,box-shadow]"
			data-dragging={isDragging || undefined}
			onDrop={handleDrop}
			onDragOver={handleDragOver}
			onDragLeave={(event) => {
				const nextTarget = event.relatedTarget;
				if (!(nextTarget instanceof Node) || !event.currentTarget.contains(nextTarget)) setIsDragging(false);
			}}
		>
			<label className="sr-only" htmlFor={promptId}>
				{labels.task}
			</label>
			<textarea
				id={promptId}
				autoFocus={autoFocusPrompt}
				className="min-h-(--size-composer-prompt-min) w-full resize-none bg-transparent px-4 pb-3 pt-4 text-md leading-relaxed text-foreground outline-none placeholder:text-passive"
				placeholder={labels.taskPlaceholder}
				value={prompt}
				onChange={(event) => onPromptChange(event.target.value)}
				onPaste={handlePaste}
				onKeyDown={(event) => {
					if (event.key === "Enter" && !event.shiftKey && !event.altKey && !event.nativeEvent.isComposing) {
						event.preventDefault();
						event.currentTarget.form?.requestSubmit();
					}
				}}
			/>

			<ul className="scrollbar-none flex w-full flex-row flex-nowrap items-center gap-2 overflow-x-auto px-3 pt-1.5 pb-2">
				<li className="shrink-0">
					<button
						type="button"
						className="flex size-14 cursor-pointer items-center justify-center rounded-lg border-2 border-dashed border-border text-muted-foreground transition-colors hover:border-accent hover:bg-surface hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring data-[dragging=true]:border-accent data-[dragging=true]:bg-surface"
						data-dragging={isDragging || undefined}
						aria-label={labels.addFile}
						onClick={() => fileInputRef.current?.click()}
					>
						<Plus className="size-5" aria-hidden="true" />
					</button>
				</li>

				{attachments.items.map((attachment) => (
					<li key={attachment.id} className="shrink-0">
						{attachment.previewUrl ? (
							<div className="relative size-14 rounded-lg border border-border bg-surface overflow-hidden group">
								<img
									src={attachment.previewUrl}
									alt=""
									className="size-full object-cover"
								/>
								<button
									type="button"
									className="absolute top-1 right-1 grid size-4.5 place-items-center rounded-full bg-background/80 text-muted-foreground hover:bg-background hover:text-foreground shadow-sm transition-colors"
									aria-label={labels.removeFile(attachment.name)}
									onClick={() => attachments.onRemove(attachment.id)}
								>
									<X className="size-3" aria-hidden="true" />
								</button>
							</div>
						) : (
							<div className="relative flex h-14 min-w-36 max-w-48 items-center gap-2 rounded-lg border border-border bg-surface pl-2.5 pr-8 py-1.5 text-xs text-foreground group">
								<FileText
									className="size-7.5 shrink-0 rounded bg-input/60 p-1.5 text-muted-foreground"
									aria-hidden="true"
								/>
								<div className="min-w-0 flex-1 flex flex-col justify-center">
									<span className="truncate font-semibold leading-tight" title={attachment.name}>
										{attachment.name}
									</span>
									<span className="text-[10px] text-muted-foreground leading-normal mt-0.5 truncate">
										File
									</span>
								</div>
								<button
									type="button"
									className="absolute top-1 right-1 grid size-4.5 place-items-center rounded-full bg-background border border-border text-muted-foreground hover:bg-muted hover:text-foreground shadow-sm transition-colors"
									aria-label={labels.removeFile(attachment.name)}
									onClick={() => attachments.onRemove(attachment.id)}
								>
									<X className="size-3" aria-hidden="true" />
								</button>
							</div>
						)}
					</li>
				))}
			</ul>
			<input
				ref={fileInputRef}
				type="file"
				multiple
				className="hidden"
				onChange={(event) => {
					if (event.target.files) attachments.onAddFiles(Array.from(event.target.files));
					event.target.value = "";
				}}
			/>
			{attachments.error && (
				<p className="px-4 pb-2 text-caption text-destructive" role="alert">{attachments.error}</p>
			)}

			{(submission.error || submission.modelWarning) && (
				<div className="px-3 pb-2">
					{submission.error && (
						<div
							className="flex items-center justify-between gap-3 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive"
							role="alert"
						>
							<span>{submission.error}</span>
							{submission.canCreateAsTui ? (
								<button
									type="button"
									disabled={submission.isSubmitting}
									onClick={submission.onSubmitAsTui}
									className="inline-flex h-control-md shrink-0 items-center justify-center rounded-md border border-border bg-background px-2.5 text-xs text-foreground transition-colors hover:bg-muted disabled:pointer-events-none disabled:opacity-50"
								>
									{labels.createAsTui}
								</button>
							) : null}
						</div>
					)}
					{!submission.error && submission.modelWarning && (
						<p className="text-caption text-warning" role="status">{submission.modelWarning}</p>
					)}
				</div>
			)}

			<div className="composer-toolbar">
				<div className="composer-run-controls" role="group" aria-label={labels.runsWith}>
					<div className="composer-toolbar-slot">
						{renderAgentControl({ ...agent, id: agentId })}
					</div>
					<span className="composer-toolbar-divider" aria-hidden="true" />
					<div className="composer-toolbar-slot">
						{renderModelControl({ ...model, id: modelId })}
					</div>
				</div>

				<button
					type="submit"
					disabled={submission.isSubmitting || !canSubmit}
					className="inline-flex h-(--size-settings-action-height) min-w-(--size-composer-start-button) shrink-0 items-center justify-center gap-1.5 rounded-md bg-primary px-3 text-xs text-primary-foreground transition-colors hover:bg-primary/80 disabled:pointer-events-none disabled:opacity-50"
				>
					{submission.isSubmitting ? <Loader2 className="size-icon-base animate-spin" aria-hidden="true" /> : null}
					{submission.isSubmitting ? labels.starting : labels.start}
				</button>
			</div>
		</form>
	);
}
