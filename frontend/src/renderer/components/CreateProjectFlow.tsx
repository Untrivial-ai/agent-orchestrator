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
import { useUiStore } from "../stores/ui-store";
import { cn } from "../lib/utils";
import type { ProjectKind } from "../types/workspace";
import { CreateProjectAgentSheet, type CreateProjectAgentSelection } from "./CreateProjectAgentSheet";
import CloneRepositoryDialog, { type CloneRepositoryDetails, type CloneRepositorySelection } from "./CloneRepositoryDialog";
import CreateProjectProgressDialog from "./CreateProjectProgressDialog";
import { ModalBackdrop } from "./ModalBackdrop";
import { PathRow } from "./PathRow";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Tabs, TabsList, TabsTrigger } from "./ui/tabs";

export type CreateProjectInput = { path: string; asWorkspace?: boolean; defaultBranch?: string } & CreateProjectAgentSelection;
export type CloneProjectInput = Pick<CloneRepositorySelection, "remoteUrl" | "destinationParent"> &
	CreateProjectAgentSelection;

const LAST_CLONE_DESTINATION_KEY = "ao.clone.lastDestinationParent";
const LAST_IMPORT_REMOTE_URL_KEY = "ao.import.lastRemoteUrl";
const WORKSPACE_FOLDER_SETUP_ACTIONS = ["git_init", "git_commit", "set_remote"];
type ImportValidationResult = components["schemas"]["ImportValidationResult"];
type GitPreparationEvent = components["schemas"]["GitPreparationEvent"];
type ProjectImportStep = "blocked" | "prepare_git";
type DisplayImportRepo = ImportFolderScan["repos"][number] & {
	requiredActions: string[];
	blockingErrors: string[];
	isRepo?: boolean;
	hasCommit?: boolean;
	hasOrigin?: boolean;
};
type RepositoryPreparationState = {
	repoPath: string;
	approvedActions: string[];
	remoteUrl: string;
};
type WorkspaceApprovalState = Record<string, string[]>;
type WorkspaceRemoteState = Record<string, string>;

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
	existingProjectNames = [],
	existingProjectPaths = [],
	idleLabel,
	mode = "single_repo",
	onCreateProject,
	onInitializeProject,
	openSignal,
	sourceSignal,
}: {
	children?: (state: { choosePath: () => void; disabled: boolean; error: string | null; label: string }) => ReactNode;
	existingProjectNames?: readonly string[];
	existingProjectPaths?: readonly string[];
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
	const [cloneDetails, setCloneDetails] = useState<CloneRepositoryDetails>(() => ({
		remoteUrl: "",
		destinationParent:
			typeof window === "undefined" ? "" : (window.localStorage.getItem(LAST_CLONE_DESTINATION_KEY) ?? ""),
	}));
	const [cloneSelection, setCloneSelection] = useState<CloneRepositorySelection | null>(null);
	const [preparedClonePath, setPreparedClonePath] = useState<string | null>(null);
	const [folderPickerOpen, setFolderPickerOpen] = useState(false);
	const [childTransitioning, setChildTransitioning] = useState(false);
	const [selectedKind, setSelectedKind] = useState<ProjectKind>(mode === "workspace" ? "workspace" : "single_repo");
	const [selectedPath, setSelectedPath] = useState<string | null>(null);
	const [validationScan, setValidationScan] = useState<ImportFolderScan | null>(null);
	const [projectValidation, setProjectValidation] = useState<ImportValidationResult | null>(null);
	const [projectImportKind, setProjectImportKind] = useState<"project" | "workspace">("project");
	const [projectImportStep, setProjectImportStep] = useState<ProjectImportStep | null>(null);
	const [projectPrepEvents, setProjectPrepEvents] = useState<GitPreparationEvent[]>([]);
	const [projectApprovedActions, setProjectApprovedActions] = useState<string[]>([]);
	const [projectRemoteUrl, setProjectRemoteUrl] = useState("");
	const [projectRepositoryPrep, setProjectRepositoryPrep] = useState<RepositoryPreparationState[]>([]);
	const [workspaceApprovedActions, setWorkspaceApprovedActions] = useState<WorkspaceApprovalState>({});
	const [workspaceRemoteUrls, setWorkspaceRemoteUrls] = useState<WorkspaceRemoteState>({});
	const [workspacePrepEvents, setWorkspacePrepEvents] = useState<GitPreparationEvent[]>([]);
	const [projectImportShake, setProjectImportShake] = useState(false);
	const [isChoosingPath, setIsChoosingPath] = useState(false);
	const [isCreating, setIsCreating] = useState(false);
	const [isInitializing, setIsInitializing] = useState(false);
	const [createProgressOpen, setCreateProgressOpen] = useState(false);
	const [createProgress, setCreateProgress] = useState(0);
	const [createProgressStage, setCreateProgressStage] = useState<CreateProgressStage>("starting");
	const [isPreparingGit, setIsPreparingGit] = useState(false);
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
	const showGlobalToast = useUiStore((state) => state.showGlobalToast);
	const resetProjectImportState = () => {
		setProjectValidation(null);
		setProjectImportKind("project");
		setProjectImportStep(null);
		setProjectPrepEvents([]);
		setProjectApprovedActions([]);
		setProjectRemoteUrl("");
		setProjectRepositoryPrep([]);
		setWorkspaceApprovedActions({});
		setWorkspaceRemoteUrls({});
		setWorkspacePrepEvents([]);
		setProjectImportShake(false);
	};

	const abandonPreparedClone = async () => {
		const path = preparedClonePath;
		if (!path) return;
		setPreparedClonePath(null);
		try {
			await apiClient.POST("/api/v1/projects/clone/cleanup", { body: { path } });
		} catch {
			// Cleanup is best effort. The marker prevents removing user-owned repos.
		}
	};

	const reportProjectError = (message: string) => {
		setError(message);
		showGlobalToast(t("createProject.setupFailedToastTitle", { defaultValue: "Project setup failed" }), message, "error");
		setProjectImportShake(false);
		window.requestAnimationFrame(() => setProjectImportShake(true));
		window.setTimeout(() => setProjectImportShake(false), 320);
	};

	const transitionToChild = (open: () => void) => {
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
				const validation = await validateImportFolder(path, "project");
				setProjectImportKind("project");
				setProjectValidation(validation);
				setProjectPrepEvents([]);
				setProjectApprovedActions(validation.root.requiredActions);
				setProjectRemoteUrl(validation.root.requiredActions.includes("set_remote") ? suggestedProjectRemoteUrl(validation.root.repoPath) : "");
				setProjectRepositoryPrep((validation.childRepos ?? []).filter((repo) => repo.requiredActions.length > 0).map((repo) => ({ repoPath: repo.repoPath, approvedActions: repo.requiredActions, remoteUrl: suggestedProjectRemoteUrl(repo.repoPath) })));
				if (!validation.isValid || validation.nextStep === "error") {
					reportProjectError(importValidationMessage(validation));
					setProjectImportStep("blocked");
					return;
				}
				if (validation.nextStep === "choose_import_kind") {
					setProjectImportStep("blocked");
					return;
				}
				if (validation.nextStep === "prepare_git") {
					setProjectImportStep("prepare_git");
					return;
				}
			}
			if (path && kind === "workspace") {
				try {
					const [validation, scan, ancestorWarning] = await Promise.all([
						validateImportFolder(path, "workspace"),
						aoBridge.app.scanImportFolder({ path, mode: "workspace" }),
						aoBridge.app.checkAncestorRepo(path).catch(() => undefined),
					]);
					setValidationScan(scan);
					setRepositorySetupWarning(ancestorWarning ?? scan.setupWarning ?? null);
					if (ancestorWarning ?? scan.setupWarning) setRepositorySetup("NOT_A_GIT_REPO");
					setProjectValidation(validation);
					setProjectPrepEvents([]);
					setProjectApprovedActions(validation.root.requiredActions);
					setProjectRemoteUrl("");
					setProjectRepositoryPrep((validation.childRepos ?? []).filter((repo) => repo.requiredActions.length > 0).map((repo) => ({
						repoPath: repo.repoPath,
						approvedActions: repo.requiredActions,
						remoteUrl: suggestedProjectRemoteUrl(repo.repoPath),
					})));
					const workspaceRepos = mergeWorkspaceImportRepos(scan, validation);
					setWorkspaceApprovedActions(Object.fromEntries(workspaceRepos.filter((repo) => repo.requiredActions.length > 0).map((repo) => [repo.path, [...repo.requiredActions]])));
					setWorkspaceRemoteUrls(Object.fromEntries(workspaceRepos.filter((repo) => repo.requiredActions.includes("set_remote")).map((repo) => [repo.path, suggestedProjectRemoteUrl(repo.path)])));
					setWorkspacePrepEvents([]);
					setProjectImportKind("workspace");
					setFolderPickerOpen(true);
					return;
				} catch (err) {
					setValidationScan({ path, repos: [] });
					setError(err instanceof Error ? err.message : t("createProject.couldNotAdd"));
					setFolderPickerOpen(true);
					return;
				}
			}
			if (path) {
				setModePickerOpen(false);
				setSelectedPath(path);
				setFolderPickerOpen(false);
			}
		} catch (err) {
			reportProjectError(err instanceof Error ? err.message : t("createProject.couldNotAdd"));
		} finally {
			setIsChoosingPath(false);
		}
	};

	const startFlow = (presetPath?: string) => {
		setPendingDropPath(presetPath ?? null);
		// Each entry starts on the default Local choice, never a leftover Cloud one.
		setOffering("local");
		resetProjectImportState();
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
		const showProgress = Boolean(cloneSelection);
		if (showProgress) {
			setCreateProgress(0);
			setCreateProgressStage("starting");
			setCreateProgressOpen(true);
		}
		try {
			if (cloneSelection) {
				if (!preparedClonePath) throw new Error(t("createProject.couldNotAdd"));
				await onCreateProject({ path: selectedPath, ...selection });
				setSelectedPath(null);
				setCloneSelection(null);
				setPreparedClonePath(null);
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
			if (showProgress) {
				setCreateProgress(100);
				setCreateProgressStage("complete");
				await new Promise((resolve) => window.setTimeout(resolve, 180));
			}
			setSelectedPath(null);
		} catch (err) {
			const code = err instanceof Error && "code" in err ? (err.code as string | undefined) : undefined;
			const message = err instanceof Error ? err.message : t("createProject.couldNotAdd");
			if (!cloneSelection && selectedKind === "single_repo" && isRepositorySetupRecoveryCode(code)) {
				setRepositorySetup(code);
			}
			setError(message);
			if (hasModePicker && !cloneSelection && shouldScanCreateFailure(message)) {
				try {
					const scan = await aoBridge.app.scanImportFolder({
						path: selectedPath,
						mode: selectedKind === "workspace" ? "workspace" : "project",
					});
					setValidationScan(scan);
				} catch {
					setValidationScan({ path: selectedPath, repos: [] });
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

	const prepareClone = async (next: CloneRepositorySelection) => {
		setError(null);
		setIsPreparingGit(true);
		try {
			const { data, error: apiError } = await apiClient.POST("/api/v1/projects/clone/prepare", {
			body: {
				remoteUrl: next.remoteUrl,
				destinationParent: next.destinationParent,
			},
			});
			if (apiError || !data) throw new Error(apiErrorMessage(apiError, t("createProject.couldNotAdd")));
			const validation = await validateImportFolder(data.path, "project");
			setCloneDialogOpen(false);
			setCloneSelection(next);
			setPreparedClonePath(data.path);
			setSelectedKind("single_repo");
			setModePickerOpen(false);
			setProjectValidation(validation);
			setProjectPrepEvents([]);
			setProjectApprovedActions(validation.root.requiredActions);
			setProjectRemoteUrl(validation.root.requiredActions.includes("set_remote") ? next.remoteUrl : "");
			if (!validation.isValid || validation.nextStep === "error") {
				reportProjectError(importValidationMessage(validation));
				setProjectImportStep("blocked");
				return;
			}
			if (validation.nextStep === "prepare_git") {
				setProjectImportStep("prepare_git");
				return;
			}
			setSelectedPath(data.path);
		} catch (err) {
			reportProjectError(err instanceof Error ? err.message : t("createProject.couldNotAdd"));
			setCloneDialogOpen(true);
		} finally {
			setIsPreparingGit(false);
		}
	};

	const reopenSourcePicker = () => {
		void abandonPreparedClone();
		setCloneSelection(null);
		resetProjectImportState();
		if (hasModePicker) {
			setModePickerOpen(true);
			return;
		}
		setError(null);
	};

	const tryProjectAsWorkspace = () => {
		if (!projectValidation) return;
		setPendingDropPath(null);
		void chooseDirectory("workspace", projectValidation.root.repoPath);
	};

	const prepareProjectGit = async () => {
		if (!projectValidation) return;
		setError(null);
		const remoteUrl = projectRemoteUrl.trim();
		if (remoteUrl !== "") {
			if (!isValidProjectRemote(remoteUrl)) {
				reportProjectError(t("createProject.cloneInvalidUrl"));
				return;
			}
		}
		if (projectImportKind === "workspace" && projectRepositoryPrep.some((repo) => repo.remoteUrl.trim() !== "" && !isValidProjectRemote(repo.remoteUrl.trim()))) {
			reportProjectError(t("createProject.cloneInvalidUrl"));
			return;
		}
		setProjectPrepEvents(
			projectImportKind === "workspace"
				? projectRepositoryPrep.flatMap((repo) => projectRequestedActionEvents(repo.repoPath, repo.approvedActions))
				: projectRequestedActionEvents(projectValidation.root.repoPath, projectApprovedActions),
		);
		setIsPreparingGit(true);
		try {
			const { data, error: apiError } = await apiClient.POST("/api/v1/imports/prepare-git", {
				body: {
					importKind: projectImportKind,
					path: projectValidation.root.repoPath,
					...(projectImportKind === "workspace"
						? { repositories: projectRepositoryPrep.map((repo) => ({ repoPath: repo.repoPath, approvedActions: repo.approvedActions, remoteUrl: repo.remoteUrl.trim() || undefined })) }
						: { approvedActions: projectApprovedActions, remoteUrl: remoteUrl || undefined }),
				},
			});
			if (apiError || !data) throw new Error(apiErrorMessage(apiError, t("createProject.couldNotAdd")));
			setProjectPrepEvents(data.events);
			setProjectValidation(data.validation);
			setProjectApprovedActions(data.validation.root.requiredActions);
			setProjectRepositoryPrep((data.validation.childRepos ?? []).filter((repo) => repo.requiredActions.length > 0).map((repo) => ({ repoPath: repo.repoPath, approvedActions: repo.requiredActions, remoteUrl: suggestedProjectRemoteUrl(repo.repoPath) })));
			if (projectRemoteUrl.trim() !== "") persistSuggestedProjectRemoteUrl(projectRemoteUrl);
			const failed = data.events.find((event) => event.state === "error");
			if (failed) {
				reportProjectError(projectPreparationFailureMessage(failed));
				return;
			}
			if (!data.validation.isValid || data.validation.nextStep === "error") {
				reportProjectError(importValidationMessage(data.validation));
				setProjectImportStep("blocked");
				return;
			}
			if (data.validation.nextStep === "continue") {
				setModePickerOpen(false);
				setProjectImportStep(null);
				setSelectedPath(data.validation.root.repoPath);
			}
		} catch (err) {
			reportProjectError(err instanceof Error ? err.message : t("createProject.couldNotAdd"));
		} finally {
			setIsPreparingGit(false);
		}
	};

	const prepareWorkspaceRepository = async (repoPath: string) => {
		if (!projectValidation) return;
		const status = projectValidation.childRepos?.find((repo) => repo.repoPath === repoPath);
		if (!status || status.requiredActions.length === 0) return;
		setIsPreparingGit(true);
		setWorkspacePrepEvents(projectRequestedActionEvents(repoPath, workspaceApprovedActions[repoPath] ?? status.requiredActions));
		try {
			const { data, error: apiError } = await apiClient.POST("/api/v1/imports/prepare-git", {
				body: {
					importKind: "workspace",
					path: projectValidation.root.repoPath,
					repositories: [{ repoPath, approvedActions: workspaceApprovedActions[repoPath] ?? status.requiredActions, remoteUrl: workspaceRemoteUrls[repoPath]?.trim() || undefined }],
				},
			});
			if (apiError || !data) throw new Error(apiErrorMessage(apiError, t("createProject.couldNotAdd")));
			setWorkspacePrepEvents(data.events);
			setProjectValidation(data.validation);
			setProjectRepositoryPrep((data.validation.childRepos ?? []).filter((repo) => repo.requiredActions.length > 0).map((repo) => ({ repoPath: repo.repoPath, approvedActions: repo.requiredActions, remoteUrl: suggestedProjectRemoteUrl(repo.repoPath) })));
			setWorkspaceApprovedActions(Object.fromEntries((data.validation.childRepos ?? []).filter((repo) => repo.requiredActions.length > 0).map((repo) => [repo.repoPath, [...repo.requiredActions]])));
		} catch (err) {
			reportProjectError(err instanceof Error ? err.message : t("createProject.couldNotAdd"));
		} finally {
			setIsPreparingGit(false);
		}
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
			<CreateProjectFlowBackdrop open={modePickerOpen || cloneDialogOpen || folderPickerOpen || selectedPath !== null || createProgressOpen || childTransitioning || projectImportOpen} />
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
						childOpen={childTransitioning || cloneDialogOpen || folderPickerOpen || projectImportOpen || selectedPath !== null}
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
					{cloneDialogOpen ? (
						<CloneRepositoryDialog
							disabled={isBusy}
							error={error}
							existingProjectNames={existingProjectNames}
							existingProjectPaths={existingProjectPaths}
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
							onError={reportProjectError}
							open={cloneDialogOpen}
							shake={projectImportShake}
							value={cloneDetails}
						/>
					) : null}
					<CreateProjectFolderDialog
						disabled={isBusy}
						error={error}
						kind={selectedKind}
						open={folderPickerOpen}
						scan={validationScan}
						validation={projectValidation}
						isPreparingGit={isPreparingGit}
						workspaceApprovedActions={workspaceApprovedActions}
						workspaceRemoteUrls={workspaceRemoteUrls}
						workspacePrepEvents={workspacePrepEvents}
						onChangeWorkspaceApprovedActions={(repoPath, actions) => setWorkspaceApprovedActions((current) => ({ ...current, [repoPath]: actions }))}
						onChangeWorkspaceRemoteUrl={(repoPath, remoteUrl) => setWorkspaceRemoteUrls((current) => ({ ...current, [repoPath]: remoteUrl }))}
						onPrepareWorkspaceRepository={(repoPath) => void prepareWorkspaceRepository(repoPath)}
						onContinue={() => {
							if (!validationScan || error) return;
							if (selectedKind === "workspace") {
								setFolderPickerOpen(false);
								if (projectValidation?.nextStep === "prepare_git") {
									setProjectImportStep("prepare_git");
								} else {
									setModePickerOpen(false);
									setSelectedPath(validationScan.path);
								}
								return;
							}
							setFolderPickerOpen(false);
							setSelectedPath(validationScan.path);
							setModePickerOpen(false);
						}}
						onBack={() => {
							setError(null);
							setValidationScan(null);
							setFolderPickerOpen(false);
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
			<ProjectImportDialog
				disabled={isBusy}
				approvedActions={projectApprovedActions}
				importKind={projectImportKind}
				repositoryPrep={projectRepositoryPrep}
				onBack={reopenSourcePicker}
				onChangeApprovedActions={setProjectApprovedActions}
				onChangeFolder={() => void chooseDirectory("single_repo")}
				onChangeRemote={setProjectRemoteUrl}
				onContinue={() => void prepareProjectGit()}
				onContinueProject={() => {
					setProjectImportKind("project");
					setProjectImportStep("prepare_git");
				}}
				onChangeRepositoryPrep={(repoPath, next) => setProjectRepositoryPrep((current) => current.map((repo) => repo.repoPath === repoPath ? { ...repo, ...next } : repo))}
				shake={projectImportShake}
				onOpenChange={(open) => {
					if (isBusy) return;
					if (!open) {
						void abandonPreparedClone();
						setCloneSelection(null);
						resetProjectImportState();
						setError(null);
					}
				}}
				onTryWorkspace={tryProjectAsWorkspace}
				open={projectImportOpen}
				remoteUrl={projectRemoteUrl}
				step={projectImportStep}
				isPreparingGit={isPreparingGit}
				events={projectPrepEvents}
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
					onBack={
					cloneSelection
						? () => {
								void abandonPreparedClone();
								setCloneSelection(null);
								setPreparedClonePath(null);
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
		case "IMPORT_PATH_UNSAFE":
			return "Choose a specific project folder outside AO's own state directories.";
		case "DETACHED_HEAD":
			return "This repository is checked out in detached HEAD state. Check out a named branch before importing it.";
		case "WORKSPACE_CHILD_REPO_REQUIRED":
			return "This workspace needs at least one direct child Git repository.";
		default:
			return "Choose a different folder or repair the repository before continuing.";
	}
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

function orderedProjectActions(actions: string[]): string[] {
	const rank = new Map([
		["git_init", 0],
		["git_commit", 1],
		["set_remote", 2],
	]);
	return [...actions].sort((left, right) => (rank.get(left) ?? Number.MAX_SAFE_INTEGER) - (rank.get(right) ?? Number.MAX_SAFE_INTEGER));
}

function projectRequestedActionEvents(repoPath: string, actions: string[]): GitPreparationEvent[] {
	const ordered = orderedProjectActions(actions);
	return ordered.map((action, index) => ({
		repoPath,
		action: action as GitPreparationEvent["action"],
		state: index === 0 ? "running" : "pending",
	}));
}

function latestPreparationEvents(events: GitPreparationEvent[]): GitPreparationEvent[] {
	const latest = new Map<string, GitPreparationEvent>();
	for (const event of events) latest.set(`${event.repoPath}:${event.action}`, event);
	return [...latest.values()];
}

function suggestedProjectRemoteUrl(repoPath: string): string {
	if (typeof window === "undefined") return "";
	const repoName = repoPath.split(/[\\/]/).filter(Boolean).pop()?.trim();
	const saved = window.localStorage.getItem(LAST_IMPORT_REMOTE_URL_KEY)?.trim() ?? "";
	if (!repoName) return saved;
	const withGitSuffix = repoName.endsWith(".git") ? repoName : `${repoName}.git`;
	if (saved === "") return `https://github.com/username/${withGitSuffix}`;
	const sshMatch = saved.match(/^(git@[^:]+:[^/]+\/)([^/]+?)(\.git)?$/);
	if (sshMatch) return `${sshMatch[1]}${withGitSuffix}`;
	try {
		const parsed = new URL(saved);
		const segments = parsed.pathname.split("/").filter(Boolean);
		if (segments.length >= 2) {
			segments[segments.length - 1] = withGitSuffix;
			parsed.pathname = `/${segments.join("/")}`;
			return parsed.toString();
		}
	} catch {
		return `https://github.com/username/${withGitSuffix}`;
	}
	return `https://github.com/username/${withGitSuffix}`;
}

function isValidProjectRemote(value: string): boolean {
	if (!value || /\s/.test(value) || value.startsWith("-")) return false;
	if (/^[^/@:\s]+@[^/\s]+:.+\/.+$/.test(value)) return true;
	try {
		const parsed = new URL(value);
		return ["file:", "git:", "http:", "https:", "ssh:"].includes(parsed.protocol) &&
			parsed.pathname.split("/").filter(Boolean).length >= 1;
	} catch {
		return false;
	}
}

function persistSuggestedProjectRemoteUrl(remoteUrl: string) {
	if (typeof window === "undefined") return;
	window.localStorage.setItem(LAST_IMPORT_REMOTE_URL_KEY, remoteUrl.trim());
}

function projectPreparationFailureMessage(event: GitPreparationEvent): string {
	return `${displayImportPath(event.repoPath)} failed while running ${gitActionLabel(event.action)}. Review the step below, then retry or go back.`;
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
				<ModalBackdrop />
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
	const { t } = useTranslation();
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
					<Dialog.Title className="sr-only">{t("createProject.addCodeTitle")}</Dialog.Title>
					<Dialog.Description className="sr-only">{t("createProject.addCodeDescription")}</Dialog.Description>
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

function ProjectImportDialog({
	approvedActions,
	disabled,
	events,
	importKind,
	onBack,
	onChangeApprovedActions,
	onChangeFolder,
	onChangeRemote,
	onContinue,
	onContinueProject,
	onChangeRepositoryPrep,
	onOpenChange,
	onTryWorkspace,
	open,
	remoteUrl,
	repositoryPrep,
	shake,
	step,
	isPreparingGit,
	validation,
}: {
	approvedActions: string[];
	disabled: boolean;
	events: GitPreparationEvent[];
	importKind: "project" | "workspace";
	onBack: () => void;
	onChangeApprovedActions: (actions: string[]) => void;
	onChangeFolder: () => void;
	onChangeRemote: (value: string) => void;
	onContinue: () => void;
	onContinueProject: () => void;
	onChangeRepositoryPrep: (repoPath: string, next: Partial<RepositoryPreparationState>) => void;
	onOpenChange: (open: boolean) => void;
	onTryWorkspace: () => void;
	open: boolean;
	remoteUrl: string;
	repositoryPrep: RepositoryPreparationState[];
	shake: boolean;
	step: ProjectImportStep | null;
	isPreparingGit: boolean;
	validation: ImportValidationResult | null;
}) {
	const { t } = useTranslation();
	if (!validation || !step) return null;
	const needsRemote = validation.root.requiredActions.includes("set_remote");
	const hasChildRepos = (validation.childRepos?.length ?? 0) > 0;
	const hasFailedStep = events.some((event) => event.state === "error");
	const latestEvents = latestPreparationEvents(events);
	const missingApprovals = validation.root.requiredActions.filter((action) => !approvedActions.includes(action));
	const missingRepositoryApprovals = repositoryPrep.some((repo) => {
		const status = validation.childRepos?.find((candidate) => candidate.repoPath === repo.repoPath);
		return status?.requiredActions.some((action) => !repo.approvedActions.includes(action)) || (status?.requiredActions.includes("set_remote") && repo.remoteUrl.trim() === "");
	});
	const continueDisabled = disabled || (importKind === "workspace" ? missingRepositoryApprovals : missingApprovals.length > 0 || (needsRemote && remoteUrl.trim() === ""));
	return (
		<Dialog.Root open={open} onOpenChange={onOpenChange}>
			<Dialog.Portal>
				<Dialog.Content
					className={cn("fixed left-1/2 top-1/2 z-overlay flex max-h-[min(640px,calc(100svh-24px))] w-[min(560px,calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-lg border border-border bg-popover p-0 text-popover-foreground shadow-xl data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none", shake && "modal-shake")}
					onInteractOutside={(event) => event.preventDefault()}
					onPointerDownOutside={(event) => event.preventDefault()}
				>
					<div className="relative flex shrink-0 items-center gap-3 px-4 pt-3">
						<Button type="button" variant="outline" size="icon" aria-label={t("createProject.backToSource")} disabled={disabled} onClick={onBack}>
							<ChevronRight className="size-4 rotate-180" aria-hidden="true" />
						</Button>
						<div className="min-w-0 flex-1 pr-8">
							<Dialog.Title className="text-[18px] font-semibold text-[var(--color-text-import-title)]">
								{step === "prepare_git"
									? t("createProject.prepareProjectTitle")
									: importKind === "workspace"
										? t("createProject.importWorkspace")
										: t("createProject.importProject")}
							</Dialog.Title>
							<Dialog.Description className="sr-only">
								{step === "blocked"
									? t("createProject.projectImportBlocked")
									: t("createProject.projectImportApproval")}
							</Dialog.Description>
						</div>
						<Dialog.Close asChild>
							<button type="button" className="settings-close-button" aria-label={t("createProject.closeProjectImport")} disabled={disabled}>
								<X className="size-4" aria-hidden="true" />
							</button>
						</Dialog.Close>
					</div>
					<div className="min-h-0 space-y-4 overflow-y-auto px-4 pb-1 pt-4">
						<div className="space-y-2">
							<Label htmlFor="projectImportFolder" className="text-[13px] font-semibold text-[var(--color-text-import-title)]">
								{t("createProject.projectFolder")}
							</Label>
							<PathRow
								action={t("createProject.change")}
								ariaLabel={t("createProject.change")}
								disabled={disabled}
								id="projectImportFolder"
								icon={<Folder className="size-4 shrink-0 text-[var(--color-text-import-muted)]" aria-hidden="true" />}
								onClick={onChangeFolder}
							>
								{displayImportPath(validation.root.repoPath)}
							</PathRow>
						</div>
						{importKind === "project" && hasChildRepos ? (
							<div className="text-[12px] leading-5 text-foreground">
								<span>{t("createProject.projectHasChildRepos")}</span>
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
							<div className="border-l-2 border-amber-500/60 pl-3 text-[12px] leading-5 text-muted-foreground">
								{validation.warning}
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
								{importKind === "workspace" ? (
									<div className="space-y-3">
										{repositoryPrep.map((repo) => {
											const status = validation.childRepos?.find((candidate) => candidate.repoPath === repo.repoPath);
											if (!status) return null;
											return <div key={repo.repoPath} className="rounded-md border border-border/70 bg-background/40 p-3">
												<div className="mb-2 truncate font-mono text-[12px] text-muted-foreground">{displayImportPath(repo.repoPath)}</div>
												<div className="divide-y divide-border overflow-hidden rounded-md border border-border/70">
													{status.requiredActions.map((action) => <div key={action} className="flex items-start gap-3 px-3 py-3">
														<input type="checkbox" className="mt-0.5 size-4" checked={repo.approvedActions.includes(action)} disabled={disabled} onChange={(event) => onChangeRepositoryPrep(repo.repoPath, { approvedActions: event.target.checked ? [...repo.approvedActions, action] : repo.approvedActions.filter((value) => value !== action) })} />
														<span className="min-w-0 flex-1 text-[13px]">{gitActionLabel(action)}{action === "set_remote" ? <Input className="mt-2 bg-[var(--color-bg-import-card)] text-[13px]" disabled={disabled} placeholder={t("createProject.cloneRepositoryUrlPlaceholder")} value={repo.remoteUrl} onChange={(event) => onChangeRepositoryPrep(repo.repoPath, { remoteUrl: event.target.value })} /> : null}</span>
													</div>)}
												</div>
											</div>;
										})}
									</div>
								) : <div className="divide-y divide-border overflow-hidden rounded-md border border-border/70 bg-background/40">
									{validation.root.requiredActions.map((action) => {
											const checked = approvedActions.includes(action);
											return (
												<div
													key={action}
													className="flex items-start gap-3 px-3 py-3 transition-colors hover:bg-muted/50"
												>
													<input
														id={`projectImportAction-${action}`}
														type="checkbox"
														className="mt-0.5 size-4 rounded border-border"
														checked={checked}
														disabled={disabled}
														onChange={(event) =>
															onChangeApprovedActions(
																event.target.checked
																	? [...approvedActions, action]
																	: approvedActions.filter((value) => value !== action),
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
										</div>}
									{latestEvents.length > 0 ? (
										<div className="space-y-1.5 rounded-md border border-border/70 bg-background/30 p-3" aria-live="polite">
											{latestEvents.map((event) => (
												<div key={`${event.repoPath}:${event.action}`} className="flex items-center gap-2 text-[12px]">
													{event.state === "success" ? <CheckCircle2 className="size-3.5 text-emerald-500" aria-hidden="true" /> : event.state === "error" ? <XCircle className="size-3.5 text-destructive" aria-hidden="true" /> : <CircleDashed className={cn("size-3.5", event.state === "running" ? "animate-spin text-primary" : "text-muted-foreground")} aria-hidden="true" />}
													<span className="min-w-0 flex-1 truncate">{displayImportPath(event.repoPath)} · {gitActionLabel(event.action)}</span>
													<span className="text-muted-foreground">{event.state === "success" ? t("createProject.projectSetupComplete", { defaultValue: "Done" }) : event.state === "error" ? t("createProject.projectSetupError", { defaultValue: "Needs attention" }) : event.state === "running" ? t("createProject.projectSetupInProgress", { defaultValue: "In progress" }) : t("createProject.projectSetupQueued", { defaultValue: "Queued" })}</span>
												</div>
											))}
										</div>
									) : null}
									{missingApprovals.length > 0 ? (
										<p className="text-[11px] leading-4 text-muted-foreground">
											{t("createProject.projectSetupContinue")}
										</p>
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
								{hasChildRepos || Boolean(validation.warning) ? <Button type="button" variant="primary" disabled={disabled} onClick={onContinueProject}>{t("createProject.cloneContinue")}</Button> : null}
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
	disabled,
	error,
	kind,
	onBack,
	onChooseFolder,
	onContinue,
	onOpenChange,
	open,
	scan,
	validation,
	workspaceApprovedActions,
	workspaceRemoteUrls,
	workspacePrepEvents,
	isPreparingGit,
	onChangeWorkspaceApprovedActions,
	onChangeWorkspaceRemoteUrl,
	onPrepareWorkspaceRepository,
}: {
	disabled: boolean;
	error: string | null;
	kind: ProjectKind;
	onBack: () => void;
	onChooseFolder: () => void;
	onContinue: () => void;
	onOpenChange: (open: boolean) => void;
	open: boolean;
	scan: ImportFolderScan | null;
	validation: ImportValidationResult | null;
	workspaceApprovedActions: WorkspaceApprovalState;
	workspaceRemoteUrls: WorkspaceRemoteState;
	workspacePrepEvents: GitPreparationEvent[];
	isPreparingGit: boolean;
	onChangeWorkspaceApprovedActions: (repoPath: string, actions: string[]) => void;
	onChangeWorkspaceRemoteUrl: (repoPath: string, remoteUrl: string) => void;
	onPrepareWorkspaceRepository: (repoPath: string) => void;
}) {
	const { t } = useTranslation();
	const isWorkspace = kind === "workspace";
	const displayRepos = isWorkspace ? mergeWorkspaceImportRepos(scan, validation) : normalizeImportRepos(scan?.repos ?? []);
	const workspaceNeedsInitializedRepo = isWorkspace && validation?.blockingErrors.includes("WORKSPACE_CHILD_REPO_REQUIRED");
	const failedRepos =
		displayRepos.filter(
			(repo) =>
				(repo.status === "error" || !repo.hasRemote) &&
				!repo.needsGitInit &&
				repo.reason !== "Repository must have at least one commit.",
		) ?? [];
	const hasScan = scan !== null;
	return (
		<Dialog.Root open={open} onOpenChange={onOpenChange}>
			<Dialog.Portal>
				<Dialog.Content className="fixed left-1/2 top-1/2 z-overlay flex max-h-[min(640px,calc(100svh-24px))] w-[min(640px,calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-lg border border-border bg-popover p-0 text-popover-foreground shadow-xl data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none">
					<div className="relative flex shrink-0 items-start gap-3 px-4 pt-3">
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
								className="settings-close-button"
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
								<PathRow
									action={t("createProject.change")}
									disabled={disabled}
									icon={<Folder className="size-4 shrink-0 text-[var(--color-text-import-muted)]" aria-hidden="true" />}
									onClick={onChooseFolder}
								>
									{displayImportPath(scan.path)}
								</PathRow>

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
								{workspaceNeedsInitializedRepo && !error ? <div className="rounded-md border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] px-3 py-3 text-[12px] leading-5 text-[var(--color-text-import-muted)]">Initialize at least one child repository with a commit and origin remote before importing this workspace.</div> : null}

							{isWorkspace ? <WorkspaceImportRepoList
								approvedActions={workspaceApprovedActions}
								disabled={disabled}
								events={workspacePrepEvents}
								isPreparingGit={isPreparingGit}
								onChangeApprovedActions={onChangeWorkspaceApprovedActions}
								onChangeRemoteUrl={onChangeWorkspaceRemoteUrl}
								onPrepareRepository={onPrepareWorkspaceRepository}
								remoteUrls={workspaceRemoteUrls}
								repos={displayRepos}
							/> : displayRepos.length > 0 ? <div className="divide-y divide-border/50 overflow-hidden rounded-sm bg-[var(--color-bg-import-card)]">
								{displayRepos.map((repo) => <ImportRepoRow key={repo.path} repo={repo} />)}
							</div> : null}

								{displayRepos.length === 0 && (
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
			{hasScan && failedRepos.length === 0 && !error && (
				<Button type="button" variant="primary" disabled={disabled || workspaceNeedsInitializedRepo} onClick={onContinue}>
									{t("createProject.cloneContinue")}
								</Button>
							)}
						</div>
					</div>
				</Dialog.Content>
			</Dialog.Portal>
		</Dialog.Root>
	);
}

function ImportRepoRow({ failed = false, onSetup, repo, setupExpanded = false }: { failed?: boolean; onSetup?: () => void; repo: DisplayImportRepo; setupExpanded?: boolean }) {
	const { t } = useTranslation();
	const repositoryAvatar = repo.hasRemote ? repositoryAvatarFromGitUrl(repo.remote) : null;
	const needsSetup = repo.requiredActions.length > 0;
	const isPlainFolder = repo.needsGitInit && !repo.isRepo && !needsSetup;
	const repositoryUrl = !failed && !needsSetup && !isPlainFolder ? repositoryWebUrl(repo.remote) : null;
	return (
		<div className={cn("flex shrink-0 items-center gap-2.5 py-1.5 pl-3", isPlainFolder ? "pr-1.5" : "pr-3")}>
			<div className="flex size-4 shrink-0 items-center justify-center">
				{failed ? <XCircle className="size-4 text-destructive" aria-hidden="true" /> : isPlainFolder ? <Folder className="size-4 text-[var(--color-text-import-muted)]" aria-hidden="true" /> : repositoryAvatar ? <ImportRepositoryAvatar owner={repositoryAvatar.owner} url={repositoryAvatar.url} /> : <Folder className="size-4 text-[var(--color-text-import-muted)]" aria-hidden="true" />}
			</div>
			<div className="min-w-0 flex-1 truncate text-[13px] font-semibold text-[var(--color-text-import-title)]">{repo.name}</div>
			<div className="flex max-w-[220px] shrink-0 items-center gap-1 truncate text-right text-[11px] text-[var(--color-text-import-muted)]">
				{needsSetup ? <button type="button" aria-expanded={setupExpanded} className="rounded-sm border border-orange-400/40 bg-orange-500/15 px-2 py-0.5 text-orange-300 hover:bg-orange-500/25" onClick={onSetup}>{setupExpanded ? "Hide setup" : `${workspaceSetupLabel(repo)} · Set up`}</button> : repositoryUrl ? <><GitBranch className="size-3.5 shrink-0" aria-hidden="true" /><a className="truncate underline decoration-border underline-offset-2 hover:text-foreground" href={repositoryUrl} rel="noreferrer" target="_blank">{repo.branch}</a></> : <><span className={cn("truncate", isPlainFolder && "rounded-sm bg-orange-500/15 px-2 py-0.5 text-orange-300")}>{isPlainFolder ? "Needs git init" : failed ? (repo.reason ?? t("createProject.repoCannotImport")) : repo.branch}</span></>}
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

function normalizeImportRepos(repos: ImportFolderScan["repos"]): DisplayImportRepo[] {
	return repos.map((repo) => ({ ...repo, requiredActions: [], blockingErrors: [] }));
}

function WorkspaceImportRepoList({ approvedActions, disabled, events, isPreparingGit, onChangeApprovedActions, onChangeRemoteUrl, onPrepareRepository, remoteUrls, repos }: {
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
	const orderedRepos = [...repos].sort((left, right) => (Number(right.requiredActions.length > 0) - Number(left.requiredActions.length > 0)) || left.name.localeCompare(right.name));
	return <div className="divide-y divide-border/50 overflow-hidden rounded-sm bg-[var(--color-bg-import-card)]">
		{orderedRepos.map((repo) => {
			const needsSetup = repo.requiredActions.length > 0;
			const expanded = expandedPath === repo.path;
			return <div key={repo.path}>
				<div className="relative">
					<ImportRepoRow onSetup={needsSetup ? () => setExpandedPath(expanded ? null : repo.path) : undefined} repo={repo} setupExpanded={expanded} />
				</div>
				{needsSetup ? <div className={cn("grid transition-[grid-template-rows] duration-200 ease-out motion-reduce:transition-none", expanded ? "grid-rows-[1fr]" : "grid-rows-[0fr]")}><div className="min-h-0 overflow-hidden"><WorkspaceInlineSetup approvedActions={approvedActions[repo.path] ?? repo.requiredActions} disabled={disabled || isPreparingGit} events={events} onChangeApprovedActions={(actions) => onChangeApprovedActions(repo.path, actions)} onChangeRemoteUrl={(remoteUrl) => onChangeRemoteUrl(repo.path, remoteUrl)} onPrepare={() => onPrepareRepository(repo.path)} repo={repo} remoteUrl={remoteUrls[repo.path] ?? ""} /></div></div> : null}
			</div>;
		})}
	</div>;
}

function WorkspaceInlineSetup({ approvedActions, disabled, events, onChangeApprovedActions, onChangeRemoteUrl, onPrepare, repo, remoteUrl }: {
	approvedActions: string[];
	disabled: boolean;
	events: GitPreparationEvent[];
	onChangeApprovedActions: (actions: string[]) => void;
	onChangeRemoteUrl: (remoteUrl: string) => void;
	onPrepare: () => void;
	repo: DisplayImportRepo;
	remoteUrl: string;
}) {
	const [dirty, setDirty] = useState(false);
	const missingApprovals = repo.requiredActions.some((action) => !approvedActions.includes(action));
	const missingRemote = repo.requiredActions.includes("set_remote") && remoteUrl.trim() === "";
	useEffect(() => {
		if (!dirty || disabled || missingApprovals || missingRemote) return;
		const timer = window.setTimeout(() => {
			setDirty(false);
			onPrepare();
		}, 500);
		return () => window.clearTimeout(timer);
	}, [dirty, disabled, missingApprovals, missingRemote, onPrepare]);
	return <div className="origin-top animate-modal-in border-t border-border/50 px-3 pb-3 pt-2 motion-reduce:animate-none"><div className="space-y-2 rounded-md border border-border/60 bg-[var(--color-bg-import-modal)] p-2.5">
		{repo.requiredActions.map((action) => <div key={action} className="space-y-2"><label className="flex items-center gap-2 text-[12px] text-[var(--color-text-import-title)]"><input checked={approvedActions.includes(action)} disabled={disabled} onChange={(event) => { setDirty(true); onChangeApprovedActions(event.target.checked ? [...approvedActions, action] : approvedActions.filter((item) => item !== action)); }} type="checkbox" /><span className="font-medium">{gitActionLabel(action)}</span><span className="ml-auto font-mono text-[11px] text-[var(--color-text-import-muted)]">{workspaceActionStateLabel(action, events, repo.path, approvedActions.includes(action), remoteUrl)}</span></label>{action === "set_remote" ? <Input aria-label="Origin remote URL" className="h-8 bg-[var(--color-bg-import-card)] font-mono text-[12px]" disabled={disabled} placeholder="https://github.com/owner/repository.git" value={remoteUrl} onChange={(event) => { setDirty(true); onChangeRemoteUrl(event.target.value); }} /> : null}</div>)}
	</div></div>;
}

function workspaceActionStateLabel(action: string, events: GitPreparationEvent[], repoPath: string, checked: boolean, remoteUrl: string): string {
	const state = [...events].reverse().find((event) => event.repoPath === repoPath && event.action === action)?.state;
	if (state === "success") return "Done";
	if (state === "error") return "Failed";
	if (state === "running") return "Running";
	if (state === "pending") return "Queued";
	if (!checked || (action === "set_remote" && remoteUrl.trim() === "")) return "Required";
	return "";
}

function mergeWorkspaceImportRepos(scan: ImportFolderScan | null, validation: ImportValidationResult | null): DisplayImportRepo[] {
	const metadata = new Map((scan?.repos ?? []).map((repo) => [repo.path, repo]));
	const statuses = new Map((validation?.childRepos ?? []).map((repo) => [repo.repoPath, repo]));
	const paths = new Set([...metadata.keys(), ...statuses.keys()]);
	return [...paths].map((path) => {
		const repo = metadata.get(path);
		const status = statuses.get(path);
		return {
			name: repo?.name ?? path.split(/[\\/]/).pop() ?? path,
			path,
			relativePath: repo?.relativePath ?? ".",
			branch: repo?.branch ?? (status?.hasCommit ? "HEAD" : ""),
			remote: repo?.remote ?? "",
			hasRemote: repo?.hasRemote ?? Boolean(status?.hasOrigin),
			status: repo?.status ?? (status?.blockingErrors.length ? "error" : "ok"),
			reason: repo?.reason ?? status?.blockingErrors[0],
							needsGitInit: repo?.needsGitInit ?? status?.needsGitInit,
							requiredActions: status?.requiredActions ?? [],
			blockingErrors: status?.blockingErrors ?? [],
			isRepo: status?.isRepo,
			hasCommit: status?.hasCommit,
			hasOrigin: status?.hasOrigin,
		};
	}).map((repo) => ({
		...repo,
		requiredActions: repo.requiredActions.length > 0 || !repo.needsGitInit ? repo.requiredActions : [...WORKSPACE_FOLDER_SETUP_ACTIONS],
	})).sort((left, right) => left.name.localeCompare(right.name));
}

function repositoryAvatarFromGitUrl(remote: string): { owner: string; url: string } | null {
	const webUrl = repositoryWebUrl(remote);
	if (!webUrl) return null;
	try {
		const parsed = new URL(webUrl);
		const [owner] = parsed.pathname.split("/").filter(Boolean);
		if (!owner) return null;
		if (parsed.hostname === "github.com") return { owner, url: `https://github.com/${owner}.png?size=64` };
		if (parsed.hostname === "gitlab.com") return { owner, url: `https://gitlab.com/${owner}/-/avatar` };
		return null;
	} catch {
		return null;
	}
}

function repositoryWebUrl(remote: string): string | null {
	const value = remote.trim();
	const scp = value.match(/^[^/@:\s]+@([^/:\s]+):(.+)$/);
	if (scp?.[1] && scp[2]) return `https://${scp[1]}/${scp[2].replace(/^\/+|\/+$/g, "").replace(/\.git$/, "")}`;
	try {
		const parsed = new URL(value);
		const path = parsed.pathname.replace(/^\/+|\/+$/g, "").replace(/\.git$/, "");
		return parsed.hostname && path ? `https://${parsed.hostname}/${path}` : null;
	} catch {
		return null;
	}
}

function ImportRepositoryAvatar({ owner, url }: { owner: string; url: string }) {
	const [state, setState] = useState<"loading" | "loaded" | "failed">("loading");
	return <span className="relative block size-4" aria-hidden="true"><img alt="" className={cn("absolute inset-0 size-4 rounded-full object-cover", state === "loaded" ? "opacity-100" : "opacity-0")} onError={() => setState("failed")} onLoad={() => setState("loaded")} referrerPolicy="no-referrer" src={url} />{state === "loading" ? <span className="absolute inset-0 size-4 animate-pulse rounded-full bg-muted-foreground/40" /> : null}{state === "failed" ? <span className="absolute inset-0 size-4 rounded-full bg-muted text-center text-[7px] font-semibold leading-4 text-muted-foreground">{owner.slice(0, 2).toUpperCase()}</span> : null}</span>;
}

function displayImportPath(value: string) {
	return value.replace(/^\/Users\/[^/]+/, "~");
}
