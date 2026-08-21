import {
	type ComponentType,
	type FormEvent,
	type HTMLAttributes,
	type KeyboardEvent,
	type ReactNode,
	useEffect,
	useRef,
	useState,
} from "react";
import { cn } from "./utils";
import type { ProjectRepositorySummary } from "./project-models";

type ProjectExternalLink = ComponentType<{
	children: ReactNode;
	className?: string;
	href: string;
	title?: string;
}>;

export type ProjectSource = "clone" | "local" | "workspace";

export type ProjectSourcePickerLabels = {
	title: string;
	description: string;
	clone: string;
	cloneDescription: string;
	cloneExample: string;
	cloneBranchExample: string;
	local: string;
	localDescription: string;
	localExample: string;
	localBranchExample: string;
	workspace: string;
	workspaceDescription: string;
	close: string;
};

export type ProjectSourcePickerViewProps = {
	arrowIcon?: ReactNode;
	cloneIcon?: ReactNode;
	closeIcon?: ReactNode;
	dialog?: boolean;
	disabled: boolean;
	folderIcon?: ReactNode;
	labels: ProjectSourcePickerLabels;
	onClose?: () => void;
	onSelect: (source: ProjectSource) => void;
	workspaceIcon?: ReactNode;
};

export function ProjectSourcePickerView({
	arrowIcon,
	cloneIcon,
	closeIcon,
	dialog = false,
	disabled,
	folderIcon,
	labels,
	onClose,
	onSelect,
	workspaceIcon,
}: ProjectSourcePickerViewProps) {
	return (
		<div
			className="relative isolate flex w-full max-w-(--size-import-modal-max) flex-col items-stretch gap-6 rounded-welcome-panel border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-modal)] p-(--size-import-modal-padding) shadow-[var(--shadow-import-modal)]"
			role={dialog ? undefined : "group"}
			aria-label={dialog ? undefined : labels.title}
		>
			<div className={cn("relative z-[1] flex flex-col items-start gap-1", onClose && "pr-10")}>
				<h2 className="import-title text-balance">{labels.title}</h2>
				<p className="import-description text-pretty">{labels.description}</p>
			</div>
			<div className="relative z-[2] grid grid-cols-1 gap-4 self-stretch sm:grid-cols-2 sm:gap-6">
				<ProjectSourceButton
					description={labels.cloneDescription}
					disabled={disabled}
					icon={cloneIcon}
					kind="clone"
					labels={labels}
					onClick={() => onSelect("clone")}
				/>
				<ProjectSourceButton
					description={labels.localDescription}
					disabled={disabled}
					icon={folderIcon}
					kind="local"
					labels={labels}
					onClick={() => onSelect("local")}
				/>
			</div>
			<button
				type="button"
				aria-label={labels.workspace}
				className="relative z-[2] flex min-h-18 w-full items-center gap-4 rounded-welcome-panel border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] px-5 py-4 text-left transition-colors hover:bg-[var(--color-bg-import-card-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:pointer-events-none disabled:opacity-50"
				disabled={disabled}
				onClick={() => onSelect("workspace")}
			>
				<span className="grid size-10 shrink-0 place-items-center rounded-lg border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-chip)] text-[var(--color-text-import-muted)]">
					{workspaceIcon}
				</span>
				<span className="min-w-0 flex-1">
					<span className="block text-[15px] font-bold leading-5 text-[var(--color-text-import-title)]">
						{labels.workspace}
					</span>
					<span className="mt-1 block text-pretty text-[13px] leading-5 text-[var(--color-text-import-muted)]">
						{labels.workspaceDescription}
					</span>
				</span>
				<span className="shrink-0 text-[var(--color-text-import-muted)]" aria-hidden="true">
					{arrowIcon}
				</span>
			</button>
			{onClose && (
				<button
					type="button"
					className="import-close-button"
					aria-label={labels.close}
					disabled={disabled}
					onClick={onClose}
				>
					{closeIcon}
				</button>
			)}
		</div>
	);
}

function ProjectSourceButton({
	description,
	disabled,
	icon,
	kind,
	labels,
	onClick,
}: {
	description: string;
	disabled: boolean;
	icon?: ReactNode;
	kind: Exclude<ProjectSource, "workspace">;
	labels: ProjectSourcePickerLabels;
	onClick: () => void;
}) {
	const isClone = kind === "clone";
	const title = isClone ? labels.clone : labels.local;
	const example = isClone ? labels.cloneExample : labels.localExample;
	const branch = isClone ? labels.cloneBranchExample : labels.localBranchExample;
	return (
		<button
			type="button"
			aria-label={title}
			className="flex min-h-56 w-full flex-col justify-start gap-5 rounded-welcome-panel border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] p-6 text-left transition-colors hover:bg-[var(--color-bg-import-card-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 disabled:pointer-events-none disabled:opacity-50"
			disabled={disabled}
			onClick={onClick}
		>
			<span className="flex min-h-24 w-full items-center justify-center">
				<span className="flex w-full max-w-[300px] flex-col overflow-hidden rounded-lg border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-illustration)]">
					<span className="flex min-w-0 items-center gap-2 border-b border-[var(--color-border-import-modal)] px-4 py-3 text-[var(--color-text-import-muted)]">
						<span className="shrink-0">{icon}</span>
						<span className="truncate font-mono text-[12px] leading-4">{example}</span>
					</span>
					<span className="flex items-center gap-2 px-4 py-3">
						<span className="size-2 shrink-0 rounded-full bg-accent-strong" aria-hidden="true" />
						<span className="font-mono text-[12px] font-bold leading-4 text-[var(--color-text-import-title)]">
							{branch}
						</span>
					</span>
				</span>
			</span>
			<span className="mt-auto flex w-full flex-col items-start gap-2">
				<span className="text-[16px] font-bold leading-6 text-[var(--color-text-import-title)]">
					{title}
				</span>
				<span className="text-pretty text-[14px] font-normal leading-[23px] text-[var(--color-text-import-muted)]">
					{description}
				</span>
			</span>
		</button>
	);
}

export type ProjectSetupHeaderTextProps = {
	children: ReactNode;
	className?: string;
};

export function ProjectSetupHeaderView({
	CloseButton,
	Description,
	Title,
	closeIcon,
	closeLabel,
	disabled,
	leadingAction,
	path,
	title,
}: {
	CloseButton: ComponentType<{ "aria-label": string; children: ReactNode; disabled: boolean }>;
	Description: ComponentType<ProjectSetupHeaderTextProps>;
	Title: ComponentType<ProjectSetupHeaderTextProps>;
	closeIcon: ReactNode;
	closeLabel: string;
	disabled: boolean;
	leadingAction?: ReactNode;
	path: string;
	title: string;
}) {
	return (
		<div className="flex items-start justify-between gap-4 border-b border-[var(--color-border-agents-sheet)] p-(--size-import-dialog-padding)">
			{leadingAction}
			<div className="min-w-0">
				<Title className="text-subtitle font-semibold text-[var(--color-text-agents-sheet-title)]">
					{title}
				</Title>
				<Description className="mt-1 break-all text-xs text-[var(--color-text-agents-sheet-description)]">
					{path}
				</Description>
			</div>
			<CloseButton aria-label={closeLabel} disabled={disabled}>
				{closeIcon}
			</CloseButton>
		</div>
	);
}

export type ProjectSetupAlert = {
	icon?: ReactNode;
	message: string;
	title: string;
	tone: "warning" | "error";
};

export function ProjectSetupFormView({
	agentControls,
	agents,
	alert,
	canSubmit,
	intakeControl,
	isBusy,
	onCancel,
	onSubmit,
	setupNotice,
	submitLabel,
	cancelLabel,
}: {
	agentControls: { worker: ReactNode; orchestrator: ReactNode };
	agents: {
		cacheMessage: string;
		error?: string | null;
		loading: boolean;
		loadingMessage: string;
		onRetry?: () => void;
		refreshing?: boolean;
		retryLabel?: string;
	};
	alert?: ProjectSetupAlert | null;
	canSubmit: boolean;
	intakeControl: ReactNode;
	isBusy: boolean;
	onCancel: () => void;
	onSubmit: () => void;
	setupNotice?: { message: string; warning?: string | null } | null;
	submitLabel: string;
	cancelLabel: string;
}) {
	const submit = (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		if (canSubmit) onSubmit();
	};
	return (
		<form className="space-y-5 p-(--size-import-dialog-padding)" onSubmit={submit}>
			<div className="grid gap-4 sm:grid-cols-2">
				{agentControls.worker}
				{agentControls.orchestrator}
			</div>

			{agents.loading && (
				<p className="text-xs leading-row text-[var(--color-text-agents-sheet-description)]" role="status">
					{agents.loadingMessage}
				</p>
			)}

			<p className="text-xs leading-row text-[var(--color-text-agents-sheet-description)]">
				{agents.cacheMessage}
			</p>

			{agents.error && (
				<div
					className="flex items-center justify-between gap-3 rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs leading-row text-destructive"
					role="alert"
				>
					<span>{agents.error}</span>
					{agents.onRetry && (
						<button
							type="button"
							className="shrink-0 rounded text-[var(--color-text-agents-sheet-title)] underline-offset-2 hover:underline disabled:pointer-events-none disabled:opacity-50"
							disabled={agents.refreshing}
							onClick={agents.onRetry}
						>
							{agents.retryLabel}
						</button>
					)}
				</div>
			)}

			<div className="border-t border-[var(--color-border-agents-sheet)] pt-5">{intakeControl}</div>

			{setupNotice && (
				<div className="rounded-lg border border-[var(--color-border-agents-sheet)] bg-[var(--color-bg-agents-sheet-control)]/80 px-3 py-2.5 text-xs leading-body-md text-[var(--color-text-agents-sheet-description)]">
					<p>{setupNotice.message}</p>
					{setupNotice.warning && <p className="mt-2 text-warning">{setupNotice.warning}</p>}
				</div>
			)}

			{alert && (
				<div
					role="alert"
					className={
						alert.tone === "warning"
							? "flex gap-2 rounded-lg border border-warning/30 bg-warning/10 px-3 py-2.5 text-xs leading-body-md"
							: "flex gap-2 rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2.5 text-xs leading-body-md"
					}
				>
					{alert.icon}
					<div className="min-w-0 space-y-0.5">
						<p
							className={
								alert.tone === "warning"
									? "font-medium text-[var(--color-text-agents-sheet-title)]"
									: "font-medium text-destructive"
							}
						>
							{alert.title}
						</p>
						<p className="text-[var(--color-text-agents-sheet-description)]">{alert.message}</p>
					</div>
				</div>
			)}

			<div className="flex items-center justify-end gap-3 pt-1">
				<button
					className="settings-footer-button disabled:pointer-events-none disabled:opacity-50"
					type="button"
					disabled={isBusy}
					onClick={onCancel}
				>
					{cancelLabel}
				</button>
				<button
					className="settings-footer-button settings-footer-button-primary disabled:pointer-events-none disabled:opacity-50"
					type="submit"
					disabled={!canSubmit}
				>
					{submitLabel}
				</button>
			</div>
		</form>
	);
}

export function ProjectSettingsSection({
	children,
	grouped,
	title,
	titleHidden,
}: {
	children: ReactNode;
	grouped?: boolean;
	title: string;
	titleHidden?: boolean;
}) {
	return (
		<section className="flex w-full flex-col items-stretch gap-(--size-settings-section-inner-gap)">
			{!titleHidden && (
				<h2 className="px-3 text-xs font-medium leading-4 text-settings-muted">
					{title}
				</h2>
			)}
			<div
				className={cn(
					"w-full",
					grouped
						? "settings-grouped-rows flex w-full flex-col"
						: "flex w-full flex-col gap-1.5",
				)}
			>
				{children}
			</div>
		</section>
	);
}

export function ProjectSettingsRow({
	children,
	className,
	icon,
	label,
}: {
	children: ReactNode;
	className?: string;
	icon?: ReactNode;
	label: string;
}) {
	return (
		<div className={cn("settings-row-bar", className)}>
			<div className="flex shrink-0 items-center gap-(--size-settings-row-icon-gap)">
				{icon}
				<span className="whitespace-nowrap text-sm leading-5 text-settings-label">{label}</span>
			</div>
			<div className="flex min-w-0 flex-1 items-center justify-end">{children}</div>
		</div>
	);
}

export function ProjectSettingsInputRow({
	editIcon,
	editLabel,
	icon,
	id,
	label,
	onChange,
	placeholder,
	value,
}: {
	editIcon?: ReactNode;
	editLabel: string;
	icon?: ReactNode;
	id: string;
	label: string;
	onChange: (value: string) => void;
	placeholder?: string;
	value: string;
}) {
	const [editing, setEditing] = useState(false);
	const inputRef = useRef<HTMLInputElement>(null);

	useEffect(() => {
		if (!editing) return;
		inputRef.current?.focus();
		inputRef.current?.select();
	}, [editing]);

	const finishEditing = () => setEditing(false);
	const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
		if (event.key === "Enter") {
			event.preventDefault();
			finishEditing();
		} else if (event.key === "Escape") {
			event.preventDefault();
			event.stopPropagation();
			finishEditing();
		}
	};

	return (
		<ProjectSettingsRow icon={icon} label={label}>
			{editing ? (
				<input
					ref={inputRef}
					id={id}
					aria-label={label}
					className="settings-inline-edit-input"
					value={value}
					onChange={(event) => onChange(event.target.value)}
					onBlur={finishEditing}
					onKeyDown={onKeyDown}
					placeholder={placeholder}
				/>
			) : (
				<button
					type="button"
					className="settings-inline-edit-trigger"
					aria-label={editLabel}
					onClick={() => setEditing(true)}
				>
					<span className="settings-row-value" title={value || placeholder}>
						{value || placeholder}
					</span>
					{editIcon}
				</button>
			)}
		</ProjectSettingsRow>
	);
}

export function ProjectSettingsValueRow({
	externalLink: ExternalLink,
	href,
	icon,
	label,
	value,
}: {
	externalLink?: ProjectExternalLink;
	href?: string;
	icon?: ReactNode;
	label: string;
	value: string;
}) {
	return (
		<ProjectSettingsRow icon={icon} label={label}>
			{href && ExternalLink ? (
				<ExternalLink
					href={href}
					className="settings-row-value text-settings-accent hover:underline"
					title={value}
				>
					{value}
				</ExternalLink>
			) : (
				<span className="settings-row-value" title={value}>
					{value}
				</span>
			)}
		</ProjectSettingsRow>
	);
}

export function ProjectGeneralSettingsView({
	displayName,
	externalLink,
	icons,
	labels,
	onDisplayNameChange,
	project,
}: {
	displayName: string;
	externalLink?: ProjectExternalLink;
	icons?: Partial<Record<"edit" | "name" | "id" | "kind" | "path" | "repo" | "workspaceRepo", ReactNode>>;
	labels: {
		title: string;
		name: string;
		id: string;
		kind: string;
		path: string;
		repo: string;
		workspaceRepos: string;
		workspaceReposEmpty: string;
		editName: string;
	};
	onDisplayNameChange: (value: string) => void;
	project: {
		id: string;
		kindLabel: string;
		path: string;
		pathHref?: string;
		repo: string;
		repoHref?: string;
		workspaceRepos?: ProjectRepositorySummary[];
	};
}) {
	return (
		<>
			<ProjectSettingsSection title={labels.title} titleHidden grouped>
				<ProjectSettingsInputRow
					editIcon={icons?.edit}
					editLabel={labels.editName}
					icon={icons?.name}
					label={labels.name}
					id="projectName"
					value={displayName}
					onChange={onDisplayNameChange}
				/>
				<ProjectSettingsValueRow icon={icons?.id} label={labels.id} value={project.id} />
				<ProjectSettingsValueRow icon={icons?.kind} label={labels.kind} value={project.kindLabel} />
				<ProjectSettingsValueRow externalLink={externalLink} href={project.pathHref} icon={icons?.path} label={labels.path} value={project.path} />
				<ProjectSettingsValueRow externalLink={externalLink} href={project.repoHref} icon={icons?.repo} label={labels.repo} value={project.repo || "—"} />
			</ProjectSettingsSection>
			{project.workspaceRepos && (
				<ProjectSettingsSection title={labels.workspaceRepos} grouped>
					{project.workspaceRepos.length > 0 ? (
						project.workspaceRepos.map((repo) => (
							<ProjectSettingsRow key={repo.name} icon={icons?.workspaceRepo} label={repo.name}>
								<span className="settings-row-value">
									{repo.relativePath}
									{repo.repo ? ` · ${repo.repo}` : ""}
								</span>
							</ProjectSettingsRow>
						))
					) : (
						<p className="px-1 text-xs text-settings-muted">{labels.workspaceReposEmpty}</p>
					)}
				</ProjectSettingsSection>
			)}
		</>
	);
}

export function ProjectAgentsSettingsView({
	missingRequiredMessage,
	orchestratorArea,
	orchestratorModelArea,
	permissions,
	title,
	workerArea,
	workerModelArea,
}: {
	missingRequiredMessage?: string | null;
	orchestratorArea: ReactNode;
	orchestratorModelArea: ReactNode;
	permissions: { control: ReactNode; icon?: ReactNode; label: string };
	title: string;
	workerArea: ReactNode;
	workerModelArea: ReactNode;
}) {
	return (
		<ProjectSettingsSection title={title} titleHidden grouped>
			{workerArea}
			{workerModelArea}
			{orchestratorArea}
			{orchestratorModelArea}
			<ProjectSettingsRow icon={permissions.icon} label={permissions.label}>
				{permissions.control}
			</ProjectSettingsRow>
			{missingRequiredMessage && (
				<p className="px-1 text-xs leading-row text-error" role="alert">{missingRequiredMessage}</p>
			)}
		</ProjectSettingsSection>
	);
}

export function ProjectWorkflowSettingsView({
	branch,
	icons,
	labels,
	onBranchChange,
	onPrefixChange,
	prefix,
	reviewerControl,
	reviewerWarning,
}: {
	branch: string;
	icons?: Partial<Record<"branch" | "edit" | "prefix" | "reviewer", ReactNode>>;
	labels: {
		worktrees: string;
		defaultBranch: string;
		sessionPrefix: string;
		reviewers: string;
		defaultReviewer: string;
		editDefaultBranch: string;
		editSessionPrefix: string;
	};
	onBranchChange: (value: string) => void;
	onPrefixChange: (value: string) => void;
	prefix: string;
	reviewerControl?: ReactNode;
	reviewerWarning?: string | null;
}) {
	return (
		<>
			<ProjectSettingsSection title={labels.worktrees} grouped>
				<ProjectSettingsInputRow
					editIcon={icons?.edit}
					editLabel={labels.editDefaultBranch}
					icon={icons?.branch}
					label={labels.defaultBranch}
					id="defaultBranch"
					value={branch}
					placeholder="auto"
					onChange={onBranchChange}
				/>
				<ProjectSettingsInputRow
					editIcon={icons?.edit}
					editLabel={labels.editSessionPrefix}
					icon={icons?.prefix}
					label={labels.sessionPrefix}
					id="sessionPrefix"
					value={prefix}
					placeholder="ao"
					onChange={onPrefixChange}
				/>
			</ProjectSettingsSection>
			{reviewerControl && (
				<ProjectSettingsSection title={labels.reviewers} grouped>
					<ProjectSettingsRow icon={icons?.reviewer} label={labels.defaultReviewer}>
						{reviewerControl}
					</ProjectSettingsRow>
					{reviewerWarning && (
						<p className="px-1 text-xs leading-row text-warning" role="status">
							{reviewerWarning}
						</p>
					)}
				</ProjectSettingsSection>
			)}
		</>
	);
}

export type ProjectSettingsFormProps = Omit<HTMLAttributes<HTMLFormElement>, "onSubmit"> & {
	onSubmit: () => void;
};

export function ProjectSettingsFormView({
	children,
	className,
	onSubmit,
	...props
}: ProjectSettingsFormProps) {
	return (
		<form
			{...props}
			className={cn("flex w-full flex-col gap-(--size-settings-section-gap)", className)}
			onSubmit={(event) => {
				event.preventDefault();
				onSubmit();
			}}
		>
			{children}
		</form>
	);
}
