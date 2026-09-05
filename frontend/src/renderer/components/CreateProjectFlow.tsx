import * as Dialog from "@radix-ui/react-dialog";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { ChevronRight, Cloud, Folder, FolderClosed, FolderPlus, Folders, GitBranch, GitFork, Link2, X, XCircle } from "lucide-react";
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
import CloneRepositoryDialog, {
	repositoryAvatarFromGitUrl,
	type CloneRepositoryDetails,
	type CloneRepositorySelection,
} from "./CloneRepositoryDialog";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Tabs, TabsList, TabsTrigger } from "./ui/tabs";

export type CreateProjectInput = {
	path: string;
	asWorkspace?: boolean;
	defaultBranch?: string;
} & CreateProjectAgentSelection;
export type CloneProjectInput = Pick<CloneRepositorySelection, "remoteUrl" | "destinationParent"> & CreateProjectAgentSelection;

const LAST_CLONE_DESTINATION_KEY = "ao.clone.lastDestinationParent";
const LAST_IMPORT_REMOTE_URL_KEY = "ao.import.lastRemoteUrl";
const LAST_IMPORT_REMOTE_OWNER_KEY = "ao.import.lastRemoteOwner";
type ImportValidationResult = components["schemas"]["ImportValidationResult"];
type RepoGitStatus = components["schemas"]["RepoGitStatus"];
type GitPreparationEvent = components["schemas"]["GitPreparationEvent"];
type ProjectImportStep = "blocked" | "prepare_git";
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
type CreateProgressKind = "clone" | "project" | "workspace";

// Shared create-project flow. Local projects/workspaces use the native folder
// picker; remote projects progressively reveal a lazily loaded clone form.
// Every source converges on the same agent sheet and project-start behavior.
export function CreateProjectFlow({
	children,
	droppedPath,
	embedded = false,
	idleLabel,
	mode = "single_repo",
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
		destinationParent: typeof window === "undefined" ? "" : (window.localStorage.getItem(LAST_CLONE_DESTINATION_KEY) ?? ""),
	}));
	const [cloneSelection, setCloneSelection] = useState<CloneRepositorySelection | null>(null);
	const [preparedClonePath, setPreparedClonePath] = useState<string | null>(null);
	const [folderPickerOpen, setFolderPickerOpen] = useState(false);
	const [childTransitioning, setChildTransitioning] = useState(false);
	const [selectedKind, setSelectedKind] = useState<ProjectKind>(mode === "workspace" ? "workspace" : "single_repo");
	const [selectedPath, setSelectedPath] = useState<string | null>(null);
	const [validationScan, setValidationScan] = useState<ImportFolderScan | null>(null);
	const [projectValidation, setProjectValidation] = useState<ImportValidationResult | null>(null);
	const [projectImportStep, setProjectImportStep] = useState<ProjectImportStep | null>(null);
	const [projectPrepEvents, setProjectPrepEvents] = useState<GitPreparationEvent[]>([]);
	const [projectApprovedActions, setProjectApprovedActions] = useState<string[]>([]);
	const [projectRemoteUrl, setProjectRemoteUrl] = useState("");
	const [projectSuggestWorkspace, setProjectSuggestWorkspace] = useState(false);
	const [workspaceValidation, setWorkspaceValidation] = useState<ImportValidationResult | null>(null);
	const [workspaceApprovedActions, setWorkspaceApprovedActions] = useState<WorkspaceApprovalState>({});
	const [workspaceRemoteUrls, setWorkspaceRemoteUrls] = useState<WorkspaceRemoteState>({});
	const [workspacePrepEvents, setWorkspacePrepEvents] = useState<GitPreparationEvent[]>([]);
	const [isChoosingPath, setIsChoosingPath] = useState(false);
	const [isCreating, setIsCreating] = useState(false);
	const [isInitializing, setIsInitializing] = useState(false);
	const [isPreparingGit, setIsPreparingGit] = useState(false);
	const [createProgressKind, setCreateProgressKind] = useState<CreateProgressKind | null>(null);
	const [createProgress, setCreateProgress] = useState(0);
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
	const projectImportOpen = projectImportStep !== null && projectValidation !== null;
	const isBusy = isChoosingPath || isCreating || isInitializing || isPreparingGit;
	const progressOpen = createProgressKind !== null;

	const resetProjectImportState = () => {
		setProjectValidation(null);
		setProjectImportStep(null);
		setProjectPrepEvents([]);
		setProjectApprovedActions([]);
		setProjectRemoteUrl("");
		setProjectSuggestWorkspace(false);
	};

	const resetWorkspaceImportState = () => {
		setWorkspaceValidation(null);
		setWorkspaceApprovedActions({});
		setWorkspaceRemoteUrls({});
		setWorkspacePrepEvents([]);
	};

	useEffect(() => {
		if (!progressOpen) return;
		const startedAt = Date.now();
		const timer = window.setInterval(() => {
			const elapsed = Date.now() - startedAt;
			setCreateProgress(Math.min(92, 8 + elapsed / 90));
		}, 180);
		return () => window.clearInterval(timer);
	}, [progressOpen]);

	const beginProgress = (kind: CreateProgressKind) => {
		setCreateProgress(6);
		setCreateProgressKind(kind);
	};

	const finishProgress = async () => {
		setCreateProgress(100);
		await new Promise((resolve) => window.setTimeout(resolve, 180));
		setCreateProgressKind(null);
	};

	const abandonPreparedClone = async () => {
		const path = preparedClonePath;
		if (!path) return;
		setPreparedClonePath(null);
		try {
			await apiClient.POST("/api/v1/projects/clone/cleanup", {
				body: { path },
			});
		} catch {
			// Cleanup is best effort and the daemon only removes marked preparations.
		}
	};

	const transitionToChild = (open: () => void) => {
		setModePickerOpen(false);
		setChildTransitioning(true);
		window.setTimeout(() => {
			open();
			setChildTransitioning(false);
		}, 80);
	};

	const selectSource = (source: ProjectSource) => {
		void abandonPreparedClone();
		const presetPath = pendingDropPath;
		setPendingDropPath(null);
		setError(null);
		setValidationScan(null);
		resetProjectImportState();
		resetWorkspaceImportState();
		if (source === "clone") {
			transitionToChild(() => setCloneDialogOpen(true));
			return;
		}
		setCloneSelection(null);
		setPreparedClonePath(null);
		// Keep the selector mounted behind the native picker. Closing it first
		// exposes a blank compositor frame on Windows before Explorer takes focus.
		void chooseDirectory(source === "workspace" ? "workspace" : "single_repo", presetPath ?? undefined);
	};

	const chooseDirectory = async (kind: ProjectKind, presetPath?: string) => {
		setError(null);
		setValidationScan(null);
		resetProjectImportState();
		resetWorkspaceImportState();
		setRepositorySetup(null);
		setRepositorySetupWarning(null);
		setSelectedKind(kind);
		setIsChoosingPath(true);
		try {
			const path =
				presetPath ??
				(await aoBridge.app.chooseDirectory(kind === "workspace" ? t("createProject.chooseWorkspace") : t("createProject.chooseRepo")));
			if (path && kind === "single_repo") {
				const validation = await validateImportFolder(path, "project");
				setProjectValidation(validation);
				setProjectPrepEvents([]);
				setProjectApprovedActions(validation.root.requiredActions);
				setProjectRemoteUrl(
					validation.root.requiredActions.includes("set_remote")
						? suggestedProjectRemoteUrl(validation.root.repoPath, await suggestedProjectRemoteOwner())
						: "",
				);
				setProjectSuggestWorkspace(validation.nextStep === "choose_import_kind");
				if (!validation.isValid || validation.nextStep === "error") {
					setError(importValidationMessage(validation));
					setProjectImportStep("blocked");
					setModePickerOpen(false);
					return;
				}
				if (validation.nextStep === "choose_import_kind" || validation.nextStep === "prepare_git") {
					setProjectImportStep("prepare_git");
					setModePickerOpen(false);
					return;
				}
				setModePickerOpen(false);
				setFolderPickerOpen(false);
				setSelectedPath(validation.root.repoPath);
				return;
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
					const validationError = !validation.isValid || validation.nextStep === "error" ? importValidationMessage(validation) : null;
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
		resetProjectImportState();
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
		beginProgress(cloneSelection ? "clone" : selectedKind === "workspace" ? "workspace" : "project");
		try {
			if (cloneSelection) {
				if (!preparedClonePath) throw new Error(t("createProject.couldNotAdd"));
				await onCreateProject({ path: preparedClonePath, ...selection });
				setSelectedPath(null);
				setCloneSelection(null);
				setPreparedClonePath(null);
				await finishProgress();
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
			// Workspace imports can adopt an existing local Git root too. Preserve
			// its branch just as for a single repository; child defaults stay separate.
			const defaultBranch = await aoBridge.app.getRepositoryBranch(selectedPath);
			await onCreateProject({
				path: selectedPath,
				asWorkspace: selectedKind === "workspace",
				...(defaultBranch ? { defaultBranch } : {}),
				...selection,
			});
			setSelectedPath(null);
			await finishProgress();
		} catch (err) {
			const code = err instanceof Error && "code" in err ? (err.code as string | undefined) : undefined;
			const message = err instanceof Error ? err.message : t("createProject.couldNotAdd");
			if (!cloneSelection && selectedKind === "single_repo" && isRepositorySetupRecoveryCode(code)) {
				setRepositorySetup(code);
			}
			setError(message);
			setCreateProgressKind(null);
			if (hasModePicker && !cloneSelection && shouldScanCreateFailure(message)) {
				try {
					const importMode = selectedKind === "workspace" ? "workspace" : "project";
					const scanPromise = aoBridge.app.scanImportFolder({ path: selectedPath, mode: importMode });
					if (importMode === "workspace") {
						const [validation, scan] = await Promise.all([validateImportFolder(selectedPath, "workspace"), scanPromise]);
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
				setSelectedPath(null);
				setFolderPickerOpen(true);
			}
		} finally {
			setIsCreating(false);
			setIsInitializing(false);
		}
	};

	const prepareClone = async (next: CloneRepositorySelection) => {
		setError(null);
		setCloneDialogOpen(false);
		setCloneDialogClosing(true);
		setIsPreparingGit(true);
		beginProgress("clone");
		try {
			const { data, error: apiError } = await apiClient.POST("/api/v1/projects/clone/prepare", {
				body: {
					remoteUrl: next.remoteUrl,
					destinationParent: next.destinationParent,
				},
			});
			if (apiError || !data) throw new Error(apiErrorMessage(apiError, t("createProject.couldNotAdd")));
			setPreparedClonePath(data.path);

			let validation = await validateImportFolder(data.path, "project");
			if (validation.nextStep === "prepare_git") {
				const { data: prepared, error: preparationError } = await apiClient.POST("/api/v1/imports/prepare-git", {
					body: {
						importKind: "project",
						path: data.path,
						approvedActions: validation.root.requiredActions,
						remoteUrl: validation.root.requiredActions.includes("set_remote") ? next.remoteUrl : undefined,
						initialCommitMessage: "Initial commit",
					},
				});
				if (preparationError || !prepared) {
					throw new Error(apiErrorMessage(preparationError, t("createProject.couldNotAdd")));
				}
				const failed = prepared.events.find((event) => event.state === "error");
				if (failed) throw new Error(projectPreparationFailureMessage(failed));
				validation = prepared.validation;
			}
			if (!validation.isValid || validation.nextStep !== "continue") {
				throw new Error(importValidationMessage(validation));
			}

			setCloneSelection(next);
			setSelectedKind("single_repo");
			setModePickerOpen(false);
			setSelectedPath(data.path);
			await finishProgress();
		} catch (err) {
			setError(err instanceof Error ? err.message : t("createProject.couldNotAdd"));
			setCreateProgressKind(null);
			setCloneDialogOpen(true);
		} finally {
			setCloneDialogClosing(false);
			setIsPreparingGit(false);
		}
	};

	const reopenSourcePicker = () => {
		void abandonPreparedClone();
		setCloneSelection(null);
		resetProjectImportState();
		setError(null);
		if (hasModePicker) setModePickerOpen(true);
	};

	const tryProjectAsWorkspace = () => {
		if (!projectValidation) return;
		const path = projectValidation.root.repoPath;
		resetProjectImportState();
		setPendingDropPath(null);
		void chooseDirectory("workspace", path);
	};

	const prepareProjectGit = async () => {
		if (!projectValidation) return;
		setError(null);
		setProjectPrepEvents(projectRequestedActionEvents(projectValidation.root.repoPath, projectApprovedActions));
		setIsPreparingGit(true);
		try {
			const { data, error: apiError } = await apiClient.POST("/api/v1/imports/prepare-git", {
				body: {
					importKind: "project",
					path: projectValidation.root.repoPath,
					approvedActions: projectApprovedActions,
					remoteUrl: projectRemoteUrl.trim() || undefined,
				},
			});
			if (apiError || !data) throw new Error(apiErrorMessage(apiError, t("createProject.couldNotAdd")));
			setProjectPrepEvents(data.events);
			setProjectValidation(data.validation);
			setProjectApprovedActions(data.validation.root.requiredActions);
			if (projectRemoteUrl.trim() !== "") persistSuggestedProjectRemoteUrl(projectRemoteUrl);
			const failed = data.events.find((event) => event.state === "error");
			if (failed) {
				setError(projectPreparationFailureMessage(failed));
				return;
			}
			if (!data.validation.isValid || data.validation.nextStep === "error") {
				setError(importValidationMessage(data.validation));
				setProjectImportStep("blocked");
				setProjectSuggestWorkspace(false);
				return;
			}
			if (data.validation.nextStep === "continue") {
				setModePickerOpen(false);
				setProjectImportStep(null);
				setProjectSuggestWorkspace(false);
				setSelectedPath(data.validation.root.repoPath);
			}
		} catch (err) {
			setError(err instanceof Error ? err.message : t("createProject.couldNotAdd"));
		} finally {
			setIsPreparingGit(false);
		}
	};

	const backFromAgentSetup = () => {
		setSelectedPath(null);
		setError(null);
		if (cloneSelection) {
			void abandonPreparedClone();
			setCloneSelection(null);
			setPreparedClonePath(null);
			setCloneDialogOpen(true);
			return;
		}
		if (selectedKind === "workspace" && validationScan && workspaceValidation) {
			setFolderPickerOpen(true);
			return;
		}
		resetProjectImportState();
		if (hasModePicker) setModePickerOpen(true);
	};

	const prepareWorkspaceRepo = async (repoPath: string) => {
		if (!workspaceValidation) return;
		setError(null);
		const status = workspaceValidation.childRepos?.find((repo) => repo.repoPath === repoPath);
		if (!status || status.requiredActions.length === 0) return;
		const repository = {
			repoPath,
			approvedActions: workspaceApprovedActions[repoPath] ?? [],
			remoteUrl: workspaceRemoteUrls[repoPath]?.trim() || undefined,
		};
		setWorkspacePrepEvents(workspaceRequestedActionEvents([repository]));
		setIsPreparingGit(true);
		try {
			const { data, error: apiError } = await apiClient.POST("/api/v1/imports/prepare-git", {
				body: {
					importKind: "workspace",
					path: workspaceValidation.root.repoPath,
					repositories: [repository],
				},
			});
			if (apiError || !data) throw new Error(apiErrorMessage(apiError, "Could not prepare this repository."));
			setWorkspacePrepEvents(data.events);
			setWorkspaceValidation(data.validation);
			setWorkspaceApprovedActions(defaultWorkspaceApprovedActions(data.validation));
			const nextRemoteUrls = await defaultWorkspaceRemoteUrls(data.validation);
			setWorkspaceRemoteUrls((current) => ({ ...nextRemoteUrls, ...current }));
			const failedStep = data.events.find((event) => event.state === "error");
			if (failedStep) {
				setError(workspacePreparationFailureMessage(failedStep));
				return;
			}
			if (!data.validation.isValid || data.validation.nextStep === "error") {
				setError(importValidationMessage(data.validation));
				return;
			}
			if (repository.remoteUrl) persistSuggestedProjectRemoteUrl(repository.remoteUrl);
		} catch (err) {
			setError(err instanceof Error ? err.message : t("createProject.couldNotAdd"));
		} finally {
			setIsPreparingGit(false);
		}
	};

	const continueWorkspaceImport = () => {
		if (!workspaceValidation) return;
		setError(null);
		if (workspaceValidation.warning) setSelectedKind("single_repo");
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
			<CreateProjectFlowBackdrop
				open={
					modePickerOpen ||
					cloneDialogOpen ||
					folderPickerOpen ||
					selectedPath !== null ||
					childTransitioning ||
					projectImportOpen ||
					progressOpen
				}
			/>
			{hasModePicker && embedded && !modePickerOpen && !cloneDialogOpen && selectedPath === null && (
				<div className="flex w-full flex-col items-center gap-3">
					{cloudEnabled && <ProjectOfferingTabs disabled={isBusy} offering={offering} onOfferingChange={setOffering} />}
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
						childOpen={
							childTransitioning || cloneDialogOpen || folderPickerOpen || projectImportOpen || selectedPath !== null || progressOpen
						}
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
							onContinue={(next) => void prepareClone(next)}
							open={cloneDialogOpen}
							value={cloneDetails}
						/>
					) : null}
				</>
			)}
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
						continueWorkspaceImport();
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
					if (hasModePicker) setModePickerOpen(true);
				}}
				onChooseFolder={() => void chooseDirectory(selectedKind)}
				onPrepareRepository={(repoPath) => void prepareWorkspaceRepo(repoPath)}
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
			<ProjectImportDialog
				approvedActions={projectApprovedActions}
				disabled={isBusy}
				error={error}
				events={projectPrepEvents}
				isPreparingGit={isPreparingGit}
				onBack={reopenSourcePicker}
				onChangeApprovedActions={setProjectApprovedActions}
				onChangeFolder={() => void chooseDirectory("single_repo")}
				onChangeRemote={setProjectRemoteUrl}
				onContinue={() => void prepareProjectGit()}
				onOpenChange={(open) => {
					if (isBusy || open) return;
					void abandonPreparedClone();
					setCloneSelection(null);
					resetProjectImportState();
						setError(null);
				}}
				onTryWorkspace={tryProjectAsWorkspace}
				open={projectImportOpen}
				remoteUrl={projectRemoteUrl}
				suggestWorkspace={projectSuggestWorkspace}
				step={projectImportStep}
				validation={projectValidation}
			/>
			<CreateProjectAgentSheet
				action={cloneSelection ? "clone" : "create"}
				error={error}
				isCreating={isCreating}
				isInitializing={isInitializing}
				kind={selectedKind}
				onOpenChange={(open) => {
					if (!open) {
						void abandonPreparedClone();
						setSelectedPath(null);
						setCloneSelection(null);
						setPreparedClonePath(null);
						resetProjectImportState();
						if (!folderPickerOpen) {
							setError(null);
						}
					}
				}}
				onBack={backFromAgentSetup}
				onSubmit={createProject}
				open={selectedPath !== null && !progressOpen}
				path={selectedPath}
				repositorySetupNeeded={repositorySetup !== null}
				repositorySetupWarning={repositorySetupWarning}
			/>
			<CreateProjectProgressDialog kind={createProgressKind} open={progressOpen} progress={createProgress} />
			{error && !embedded && !modePickerOpen && !folderPickerOpen && !projectImportOpen && selectedPath === null && (
				<p className="mt-3 text-caption leading-body text-error" role="status">
					{error}
				</p>
			)}
		</>
	);
}

function isRepositorySetupRecoveryCode(code: string | undefined): code is "NOT_A_GIT_REPO" | "PROJECT_UNBORN" {
	return code === "NOT_A_GIT_REPO" || code === "PROJECT_UNBORN";
}

async function validateImportFolder(path: string, importKind: "project" | "workspace"): Promise<ImportValidationResult> {
	const { data, error } = await apiClient.POST("/api/v1/imports/validate", {
		body: { importKind, path },
	});
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

function workspaceRequestedActionEvents(repositories: Array<{ repoPath: string; approvedActions: string[] }>): GitPreparationEvent[] {
	return repositories.flatMap((repo) =>
		orderedGitActions(repo.approvedActions).map((action, index) => ({
			repoPath: repo.repoPath,
			action: action as GitPreparationEvent["action"],
			state: index === 0 ? "running" : "pending",
		})),
	);
}

function projectRequestedActionEvents(repoPath: string, actions: string[]): GitPreparationEvent[] {
	return orderedGitActions(actions).map((action, index) => ({
		repoPath,
		action: action as GitPreparationEvent["action"],
		state: index === 0 ? "running" : "pending",
	}));
}

function workspacePreparationFailureMessage(event: GitPreparationEvent): string {
	return `${displayImportPath(event.repoPath)} failed while running ${gitActionLabel(event.action)}. Review the step below, then retry or choose a different folder.`;
}

function projectPreparationFailureMessage(event: GitPreparationEvent): string {
	return `${displayImportPath(event.repoPath)} failed while running ${gitActionLabel(event.action)}. Review the setup, then retry or go back.`;
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
	if (typeof window === "undefined") return "username";
	const savedRemote = window.localStorage.getItem(LAST_IMPORT_REMOTE_URL_KEY)?.trim() ?? "";
	const savedOwner = window.localStorage.getItem(LAST_IMPORT_REMOTE_OWNER_KEY)?.trim() ?? "";
	const ownerFromRemote = remoteOwnerFromUrl(savedRemote);
	if (ownerFromRemote) {
		window.localStorage.setItem(LAST_IMPORT_REMOTE_OWNER_KEY, ownerFromRemote);
		return ownerFromRemote;
	}
	if (savedOwner !== "") return savedOwner;
	const owner = "username";
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
		return segments.length >= 2 ? (segments[segments.length - 2] ?? "") : "";
	} catch {
		return "";
	}
}

function shouldScanCreateFailure(message: string): boolean {
	if (/daemon|server|conflict|already exists|not ready|start|orchestrator|permission denied/i.test(message)) return false;
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

function CreateProjectProgressDialog({ kind, open, progress }: { kind: CreateProgressKind | null; open: boolean; progress: number }) {
	const title = kind === "workspace" ? "Creating workspace" : kind === "clone" ? "Preparing repository" : "Creating project";
	const message =
		progress >= 100
			? kind === "workspace"
				? "Workspace created"
				: "Project created"
			: kind === "clone"
				? "Cloning and preparing the repository"
				: kind === "workspace"
					? "Registering repositories and starting agents"
					: "Registering the project and starting agents";
	return (
		<Dialog.Root open={open}>
			<Dialog.Portal>
				<Dialog.Content
					aria-describedby="createProjectProgressMessage"
					className="fixed left-1/2 top-1/2 z-overlay w-[min(440px,calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 overflow-hidden rounded-lg border border-border bg-popover p-5 text-popover-foreground shadow-xl data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none"
					onInteractOutside={(event) => event.preventDefault()}
					onPointerDownOutside={(event) => event.preventDefault()}
				>
					<Dialog.Title className="text-[18px] font-semibold text-[var(--color-text-import-title)]">{title}</Dialog.Title>
					<div className="mt-6 space-y-3">
						<div
							aria-label={`${Math.round(progress)}%`}
							aria-valuemax={100}
							aria-valuemin={0}
							aria-valuenow={Math.round(progress)}
							className="h-2 w-full overflow-hidden rounded-full bg-muted"
							role="progressbar"
						>
							<div
								className="h-full rounded-full bg-primary transition-[width] duration-300 ease-out"
								style={{ width: `${Math.max(0, Math.min(100, progress))}%` }}
							/>
						</div>
						<Dialog.Description asChild>
							<p id="createProjectProgressMessage" className="min-h-5 text-[13px] text-muted-foreground" role="status">
								{message}
							</p>
						</Dialog.Description>
					</div>
				</Dialog.Content>
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
						{cloudEnabled && <ProjectOfferingTabs disabled={disabled} offering={offering} onOfferingChange={onOfferingChange} />}
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
function CloudSignInPanel({ disabled, onSignIn }: { dialog?: boolean; disabled: boolean; onSignIn: () => void }) {
	const { t } = useTranslation();
	return (
		<div className="flex w-full max-w-(--size-import-modal-max) flex-col items-center gap-4 rounded-welcome-panel border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-modal)] p-(--size-import-modal-padding) text-center shadow-[var(--shadow-import-modal)]">
			<Cloud className="size-6 text-[var(--color-text-import-title)]" aria-hidden="true" />
			<p className="text-[13px] leading-5 text-[var(--color-text-import-subtitle)]">{t("createProject.cloudSignInPrompt")}</p>
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
function CloudProjectCard({ dialog = false, onClose, onCreated }: { dialog?: boolean; onClose?: () => void; onCreated: () => void }) {
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
					<Label htmlFor="cloudRepositoryUrl" className="text-[13px] font-semibold text-[var(--color-text-import-title)]">
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
						<Label htmlFor="cloudDisplayName" className="text-[13px] font-semibold text-[var(--color-text-import-title)]">
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
						<Label htmlFor="cloudDefaultBranch" className="text-[13px] font-semibold text-[var(--color-text-import-title)]">
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
							<p id="cloudDefaultBranchError" className="text-pretty text-[12px] leading-5 text-destructive" role="alert">
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
	const sources: Array<{
		source: ProjectSource;
		icon: ReactNode;
		label: string;
		description: string;
	}> = [
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
				<p className="px-4 pb-3 pt-1 text-[13px] leading-5 text-muted-foreground">{t("createProject.addCodeDescription")}</p>
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
							<span className="grid w-9 shrink-0 place-items-center text-muted-foreground group-hover:text-foreground">{icon}</span>
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

function ProjectImportDialog({
	approvedActions,
	disabled,
	error,
	events,
	onBack,
	onChangeApprovedActions,
	onChangeFolder,
	onChangeRemote,
	onContinue,
	onOpenChange,
	onTryWorkspace,
	open,
	remoteUrl,
	suggestWorkspace,
	step,
	isPreparingGit,
	validation,
}: {
	approvedActions: string[];
	disabled: boolean;
	error: string | null;
	events: GitPreparationEvent[];
	onBack: () => void;
	onChangeApprovedActions: (actions: string[]) => void;
	onChangeFolder: () => void;
	onChangeRemote: (value: string) => void;
	onContinue: () => void;
	onOpenChange: (open: boolean) => void;
	onTryWorkspace: () => void;
	open: boolean;
	remoteUrl: string;
	suggestWorkspace: boolean;
	step: ProjectImportStep | null;
	isPreparingGit: boolean;
	validation: ImportValidationResult | null;
}) {
	const { t } = useTranslation();
	if (!validation || !step) return null;
	const needsRemote = validation.root.requiredActions.includes("set_remote");
	const hasChildRepos = (validation.childRepos?.length ?? 0) > 0;
	const hasFailedStep = events.some((event) => event.state === "error");
	const missingApprovals = validation.root.requiredActions.filter((action) => !approvedActions.includes(action));
	const continueDisabled = disabled || missingApprovals.length > 0 || (needsRemote && remoteUrl.trim() === "");
	return (
		<Dialog.Root open={open} onOpenChange={onOpenChange}>
			<Dialog.Portal>
				<Dialog.Content
					className="fixed left-1/2 top-1/2 z-overlay flex max-h-[min(640px,calc(100svh-24px))] w-[min(560px,calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-lg border border-border bg-popover p-0 text-popover-foreground shadow-xl data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none"
					onInteractOutside={(event) => event.preventDefault()}
					onPointerDownOutside={(event) => event.preventDefault()}
				>
					<div className="relative flex shrink-0 items-center gap-3 px-4 pt-3">
						<Button
							type="button"
							variant="outline"
							size="icon"
							aria-label={t("createProject.backToSource")}
							disabled={disabled}
							onClick={onBack}
						>
							<ChevronRight className="size-4 rotate-180" aria-hidden="true" />
						</Button>
						<div className="min-w-0 flex-1 pr-8">
							<Dialog.Title className="text-[18px] font-semibold text-[var(--color-text-import-title)]">
								{step === "prepare_git" ? t("createProject.prepareProjectTitle") : t("createProject.importProject")}
							</Dialog.Title>
							<Dialog.Description className="sr-only">
								{step === "blocked" ? t("createProject.projectImportBlocked") : t("createProject.projectImportApproval")}
							</Dialog.Description>
						</div>
						<Dialog.Close asChild>
							<button
								type="button"
								className="settings-close-button"
								aria-label={t("createProject.closeProjectImport")}
								disabled={disabled}
							>
								<X className="size-4" aria-hidden="true" />
							</button>
						</Dialog.Close>
					</div>
					<div className="min-h-0 space-y-4 overflow-y-auto px-4 pb-1 pt-4">
						<div className="space-y-2">
							<Label htmlFor="projectImportFolder" className="text-[13px] font-semibold text-[var(--color-text-import-title)]">
								{t("createProject.projectFolder")}
							</Label>
							<button
								type="button"
								id="projectImportFolder"
								aria-label={t("createProject.change")}
								className="flex h-control-form w-full items-center overflow-hidden rounded-md border border-transparent bg-[var(--color-bg-import-card)] text-left text-[13px] text-foreground outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50"
								disabled={disabled}
								onClick={onChangeFolder}
							>
								<span className="flex min-w-0 flex-1 items-center gap-3 px-3">
									<Folder className="size-4 shrink-0 text-[var(--color-text-import-muted)]" aria-hidden="true" />
									<span className="truncate">{displayImportPath(validation.root.repoPath)}</span>
								</span>
								<span className="flex h-full shrink-0 items-center border-l border-border/60 px-4 text-foreground hover:bg-foreground/10">
									{t("createProject.change")}
								</span>
							</button>
						</div>
						{hasChildRepos || suggestWorkspace ? (
							<div className="text-[12px] leading-5 text-foreground">
								<span>{t(hasChildRepos ? "createProject.projectHasChildRepos" : "createProject.projectSuggestWorkspace")}</span>
								<button
									type="button"
									className="ml-2 inline-flex items-center rounded-md border border-border/70 bg-muted/50 px-2 py-0.5 text-[11px] font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50"
									disabled={disabled}
									onClick={onTryWorkspace}
								>
									{t("createProject.tryImportWorkspace")}
								</button>
							</div>
						) : null}
						{validation.warning ? (
							<div className="border-l-2 border-amber-500/60 pl-3 text-[12px] leading-5 text-muted-foreground">{validation.warning}</div>
						) : null}
						{error ? (
							<div className="rounded-md bg-destructive/10 px-3 py-2.5 text-[12px] leading-5 text-destructive" role="alert">
								{error}
							</div>
						) : null}
						{step === "prepare_git" ? (
							<section className="space-y-2">
								<div className="flex items-center justify-between">
									<h3 className="text-[13px] font-semibold text-[var(--color-text-import-title)]">{t("createProject.projectSetup")}</h3>
									{isPreparingGit ? (
										<span className="text-[11px] text-muted-foreground" role="status">
											{t("createProject.projectSetupRunning")}
										</span>
									) : null}
								</div>
								<div className="divide-y divide-border overflow-hidden rounded-md border border-border/70 bg-background/40">
									{validation.root.requiredActions.map((action) => {
											const checked = approvedActions.includes(action);
											return (
											<div key={action} className="flex items-start gap-3 px-3 py-3 transition-colors hover:bg-muted/50">
													<input
														id={`projectImportAction-${action}`}
														type="checkbox"
														className="mt-0.5 size-4 rounded border-border"
														checked={checked}
														disabled={disabled}
														onChange={(event) =>
															onChangeApprovedActions(
															event.target.checked ? [...approvedActions, action] : approvedActions.filter((value) => value !== action),
															)
														}
													/>
													<span className="min-w-0 flex-1">
														<Label
															htmlFor={`projectImportAction-${action}`}
															className="block cursor-pointer text-[13px] font-medium text-foreground"
														>
															{gitActionLabel(action)}
														</Label>
														{action === "set_remote" ? (
															<span className="mt-3 block space-y-2">
																<Label
																	htmlFor="projectImportRemote"
																	className="text-[12px] font-semibold text-[var(--color-text-import-title)]"
																>
																	{t("createProject.originRemoteUrl")}
																</Label>
																<Input
																	id="projectImportRemote"
																	autoCapitalize="none"
																	autoComplete="off"
													className="bg-[var(--color-bg-import-card)] text-[13px]"
																	disabled={disabled}
																	placeholder={t("createProject.cloneRepositoryUrlPlaceholder")}
																	spellCheck={false}
																	value={remoteUrl}
																	onChange={(event) => onChangeRemote(event.target.value)}
																/>
																<span className="block text-[11px] leading-4 text-muted-foreground">
																	{t("createProject.remoteRepoRequired")}
																</span>
															</span>
														) : null}
													</span>
												</div>
											);
										})}
									</div>
									{missingApprovals.length > 0 ? (
									<p className="text-[11px] leading-4 text-muted-foreground">{t("createProject.projectSetupContinue")}</p>
									) : null}
							</section>
						) : null}
					</div>
					<div className="flex shrink-0 items-center justify-end gap-2 px-4 pb-4 pt-3">
						{step === "blocked" ? (
							<>
								<Button type="button" variant="outline" disabled={disabled} onClick={onBack}>
									{t("createProject.back")}
								</Button>
								<Button type="button" variant="primary" disabled={disabled} onClick={onChangeFolder}>
									{t("createProject.chooseAnotherFolder")}
								</Button>
							</>
						) : null}
						{step === "prepare_git" ? (
							<>
								<Button type="button" variant="outline" disabled={disabled} onClick={onBack}>
									{t("createProject.back")}
								</Button>
								<Button type="button" variant="primary" disabled={continueDisabled} onClick={onContinue}>
									{hasFailedStep ? t("createProject.retry") : t("createProject.cloneContinue")}
								</Button>
							</>
						) : null}
					</div>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
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
	onPrepareRepository,
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
	onPrepareRepository: (repoPath: string) => void;
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
			? repo.blockingErrors.length > 0
			: (repo.status === "error" || !repo.hasRemote) && !repo.needsGitInit && repo.reason !== "Repository must have at least one commit.",
	);
	const readyRepos = isWorkspace
		? displayRepos.filter((repo) => repo.blockingErrors.length === 0 && repo.requiredActions.length === 0)
		: displayRepos.filter((repo) => (repo.status !== "error" && repo.hasRemote) || repo.needsGitInit);
	const workspaceRequiresInitializedChildRepo =
		isWorkspace && validation?.blockingErrors.includes("WORKSPACE_CHILD_REPO_REQUIRED") === true;
	const workspaceWillImportAsProject = isWorkspace && Boolean(validation?.warning);
	const workspaceHasReadyRepo =
		isWorkspace &&
		(validation?.childRepos ?? []).some((repo) => repo.isRepo && repo.hasCommit && repo.hasOrigin && repo.blockingErrors.length === 0);
	const workspaceNeedsReadyRepo =
		isWorkspace && !workspaceWillImportAsProject && !workspaceRequiresInitializedChildRepo && !workspaceHasReadyRepo;
	const hasScan = scan !== null;
	const canContinue =
		hasScan && failedRepos.length === 0 && !error && (!isWorkspace || workspaceWillImportAsProject || workspaceHasReadyRepo);
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
								<div className="space-y-2">
									<Label htmlFor="importFolderPath" className="text-[13px] font-semibold text-[var(--color-text-import-title)]">
											{isWorkspace ? t("createProject.workspaceRoot") : t("createProject.projectFolder")}
									</Label>
									<button
										type="button"
										id="importFolderPath"
										aria-label={t("createProject.change")}
										className="flex h-control-form w-full items-center overflow-hidden rounded-md border border-transparent bg-[var(--color-bg-import-card)] text-left text-[13px] text-foreground outline-none focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/50 disabled:pointer-events-none disabled:opacity-50"
										disabled={disabled}
										onClick={onChooseFolder}
									>
										<span className="flex min-w-0 flex-1 items-center gap-3 px-3">
											<Folder className="size-4 shrink-0 text-[var(--color-text-import-muted)]" aria-hidden="true" />
											<span className="truncate">{displayImportPath(scan.path)}</span>
										</span>
										<span className="flex h-full shrink-0 items-center border-l border-border/60 px-4 text-foreground hover:bg-foreground/10">
										{t("createProject.change")}
										</span>
									</button>
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
													{t("createProject.footerResolve", {
														count: failedRepos.length,
													})}
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
										Initialize at least one child repository with a commit and origin remote before importing this workspace.
									</div>
								) : null}
								{workspaceNeedsReadyRepo && !error ? (
									<div className="rounded-md bg-[var(--color-bg-import-card)] px-3 py-2.5 text-[12px] leading-5 text-[var(--color-text-import-muted)]">
										Set up at least one child repository to continue. Other children can remain unconfigured.
									</div>
								) : null}

								{isWorkspace ? (
									<WorkspaceImportRepoList
										approvedActions={approvedActions}
										disabled={disabled}
										events={events}
										isPreparingGit={isPreparingGit}
										onChangeApprovedActions={onChangeApprovedActions}
										onChangeRemoteUrl={onChangeRemoteUrl}
										onPrepareRepository={onPrepareRepository}
										remoteUrls={remoteUrls}
										repos={displayRepos}
									/>
								) : readyRepos.length > 0 ? (
										<div
										className="max-h-[288px] min-h-0 divide-y divide-border/50 overflow-y-auto overscroll-contain rounded-sm bg-[var(--color-bg-import-card)]"
										tabIndex={0}
										onWheel={(event) => event.stopPropagation()}
										style={{
											height: "auto",
											maxHeight: "min(288px, 40vh)",
											overflowY: "auto",
										}}
										>
										{readyRepos.map((repo) => (
											<ImportRepoRow key={repo.path} repo={repo} />
									))}
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
					<div className="flex shrink-0 justify-end gap-2 px-4 pb-4 pt-3">
						<div className="flex flex-wrap items-center justify-end gap-3">
							<Button type="button" variant="outline" disabled={disabled} onClick={() => onOpenChange(false)}>
								{t("createProject.cancel")}
							</Button>
							{canContinue && (
								<Button type="button" variant="primary" disabled={disabled} onClick={onContinue}>
									{workspaceWillImportAsProject ? "Continue as project" : t("createProject.cloneContinue")}
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
	].sort((left, right) => left.name.localeCompare(right.name));
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
		needsGitInit: scanRepo?.needsGitInit ?? validationRepo?.needsGitInit,
		requiredActions: validationRepo?.requiredActions ?? [],
		blockingErrors: validationRepo?.blockingErrors ?? [],
		isRepo: validationRepo?.isRepo,
		hasCommit: validationRepo?.hasCommit,
		hasOrigin: validationRepo?.hasOrigin,
	};
}

function WorkspaceImportRepoList({
	approvedActions,
	disabled,
	events,
	isPreparingGit,
	onChangeApprovedActions,
	onChangeRemoteUrl,
	onPrepareRepository,
	remoteUrls,
	repos,
}: {
	approvedActions: WorkspaceApprovalState;
	disabled: boolean;
	events: GitPreparationEvent[];
	isPreparingGit: boolean;
	onChangeApprovedActions: (repoPath: string, actions: string[]) => void;
	onChangeRemoteUrl: (repoPath: string, remoteUrl: string) => void;
	onPrepareRepository: (repoPath: string) => void;
	remoteUrls: WorkspaceRemoteState;
	repos: DisplayImportRepo[];
}) {
	const [expandedPath, setExpandedPath] = useState<string | null>(null);
	const orderedRepos = [...repos].sort((left, right) => {
		const leftNeedsSetup = left.requiredActions.length > 0 ? 0 : 1;
		const rightNeedsSetup = right.requiredActions.length > 0 ? 0 : 1;
		return leftNeedsSetup - rightNeedsSetup || left.name.localeCompare(right.name);
	});

	return (
		<div className="overflow-hidden rounded-sm bg-[var(--color-bg-import-card)] divide-y divide-border/50">
			{orderedRepos.map((repo) => {
				const needsSetup = repo.requiredActions.length > 0;
				const expanded = expandedPath === repo.path;
				return (
					<div key={repo.path}>
						<ImportRepoRow
							onSetup={needsSetup ? () => setExpandedPath(expanded ? null : repo.path) : undefined}
							repo={repo}
							setupExpanded={expanded}
						/>
						{needsSetup ? (
							<div
								aria-hidden={!expanded}
								className="grid transition-[grid-template-rows] duration-200 ease-out motion-reduce:transition-none"
								style={{ gridTemplateRows: expanded ? "1fr" : "0fr" }}
							>
								<div className="min-h-0 overflow-hidden">
									<div className="border-t border-border/50 px-3 pb-3 pt-2">
										<WorkspaceInlineSetup
											approvedActions={approvedActions[repo.path] ?? []}
											disabled={disabled || isPreparingGit}
											events={events}
											onChangeApprovedActions={(actions) => onChangeApprovedActions(repo.path, actions)}
											onChangeRemoteUrl={(remoteUrl) => onChangeRemoteUrl(repo.path, remoteUrl)}
											onPrepare={() => onPrepareRepository(repo.path)}
											repo={repo}
											remoteUrl={remoteUrls[repo.path] ?? ""}
										/>
									</div>
								</div>
							</div>
						) : null}
					</div>
				);
			})}
		</div>
	);
}

function WorkspaceInlineSetup({
	approvedActions,
	disabled,
	events,
	onChangeApprovedActions,
	onChangeRemoteUrl,
	onPrepare,
	repo,
	remoteUrl,
}: {
	approvedActions: string[];
	disabled: boolean;
	events: GitPreparationEvent[];
	onChangeApprovedActions: (actions: string[]) => void;
	onChangeRemoteUrl: (remoteUrl: string) => void;
	onPrepare: () => void;
	repo: DisplayImportRepo;
	remoteUrl: string;
}) {
	const { t } = useTranslation();
	const missingApprovals = repo.requiredActions.some((action) => !approvedActions.includes(action));
	const missingRemote = repo.requiredActions.includes("set_remote") && remoteUrl.trim() === "";
	return (
		<div className="space-y-2 rounded-md border border-border/60 bg-[var(--color-bg-import-modal)] p-2.5">
			{orderedGitActions(repo.requiredActions).map((action) => {
				const checked = approvedActions.includes(action);
				const state = latestWorkspaceActionState(repo.path, action, events);
				return (
					<div key={action} className="space-y-2">
						<label className="flex items-center gap-2 text-[12px] text-[var(--color-text-import-title)]">
							<input
								checked={checked}
								disabled={disabled}
								onChange={(event) =>
									onChangeApprovedActions(
										event.target.checked
											? orderedGitActions([...approvedActions, action])
											: approvedActions.filter((item) => item !== action),
									)
								}
								type="checkbox"
							/>
							<span className="font-medium">{gitActionLabel(action)}</span>
							{workspaceActionStateLabel(action, state, checked, remoteUrl) ? (
								<span className="ml-auto font-mono text-[11px] text-[var(--color-text-import-muted)]">
									{workspaceActionStateLabel(action, state, checked, remoteUrl)}
								</span>
							) : null}
						</label>
						{action === "set_remote" ? (
							<Input
								aria-label={t("createProject.originRemoteUrl")}
								className="h-8 bg-[var(--color-bg-import-card)] font-mono text-[12px]"
								disabled={disabled}
								placeholder="https://github.com/owner/repository.git"
								value={remoteUrl}
								onChange={(event) => onChangeRemoteUrl(event.target.value)}
							/>
						) : null}
					</div>
				);
			})}
			<div className="flex justify-end pt-1">
				<Button type="button" variant="primary" disabled={disabled || missingApprovals || missingRemote} onClick={onPrepare}>
					{events.some((event) => event.repoPath === repo.path && event.state === "error") ? "Retry setup" : "Apply setup"}
				</Button>
			</div>
		</div>
	);
}

function workspaceActionStateLabel(action: string, state: string, checked: boolean, remoteUrl: string): string {
	if (state === "success") return "Done";
	if (state === "error") return "Failed";
	if (state === "running") return "Running";
	if (state === "pending") return "Queued";
	if (!checked) return "Required";
	if (action === "set_remote") return remoteUrl.trim() === "" ? "Required" : "";
	return "";
}

function ImportRepoRow({
	failed = false,
	onSetup,
	repo,
	setupExpanded = false,
}: {
	failed?: boolean;
	onSetup?: () => void;
	repo: DisplayImportRepo;
	setupExpanded?: boolean;
}) {
	const { t } = useTranslation();
	const repositoryAvatar = repo.hasRemote ? repositoryAvatarFromGitUrl(repo.remote) : null;
	const repositoryUrl = !failed && !repo.needsGitInit ? repositoryWebUrl(repo.remote) : null;
	return (
		<div className={cn("flex shrink-0 items-center gap-2.5 py-1.5 pl-3", repo.needsGitInit ? "pr-1.5" : "pr-3")}>
			<div className="flex size-4 shrink-0 items-center justify-center">
				{repo.needsGitInit ? (
					<Folder className="size-4 text-[var(--color-text-import-muted)]" aria-hidden="true" />
				) : repositoryAvatar ? (
					<ImportRepositoryAvatar owner={repositoryAvatar.owner} url={repositoryAvatar.url} />
			) : (
					<Folder className="size-5 text-[var(--color-text-import-muted)]" aria-hidden="true" />
			)}
			</div>
			<div className="min-w-0 flex-1 truncate text-[13px] font-semibold text-[var(--color-text-import-title)]">{repo.name}</div>
			<div className="group flex max-w-[220px] shrink-0 items-center gap-1 truncate text-right text-[11px] text-[var(--color-text-import-muted)]">
				{onSetup ? (
					<button
						aria-expanded={setupExpanded}
						className="cursor-pointer rounded-sm border border-orange-400/40 bg-orange-500/15 px-2 py-0.5 text-[11px] text-orange-300 hover:bg-orange-500/25"
						onClick={onSetup}
						type="button"
					>
						{setupExpanded ? "Hide setup" : `${workspaceSetupLabel(repo)} · Set up`}
					</button>
				) : repositoryUrl ? (
					<button
						type="button"
						className="group flex min-w-0 items-center gap-1 truncate hover:text-foreground hover:underline"
						onClick={() => void aoBridge.app.openExternal(repositoryUrl)}
					>
						<GitBranch className="size-3.5 shrink-0 group-hover:text-foreground" aria-hidden="true" />
						<span className="truncate">{repo.branch}</span>
					</button>
				) : (
					<>
						{failed ? (
							<XCircle className="size-3.5 shrink-0 text-destructive" aria-hidden="true" />
						) : !repo.needsGitInit ? (
							<GitBranch className="size-3.5 shrink-0" aria-hidden="true" />
						) : null}
						<span className={cn("truncate", repo.needsGitInit && "rounded-sm bg-orange-500/15 px-2 py-0.5 text-orange-300")}>
							{repo.needsGitInit ? "Needs git init" : failed ? (repo.reason ?? t("createProject.repoCannotImport")) : repo.branch}
						</span>
					</>
				)}
			</div>
		</div>
	);
}

function workspaceSetupLabel(repo: DisplayImportRepo): string {
	const actions = new Set(repo.requiredActions);
	if (actions.size === 1 && actions.has("set_remote")) return "No remote";
	if (actions.size === 1 && actions.has("git_commit")) return "No commits";
	if (actions.has("git_init")) return "Not a Git repo";
	return "Git setup needed";
}

function ImportRepositoryAvatar({ owner, url }: { owner: string; url: string }) {
	const [state, setState] = useState<"loading" | "loaded" | "failed">("loading");

	return (
		<span className="relative block size-4" aria-hidden="true">
			<img
				alt=""
				className={cn(
					"absolute inset-0 size-4 rounded-full object-cover outline outline-1 -outline-offset-1 outline-black/10 dark:outline-white/10",
					state === "loaded" ? "opacity-100" : "opacity-0",
				)}
				draggable={false}
				loading="eager"
				onError={() => setState("failed")}
				onLoad={() => setState("loaded")}
				referrerPolicy="no-referrer"
				src={url}
			/>
			{state === "loading" ? <span className="absolute inset-0 size-4 animate-pulse rounded-full bg-muted-foreground/40" /> : null}
			{state === "failed" ? (
				<span className="absolute inset-0 size-4 rounded-full bg-muted text-center text-[7px] font-semibold leading-4 text-muted-foreground">
					{ownerInitials(owner)}
				</span>
			) : null}
		</span>
	);
}

function ownerInitials(owner: string): string {
	return (
		owner
			.split(/[-_\s/]+/)
			.filter(Boolean)
			.slice(0, 2)
			.map((part) => part[0]?.toUpperCase() ?? "")
			.join("") || "?"
	);
}

function displayImportPath(value: string) {
	return value.replace(/^\/Users\/[^/]+/, "~");
}

function repositoryWebUrl(remote: string): string | null {
	const value = remote.trim();
	const scpMatch = value.match(/^[^/@:\s]+@([^/:\s]+):(.+)$/);
	if (scpMatch?.[1] && scpMatch[2]) {
		return `https://${scpMatch[1]}/${scpMatch[2].replace(/^\/+|\/+$/g, "").replace(/\.git$/, "")}`;
	}
	try {
		const parsed = new URL(value);
		if (!["http:", "https:", "ssh:", "git:"].includes(parsed.protocol) || !parsed.hostname) return null;
		const repositoryPath = parsed.pathname.replace(/^\/+|\/+$/g, "").replace(/\.git$/, "");
		return repositoryPath ? `https://${parsed.hostname}/${repositoryPath}` : null;
	} catch {
		return null;
	}
}
