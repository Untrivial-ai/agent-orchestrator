import * as Dialog from "@radix-ui/react-dialog";
import { ProjectSourcePickerView, type ProjectSource } from "@aoagents/product-ui";
import { useTranslation } from "react-i18next";
import {
	ArrowRight,
	CheckCircle2,
	ChevronRight,
	Folder,
	FolderOpen,
	FolderPlus,
	Folders,
	GitFork,
	X,
	XCircle,
} from "lucide-react";
import { lazy, Suspense, useEffect, useRef, useState, type ReactNode } from "react";
import type { ImportFolderScan } from "../../preload";
import { aoBridge } from "../lib/bridge";
import { cn } from "../lib/utils";
import type { ProjectKind } from "../types/workspace";
import { CreateProjectAgentSheet, type CreateProjectAgentSelection } from "./CreateProjectAgentSheet";
import type { CloneRepositoryDetails, CloneRepositorySelection } from "./CloneRepositoryDialog";
import { Button } from "./ui/button";

export type CreateProjectInput = { path: string; asWorkspace?: boolean } & CreateProjectAgentSelection;
export type CloneProjectInput = Pick<CloneRepositorySelection, "remoteUrl" | "destinationParent"> &
	CreateProjectAgentSelection;

const CloneRepositoryDialog = lazy(() => import("./CloneRepositoryDialog"));
const LAST_CLONE_DESTINATION_KEY = "ao.clone.lastDestinationParent";

type CreateProjectFlowMode = ProjectKind | "choose";

// Shared create-project flow. Local projects/workspaces use the native folder
// picker; remote projects progressively reveal a lazily loaded clone form.
// Every source converges on the same agent sheet and project-start behavior.
export function CreateProjectFlow({
	children,
	droppedPath,
	embedded = false,
	idleLabel,
	mode = "single_repo",
	onCloneProject,
	onCreateProject,
	onInitializeProject,
	openSignal,
}: {
	children?: (state: { choosePath: () => void; disabled: boolean; error: string | null; label: string }) => ReactNode;
	// A folder was dropped on the app window (ShellLayout owns the global
	// listener). Mirrors openSignal but carries a path: skips straight to the
	// mode picker with the native OS dialog step skipped.
	droppedPath?: { path: string; nonce: number } | null;
	// When true, render the Workspace/Project chooser inline (start page) instead
	// of behind a trigger + dialog. Folder validation + agent sheet stay modal.
	embedded?: boolean;
	idleLabel?: string;
	mode?: CreateProjectFlowMode;
	onCloneProject: (input: CloneProjectInput) => Promise<void>;
	onCreateProject: (input: CreateProjectInput) => Promise<void>;
	onInitializeProject: (path: string) => Promise<void>;
	// Monotonic counter: each new value opens the flow programmatically (the ⌘N
	// "no project in scope" fallback). Lets the shortcut reuse the sidebar's own
	// create-project flow instead of a separate delegating component.
	openSignal?: number;
}) {
	const { t } = useTranslation();
	const resolvedIdleLabel = idleLabel ?? t("createProject.newProject");
	const [error, setError] = useState<string | null>(null);
	const [modePickerOpen, setModePickerOpen] = useState(false);
	const [cloneDialogOpen, setCloneDialogOpen] = useState(false);
	const [cloneDetails, setCloneDetails] = useState<CloneRepositoryDetails>(() => ({
		remoteUrl: "",
		destinationParent:
			typeof window === "undefined" ? "" : (window.localStorage.getItem(LAST_CLONE_DESTINATION_KEY) ?? ""),
	}));
	const [cloneSelection, setCloneSelection] = useState<CloneRepositorySelection | null>(null);
	const [folderPickerOpen, setFolderPickerOpen] = useState(false);
	const [selectedKind, setSelectedKind] = useState<ProjectKind>(mode === "workspace" ? "workspace" : "single_repo");
	const [selectedPath, setSelectedPath] = useState<string | null>(null);
	const [validationScan, setValidationScan] = useState<ImportFolderScan | null>(null);
	const [isChoosingPath, setIsChoosingPath] = useState(false);
	const [isCreating, setIsCreating] = useState(false);
	const [isInitializing, setIsInitializing] = useState(false);
	const [repositorySetup, setRepositorySetup] = useState<"NOT_A_GIT_REPO" | "PROJECT_UNBORN" | null>(null);
	const [repositorySetupWarning, setRepositorySetupWarning] = useState<string | null>(null);
	// A path that arrived via droppedPath, staged until the user confirms
	// Workspace vs Project. Consumed exactly once by openFolderStep.
	const [pendingDropPath, setPendingDropPath] = useState<string | null>(null);

	const hasModePicker = mode === "choose";
	const isBusy = isChoosingPath || isCreating || isInitializing;

	const selectSource = (source: ProjectSource) => {
		const presetPath = pendingDropPath;
		setPendingDropPath(null);
		setError(null);
		setValidationScan(null);
		if (source === "clone") {
			setModePickerOpen(false);
			setCloneDialogOpen(true);
			return;
		}
		setCloneSelection(null);
		// Keep the selector mounted behind the native picker. Closing it first
		// exposes a blank compositor frame on Windows before Explorer takes focus.
		void chooseDirectory(source === "workspace" ? "workspace" : "single_repo", presetPath ?? undefined);
	};

	const chooseDirectory = async (kind: ProjectKind, presetPath?: string) => {
		setError(null);
		setValidationScan(null);
		setRepositorySetup(null);
		setRepositorySetupWarning(null);
		setSelectedKind(kind);
		setIsChoosingPath(true);
		try {
			const path =
				presetPath ??
				(await aoBridge.app.chooseDirectory(
					kind === "workspace" ? t("createProject.chooseWorkspace") : t("createProject.chooseRepo"),
				));
			if (path && kind === "single_repo") {
				const preflight = await projectRepositoryPreflight(path);
				if (preflight.blockingError) {
					setError(preflight.blockingError);
					setValidationScan(preflight.scan);
					setModePickerOpen(false);
					setFolderPickerOpen(true);
					return;
				}
				setRepositorySetup(preflight.setupCode);
				setRepositorySetupWarning(preflight.setupWarning);
			}
			if (path && kind === "workspace") {
				try {
					const warning = await aoBridge.app.checkAncestorRepo(path);
					if (warning) {
						setRepositorySetupWarning(warning);
						setRepositorySetup("NOT_A_GIT_REPO");
					}
				} catch {
					// Ancestor check failed — proceed without warning
				}
				// Preflight: scan workspace children and require at least one
				// fully resolved repo before advancing to harness selection.
				try {
					const scan = await aoBridge.app.scanImportFolder({ path, mode: "workspace" });
					const hasReadyRepo = scan.repos.some(
						(repo) => repo.status !== "error" && repo.hasRemote && !repo.needsGitInit,
					);
					if (!hasReadyRepo) {
						setError(
							scan.repos.length === 0
								? t("createProject.workspaceNoRepos")
								: t("createProject.workspaceNoReadyRepos"),
						);
						setValidationScan(scan);
						setModePickerOpen(false);
						setFolderPickerOpen(true);
						return;
					}
				} catch {
					// Scan failed — fall through and let the backend validate.
				}
			}
			if (path) {
				setModePickerOpen(false);
				setSelectedPath(path);
				setFolderPickerOpen(false);
			}
		} catch (err) {
			setError(err instanceof Error ? err.message : t("createProject.couldNotAdd"));
		} finally {
			setIsChoosingPath(false);
		}
	};

	const startFlow = (presetPath?: string) => {
		setPendingDropPath(presetPath ?? null);
		if (hasModePicker) {
			setError(null);
			setCloneSelection(null);
			setModePickerOpen(true);
			return;
		}
		void chooseDirectory(mode, presetPath);
	};

	// Seed with the current value so we never open on mount; open when it changes.
	const lastOpenSignal = useRef(openSignal);
	useEffect(() => {
		if (openSignal === undefined || openSignal === lastOpenSignal.current) return;
		lastOpenSignal.current = openSignal;
		startFlow();
	}, [openSignal]);

	// A folder was dropped on the app window. Ignored while the flow already has
	// UI on screen so an in-progress manual selection is never silently discarded.
	const lastDropNonce = useRef(droppedPath?.nonce);
	useEffect(() => {
		if (!droppedPath || droppedPath.nonce === lastDropNonce.current) return;
		lastDropNonce.current = droppedPath.nonce;
		if (isBusy || modePickerOpen || cloneDialogOpen || folderPickerOpen || selectedPath !== null) return;
		startFlow(droppedPath.path);
	}, [droppedPath]);

	const createProject = async (selection: CreateProjectAgentSelection) => {
		if (!selectedPath) return;
		setError(null);
		setIsCreating(true);
		try {
			if (cloneSelection) {
				await onCloneProject({
					remoteUrl: cloneSelection.remoteUrl,
					destinationParent: cloneSelection.destinationParent,
					...selection,
				});
				setSelectedPath(null);
				setCloneSelection(null);
				return;
			}
			if (selectedKind === "single_repo" && repositorySetup) {
				setIsCreating(false);
				setIsInitializing(true);
				await onInitializeProject(selectedPath);
				setRepositorySetup(null);
				setRepositorySetupWarning(null);
				setIsInitializing(false);
				setIsCreating(true);
			}
			await onCreateProject({ path: selectedPath, asWorkspace: selectedKind === "workspace", ...selection });
			setSelectedPath(null);
		} catch (err) {
			const code = err instanceof Error && "code" in err ? (err.code as string | undefined) : undefined;
			const message = err instanceof Error ? err.message : t("createProject.couldNotAdd");
			if (!cloneSelection && selectedKind === "single_repo" && isRepositorySetupRecoveryCode(code)) {
				setRepositorySetup(code);
			}
			setError(message);
			if (hasModePicker && !cloneSelection) {
				if (shouldScanCreateFailure(message)) {
					try {
						const scan = await aoBridge.app.scanImportFolder({
							path: selectedPath,
							mode: selectedKind === "workspace" ? "workspace" : "project",
						});
						setValidationScan(scan);
					} catch {
						setValidationScan({ path: selectedPath, repos: [] });
					}
				} else {
					setValidationScan(null);
				}
				setSelectedPath(null);
				setFolderPickerOpen(true);
			}
		} finally {
			setIsCreating(false);
			setIsInitializing(false);
		}
	};

	const label = isChoosingPath
		? t("createProject.opening")
		: isInitializing
			? hasModePicker
				? t("createProject.initializing")
				: t("createProject.settingUp")
			: isCreating
				? t("createProject.creating")
				: resolvedIdleLabel;

	return (
		<>
			{!embedded &&
				children?.({
					// Zero-arg wrapper: callers wire this directly to onClick, whose
					// SyntheticEvent would otherwise be forwarded as startFlow's
					// presetPath and get treated as a dropped path.
					choosePath: () => startFlow(),
					disabled: isBusy,
					error,
					label,
				})}
			{hasModePicker && embedded && !modePickerOpen && !cloneDialogOpen && selectedPath === null && (
				<div className="flex w-full flex-col items-center gap-3">
					<ImportSourcePicker disabled={isBusy} onSelect={selectSource} />
					{error && !folderPickerOpen && selectedPath === null && (
						<p className="text-caption leading-body text-error" role="status">
							{error}
						</p>
					)}
				</div>
			)}
			{hasModePicker && (
				<>
					<CreateProjectSourceDialog
						disabled={isBusy}
						open={modePickerOpen}
						onOpenChange={(open) => {
							if (isBusy) return;
							setModePickerOpen(open);
							// Dismissed without picking a kind — don't let a stale dropped
							// path hijack the next manual "New Project" click.
							if (!open) setPendingDropPath(null);
						}}
						onSelect={selectSource}
					/>
					{cloneDialogOpen ? (
						<Suspense fallback={<CloneRepositoryDialogSkeleton />}>
							<CloneRepositoryDialog
								disabled={isBusy}
								error={error}
								onBack={() => {
									setError(null);
									setCloneDialogOpen(false);
									if (!embedded) window.requestAnimationFrame(() => setModePickerOpen(true));
								}}
								onChange={(next) => {
									setCloneDetails(next);
									setError(null);
								}}
								onClose={() => {
									setCloneDialogOpen(false);
									setError(null);
								}}
								onContinue={(next) => {
									setCloneSelection(next);
									setSelectedKind("single_repo");
									setSelectedPath(next.targetPath);
									setCloneDialogOpen(false);
								}}
								open
								value={cloneDetails}
							/>
						</Suspense>
					) : null}
					<CreateProjectFolderDialog
						disabled={isBusy}
						error={error}
						kind={selectedKind}
						open={folderPickerOpen}
						scan={validationScan}
						onBack={() => {
							setError(null);
							setValidationScan(null);
							setFolderPickerOpen(false);
							if (!embedded) {
								window.requestAnimationFrame(() => setModePickerOpen(true));
							}
						}}
						onChooseFolder={() => void chooseDirectory(selectedKind)}
						onOpenChange={(open) => {
							if (!isBusy) {
								setFolderPickerOpen(open);
								if (!open) {
									setError(null);
									setValidationScan(null);
								}
							}
						}}
					/>
				</>
			)}
			<CreateProjectAgentSheet
				action={cloneSelection ? "clone" : "create"}
				error={error}
				isCreating={isCreating}
				isInitializing={isInitializing}
				kind={selectedKind}
				onOpenChange={(open) => {
					if (!open) {
						setSelectedPath(null);
						setCloneSelection(null);
						if (!folderPickerOpen) {
							setError(null);
						}
					}
				}}
				onBack={
					cloneSelection
						? () => {
								setSelectedPath(null);
								setCloneDialogOpen(true);
							}
						: undefined
				}
				onSubmit={createProject}
				open={selectedPath !== null}
				path={selectedPath}
				repositorySetupNeeded={repositorySetup !== null}
				repositorySetupWarning={repositorySetupWarning}
			/>
			{error && !hasModePicker && (
				<span className="sr-only" role="status">
					{error}
				</span>
			)}
		</>
	);
}

function isRepositorySetupRecoveryCode(code: string | undefined): code is "NOT_A_GIT_REPO" | "PROJECT_UNBORN" {
	return code === "NOT_A_GIT_REPO" || code === "PROJECT_UNBORN";
}

type RepositorySetupCode = "NOT_A_GIT_REPO" | "PROJECT_UNBORN";

type ProjectRepositoryPreflight = {
	blockingError: string | null;
	scan: ImportFolderScan | null;
	setupCode: RepositorySetupCode | null;
	setupWarning: string | null;
};

async function projectRepositoryPreflight(path: string): Promise<ProjectRepositoryPreflight> {
	try {
		const scan = await aoBridge.app.scanImportFolder({ path, mode: "project" });
		const reason = scan.repos[0]?.reason ?? "";
		if (reason.startsWith("Selected folder is inside AO's internal data directory.")) {
			return {
				blockingError: reason,
				scan,
				setupCode: null,
				setupWarning: null,
			};
		}
		if (scan.repos.length === 0) {
			return { blockingError: null, scan, setupCode: "NOT_A_GIT_REPO", setupWarning: scan.setupWarning ?? null };
		}
		return {
			blockingError: null,
			scan,
			setupCode: reason === "Repository must have at least one commit." ? "PROJECT_UNBORN" : null,
			setupWarning: null,
		};
	} catch {
		return { blockingError: null, scan: null, setupCode: null, setupWarning: null };
	}
}

function shouldScanCreateFailure(message: string): boolean {
	if (/daemon|server|conflict|already exists|not ready|start|orchestrator|permission denied/i.test(message))
		return false;
	if (/\b(?:PATH|ID)_ALREADY_REGISTERED\b/i.test(message) || /already registered/i.test(message)) return false;
	return /workspace|repo|repository|git|path|folder|worktree|bare|branch|commit|remote/i.test(message);
}

function CreateProjectSourceDialog({
	disabled,
	onOpenChange,
	onSelect,
	open,
}: {
	disabled: boolean;
	onOpenChange: (open: boolean) => void;
	onSelect: (source: ProjectSource) => void;
	open: boolean;
}) {
	return (
		<Dialog.Root open={open} onOpenChange={onOpenChange}>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay data-[state=open]:animate-overlay-in" />
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay w-[min(var(--size-import-modal-max),calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 border-0 bg-transparent p-0 shadow-none outline-none data-[state=open]:animate-modal-in">
					<ImportSourcePicker disabled={disabled} onClose={() => onOpenChange(false)} onSelect={onSelect} dialog />
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}

/** Shared source chooser for first-run and subsequent project creation. */
function ImportSourcePicker({
	dialog = false,
	disabled,
	onClose,
	onSelect,
}: {
	dialog?: boolean;
	disabled: boolean;
	onClose?: () => void;
	onSelect: (source: ProjectSource) => void;
}) {
	const { t } = useTranslation();
	return (
		<>
			{dialog && (
				<>
					<Dialog.Title className="sr-only">{t("createProject.addCodeTitle")}</Dialog.Title>
					<Dialog.Description className="sr-only">{t("createProject.addCodeDescription")}</Dialog.Description>
				</>
			)}
			<ProjectSourcePickerView
				dialog={dialog}
				disabled={disabled}
				onClose={onClose}
				onSelect={onSelect}
				closeIcon={<X className="size-5" aria-hidden="true" strokeWidth={1.67} />}
				arrowIcon={<ArrowRight className="size-4" aria-hidden="true" />}
				cloneIcon={<GitFork className="size-[14px] shrink-0" aria-hidden="true" />}
				folderIcon={<FolderOpen className="size-[14px] shrink-0" aria-hidden="true" />}
				workspaceIcon={<Folders className="size-5" aria-hidden="true" />}
				labels={{
					title: t("createProject.addCodeTitle"),
					description: t("createProject.addCodeDescription"),
					clone: t("createProject.cloneFromGit"),
					cloneDescription: t("createProject.cloneFromGitDesc"),
					cloneExample: "github.com/acme/web-app",
					cloneBranchExample: "origin / main",
					local: t("createProject.openLocal"),
					localDescription: t("createProject.openLocalDesc"),
					localExample: "~/Development/web-app",
					localBranchExample: "main",
					workspace: t("createProject.addWorkspace"),
					workspaceDescription: t("createProject.workspaceDesc"),
					close: t("createProject.closeDialog"),
				}}
			/>
		</>
	);
}

function CloneRepositoryDialogSkeleton() {
	const { t } = useTranslation();
	return (
		<Dialog.Root open>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay" />
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay w-[min(var(--size-import-folder-dialog),calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 overflow-hidden rounded-welcome-panel border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-modal)] p-0 shadow-[var(--shadow-import-modal)]">
					<Dialog.Title className="sr-only">{t("createProject.cloneTitle")}</Dialog.Title>
					<Dialog.Description className="sr-only">{t("createProject.cloneDescription")}</Dialog.Description>
					<div className="border-b border-[var(--color-border-import-modal)] p-(--size-import-dialog-padding)">
						<div className="h-5 w-40 rounded bg-[var(--color-bg-import-chip)]" />
						<div className="mt-2 h-3 w-72 max-w-full rounded bg-[var(--color-bg-import-chip)]" />
					</div>
					<div className="space-y-5 p-(--size-import-dialog-padding)">
						<div className="h-11 rounded-md bg-[var(--color-bg-import-card)]" />
						<div className="h-11 rounded-md bg-[var(--color-bg-import-card)]" />
					</div>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}

function CreateProjectFolderDialog({
	disabled,
	error,
	kind,
	onBack,
	onChooseFolder,
	onOpenChange,
	open,
	scan,
}: {
	disabled: boolean;
	error: string | null;
	kind: ProjectKind;
	onBack: () => void;
	onChooseFolder: () => void;
	onOpenChange: (open: boolean) => void;
	open: boolean;
	scan: ImportFolderScan | null;
}) {
	const { t } = useTranslation();
	const isWorkspace = kind === "workspace";
	const failedRepos = scan?.repos.filter((repo) => (repo.status === "error" || !repo.hasRemote) && !repo.needsGitInit) ?? [];
	const hasScan = scan !== null;
	const footerMessage =
		failedRepos.length > 0
			? t("createProject.footerResolve", { count: failedRepos.length })
			: hasScan
				? t("createProject.footerReview")
				: t("createProject.footerChoose");
	return (
		<Dialog.Root open={open} onOpenChange={onOpenChange}>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay data-[state=open]:animate-overlay-in" />
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay flex max-h-[min(var(--size-import-folder-dialog),calc(100svh-24px))] w-[min(var(--size-import-folder-dialog),calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-welcome-panel border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-modal)] p-0 text-[var(--color-text-import-title)] shadow-[var(--shadow-import-modal)] data-[state=open]:animate-modal-in">
					<div className="flex shrink-0 items-start gap-3 border-b border-[var(--color-border-import-modal)] p-(--size-import-dialog-padding) sm:gap-4">
						<Button
							type="button"
							variant="outline"
							size="icon"
							aria-label={t("createProject.backToType")}
							disabled={disabled}
							onClick={onBack}
						>
							<ChevronRight className="size-4 rotate-180" aria-hidden="true" />
						</Button>
						<div className="min-w-0 flex-1">
							<Dialog.Title className="text-[18px] font-semibold text-[var(--color-text-import-title)]">
								{isWorkspace ? t("createProject.importWorkspace") : t("createProject.importProject")}
							</Dialog.Title>
							<Dialog.Description className="mt-1 max-w-[520px] text-[13px] font-medium leading-5 text-[var(--color-text-import-muted)]">
								{isWorkspace ? t("createProject.importWorkspaceDesc") : t("createProject.importProjectDesc")}
							</Dialog.Description>
						</div>
						<Dialog.Close asChild>
							<button
								type="button"
								className="settings-close-button"
								aria-label={t("createProject.closeImport")}
								disabled={disabled}
							>
								<X className="size-4" aria-hidden="true" />
							</button>
						</Dialog.Close>
					</div>
					<div className="min-h-0 overflow-y-auto p-(--size-import-dialog-padding)">
						{hasScan ? (
							<div className="space-y-4">
								<div className="flex items-center gap-3 rounded-lg border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] p-4">
									<Folder className="size-5 shrink-0 text-[var(--color-text-import-muted)]" aria-hidden="true" />
									<div className="min-w-0 flex-1">
										<div className="truncate font-mono text-[14px] font-semibold text-[var(--color-text-import-title)]">
											{displayImportPath(scan.path)}
										</div>
										<div className="mt-0.5 text-[12px] text-[var(--color-text-import-muted)]">
											{isWorkspace ? t("createProject.workspaceRoot") : t("createProject.projectFolder")}
										</div>
									</div>
									<Button type="button" variant="footer" disabled={disabled} onClick={onChooseFolder}>
										{t("createProject.change")}
									</Button>
								</div>

								{error && (
									<div className="rounded-lg border border-destructive/40 bg-destructive/10">
										<div className="border-b border-destructive/30 px-4 py-3 font-mono text-[12px] font-semibold uppercase tracking-[0.12em] text-destructive">
											<span className="mr-2 inline-block size-2 rounded-full bg-destructive" aria-hidden="true" />
											{isWorkspace ? t("createProject.importFailedWorkspace") : t("createProject.importFailedProject")}
										</div>
										<div className="px-4 py-3 text-[12px] leading-5 text-destructive">{error}</div>
										{failedRepos.length > 0 && (
											<div className="border-t border-destructive/30">
												{failedRepos.map((repo) => (
													<ImportRepoRow key={repo.path} repo={repo} failed />
												))}
											</div>
										)}
									</div>
								)}

							{scan.repos
								.filter((repo) => (repo.status !== "error" && repo.hasRemote) || repo.needsGitInit)
								.map((repo) => (
										<div
											key={repo.path}
											className="rounded-lg border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)]"
										>
											<ImportRepoRow repo={repo} />
										</div>
									))}

								{scan.repos.length === 0 && (
									<div className="rounded-lg border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] p-4 text-[12px] text-[var(--color-text-import-muted)]">
										{t("createProject.noRepos")}
									</div>
								)}
							</div>
						) : (
							<button
								type="button"
								className="flex min-h-[132px] w-full flex-col items-center justify-center rounded-lg border border-dashed border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] p-6 text-center transition-colors hover:bg-[var(--color-bg-import-card-hover)] disabled:pointer-events-none disabled:opacity-50 sm:min-h-[160px]"
								disabled={disabled}
								onClick={onChooseFolder}
							>
								<span className="mb-4 grid size-11 place-items-center rounded-xl bg-[var(--color-bg-import-chip)] text-[var(--color-text-import-muted)]">
									<FolderPlus className="size-5" aria-hidden="true" />
								</span>
								<span className="text-[15px] font-semibold text-[var(--color-text-import-title)]">
									{isWorkspace ? t("createProject.chooseFolder") : t("createProject.chooseProjectFolder")}
								</span>
								<span className="mt-2 max-w-full text-pretty text-[12px] text-[var(--color-text-import-muted)] sm:text-[13px]">
									{isWorkspace ? t("createProject.pickerWorkspaceHint") : t("createProject.pickerProjectHint")}
								</span>
							</button>
						)}
						{error && !hasScan && (
							<div
								className={cn(
									"mt-4 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-3 text-[12px] leading-5 text-destructive",
								)}
							>
								{error}
							</div>
						)}
					</div>
					<div className="flex shrink-0 flex-col gap-3 border-t border-[var(--color-border-import-modal)] p-(--size-import-dialog-padding) sm:flex-row sm:items-center sm:justify-between">
						<p className="text-[12px] font-medium text-[var(--color-text-import-muted)]">{footerMessage}</p>
						<div className="flex flex-wrap items-center justify-end gap-3">
							<Button type="button" variant="footer" disabled={disabled} onClick={() => onOpenChange(false)}>
								{t("createProject.cancel")}
							</Button>
						</div>
					</div>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}

function ImportRepoRow({ failed = false, repo }: { failed?: boolean; repo: ImportFolderScan["repos"][number] }) {
	const { t } = useTranslation();
	return (
		<div className="flex items-center gap-3 p-4">
			{failed ? (
				<XCircle className="size-5 shrink-0 text-destructive" aria-hidden="true" />
			) : (
				<CheckCircle2 className="size-5 shrink-0 text-success" aria-hidden="true" />
			)}
			<div className="min-w-0 flex-1">
				<div className="truncate text-[14px] font-semibold text-[var(--color-text-import-title)]">{repo.name}</div>
				<div className="mt-0.5 truncate font-mono text-[12px] text-[var(--color-text-import-muted)]">
					{displayImportPath(repo.path)}
				</div>
			</div>
			<div className="hidden max-w-[260px] shrink-0 truncate text-right font-mono text-[12px] text-[var(--color-text-import-muted)] sm:block">
				{repo.needsGitInit
					? "Needs git init"
					: failed
						? (repo.reason ?? t("createProject.repoCannotImport"))
						: `${repo.branch} ${remoteDisplay(repo.remote)}`}
			</div>
		</div>
	);
}

function displayImportPath(value: string) {
	return value.replace(/^\/Users\/[^/]+/, "~");
}

function remoteDisplay(remote: string) {
	const ssh = remote.match(/^[^@]+@([^:]+):(.+)$/);
	if (ssh?.[1] && ssh[2]) return `${ssh[1]}/${ssh[2].replace(/\.git$/, "")}`;
	try {
		const url = new URL(remote);
		return `${url.host}${url.pathname.replace(/\.git$/, "")}`;
	} catch {
		return remote.replace(/^https?:\/\//, "").replace(/\.git$/, "");
	}
}
