import * as Dialog from "@radix-ui/react-dialog";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import {
	CheckCircle2,
	CircleDashed,
	ChevronRight,
	Cloud,
	Folder,
	FolderClosed,
	FolderPlus,
	Folders,
	GitBranch,
	GitFork,
	Link2,
	X,
	XCircle,
} from "lucide-react";
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import type { components } from "../../api/schema";
import type { ImportFolderScan } from "../../preload";
import { useCloudCp } from "../hooks/useCloudCp";
import { useCloudGate } from "../hooks/useCloudGate";
import { useCloudOrg } from "../hooks/useCloudOrg";
import { cloudProjectsQueryKey } from "../hooks/useWorkspaceQuery";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { aoBridge } from "../lib/bridge";
import { useCloudSession } from "../lib/cloud-session";
import { cn } from "../lib/utils";
import type { ProjectKind } from "../types/workspace";
import { CreateProjectAgentSheet, type CreateProjectAgentSelection } from "./CreateProjectAgentSheet";
import CloneRepositoryDialog, { type CloneRepositoryDetails, type CloneRepositorySelection } from "./CloneRepositoryDialog";
import CreateProjectProgressDialog from "./CreateProjectProgressDialog";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Tabs, TabsList, TabsTrigger } from "./ui/tabs";

export type CreateProjectInput = { path: string; asWorkspace?: boolean; defaultBranch?: string } & CreateProjectAgentSelection;
export type CloneProjectInput = Pick<CloneRepositorySelection, "remoteUrl" | "destinationParent"> &
	CreateProjectAgentSelection;

const LAST_CLONE_DESTINATION_KEY = "ao.clone.lastDestinationParent";
const LAST_IMPORT_REMOTE_URL_KEY = "ao.import.lastRemoteUrl";
const LAST_IMPORT_REMOTE_OWNER_KEY = "ao.import.lastRemoteOwner";
type ImportValidationResult = components["schemas"]["ImportValidationResult"];
type RepoGitStatus = components["schemas"]["RepoGitStatus"];
type GitPreparationEvent = components["schemas"]["GitPreparationEvent"];
type WorkspaceApprovalState = Record<string, string[]>;
type WorkspaceRemoteState = Record<string, string>;
type DisplayImportRepo = ImportFolderScan["repos"][number] & {
	requiredActions: string[];
	blockingErrors: string[];
	isRepo?: boolean;
	hasCommit?: boolean;
	hasOrigin?: boolean;
};

type CreateProjectFlowMode = ProjectKind | "choose";
type ProjectSource = "clone" | "local" | "workspace";

/** Where the new project should live: on this machine or in AO Cloud. */
type ProjectOffering = "local" | "cloud";
type CreateProgressStage = "starting" | "connecting" | "creating" | "settingUp" | "finishing" | "complete";

function createProgressMessage(stage: CreateProgressStage, workspace: boolean): string {
	switch (stage) {
		case "starting": return "Preparing the project";
		case "connecting": return "Connecting to the repository";
		case "creating": return workspace ? "Creating the workspace" : "Creating the project";
		case "settingUp": return "Setting up the project";
		case "finishing": return "Finishing project setup";
		default: return "Project created";
	}
}

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
	sourceSignal,
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
	// Home-page action cards: each new nonce jumps straight to clone/local/workspace.
	sourceSignal?: { source: ProjectSource; nonce: number } | null;
}) {
	const { t } = useTranslation();
	const resolvedIdleLabel = idleLabel ?? t("createProject.newProject");
	const [error, setError] = useState<string | null>(null);
	const [modePickerOpen, setModePickerOpen] = useState(false);
	const [cloneDialogOpen, setCloneDialogOpen] = useState(false);
	const [cloneDialogClosing, setCloneDialogClosing] = useState(false);
	const [cloneDetails, setCloneDetails] = useState<CloneRepositoryDetails>(() => ({
		remoteUrl: "",
		destinationParent:
			typeof window === "undefined" ? "" : (window.localStorage.getItem(LAST_CLONE_DESTINATION_KEY) ?? ""),
	}));
	const [cloneSelection, setCloneSelection] = useState<CloneRepositorySelection | null>(null);
	const [folderPickerOpen, setFolderPickerOpen] = useState(false);
	const [childTransitioning, setChildTransitioning] = useState(false);
	const [selectedKind, setSelectedKind] = useState<ProjectKind>(mode === "workspace" ? "workspace" : "single_repo");
	const [selectedPath, setSelectedPath] = useState<string | null>(null);
	const [validationScan, setValidationScan] = useState<ImportFolderScan | null>(null);
	const [workspaceValidation, setWorkspaceValidation] = useState<ImportValidationResult | null>(null);
	const [workspaceApprovedActions, setWorkspaceApprovedActions] = useState<WorkspaceApprovalState>({});
	const [workspaceRemoteUrls, setWorkspaceRemoteUrls] = useState<WorkspaceRemoteState>({});
	const [workspacePrepEvents, setWorkspacePrepEvents] = useState<GitPreparationEvent[]>([]);
	const [isChoosingPath, setIsChoosingPath] = useState(false);
	const [isCreating, setIsCreating] = useState(false);
	const [isInitializing, setIsInitializing] = useState(false);
	const [isPreparingGit, setIsPreparingGit] = useState(false);
	const [createProgressOpen, setCreateProgressOpen] = useState(false);
	const [createProgress, setCreateProgress] = useState(0);
	const [createProgressStage, setCreateProgressStage] = useState<CreateProgressStage>("starting");
	const [repositorySetup, setRepositorySetup] = useState<"NOT_A_GIT_REPO" | "PROJECT_UNBORN" | null>(null);
	const [repositorySetupWarning, setRepositorySetupWarning] = useState<string | null>(null);
	// A path that arrived via droppedPath, staged until the user confirms
	// Workspace vs Project. Consumed exactly once by openFolderStep.
	const [pendingDropPath, setPendingDropPath] = useState<string | null>(null);

	// The Local | Cloud choice renders whenever this deployment offers cloud
	// (cloudEnabled). Actually creating a cloud project also needs the user
	// signed in (cloudAvailable); when they aren't, the Cloud tab shows a
	// sign-in prompt instead of the create form so the option is always
	// discoverable rather than silently absent.
	const { cloudEnabled } = useCloudGate();
	const { status: cloudSessionStatus, signIn: cloudSignIn } = useCloudSession();
	const cloudAvailable = cloudEnabled && cloudSessionStatus === "authenticated";
	const [offering, setOffering] = useState<ProjectOffering>("local");

	const hasModePicker = mode === "choose";
	const isBusy = isChoosingPath || isCreating || isInitializing || isPreparingGit;

	const resetWorkspaceImportState = () => {
		setWorkspaceValidation(null);
		setWorkspaceApprovedActions({});
		setWorkspaceRemoteUrls({});
		setWorkspacePrepEvents([]);
	};

	useEffect(() => {
		if (!createProgressOpen) return;
		const startedAt = Date.now();
		const updateProgress = () => {
			const elapsed = Date.now() - startedAt;
			if (elapsed < 800) {
				setCreateProgressStage("starting");
				setCreateProgress(Math.min(12, 4 + elapsed / 100));
			} else if (elapsed < 1800) {
				setCreateProgressStage("connecting");
				setCreateProgress(12 + ((elapsed - 800) / 1000) * 18);
			} else if (elapsed < 5000) {
				setCreateProgressStage("creating");
				setCreateProgress(30 + ((elapsed - 1800) / 3200) * 38);
			} else if (elapsed < 7600) {
				setCreateProgressStage("settingUp");
				setCreateProgress(68 + ((elapsed - 5000) / 2600) * 17);
			} else {
				setCreateProgressStage("finishing");
				setCreateProgress(Math.min(90, 85 + (elapsed - 7600) / 1000));
			}
		};
		updateProgress();
		const timer = window.setInterval(updateProgress, 250);
		return () => window.clearInterval(timer);
	}, [createProgressOpen]);

	const transitionToChild = (open: () => void) => {
		setChildTransitioning(true);
		window.setTimeout(() => {
			open();
			setChildTransitioning(false);
		}, 80);
	};

	const selectSource = (source: ProjectSource) => {
		const presetPath = pendingDropPath;
		setPendingDropPath(null);
		setError(null);
		setValidationScan(null);
		resetWorkspaceImportState();
		if (source === "clone") {
			transitionToChild(() => setCloneDialogOpen(true));
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
		resetWorkspaceImportState();
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
					transitionToChild(() => setFolderPickerOpen(true));
					return;
				}
				setRepositorySetup(preflight.setupCode);
				setRepositorySetupWarning(preflight.setupWarning);
			}
			if (path && kind === "workspace") {
				try {
					const [validation, scan, ancestorWarning] = await Promise.all([
						validateImportFolder(path, "workspace"),
						aoBridge.app.scanImportFolder({ path, mode: "workspace" }),
						aoBridge.app.checkAncestorRepo(path).catch(() => undefined),
					]);
					setWorkspaceValidation(validation);
					setWorkspaceApprovedActions(defaultWorkspaceApprovedActions(validation));
					setWorkspaceRemoteUrls(await defaultWorkspaceRemoteUrls(validation));
					setValidationScan(scan);
					const nestedWarning = ancestorWarning ?? scan.setupWarning ?? null;
					setRepositorySetupWarning(nestedWarning);
					if (nestedWarning) setRepositorySetup("NOT_A_GIT_REPO");
					const blockingReason = scan.repos.find((repo) => repo.status === "error")?.reason;
					const validationError = !validation.isValid || validation.nextStep === "error"
						? importValidationMessage(validation)
						: blockingReason ?? null;
					setError(validationError);
				} catch (err) {
					setValidationScan({ path, repos: [] });
					setError(err instanceof Error ? err.message : t("createProject.couldNotAdd"));
				}
				transitionToChild(() => setFolderPickerOpen(true));
				return;
			}
			if (path && hasModePicker && !presetPath) {
				try {
					const scan = await aoBridge.app.scanImportFolder({
						path,
						mode: kind === "workspace" ? "workspace" : "project",
					});
					setValidationScan(scan);
					const blockingReason = scan.repos.find(
						(repo) => repo.status === "error" && repo.reason !== "Repository must have at least one commit.",
					)?.reason;
					setError(blockingReason ?? null);
				} catch (err) {
					setValidationScan({ path, repos: [] });
					setError(err instanceof Error ? err.message : t("createProject.couldNotAdd"));
				}
				transitionToChild(() => setFolderPickerOpen(true));
				return;
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
		// Each entry starts on the default Local choice, never a leftover Cloud one.
		setOffering("local");
		resetWorkspaceImportState();
		if (hasModePicker) {
			setError(null);
			setCloneSelection(null);
			setModePickerOpen(true);
			return;
		}
		void chooseDirectory(mode, presetPath);
	};

	// Cloud create finished: the list refetch is already invalidated by the
	// form; just close the picker and fall back to the default Local choice.
	const onCloudProjectCreated = () => {
		setModePickerOpen(false);
		setOffering("local");
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

	const lastSourceNonce = useRef(sourceSignal?.nonce);
	useEffect(() => {
		if (!sourceSignal || sourceSignal.nonce === lastSourceNonce.current) return;
		lastSourceNonce.current = sourceSignal.nonce;
		if (isBusy || modePickerOpen || cloneDialogOpen || folderPickerOpen || selectedPath !== null) return;
		selectSource(sourceSignal.source);
	}, [sourceSignal]);

	const createProject = async (selection: CreateProjectAgentSelection) => {
		if (!selectedPath) return;
		setError(null);
		setIsCreating(true);
		const showProgress = Boolean(cloneSelection) || selectedKind === "workspace";
		if (showProgress) {
			setCreateProgress(0);
			setCreateProgressStage("starting");
			setCreateProgressOpen(true);
		}
		try {
			if (cloneSelection) {
				await onCloneProject({
					remoteUrl: cloneSelection.remoteUrl,
					destinationParent: cloneSelection.destinationParent,
					...selection,
				});
				setSelectedPath(null);
				setCloneSelection(null);
				if (showProgress) {
					setCreateProgress(100);
					setCreateProgressStage("complete");
					await new Promise((resolve) => window.setTimeout(resolve, 180));
				}
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
			const defaultBranch = cloneSelection ? undefined : await aoBridge.app.getRepositoryBranch(selectedPath);
			await onCreateProject({
				path: selectedPath,
				asWorkspace: selectedKind === "workspace",
				...(defaultBranch ? { defaultBranch } : {}),
				...selection,
			});
			setSelectedPath(null);
			if (showProgress) {
				setCreateProgress(100);
				setCreateProgressStage("complete");
				await new Promise((resolve) => window.setTimeout(resolve, 180));
			}
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
						const importMode = selectedKind === "workspace" ? "workspace" : "project";
						const scanPromise = aoBridge.app.scanImportFolder({
							path: selectedPath,
							mode: importMode,
						});
						if (importMode === "workspace") {
							const [validation, scan] = await Promise.all([
								validateImportFolder(selectedPath, "workspace"),
								scanPromise,
							]);
							setWorkspaceValidation(validation);
							setWorkspaceApprovedActions(defaultWorkspaceApprovedActions(validation));
							setWorkspaceRemoteUrls(await defaultWorkspaceRemoteUrls(validation));
							setWorkspacePrepEvents([]);
							setValidationScan(scan);
						} else {
							setValidationScan(await scanPromise);
						}
					} catch {
						setValidationScan({ path: selectedPath, repos: [] });
						resetWorkspaceImportState();
					}
				} else {
					setValidationScan(null);
					resetWorkspaceImportState();
				}
				setSelectedPath(null);
				setFolderPickerOpen(true);
			}
		} finally {
			setCreateProgressOpen(false);
			setIsCreating(false);
			setIsInitializing(false);
		}
	};

	const continueWorkspaceImport = async () => {
		if (!workspaceValidation) return;
		setError(null);
		if (workspaceValidation.warning) {
			setSelectedKind("single_repo");
			setFolderPickerOpen(false);
			setModePickerOpen(false);
			setSelectedPath(workspaceValidation.root.repoPath);
			return;
		}
		if (workspaceValidation.nextStep === "prepare_git") {
			setIsPreparingGit(true);
			try {
				const repositories = (workspaceValidation.childRepos ?? [])
					.filter((repo) => repo.requiredActions.length > 0)
					.map((repo) => ({
						repoPath: repo.repoPath,
						approvedActions: workspaceApprovedActions[repo.repoPath] ?? [],
						remoteUrl: workspaceRemoteUrls[repo.repoPath]?.trim() || undefined,
					}));
				setWorkspacePrepEvents(workspaceRequestedActionEvents(repositories));
				const { data, error: apiError } = await apiClient.POST("/api/v1/imports/prepare-git", {
					body: {
						importKind: "workspace",
						path: workspaceValidation.root.repoPath,
						repositories,
					},
				});
				if (apiError || !data) throw new Error(apiErrorMessage(apiError, "Could not prepare this workspace."));
				setWorkspacePrepEvents(data.events);
				setWorkspaceValidation(data.validation);
				setWorkspaceApprovedActions(defaultWorkspaceApprovedActions(data.validation));
				const nextRemoteUrls = await defaultWorkspaceRemoteUrls(data.validation);
				setWorkspaceRemoteUrls((current) => ({
					...nextRemoteUrls,
					...current,
				}));
				const failedStep = data.events.find((event) => event.state === "error");
				if (failedStep) {
					setError(workspacePreparationFailureMessage(failedStep));
					return;
				}
				if (!data.validation.isValid || data.validation.nextStep === "error") {
					setError(importValidationMessage(data.validation));
					return;
				}
				if (repositories.some((repo) => repo.remoteUrl)) {
					Object.values(workspaceRemoteUrls).forEach((remoteUrl) => {
						if (remoteUrl.trim() !== "") persistSuggestedProjectRemoteUrl(remoteUrl);
					});
				}
				if (data.validation.nextStep === "continue") {
					setFolderPickerOpen(false);
					setModePickerOpen(false);
					setSelectedPath(data.validation.root.repoPath);
				}
			} catch (err) {
				setError(err instanceof Error ? err.message : t("createProject.couldNotAdd"));
			} finally {
				setIsPreparingGit(false);
			}
			return;
		}
		setFolderPickerOpen(false);
		setModePickerOpen(false);
		setSelectedPath(workspaceValidation.root.repoPath);
	};

	const label = isInitializing
		? hasModePicker
			? t("createProject.initializing")
			: t("createProject.settingUp")
		: isPreparingGit
			? t("createProject.settingUp")
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
			<CreateProjectFlowBackdrop open={modePickerOpen || cloneDialogOpen || folderPickerOpen || selectedPath !== null || createProgressOpen || childTransitioning} />
			{hasModePicker && embedded && !modePickerOpen && !cloneDialogOpen && selectedPath === null && (
				<div className="flex w-full flex-col items-center gap-3">
					{cloudEnabled && (
						<ProjectOfferingTabs disabled={isBusy} offering={offering} onOfferingChange={setOffering} />
					)}
					{cloudEnabled && offering === "cloud" ? (
						cloudAvailable ? (
							<CloudProjectCard onCreated={onCloudProjectCreated} />
						) : (
							<CloudSignInPanel disabled={isBusy} onSignIn={cloudSignIn} />
						)
					) : (
						<ImportSourcePicker disabled={isBusy} onSelect={selectSource} />
					)}
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
						childOpen={childTransitioning || cloneDialogOpen || folderPickerOpen}
						cloudAvailable={cloudAvailable}
						cloudEnabled={cloudEnabled}
						disabled={isBusy}
						offering={offering}
						onCloudCreated={onCloudProjectCreated}
						onOfferingChange={setOffering}
						onSignIn={cloudSignIn}
						open={modePickerOpen}
						onOpenChange={(open) => {
							if (isBusy) return;
							setModePickerOpen(open);
							// Dismissed without picking a kind — don't let a stale dropped
							// path hijack the next manual "New Project" click, and reopen
							// on the default Local choice.
							if (!open) {
								setPendingDropPath(null);
								setOffering("local");
							}
						}}
						onSelect={selectSource}
					/>
					{cloneDialogOpen || cloneDialogClosing ? (
						<CloneRepositoryDialog
							disabled={isBusy}
							error={error}
							onBack={() => {
								setError(null);
								setCloneDialogOpen(false);
								setModePickerOpen(true);
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
								setModePickerOpen(false);
								setCloneDialogOpen(false);
								setCloneDialogClosing(true);
								setChildTransitioning(true);
								setCloneDialogOpen(false);
								window.setTimeout(() => {
									setCloneDialogClosing(false);
									setSelectedPath(next.targetPath);
									setChildTransitioning(false);
								}, 80);
							}}
							open={cloneDialogOpen}
							value={cloneDetails}
						/>
					) : null}
					<CreateProjectFolderDialog
						approvedActions={workspaceApprovedActions}
						disabled={isBusy}
						error={error}
						events={workspacePrepEvents}
						kind={selectedKind}
						isPreparingGit={isPreparingGit}
						open={folderPickerOpen}
						remoteUrls={workspaceRemoteUrls}
						scan={validationScan}
						validation={workspaceValidation}
						onChangeApprovedActions={(repoPath, actions) =>
							setWorkspaceApprovedActions((current) => ({ ...current, [repoPath]: actions }))
						}
						onChangeRemoteUrl={(repoPath, remoteUrl) =>
							setWorkspaceRemoteUrls((current) => ({ ...current, [repoPath]: remoteUrl }))
						}
						onContinue={() => {
							if (selectedKind === "workspace") {
								void continueWorkspaceImport();
								return;
							}
							if (!validationScan || error) return;
							setFolderPickerOpen(false);
							setSelectedPath(validationScan.path);
							setModePickerOpen(false);
						}}
						onBack={() => {
							setError(null);
							setValidationScan(null);
							resetWorkspaceImportState();
							setFolderPickerOpen(false);
						}}
						onChooseFolder={() => void chooseDirectory(selectedKind)}
						onOpenChange={(open) => {
							if (!isBusy) {
								setFolderPickerOpen(open);
								if (!open) {
									setError(null);
									setValidationScan(null);
									resetWorkspaceImportState();
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
				open={selectedPath !== null && !createProgressOpen}
				path={selectedPath}
				repositorySetupNeeded={repositorySetup !== null}
				repositorySetupWarning={repositorySetupWarning}
			/>
			<CreateProjectProgressDialog
				message={createProgressMessage(createProgressStage, selectedKind === "workspace")}
				onCancel={() => setCreateProgressOpen(false)}
				open={createProgressOpen}
				progress={createProgress}
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

async function validateImportFolder(path: string, importKind: "project" | "workspace"): Promise<ImportValidationResult> {
	const { data, error } = await apiClient.POST("/api/v1/imports/validate", { body: { importKind, path } });
	if (error || !data) throw new Error(apiErrorMessage(error, "Could not validate this folder."));
	return data;
}

function importValidationMessage(result: ImportValidationResult): string {
	if (result.blockingErrors.length === 0) return "This folder cannot be imported yet.";
	return result.blockingErrors.map(importBlockingErrorLabel).join(" ");
}

function importBlockingErrorLabel(code: string): string {
	switch (code) {
		case "INVALID_PATH":
			return "Choose a folder AO can read.";
		case "PATH_NOT_DIRECTORY":
			return "Choose a folder, not a file.";
		case "BARE_REPOSITORY":
			return "Choose a normal working checkout instead of a bare Git repository.";
		case "UNSUPPORTED_GIT_METADATA":
			return "Repair the Git metadata or choose a different folder.";
		case "CHILD_REPO_SCAN_FAILED":
			return "AO could not inspect the repositories under this folder.";
		case "WORKSPACE_CHILD_REPO_REQUIRED":
			return "Initialize at least one direct child repository with a commit and origin remote before importing this workspace.";
		default:
			return "Choose a different folder or repair the repository before continuing.";
	}
}

function defaultWorkspaceApprovedActions(validation: ImportValidationResult): WorkspaceApprovalState {
	const approved: WorkspaceApprovalState = {};
	for (const repo of validation.childRepos ?? []) {
		if (repo.requiredActions.length > 0) approved[repo.repoPath] = [...repo.requiredActions];
	}
	return approved;
}

async function defaultWorkspaceRemoteUrls(validation: ImportValidationResult): Promise<WorkspaceRemoteState> {
	const remoteUrls: WorkspaceRemoteState = {};
	const owner = await suggestedProjectRemoteOwner();
	for (const repo of validation.childRepos ?? []) {
		if (repo.requiredActions.includes("set_remote")) remoteUrls[repo.repoPath] = suggestedProjectRemoteUrl(repo.repoPath, owner);
	}
	return remoteUrls;
}

function workspaceRequestedActionEvents(
	repositories: Array<{ repoPath: string; approvedActions: string[] }>,
): GitPreparationEvent[] {
	return repositories.flatMap((repo) =>
		orderedGitActions(repo.approvedActions).map((action, index) => ({
			repoPath: repo.repoPath,
			action: action as GitPreparationEvent["action"],
			state: index === 0 ? "running" : "pending",
		})),
	);
}

function workspacePreparationFailureMessage(event: GitPreparationEvent): string {
	return `${displayImportPath(event.repoPath)} failed while running ${gitActionLabel(event.action)}. Review the step below, then retry or choose a different folder.`;
}

function gitActionLabel(action: string): string {
	switch (action) {
		case "git_init":
			return "Git initialization";
		case "git_commit":
			return "Initial commit";
		case "set_remote":
			return "Remote setup";
		default:
			return "Git setup";
	}
}

function orderedGitActions(actions: string[]): string[] {
	const rank = new Map([
		["git_init", 0],
		["git_commit", 1],
		["set_remote", 2],
	]);
	return [...actions].sort((left, right) => (rank.get(left) ?? Number.MAX_SAFE_INTEGER) - (rank.get(right) ?? Number.MAX_SAFE_INTEGER));
}

function latestWorkspaceActionState(repoPath: string, action: string, events: GitPreparationEvent[]): string {
	for (let index = events.length - 1; index >= 0; index -= 1) {
		const event = events[index];
		if (event?.repoPath === repoPath && event.action === action) return event.state;
	}
	return "required";
}

function suggestedProjectRemoteUrl(repoPath: string, owner: string): string {
	const repoName = repoPath.split(/[\\/]/).filter(Boolean).pop()?.trim();
	const saved = window.localStorage.getItem(LAST_IMPORT_REMOTE_URL_KEY)?.trim() ?? "";
	if (!repoName) return saved;
	const withGitSuffix = repoName.endsWith(".git") ? repoName : `${repoName}.git`;
	if (saved === "") return `https://github.com/${owner}/${withGitSuffix}`;
	const sshMatch = saved.match(/^git@([^:]+):([^/]+)\/([^/]+?)(\.git)?$/);
	if (sshMatch) return `git@${sshMatch[1]}:${owner}/${withGitSuffix}`;
	try {
		const parsed = new URL(saved);
		const segments = parsed.pathname.split("/").filter(Boolean);
		if (segments.length >= 2) {
			segments[segments.length - 2] = owner;
			segments[segments.length - 1] = withGitSuffix;
			parsed.pathname = `/${segments.join("/")}`;
			return parsed.toString();
		}
	} catch {
		return `https://github.com/${owner}/${withGitSuffix}`;
	}
	return `https://github.com/${owner}/${withGitSuffix}`;
}

async function suggestedProjectRemoteOwner(): Promise<string> {
	if (typeof window === "undefined") return "org";
	const githubLogin = (await aoBridge.app.getGitHubLogin()).trim();
	if (githubLogin !== "") {
		window.localStorage.setItem(LAST_IMPORT_REMOTE_OWNER_KEY, githubLogin);
		return githubLogin;
	}
	const savedRemote = window.localStorage.getItem(LAST_IMPORT_REMOTE_URL_KEY)?.trim() ?? "";
	const savedOwner = window.localStorage.getItem(LAST_IMPORT_REMOTE_OWNER_KEY)?.trim() ?? "";
	const ownerFromRemote = remoteOwnerFromUrl(savedRemote);
	if (ownerFromRemote) {
		window.localStorage.setItem(LAST_IMPORT_REMOTE_OWNER_KEY, ownerFromRemote);
		return ownerFromRemote;
	}
	if (savedOwner !== "") return savedOwner;
	const owner = "org";
	window.localStorage.setItem(LAST_IMPORT_REMOTE_OWNER_KEY, owner);
	return owner;
}

function persistSuggestedProjectRemoteUrl(remoteUrl: string) {
	if (typeof window === "undefined") return;
	window.localStorage.setItem(LAST_IMPORT_REMOTE_URL_KEY, remoteUrl.trim());
	const owner = remoteOwnerFromUrl(remoteUrl);
	if (owner) window.localStorage.setItem(LAST_IMPORT_REMOTE_OWNER_KEY, owner);
}

function remoteOwnerFromUrl(remoteUrl: string): string {
	const trimmed = remoteUrl.trim();
	if (trimmed === "") return "";
	const sshMatch = trimmed.match(/^git@[^:]+:([^/]+)\/[^/]+(?:\.git)?$/);
	if (sshMatch?.[1]) return sshMatch[1];
	try {
		const parsed = new URL(trimmed);
		const segments = parsed.pathname.split("/").filter(Boolean);
		return segments.length >= 2 ? segments[segments.length - 2] ?? "" : "";
	} catch {
		return "";
	}
}

function shouldScanCreateFailure(message: string): boolean {
	if (/daemon|server|conflict|already exists|not ready|start|orchestrator|permission denied/i.test(message))
		return false;
	if (/\b(?:PATH|ID)_ALREADY_REGISTERED\b/i.test(message) || /already registered/i.test(message)) return false;
	return /workspace|repo|repository|git|path|folder|worktree|bare|branch|commit|remote/i.test(message);
}

function CreateProjectFlowBackdrop({ open }: { open: boolean }) {
	return (
		<Dialog.Root open={open}>
			<Dialog.Portal>
				<Dialog.Overlay className="dialog-overlay data-[state=open]:animate-overlay-in data-[state=closed]:animate-overlay-out" />
			</Dialog.Portal>
		</Dialog.Root>
	);
}

function CreateProjectSourceDialog({
	childOpen,
	cloudAvailable,
	cloudEnabled,
	disabled,
	offering,
	onCloudCreated,
	onOfferingChange,
	onSignIn,
	onOpenChange,
	onSelect,
	open,
}: {
	childOpen: boolean;
	cloudAvailable: boolean;
	cloudEnabled: boolean;
	disabled: boolean;
	offering: ProjectOffering;
	onCloudCreated: () => void;
	onOfferingChange: (offering: ProjectOffering) => void;
	onSignIn: () => void;
	onOpenChange: (open: boolean) => void;
	onSelect: (source: ProjectSource) => void;
	open: boolean;
}) {
	return (
		<Dialog.Root open={open} onOpenChange={onOpenChange}>
			<Dialog.Portal>
				<Dialog.Content
					hidden={childOpen}
					className={cn(
						"fixed left-1/2 top-1/2 z-overlay w-[min(560px,calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 border-0 bg-transparent p-0 shadow-none outline-none motion-reduce:animate-none",
						childOpen
							? "pointer-events-none opacity-0 animate-modal-out"
							: "data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out",
					)}
				>
					<div className="flex w-full flex-col items-center gap-3">
						{cloudEnabled && (
							<ProjectOfferingTabs disabled={disabled} offering={offering} onOfferingChange={onOfferingChange} />
						)}
						{cloudEnabled && offering === "cloud" ? (
							cloudAvailable ? (
								<CloudProjectCard dialog onClose={() => onOpenChange(false)} onCreated={onCloudCreated} />
							) : (
								<CloudSignInPanel dialog disabled={disabled} onSignIn={onSignIn} />
							)
						) : (
							<ImportSourcePicker disabled={disabled} onClose={() => onOpenChange(false)} onSelect={onSelect} dialog />
						)}
					</div>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}

/**
 * Local | Cloud segmented choice, shown whenever this deployment offers cloud.
 * A caption below spells out what each choice means (sessions on this machine
 * vs. each session in its own cloud sandbox) so the decision is explicit rather
 * than a subtle toggle that is easy to miss.
 */
function ProjectOfferingTabs({
	disabled,
	offering,
	onOfferingChange,
}: {
	disabled: boolean;
	offering: ProjectOffering;
	onOfferingChange: (offering: ProjectOffering) => void;
}) {
	const { t } = useTranslation();
	return (
		<div className="flex w-full flex-col items-center gap-1.5">
			<Tabs value={offering} onValueChange={(value) => onOfferingChange(value === "cloud" ? "cloud" : "local")}>
				<TabsList aria-label={t("createProject.kindChoice")}>
					<TabsTrigger disabled={disabled} value="local">
						{t("createProject.kindLocal")}
					</TabsTrigger>
					<TabsTrigger disabled={disabled} value="cloud">
						<Cloud className="size-3.5" aria-hidden="true" />
						{t("createProject.kindCloud")}
					</TabsTrigger>
				</TabsList>
			</Tabs>
			<p className="text-caption leading-body text-secondary text-center" role="status">
				{offering === "cloud" ? t("createProject.kindCloudHint") : t("createProject.kindLocalHint")}
			</p>
		</div>
	);
}

/**
 * Shown when the user picks Cloud but is not signed in yet. Keeps the Cloud
 * option discoverable and actionable from the create-project flow instead of
 * silently hiding it: a single button starts the WorkOS sign-in.
 */
function CloudSignInPanel({
	disabled,
	onSignIn,
}: {
	dialog?: boolean;
	disabled: boolean;
	onSignIn: () => void;
}) {
	const { t } = useTranslation();
	return (
		<div className="flex w-full max-w-(--size-import-modal-max) flex-col items-center gap-4 rounded-welcome-panel border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-modal)] p-(--size-import-modal-padding) text-center shadow-[var(--shadow-import-modal)]">
			<Cloud className="size-6 text-[var(--color-text-import-title)]" aria-hidden="true" />
			<p className="text-[13px] leading-5 text-[var(--color-text-import-subtitle)]">
				{t("createProject.cloudSignInPrompt")}
			</p>
			<Button disabled={disabled} onClick={onSignIn} type="button">
				{t("shell.signInToAOCloud")}
			</Button>
		</div>
	);
}

function isHttpsRepositoryUrl(raw: string): boolean {
	try {
		const parsed = new URL(raw.trim());
		return parsed.protocol === "https:" && parsed.host !== "";
	} catch {
		return false;
	}
}

// Cloud project creation goes straight to the control plane
// (client.createProject) instead of the daemon POST the local flow uses; the
// repository is cloned in a cloud sandbox, so no folder picker or agent sheet.
function CloudProjectCard({
	dialog = false,
	onClose,
	onCreated,
}: {
	dialog?: boolean;
	onClose?: () => void;
	onCreated: () => void;
}) {
	const { t } = useTranslation();
	const { client } = useCloudCp();
	const { org, error: orgError } = useCloudOrg();
	const queryClient = useQueryClient();
	const [repositoryUrl, setRepositoryUrl] = useState("");
	const [displayName, setDisplayName] = useState("");
	const [defaultBranch, setDefaultBranch] = useState("main");
	const [submitted, setSubmitted] = useState(false);
	const [isCreating, setIsCreating] = useState(false);
	const [submitError, setSubmitError] = useState<string | null>(null);

	const urlError = submitted && !isHttpsRepositoryUrl(repositoryUrl) ? t("createProject.cloudInvalidUrl") : null;
	const nameError = submitted && displayName.trim() === "" ? t("createProject.cloudDisplayNameRequired") : null;
	const branchError = submitted && defaultBranch.trim() === "" ? t("createProject.cloudDefaultBranchRequired") : null;
	const orgFailure = orgError ? (orgError instanceof Error ? orgError.message : String(orgError)) : null;

	const submit = async (event: FormEvent<HTMLFormElement>) => {
		event.preventDefault();
		setSubmitted(true);
		if (isCreating || org === undefined) return;
		if (!isHttpsRepositoryUrl(repositoryUrl) || displayName.trim() === "" || defaultBranch.trim() === "") return;
		setSubmitError(null);
		setIsCreating(true);
		try {
			await client.createProject(org.id, {
				displayName: displayName.trim(),
				repositoryUrl: repositoryUrl.trim(),
				defaultBranch: defaultBranch.trim(),
			});
			await queryClient.invalidateQueries({ queryKey: cloudProjectsQueryKey });
			onCreated();
		} catch (err) {
			setSubmitError(err instanceof Error ? err.message : t("createProject.couldNotAdd"));
		} finally {
			setIsCreating(false);
		}
	};

	const title = <h2 className="import-title text-balance">{t("createProject.cloudTitle")}</h2>;
	const description = <p className="import-description text-pretty">{t("createProject.cloudDescription")}</p>;

	return (
		<div className="relative isolate flex w-full max-w-(--size-import-modal-max) flex-col items-stretch gap-6 rounded-welcome-panel border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-modal)] p-(--size-import-modal-padding) shadow-[var(--shadow-import-modal)]">
			<div className={cn("flex flex-col items-start gap-1", dialog && onClose && "pr-10")}>
				{dialog ? (
					<>
						<Dialog.Title asChild>{title}</Dialog.Title>
						<Dialog.Description asChild>{description}</Dialog.Description>
					</>
				) : (
					<>
						{title}
						{description}
					</>
				)}
			</div>
			{dialog && onClose ? (
				<button
					type="button"
					className="settings-close-button absolute right-4 top-4"
					aria-label={t("createProject.closeDialog")}
					disabled={isCreating}
					onClick={onClose}
				>
					<X className="size-4" aria-hidden="true" />
				</button>
			) : null}
			<form className="flex flex-col gap-5" onSubmit={(event) => void submit(event)}>
				{(submitError ?? orgFailure) ? (
					<div
						className="rounded-lg border border-destructive/40 bg-destructive/10 px-4 py-3 text-pretty text-[12px] leading-5 text-destructive"
						role="alert"
					>
						{submitError ?? orgFailure}
					</div>
				) : null}
				<div className="space-y-2">
					<Label
						htmlFor="cloudRepositoryUrl"
						className="text-[13px] font-semibold text-[var(--color-text-import-title)]"
					>
						{t("createProject.cloneRepositoryUrl")}
					</Label>
					<div className="relative">
						<span className="pointer-events-none absolute inset-y-0 left-3 flex w-4 items-center justify-center text-[var(--color-text-import-muted)]">
							<Link2 className="size-4" aria-hidden="true" />
						</span>
						<Input
							id="cloudRepositoryUrl"
							autoFocus
							autoCapitalize="none"
							autoComplete="off"
							aria-describedby={urlError ? "cloudRepositoryUrlError" : undefined}
							aria-invalid={urlError ? true : undefined}
							className="bg-[var(--color-bg-import-card)] pl-10 font-mono text-[13px]"
							disabled={isCreating}
							placeholder={t("createProject.cloneRepositoryUrlPlaceholder")}
							spellCheck={false}
							value={repositoryUrl}
							onChange={(event) => setRepositoryUrl(event.target.value)}
						/>
					</div>
					{urlError ? (
						<p id="cloudRepositoryUrlError" className="text-pretty text-[12px] leading-5 text-destructive" role="alert">
							{urlError}
						</p>
					) : null}
				</div>
				<div className="grid gap-5 sm:grid-cols-2">
					<div className="space-y-2">
						<Label
							htmlFor="cloudDisplayName"
							className="text-[13px] font-semibold text-[var(--color-text-import-title)]"
						>
							{t("createProject.cloudDisplayName")}
						</Label>
						<div className="relative">
							<span className="pointer-events-none absolute inset-y-0 left-3 flex w-4 items-center justify-center text-[var(--color-text-import-muted)]">
								<Folder className="size-4" aria-hidden="true" />
							</span>
							<Input
								id="cloudDisplayName"
								autoComplete="off"
								aria-describedby={nameError ? "cloudDisplayNameError" : undefined}
								aria-invalid={nameError ? true : undefined}
								className="bg-[var(--color-bg-import-card)] pl-10 text-[13px]"
								disabled={isCreating}
								placeholder="web-app"
								spellCheck={false}
								value={displayName}
								onChange={(event) => setDisplayName(event.target.value)}
							/>
						</div>
						{nameError ? (
							<p id="cloudDisplayNameError" className="text-pretty text-[12px] leading-5 text-destructive" role="alert">
								{nameError}
							</p>
						) : null}
					</div>
					<div className="space-y-2">
						<Label
							htmlFor="cloudDefaultBranch"
							className="text-[13px] font-semibold text-[var(--color-text-import-title)]"
						>
							{t("createProject.cloudDefaultBranch")}
						</Label>
						<div className="relative">
							<span className="pointer-events-none absolute inset-y-0 left-3 flex w-4 items-center justify-center text-[var(--color-text-import-muted)]">
								<GitBranch className="size-4" aria-hidden="true" />
							</span>
							<Input
								id="cloudDefaultBranch"
								autoCapitalize="none"
								autoComplete="off"
								aria-describedby={branchError ? "cloudDefaultBranchError" : undefined}
								aria-invalid={branchError ? true : undefined}
								className="bg-[var(--color-bg-import-card)] pl-10 font-mono text-[13px]"
								disabled={isCreating}
								placeholder="main"
								spellCheck={false}
								value={defaultBranch}
								onChange={(event) => setDefaultBranch(event.target.value)}
							/>
						</div>
						{branchError ? (
							<p
								id="cloudDefaultBranchError"
								className="text-pretty text-[12px] leading-5 text-destructive"
								role="alert"
							>
								{branchError}
							</p>
						) : null}
					</div>
				</div>
				<div className="flex items-center justify-end gap-3">
					{org === undefined && orgFailure === null ? (
						<p className="mr-auto text-pretty text-[12px] leading-5 text-[var(--color-text-import-muted)]" role="status">
							{t("createProject.cloudWorkspaceConnecting")}
						</p>
					) : null}
					<Button type="submit" variant="footer-primary" disabled={isCreating || org === undefined}>
						{isCreating ? t("createProject.creating") : t("createProject.cloudCreate")}
					</Button>
				</div>
			</form>
		</div>
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
	const sources: Array<{ source: ProjectSource; icon: ReactNode; label: string; description: string }> = [
		{
			source: "clone",
			icon: <GitFork className="size-5" aria-hidden="true" strokeWidth={1.8} />,
			label: t("createProject.cloneFromGit"),
			description: t("createProject.cloneFromGitDesc"),
		},
		{
			source: "local",
			icon: <FolderClosed className="size-5" aria-hidden="true" strokeWidth={1.8} />,
			label: t("createProject.openLocal"),
			description: t("createProject.openLocalDesc"),
		},
		{
			source: "workspace",
			icon: <Folders className="size-5" aria-hidden="true" strokeWidth={1.8} />,
			label: t("createProject.addWorkspace"),
			description: t("createProject.workspaceDesc"),
		},
	];
	return (
		<div className="relative w-full max-w-[520px] overflow-hidden rounded-lg border border-border bg-popover text-popover-foreground shadow-xl">
			{dialog ? (
				<Dialog.Title className="settings-dialog-title px-4 pt-3">{t("createProject.addCodeTitle")}</Dialog.Title>
			) : (
				<h2 className="settings-dialog-title px-4 pt-3">{t("createProject.addCodeTitle")}</h2>
			)}
			{dialog ? (
				<Dialog.Description className="px-4 pb-3 pt-1 text-[13px] leading-5 text-muted-foreground">
					{t("createProject.addCodeDescription")}
				</Dialog.Description>
			) : (
				<p className="px-4 pb-3 pt-1 text-[13px] leading-5 text-muted-foreground">
					{t("createProject.addCodeDescription")}
				</p>
			)}
			<div className="mx-4 mb-4 overflow-hidden rounded-md border border-border/50 bg-[var(--color-bg-import-modal)]">
				<div className="flex flex-col divide-y divide-border/50">
				{sources.map(({ source, icon, label, description }) => (
					<button
						key={source}
						type="button"
						className="group flex min-h-[76px] items-center gap-3 px-3.5 py-3 text-left hover:bg-accent/50 active:bg-accent disabled:pointer-events-none disabled:opacity-50"
						aria-label={label}
						disabled={disabled}
						onClick={() => onSelect(source)}
					>
						<span className="grid w-9 shrink-0 place-items-center text-muted-foreground group-hover:text-foreground">
							{icon}
						</span>
						<span className="min-w-0">
							<span className="block text-[14px] font-medium text-foreground">{label}</span>
							<span className="mt-0.5 block text-[12px] leading-5 text-muted-foreground">{description}</span>
						</span>
					</button>
				))}
				</div>
			</div>
			{dialog && onClose ? (
				<button
					type="button"
					className="settings-close-button absolute right-3 top-3"
					aria-label={t("createProject.closeDialog")}
					disabled={disabled}
					onClick={onClose}
				>
					<X className="size-4" aria-hidden="true" />
				</button>
			) : null}
		</div>
	);
}

function CreateProjectFolderDialog({
	approvedActions,
	disabled,
	error,
	events,
	kind,
	isPreparingGit,
	onBack,
	onChangeApprovedActions,
	onChangeRemoteUrl,
	onChooseFolder,
	onContinue,
	onOpenChange,
	open,
	remoteUrls,
	scan,
	validation,
}: {
	approvedActions: WorkspaceApprovalState;
	disabled: boolean;
	error: string | null;
	events: GitPreparationEvent[];
	kind: ProjectKind;
	isPreparingGit: boolean;
	onBack: () => void;
	onChangeApprovedActions: (repoPath: string, actions: string[]) => void;
	onChangeRemoteUrl: (repoPath: string, remoteUrl: string) => void;
	onChooseFolder: () => void;
	onContinue: () => void;
	onOpenChange: (open: boolean) => void;
	open: boolean;
	remoteUrls: WorkspaceRemoteState;
	scan: ImportFolderScan | null;
	validation: ImportValidationResult | null;
}) {
	const { t } = useTranslation();
	const isWorkspace = kind === "workspace";
	const displayRepos = isWorkspace ? mergeWorkspaceImportRepos(scan, validation) : normalizeImportRepos(scan?.repos ?? []);
	const failedRepos = displayRepos.filter((repo) =>
		isWorkspace
			? repo.status === "error" || repo.blockingErrors.length > 0
			: ((repo.status === "error" || !repo.hasRemote) &&
				!repo.needsGitInit &&
				repo.reason !== "Repository must have at least one commit."),
	);
	const setupRepos = isWorkspace
		? displayRepos.filter((repo) => repo.status !== "error" && repo.blockingErrors.length === 0 && repo.requiredActions.length > 0)
		: [];
	const readyRepos = isWorkspace
		? displayRepos.filter((repo) => repo.status !== "error" && repo.blockingErrors.length === 0 && repo.requiredActions.length === 0)
		: displayRepos.filter((repo) => (repo.status !== "error" && repo.hasRemote) || repo.needsGitInit);
	const workspaceRequiresSetup = isWorkspace && validation?.nextStep === "prepare_git";
	const workspaceRequiresInitializedChildRepo =
		isWorkspace && validation?.blockingErrors.includes("WORKSPACE_CHILD_REPO_REQUIRED") === true;
	const workspaceWillImportAsProject = isWorkspace && Boolean(validation?.warning);
	const missingWorkspaceApprovals = setupRepos.filter((repo) =>
		repo.requiredActions.some((action) => !(approvedActions[repo.path] ?? []).includes(action)),
	);
	const missingWorkspaceRemote = setupRepos.filter(
		(repo) =>
			repo.requiredActions.includes("set_remote") &&
			(remoteUrls[repo.path] ?? "").trim() === "",
	);
	const hasScan = scan !== null;
	const canContinue =
		hasScan &&
		failedRepos.length === 0 &&
		!error &&
		(!workspaceRequiresSetup || (missingWorkspaceApprovals.length === 0 && missingWorkspaceRemote.length === 0));
	return (
		<Dialog.Root open={open} onOpenChange={onOpenChange}>
			<Dialog.Portal>
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay flex max-h-[min(720px,calc(100svh-24px))] w-[min(640px,calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-lg border border-border bg-popover p-0 text-popover-foreground shadow-xl data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none">
					<div className="relative flex shrink-0 items-center gap-3 px-4 pt-3">
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
						<div className="min-w-0 flex-1 pr-8">
							<Dialog.Title className="text-[18px] font-semibold text-[var(--color-text-import-title)]">
								{isWorkspace ? t("createProject.importWorkspace") : t("createProject.importProject")}
							</Dialog.Title>
							<Dialog.Description className="sr-only">
								{isWorkspace ? t("createProject.importWorkspaceDesc") : t("createProject.importProjectDesc")}
							</Dialog.Description>
						</div>
						<Dialog.Close asChild>
							<button
								type="button"
								className="settings-close-button shrink-0"
								aria-label={t("createProject.closeImport")}
								disabled={disabled}
							>
								<X className="size-4" aria-hidden="true" />
							</button>
						</Dialog.Close>
					</div>
					<div className="min-h-0 flex-1 overflow-y-auto px-4 pb-1 pt-3">
						{hasScan ? (
							<div className="space-y-3">
								<div className="flex items-center gap-3 rounded-md border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] px-3 py-2.5">
									<Folder className="size-4 shrink-0 text-[var(--color-text-import-muted)]" aria-hidden="true" />
									<div className="min-w-0 flex-1">
										<div className="truncate font-mono text-[13px] font-semibold text-[var(--color-text-import-title)]">
											{displayImportPath(scan.path)}
										</div>
										<div className="mt-0.5 text-[11px] text-[var(--color-text-import-muted)]">
											{isWorkspace ? t("createProject.workspaceRoot") : t("createProject.projectFolder")}
										</div>
									</div>
									<Button type="button" variant="outline" disabled={disabled} onClick={onChooseFolder}>
										{t("createProject.change")}
									</Button>
								</div>

								{error && (
									<div className="rounded-lg border border-destructive/40 bg-destructive/10">
										<div className="border-b border-destructive/30 px-3 py-2 font-mono text-[11px] font-semibold uppercase tracking-[0.12em] text-destructive">
											<span className="mr-2 inline-block size-2 rounded-full bg-destructive" aria-hidden="true" />
											{isWorkspace ? t("createProject.importFailedWorkspace") : t("createProject.importFailedProject")}
										</div>
						<div className="px-3 py-2 text-[12px] leading-5 text-destructive">{error}</div>
						<div className="border-t border-destructive/30 px-3 py-2 text-[12px] text-[var(--color-text-import-muted)]">
							{t("createProject.footerReview")}
						</div>
										{failedRepos.length > 0 && (
											<div className="border-t border-destructive/30">
									{failedRepos.map((repo) => (
										<ImportRepoRow key={repo.path} repo={repo} failed />
									))}
									<div className="border-t border-destructive/30 px-3 py-2 text-[12px] text-[var(--color-text-import-muted)]">
										{t("createProject.footerResolve", { count: failedRepos.length })}
									</div>
								</div>
										)}
									</div>
								)}

								{workspaceWillImportAsProject ? (
									<div className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-3 text-[12px] leading-5 text-amber-100">
										{validation?.warning}
									</div>
								) : null}

				{workspaceRequiresInitializedChildRepo && !error ? (
					<div className="rounded-md border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] px-3 py-3 text-[12px] leading-5 text-[var(--color-text-import-muted)]">
						{t("createProject.workspaceRequiresInitializedChildRepo")}
					</div>
								) : null}

								{readyRepos.map((repo) => (
										<div
											key={repo.path}
											className="rounded-md border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)]"
										>
											<ImportRepoRow repo={repo} />
										</div>
									))}

								{setupRepos.length > 0 ? (
									<div className="rounded-md border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] p-4">
										<div className="space-y-1">
						<h3 className="text-[14px] font-semibold text-[var(--color-text-import-title)]">{t("createProject.workspaceSetup")}</h3>
						<p className="text-[12px] leading-6 text-[var(--color-text-import-muted)]">
							{t("createProject.workspaceSetupApproval")}
											</p>
										</div>
										<div
											className={cn(
												"mt-4 space-y-3",
												setupRepos.length > 1 && "max-h-[340px] overflow-y-auto pr-1",
											)}
										>
											{setupRepos.map((setupRepo, setupIndex) => (
												<WorkspaceSetupRepoPanel
													key={setupRepo.path}
													approvedActions={approvedActions[setupRepo.path] ?? []}
													disabled={disabled}
													events={events}
													isPreparingGit={isPreparingGit}
													onChangeApprovedActions={(actions) => onChangeApprovedActions(setupRepo.path, actions)}
													onChangeRemoteUrl={(remoteUrl) => onChangeRemoteUrl(setupRepo.path, remoteUrl)}
													openByDefault={setupIndex === 0}
													repo={setupRepo}
													remoteUrl={remoteUrls[setupRepo.path] ?? ""}
												/>
											))}
										</div>
									</div>
								) : null}

								{displayRepos.length === 0 && !workspaceRequiresInitializedChildRepo && (
									<div className="rounded-md border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] p-3 text-[12px] text-[var(--color-text-import-muted)]">
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
					<div className="flex shrink-0 justify-end gap-2 border-t border-[var(--color-border-import-modal)] px-4 pb-4 pt-3">
						<div className="flex flex-wrap items-center justify-end gap-3">
							<Button type="button" variant="outline" disabled={disabled} onClick={() => onOpenChange(false)}>
								{t("createProject.cancel")}
							</Button>
							{canContinue && (
								<Button type="button" variant="primary" disabled={disabled} onClick={onContinue}>
									{workspaceWillImportAsProject
										? "Continue as project"
										: workspaceRequiresSetup
											? isPreparingGit
												? "Preparing workspace"
												: "Approve and set up"
											: t("createProject.cloneContinue")}
								</Button>
							)}
						</div>
					</div>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}

function mergeWorkspaceImportRepos(scan: ImportFolderScan | null, validation: ImportValidationResult | null): DisplayImportRepo[] {
	const metadataByPath = new Map((scan?.repos ?? []).map((repo) => [repo.path, repo]));
	const validationRepos = validation?.childRepos ?? [];
	const validationPaths = new Set(validationRepos.map((repo) => repo.repoPath));
	const scanErrors = (scan?.repos ?? []).filter((repo) => repo.status === "error" && !validationPaths.has(repo.path));
	return [
		...validationRepos.map((repo) => mergeWorkspaceImportRepo(repo.repoPath, metadataByPath.get(repo.repoPath), repo)),
		...scanErrors.map((repo) => mergeWorkspaceImportRepo(repo.path, repo)),
	]
		.sort((left, right) => left.name.localeCompare(right.name));
}

function normalizeImportRepos(repos: ImportFolderScan["repos"]): DisplayImportRepo[] {
	return repos.map((repo) => ({
		...repo,
		requiredActions: [],
		blockingErrors: [],
	}));
}

function mergeWorkspaceImportRepo(
	repoPath: string,
	scanRepo?: ImportFolderScan["repos"][number],
	validationRepo?: RepoGitStatus,
): DisplayImportRepo {
	return {
		name: scanRepo?.name ?? repoPath.split(/[\\/]/).filter(Boolean).pop() ?? repoPath,
		path: repoPath,
		relativePath: scanRepo?.relativePath ?? ".",
		branch: scanRepo?.branch ?? (validationRepo?.hasCommit ? "HEAD" : ""),
		remote: scanRepo?.remote ?? "",
		hasRemote: scanRepo?.hasRemote ?? Boolean(validationRepo?.hasOrigin),
		status: scanRepo?.status ?? (validationRepo?.blockingErrors.length ? "error" : "ok"),
		reason: scanRepo?.reason ?? validationRepo?.blockingErrors[0],
		// The daemon validation is authoritative here: the legacy folder scan
		// uses needsGitInit as a general "Git setup is needed" flag, while the
		// validation API reserves it for folders that are not repositories.
		needsGitInit: validationRepo ? validationRepo.needsGitInit : scanRepo?.needsGitInit,
		requiredActions: validationRepo?.requiredActions ?? [],
		blockingErrors: validationRepo?.blockingErrors ?? [],
		isRepo: validationRepo?.isRepo,
		hasCommit: validationRepo?.hasCommit,
		hasOrigin: validationRepo?.hasOrigin,
	};
}

function WorkspaceSetupRepoPanel({
	approvedActions,
	disabled,
	events,
	isPreparingGit,
	onChangeApprovedActions,
	onChangeRemoteUrl,
	openByDefault,
	repo,
	remoteUrl,
}: {
	approvedActions: string[];
	disabled: boolean;
	events: GitPreparationEvent[];
	isPreparingGit: boolean;
	onChangeApprovedActions: (actions: string[]) => void;
	onChangeRemoteUrl: (remoteUrl: string) => void;
	openByDefault: boolean;
	repo: DisplayImportRepo;
	remoteUrl: string;
}) {
	const { t } = useTranslation();
	const actionCount = repo.requiredActions.length;
	return (
		<details
			className="group overflow-hidden rounded-md border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-modal)]"
			open={openByDefault}
		>
			<summary className="flex cursor-pointer list-none items-center gap-3 px-4 py-3">
				<ChevronRight
					className="size-4 shrink-0 text-[var(--color-text-import-muted)] transition-transform group-open:rotate-90"
					aria-hidden="true"
				/>
				<div className="min-w-0 flex-1">
					<div className="truncate text-[14px] font-semibold text-[var(--color-text-import-title)]">{repo.name}</div>
					<div className="mt-0.5 truncate font-mono text-[12px] text-[var(--color-text-import-muted)]">
						{displayImportPath(repo.path)}
					</div>
				</div>
				<div className="shrink-0 text-right">
					<div className="font-mono text-[12px] text-[var(--color-text-import-muted)]">
						{actionCount === 1 ? "1 required step" : `${actionCount} required steps`}
					</div>
				</div>
			</summary>
			<div className="space-y-3 border-t border-[var(--color-border-import-modal)] px-4 py-4">
				<p className="text-[12px] leading-5 text-[var(--color-text-import-muted)]">
					{t("createProject.workspaceSetupRepoApproval")}
				</p>
				{orderedGitActions(repo.requiredActions).map((action) => {
					const checked = approvedActions.includes(action);
					const state = latestWorkspaceActionState(repo.path, action, events);
					return (
						<WorkspaceSetupActionCard
							key={action}
							action={action}
							checked={checked}
							disabled={disabled || isPreparingGit}
							inputId={`workspace-remote-${repo.path}`}
							remoteUrl={remoteUrl}
							state={state}
							onChangeChecked={(nextChecked) => {
								onChangeApprovedActions(
									nextChecked
										? orderedGitActions([...approvedActions, action])
										: approvedActions.filter((item) => item !== action),
								);
							}}
							onChangeRemoteUrl={onChangeRemoteUrl}
						/>
					);
				})}
			</div>
		</details>
	);
}

function WorkspaceSetupActionCard({
	action,
	checked,
	disabled,
	inputId,
	remoteUrl,
	state,
	onChangeChecked,
	onChangeRemoteUrl,
}: {
	action: string;
	checked: boolean;
	disabled: boolean;
	inputId: string;
	remoteUrl: string;
	state: string;
	onChangeChecked: (checked: boolean) => void;
	onChangeRemoteUrl: (remoteUrl: string) => void;
}) {
	const { t } = useTranslation();
	const label = workspaceActionStateLabel(action, state, checked, remoteUrl);
	return (
		<div className="rounded-md border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] px-4 py-4">
			<label className="flex items-start gap-3 text-[12px] leading-5 text-[var(--color-text-import-title)]">
				<input
					type="checkbox"
					checked={checked}
					disabled={disabled}
					onChange={(event) => onChangeChecked(event.target.checked)}
				/>
				<span className="flex-1">
					<span className="flex items-start justify-between gap-3">
						<span className="block text-[13px] font-semibold">{gitActionLabel(action)}</span>
						<span className="shrink-0 font-mono text-[12px] text-[var(--color-text-import-muted)]">{label}</span>
					</span>
					<span className="mt-1 block text-[12px] text-[var(--color-text-import-muted)]">
						{gitActionDescription(action)}
					</span>
				</span>
			</label>
			{action === "set_remote" ? (
				<div className="mt-4 space-y-2 pl-7">
					<Label htmlFor={inputId} className="text-[12px] font-semibold text-[var(--color-text-import-title)]">
						{t("createProject.originRemoteUrl")}
					</Label>
					<Input
						id={inputId}
						autoCapitalize="none"
						autoComplete="off"
						className="bg-[var(--color-bg-import-modal)] font-mono text-[13px]"
						disabled={disabled}
						placeholder={t("createProject.originRemoteUrlPlaceholder")}
						spellCheck={false}
						value={remoteUrl}
						onChange={(event) => onChangeRemoteUrl(event.target.value)}
					/>
					<div className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2.5 text-[11px] leading-5 text-amber-100">
						{remoteSetupNote(remoteUrl)}
					</div>
				</div>
			) : null}
		</div>
	);
}

function workspaceActionStateLabel(action: string, state: string, checked: boolean, remoteUrl: string): string {
	if (state === "success") return "Done";
	if (state === "error") return "Failed";
	if (state === "running") return "Running";
	if (state === "pending") return "Queued";
	if (!checked) return "Required";
	if (action === "set_remote") return remoteUrl.trim() === "" ? "Set URL" : "URL ready";
	return "Ready";
}

function ImportRepoRow({ failed = false, repo }: { failed?: boolean; repo: DisplayImportRepo }) {
	const { t } = useTranslation();
	return (
		<div className="flex items-center gap-3 px-3 py-2.5">
			{failed ? (
				<XCircle className="size-4 shrink-0 text-destructive" aria-hidden="true" />
			) : repo.needsGitInit ? (
				<CircleDashed className="size-4 shrink-0 text-[var(--color-text-import-muted)]" aria-hidden="true" />
			) : (
				<CheckCircle2 className="size-4 shrink-0 text-success" aria-hidden="true" />
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
						? (repo.reason ?? repo.blockingErrors[0] ?? t("createProject.repoCannotImport"))
						: repo.hasOrigin
							? `${repo.branch} ${remoteDisplay(repo.remote)}`
							: repo.hasCommit
								? "Missing origin remote"
								: "Missing initial commit"}
			</div>
		</div>
	);
}

function gitActionDescription(action: string): string {
	switch (action) {
		case "git_init":
			return "Create a Git repository in this folder.";
		case "git_commit":
			return "Create the first commit for this repository.";
		case "set_remote":
			return "Configure the origin remote for this repository.";
		default:
			return "Complete the required Git setup.";
	}
}

function remoteSetupNote(remoteUrl: string): string {
	const host = remoteUrlHost(remoteUrl) || "GitHub";
	return `To create sessions and PRs successfully, make sure this repository also exists on ${host} and that you can push the default branch to it.`;
}

function remoteUrlHost(remoteUrl: string): string {
	const trimmed = remoteUrl.trim();
	if (trimmed === "") return "";
	const sshMatch = trimmed.match(/^git@([^:]+):/);
	if (sshMatch?.[1]) return sshMatch[1];
	try {
		return new URL(trimmed).host;
	} catch {
		return "";
	}
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
