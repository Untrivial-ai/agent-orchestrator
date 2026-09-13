import * as Dialog from "@radix-ui/react-dialog";
import { useQueryClient } from "@tanstack/react-query";
import { AnimatePresence, motion } from "motion/react";
import { useTranslation } from "react-i18next";
import {
	CheckCircle2,
	CircleDashed,
	ChevronRight,
	Cloud,
	Bot,
	Folder,
	FolderClosed,
	Folders,
	GitBranch,
	GitFork,
	Globe,
	Link2,
	LoaderCircle,
	Lock,
	X,
	XCircle,
} from "lucide-react";
import { useEffect, useReducer, useRef, useState, type FormEvent, type ReactNode } from "react";
import type { components } from "../../api/schema";
import type { ImportFolderScan } from "../../preload";
import { useCloudCp } from "../hooks/useCloudCp";
import { useCloudGate } from "../hooks/useCloudGate";
import { useCloudOrg } from "../hooks/useCloudOrg";
import { usePreparedClone } from "../hooks/usePreparedClone";
import { cloudProjectsQueryKey } from "../hooks/useWorkspaceQuery";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { aoBridge } from "../lib/bridge";
import { useCloudSession } from "../lib/cloud-session";
import { useUiStore } from "../stores/ui-store";
import { cn } from "../lib/utils";
import type { ProjectKind } from "../types/workspace";
import { CreateProjectAgentSheet, type CreateProjectAgentSelection } from "./CreateProjectAgentSheet";
import CloneRepositoryDialog, { type CloneRepositoryDetails, type CloneRepositorySelection } from "./CloneRepositoryDialog";
import { PathRow } from "./PathRow";
import { Button } from "./ui/button";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "./ui/select";
import { Switch } from "./ui/switch";
import { Tabs, TabsList, TabsTrigger } from "./ui/tabs";

export type CreateProjectInput = {
	path: string;
	asWorkspace?: boolean;
	defaultBranch?: string;
	clonePreparationId?: string;
} & CreateProjectAgentSelection;
export type CloneProjectInput = Pick<CloneRepositorySelection, "remoteUrl" | "destinationParent"> &
	CreateProjectAgentSelection;

const LAST_CLONE_DESTINATION_KEY = "ao.clone.lastDestinationParent";
const LAST_IMPORT_REMOTE_URL_KEY = "ao.import.lastRemoteUrl";
const GIT_PREPARATION_ACTIONS = ["git_init", "git_commit", "create_remote_repository", "set_remote"] as const;
const GIT_ACTION_LABELS: Record<string, string> = {
	git_init: "Git initialization", git_commit: "Initial commit", create_remote_repository: "Create remote repository", set_remote: "Remote setup",
};
type ImportValidationResult = components["schemas"]["ImportValidationResult"];
type GitPreparationEvent = components["schemas"]["GitPreparationEvent"];
type ProjectImportStep = "blocked" | "prepare_git";
type CreateProjectView = "source" | "clone" | "folder" | ProjectImportStep | null;
type CreateProjectViewAction =
	| { type: "open"; view: Exclude<CreateProjectView, null> }
	| { type: "close"; view: Exclude<CreateProjectView, null> }
	| { type: "closeProjectImport" };
type DisplayImportRepo = ImportFolderScan["repos"][number] & {
	requiredActions: string[];
	blockingErrors: string[];
	isRepo?: boolean;
	hasCommit?: boolean;
	hasOrigin?: boolean;
};
type ProjectGitHubRepository = { owner: string; name: string; private: boolean };
type GitHubOwner = { login: string; avatarUrl: string };
type GitHubRepositoryAvailability = { state: "idle" | "checking" | "available" | "unavailable"; message?: string };

function GitHubIcon({ className }: { className?: string }) {
	return (
		<svg className={className} viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
			<path d="M12 .5C5.65.5.5 5.65.5 12c0 5.08 3.29 9.38 7.86 10.9.58.1.79-.25.79-.56v-2.15c-3.2.7-3.88-1.37-3.88-1.37-.52-1.34-1.28-1.7-1.28-1.7-1.05-.72.08-.7.08-.7 1.16.08 1.77 1.2 1.77 1.2 1.03 1.76 2.7 1.25 3.36.96.1-.75.4-1.25.73-1.54-2.56-.29-5.26-1.28-5.26-5.7 0-1.26.45-2.29 1.19-3.1-.12-.3-.52-1.47.11-3.05 0 0 .97-.31 3.18 1.18A10.96 10.96 0 0 1 12 5.99c.98 0 1.97.13 2.9.38 2.2-1.49 3.17-1.18 3.17-1.18.63 1.58.23 2.75.11 3.05.74.81 1.19 1.84 1.19 3.1 0 4.43-2.7 5.4-5.27 5.69.41.36.78 1.07.78 2.16v3.2c0 .31.21.67.8.55A11.51 11.51 0 0 0 23.5 12C23.5 5.65 18.35.5 12 .5Z" />
		</svg>
	);
}

type CreateProjectFlowMode = ProjectKind | "choose";
type ProjectSource = "clone" | "local" | "workspace";

/** Where the new project should live: on this machine or in AO Cloud. */
type ProjectOffering = "local" | "cloud";
type CreateProgressStage = "starting" | "connecting" | "creating" | "settingUp" | "finishing" | "complete";

function initialCloneDetails(): CloneRepositoryDetails {
	return {
		remoteUrl: "",
		destinationParent:
			typeof window === "undefined" ? "~/ao/projects" : (window.localStorage.getItem(LAST_CLONE_DESTINATION_KEY) || "~/ao/projects"),
	};
}

function createProjectViewReducer(state: CreateProjectView, action: CreateProjectViewAction): CreateProjectView {
	if (action.type === "open") return action.view;
	if (action.type === "closeProjectImport") return state === "blocked" || state === "prepare_git" ? null : state;
	return state === action.view ? null : state;
}

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
	onCreateStandaloneAgent,
	onOpenExistingProject,
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
	onCreateStandaloneAgent?: () => void;
	onOpenExistingProject?: (path: string) => void | Promise<void>;
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
	const [activeView, dispatchView] = useReducer(createProjectViewReducer, null);
	const modePickerOpen = activeView === "source";
	const cloneDialogOpen = activeView === "clone";
	const [cloneDetails, setCloneDetails] = useState<CloneRepositoryDetails>(initialCloneDetails);
	const [cloneSelection, setCloneSelection] = useState<CloneRepositorySelection | null>(null);
	const preparedClone = usePreparedClone();
	const folderPickerOpen = activeView === "folder";
	const [childTransitioning, setChildTransitioning] = useState(false);
	const [selectedKind, setSelectedKind] = useState<ProjectKind>(mode === "workspace" ? "workspace" : "single_repo");
	const [selectedPath, setSelectedPath] = useState<string | null>(null);
	const [validationScan, setValidationScan] = useState<ImportFolderScan | null>(null);
	const [projectValidation, setProjectValidation] = useState<ImportValidationResult | null>(null);
	const projectImportStep = activeView === "blocked" || activeView === "prepare_git" ? activeView : null;
	const [projectPrepEvents, setProjectPrepEvents] = useState<GitPreparationEvent[]>([]);
	const [projectApprovedActions, setProjectApprovedActions] = useState<string[]>([]);
	const [projectRemoteUrl, setProjectRemoteUrl] = useState("");
	const [projectGitHubRepo, setProjectGitHubRepo] = useState<ProjectGitHubRepository | null>(null);
	const [projectImportShake, setProjectImportShake] = useState(false);
	const [isChoosingPath, setIsChoosingPath] = useState(false);
	const [isCreating, setIsCreating] = useState(false);
	const [isInitializing, setIsInitializing] = useState(false);
	const [createProgress, setCreateProgress] = useState({ open: false, value: 0, stage: "starting" as CreateProgressStage });
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
	const folderDialogOpen = folderPickerOpen && validationScan !== null;
	const isBusy = isChoosingPath || isCreating || isInitializing || isPreparingGit || preparedClone.isCleaning;
	const setModePickerOpen = (open: boolean) => dispatchView(open ? { type: "open", view: "source" } : { type: "close", view: "source" });
	const setCloneDialogOpen = (open: boolean) => dispatchView(open ? { type: "open", view: "clone" } : { type: "close", view: "clone" });
	const setFolderPickerOpen = (open: boolean) => dispatchView(open ? { type: "open", view: "folder" } : { type: "close", view: "folder" });
	const setProjectImportStep = (step: ProjectImportStep | null) => dispatchView(step ? { type: "open", view: step } : { type: "closeProjectImport" });

	useEffect(() => {
		if (!createProgress.open) return;
		const startedAt = Date.now();
		const updateProgress = () => {
			const elapsed = Date.now() - startedAt;
			if (elapsed < 800) {
				setCreateProgress({ open: true, stage: "starting", value: Math.min(12, 4 + elapsed / 100) });
			} else if (elapsed < 1800) {
				setCreateProgress({ open: true, stage: "connecting", value: 12 + ((elapsed - 800) / 1000) * 18 });
			} else if (elapsed < 5000) {
				setCreateProgress({ open: true, stage: "creating", value: 30 + ((elapsed - 1800) / 3200) * 38 });
			} else if (elapsed < 7600) {
				setCreateProgress({ open: true, stage: "settingUp", value: 68 + ((elapsed - 5000) / 2600) * 17 });
			} else {
				setCreateProgress({ open: true, stage: "finishing", value: Math.min(90, 85 + (elapsed - 7600) / 1000) });
			}
		};
		updateProgress();
		const timer = window.setInterval(updateProgress, 250);
		return () => window.clearInterval(timer);
	}, [createProgress.open]);
	const showGlobalToast = useUiStore((state) => state.showGlobalToast);
	const resetProjectImportState = () => {
		setProjectValidation(null);
		setProjectImportStep(null);
		setProjectPrepEvents([]);
		setProjectApprovedActions([]);
		setProjectRemoteUrl("");
		setProjectGitHubRepo(null);
		setProjectImportShake(false);
	};

	const reportProjectError = (message: string) => {
		setError(message);
		showGlobalToast(t("createProject.setupFailedToastTitle", { defaultValue: "Project setup failed" }), message, "error");
		setProjectImportShake(false);
		window.requestAnimationFrame(() => setProjectImportShake(true));
		window.setTimeout(() => setProjectImportShake(false), 320);
	};

	const abandonPreparedClone = async (): Promise<boolean> => {
		try {
			await preparedClone.cleanup();
			return true;
		} catch {
			reportProjectError(t("createProject.cloneCleanupFailed", {
				defaultValue: "AO could not remove the incomplete checkout. Try again before leaving this flow.",
			}));
			return false;
		}
	};

	const transitionToChild = (open: () => void) => {
		setChildTransitioning(true);
		window.setTimeout(() => {
			open();
			setChildTransitioning(false);
		}, 80);
	};
	const transitionFromFolder = (open: () => void) => {
		setFolderPickerOpen(false);
		transitionToChild(open);
	};

	const selectSource = async (source: ProjectSource) => {
		if (preparedClone.current() && !(await abandonPreparedClone())) return;
		const presetPath = pendingDropPath;
		setPendingDropPath(null);
		setError(null);
		setValidationScan(null);
		resetProjectImportState();
		if (source === "clone") {
			setCloneDetails(initialCloneDetails());
			setCloneSelection(null);
			transitionToChild(() => setCloneDialogOpen(true));
			return;
		}
		setCloneSelection(null);
		preparedClone.complete();
		// Keep the selector mounted behind the native picker. Closing it first
		// exposes a blank compositor frame on Windows before Explorer takes focus.
		void chooseDirectory(source === "workspace" ? "workspace" : "single_repo", presetPath ?? undefined);
	};

	const chooseDirectory = async (kind: ProjectKind, presetPath?: string, preserveCurrentDialog = false) => {
		if (!preserveCurrentDialog) {
			setError(null);
			setValidationScan(null);
			resetProjectImportState();
			setRepositorySetup(null);
			setRepositorySetupWarning(null);
			setSelectedKind(kind);
		}
		setIsChoosingPath(true);
		try {
			const path =
				presetPath ??
				(await aoBridge.app.chooseDirectory(
					kind === "workspace" ? t("createProject.chooseWorkspace") : t("createProject.chooseRepo"),
				));
			if (path && kind === "single_repo") {
				const registeredPath = existingProjectPaths.find((candidate) => sameProjectPath(candidate, path));
				if (registeredPath && onOpenExistingProject) {
					setModePickerOpen(false);
					setFolderPickerOpen(false);
					showGlobalToast("Project already added", "Opened the registered project for this folder.");
					await onOpenExistingProject(registeredPath);
					return;
				}
				const validation = await validateImportFolder(path, "project");
				const applyProjectValidation = () => {
					setError(null);
					setValidationScan(null);
					resetProjectImportState();
					setRepositorySetup(null);
					setRepositorySetupWarning(null);
					setSelectedKind(kind);
					setProjectValidation(validation);
					setProjectPrepEvents([]);
					setProjectApprovedActions(validation.root.requiredActions);
					const needsRemoteSetup = importNeedsRemoteSetup(validation.root.requiredActions);
					const githubRepo = needsRemoteSetup ? defaultProjectGitHubRepository(validation.root.repoPath) : null;
					setProjectGitHubRepo(githubRepo);
					setProjectRemoteUrl(needsRemoteSetup && githubRepo ? githubRepositoryRemoteUrl(githubRepo) : "");
				};
				if (!preserveCurrentDialog) applyProjectValidation();
				const openProjectStep = (step: ProjectImportStep) => {
					if (preserveCurrentDialog) transitionFromFolder(() => {
						applyProjectValidation();
						setProjectImportStep(step);
					});
					else setProjectImportStep(step);
				};
				if (!validation.isValid || validation.nextStep === "error") {
					reportProjectError(importValidationMessage(validation));
					openProjectStep("blocked");
					return;
				}
				if (validation.nextStep === "choose_import_kind") {
					openProjectStep("blocked");
					return;
				}
				if (validation.nextStep === "prepare_git") {
					openProjectStep("prepare_git");
					return;
				}
				if (validation.warning) {
					openProjectStep("blocked");
					return;
				}
				if (preserveCurrentDialog) {
					setModePickerOpen(false);
					transitionFromFolder(() => {
						applyProjectValidation();
						setSelectedPath(path);
					});
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
					setProjectGitHubRepo(null);
					if ((!validation.isValid || validation.nextStep === "error") && !validation.blockingErrors.includes("WORKSPACE_CHILD_REPO_REQUIRED")) {
						reportProjectError(importValidationMessage(validation));
					}
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
				if (preserveCurrentDialog) transitionFromFolder(() => setSelectedPath(path));
				else {
					setSelectedPath(path);
					setFolderPickerOpen(false);
				}
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
		setCloneDetails(initialCloneDetails());
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
		void selectSource(sourceSignal.source);
	}, [sourceSignal]);

	const createProject = async (selection: CreateProjectAgentSelection) => {
		if (!selectedPath) return;
		setError(null);
		setIsCreating(true);
		const showProgress = Boolean(cloneSelection);
		if (showProgress) {
			setCreateProgress({ open: true, stage: "starting", value: 0 });
		}
		try {
			if (cloneSelection) {
				const prepared = preparedClone.current();
				if (!prepared) throw new Error(t("createProject.couldNotAdd"));
				await onCreateProject({ path: selectedPath, clonePreparationId: prepared.preparationId, ...selection });
				setCreateProgress({ open: true, stage: "complete", value: 100 });
				await new Promise((resolve) => window.setTimeout(resolve, 180));
				setSelectedPath(null);
				setCloneSelection(null);
				preparedClone.complete();
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
		// Workspace imports can adopt an existing local Git root. Preserve its
		// checked-out branch as the workspace default (child defaults stay
		// separate); the daemon resolves it at spawn time. Single-repo imports
		// skip this lookup entirely — the daemon resolves their base branch
		// itself, saving a blocking IPC round-trip on the critical path.
		const defaultBranch =
			selectedKind === "workspace" ? await aoBridge.app.getRepositoryBranch(selectedPath) : undefined;
		await onCreateProject({
			path: selectedPath,
			asWorkspace: selectedKind === "workspace",
			...(defaultBranch ? { defaultBranch } : {}),
			...selection,
		});
			if (showProgress) {
				setCreateProgress({ open: true, stage: "complete", value: 100 });
				await new Promise((resolve) => window.setTimeout(resolve, 180));
			}
			setSelectedPath(null);
		} catch (err) {
			const code = err instanceof Error && "code" in err ? (err.code as string | undefined) : undefined;
			const message = cloneSelection
				? t("createProject.cloneFailedTitle", { defaultValue: "Could not clone repository" })
				: err instanceof Error ? err.message : t("createProject.couldNotAdd");
			if (!cloneSelection && selectedKind === "single_repo" && isRepositorySetupRecoveryCode(code)) {
				setRepositorySetup(code);
			}
			if (cloneSelection) reportProjectError(message);
			else if (selectedKind === "single_repo") reportProjectError(safeProjectCreationError(
				message,
				t("createProject.createFailedBody", { defaultValue: "AO could not create this project. Try again." }),
				code,
			));
			else reportProjectError(message);
			if (hasModePicker && !cloneSelection && selectedKind !== "single_repo") {
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
					setFolderPickerOpen(true);
				} else {
					setValidationScan(null);
					setFolderPickerOpen(false);
					setModePickerOpen(true);
				}
				setSelectedPath(null);
			}
		} finally {
			setCreateProgress((current) => ({ ...current, open: false }));
			setIsCreating(false);
			setIsInitializing(false);
		}
	};

	const prepareClone = async (next: CloneRepositorySelection) => {
		if (preparedClone.current() && !(await abandonPreparedClone())) return;
		setError(null);
		setIsPreparingGit(true);
		let clonedPath: string | null = null;
		try {
			const data = await preparedClone.prepare(next.remoteUrl, next.destinationParent);
			clonedPath = data.path;
			const validation = await validateImportFolder(data.path, "project");
			setCloneDialogOpen(false);
			setCloneSelection(next);
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
		} catch {
			reportProjectError(clonedPath
				? t("createProject.cloneValidationFailed", {
					defaultValue: "AO cloned the repository but could not verify the checkout. Try again.",
				})
				: t("createProject.cloneFailedTitle", { defaultValue: "Could not clone repository" }));
			if (clonedPath) await abandonPreparedClone();
			setCloneDialogOpen(true);
		} finally {
			setIsPreparingGit(false);
		}
	};

	const reopenSourcePicker = async () => {
		if (!(await abandonPreparedClone())) return;
		setCloneSelection(null);
		resetProjectImportState();
		if (hasModePicker) {
			setModePickerOpen(true);
			return;
		}
		setError(null);
	};

	const leaveCloneDialog = async (back: boolean) => {
		if (!(await abandonPreparedClone())) return;
		setError(null);
		setCloneDialogOpen(false);
		setCloneSelection(null);
		setCloneDetails(initialCloneDetails());
		if (back) setModePickerOpen(true);
	};

	const tryProjectAsWorkspace = () => {
		if (!projectValidation) return;
		setPendingDropPath(null);
		void chooseDirectory("workspace", projectValidation.root.repoPath);
	};

	const prepareProjectGit = async () => {
		if (!projectValidation) return;
		setError(null);
		const needsRemoteSetup = importNeedsRemoteSetup(projectValidation.root.requiredActions);
		const githubRepository = needsRemoteSetup ? projectGitHubRepo : null;
		const remoteUrl = githubRepository ? githubRepositoryRemoteUrl(githubRepository) : projectRemoteUrl.trim();
		if (remoteUrl !== "" && !isValidProjectRemote(remoteUrl)) {
			reportProjectError(t("createProject.cloneInvalidUrl"));
			return;
		}
		if (projectApprovedActions.includes("set_remote") && remoteUrl !== "") {
			setIsPreparingGit(true);
			try {
				if (!(await aoBridge.app.checkGitRepository(remoteUrl))) {
					reportProjectError(t("createProject.cloneRepositoryUnavailable", {
						defaultValue: "This isn't a repository or you don't have access",
					}));
					return;
				}
			} catch {
				reportProjectError(t("createProject.cloneRepositoryUnavailable", {
					defaultValue: "This isn't a repository or you don't have access",
				}));
				return;
			} finally {
				setIsPreparingGit(false);
			}
		}
		setProjectPrepEvents((current) => mergePreparationEvents(current, projectRequestedActionEvents(
			projectValidation.root.repoPath,
			projectValidation.root.requiredActions,
		)));
		setIsPreparingGit(true);
		try {
			let currentValidation = projectValidation;
			while (currentValidation.nextStep === "prepare_git") {
				const activeAction = orderedProjectActions(currentValidation.root.requiredActions)[0];
				if (!activeAction) throw new Error(t("createProject.couldNotAdd"));
				setProjectPrepEvents((current) => mergePreparationEvents(current, [{
					repoPath: currentValidation.root.repoPath,
					action: activeAction as GitPreparationEvent["action"],
					state: "running",
				}]));
				const { data, error: apiError } = await apiClient.POST("/api/v1/imports/prepare-git", {
					body: {
						importKind: "project",
						path: currentValidation.root.repoPath,
							approvedActions: projectApprovedActions,
							remoteUrl: remoteUrl || undefined,
							githubRepository: githubRepository ?? undefined,
							stepwise: true,
						},
				});
				if (apiError || !data) throw new Error(apiErrorMessage(apiError, t("createProject.couldNotAdd")));
				setProjectPrepEvents((current) => mergePreparationEvents(current, data.events));
				setProjectValidation(data.validation);
				setProjectApprovedActions(data.validation.root.requiredActions);
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
				if (data.validation.nextStep !== "prepare_git" && data.validation.nextStep !== "continue") {
					throw new Error(t("createProject.couldNotAdd"));
				}
				if (data.validation.nextStep === "prepare_git" && orderedProjectActions(data.validation.root.requiredActions)[0] === activeAction) {
					throw new Error(t("createProject.couldNotAdd"));
				}
				currentValidation = data.validation;
			}
			if (remoteUrl !== "") persistSuggestedProjectRemoteUrl(remoteUrl);
			setModePickerOpen(false);
			setProjectImportStep(null);
			setSelectedPath(currentValidation.root.repoPath);
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
			<CreateProjectFlowBackdrop open={modePickerOpen || cloneDialogOpen || folderDialogOpen || selectedPath !== null || createProgress.open || childTransitioning || projectImportOpen} />
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
						<ImportSourcePicker disabled={isBusy} onSelect={selectSource} onCreateStandaloneAgent={onCreateStandaloneAgent} />
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
						childOpen={childTransitioning || cloneDialogOpen || folderDialogOpen || projectImportOpen || selectedPath !== null}
						cloudAvailable={cloudAvailable}
						cloudEnabled={cloudEnabled}
						disabled={isBusy}
						offering={offering}
						onCloudCreated={onCloudProjectCreated}
						onOfferingChange={setOffering}
						onSignIn={cloudSignIn}
						onCreateStandaloneAgent={onCreateStandaloneAgent}
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
								void leaveCloneDialog(true);
							}}
							onChange={(next) => {
								setCloneDetails(next);
								setError(null);
							}}
							onClose={() => {
								void leaveCloneDialog(false);
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
						open={folderDialogOpen}
						scan={validationScan}
						validation={projectValidation}
						isPreparingGit={isPreparingGit}
						shake={projectImportShake}
						onContinueAsProject={() => {
							if (projectValidation) void chooseDirectory("single_repo", projectValidation.root.repoPath, true);
						}}
						onContinue={() => {
							if (!validationScan) return;
							if (selectedKind === "workspace") {
								setFolderPickerOpen(false);
								setModePickerOpen(false);
								setSelectedPath(validationScan.path);
								return;
							}
							setFolderPickerOpen(false);
							setSelectedPath(validationScan.path);
							setModePickerOpen(false);
						}}
						onBack={() => {
							setError(null);
							setValidationScan(null);
							if (hasModePicker) setModePickerOpen(true);
							else setFolderPickerOpen(false);
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
				onBack={() => void reopenSourcePicker()}
				onChangeApprovedActions={setProjectApprovedActions}
				onChangeFolder={() => void chooseDirectory("single_repo")}
				onChangeGitHubRepository={setProjectGitHubRepo}
				onChangeRemote={setProjectRemoteUrl}
				onContinue={() => void prepareProjectGit()}
				onContinueProject={() => {
					if (projectValidation?.root.requiredActions.length) {
						setProjectImportStep("prepare_git");
					} else if (projectValidation) {
						setModePickerOpen(false);
						setProjectImportStep(null);
						setSelectedPath(projectValidation.root.repoPath);
					}
				}}
				shake={projectImportShake}
				onOpenChange={(open) => {
					if (isBusy) return;
					if (!open) {
						void (async () => {
							if (!(await abandonPreparedClone())) return;
							setCloneSelection(null);
							resetProjectImportState();
							setError(null);
						})();
					}
				}}
				onTryWorkspace={tryProjectAsWorkspace}
				open={projectImportOpen}
				githubRepository={projectGitHubRepo}
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
				shake={projectImportShake}
				onOpenChange={(open) => {
					if (!open) {
						void (async () => {
							if (!(await abandonPreparedClone())) return;
							setSelectedPath(null);
							setCloneSelection(null);
							resetProjectImportState();
							if (!folderPickerOpen) setError(null);
						})();
					}
				}}
					onBack={
					cloneSelection
						? () => {
								void (async () => {
									if (!(await abandonPreparedClone())) return;
									setCloneSelection(null);
									setSelectedPath(null);
									setCloneDetails(initialCloneDetails());
									setCloneDialogOpen(true);
								})();
							}
						: undefined
				}
				onSubmit={createProject}
				open={selectedPath !== null && !createProgress.open}
				path={selectedPath}
				repositorySetupNeeded={repositorySetup !== null}
				repositorySetupWarning={repositorySetupWarning}
			/>
			<CreateProjectProgressDialog
				message={createProgressMessage(createProgress.stage, selectedKind === "workspace")}
				open={createProgress.open}
				progress={createProgress.value}
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

function sameProjectPath(left: string, right: string): boolean {
	const normalize = (path: string) => path.trim().replace(/[\\/]+$/, "").replaceAll("\\", "/");
	return normalize(left) === normalize(right);
}

function safeProjectCreationError(message: string, fallback: string, code?: string): string {
	const cleaned = message
		.replace(/^Setup failed:\s*/i, "")
		.replace(/\s*\([A-Z][A-Z0-9_]+\)\s*$/, "")
		.trim();
	if (
		code !== undefined ||
		cleaned === "" ||
		/\b(?:request[_ -]?id|rpc|stack|stderr|stdout|exit status|internal server error|panic)\b/i.test(cleaned) ||
		/\bfatal:/i.test(cleaned) ||
		/\b[A-Z][A-Z0-9_]{3,}\b/.test(cleaned)
	) {
		return fallback;
	}
	return cleaned;
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
	return GIT_ACTION_LABELS[action] ?? "Git setup";
}

function orderedProjectActions(actions: string[]): string[] {
	const rank = (action: string) => {
		const index = GIT_PREPARATION_ACTIONS.indexOf(action as (typeof GIT_PREPARATION_ACTIONS)[number]);
		return index < 0 ? GIT_PREPARATION_ACTIONS.length : index;
	};
	return [...actions].sort((left, right) => rank(left) - rank(right));
}

function importNeedsRemoteSetup(actions: string[]): boolean {
	return actions.includes("create_remote_repository") || actions.includes("set_remote");
}

function defaultProjectGitHubRepository(repoPath: string): ProjectGitHubRepository {
	return {
		owner: "",
		name: projectNameFromPath(repoPath) || "repository",
		private: true,
	};
}

function projectNameFromPath(repoPath: string): string {
	return repoPath.split(/[\\/]/).filter(Boolean).pop()?.replace(/\.git$/i, "").trim() ?? "";
}

function githubRepositoryRemoteUrl(repository: ProjectGitHubRepository): string {
	const owner = repository.owner.trim();
	const name = repository.name.trim().replace(/\.git$/i, "");
	if (!owner || !name) return "";
	return `https://github.com/${owner}/${name}.git`;
}

function projectRequestedActionEvents(repoPath: string, actions: string[]): GitPreparationEvent[] {
	const ordered = orderedProjectActions(actions);
	return ordered.map((action, index) => ({
		repoPath,
		action: action as GitPreparationEvent["action"],
		state: index === 0 ? "running" : "pending",
	}));
}

function mergePreparationEvents(current: GitPreparationEvent[], incoming: GitPreparationEvent[]): GitPreparationEvent[] {
	const latest = new Map<string, GitPreparationEvent>();
	for (const event of [...current, ...incoming]) latest.set(`${event.repoPath}:${event.action}`, event);
	return [...latest.values()];
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
				<Dialog.Overlay className="dialog-overlay z-[calc(var(--z-overlay)-1)] data-[state=open]:animate-overlay-in data-[state=closed]:animate-overlay-out" />
			</Dialog.Portal>
		</Dialog.Root>
	);
}

function CreateProjectProgressDialog({ message, open, progress }: { message: string; open: boolean; progress: number }) {
	const { t } = useTranslation();
	const roundedProgress = Math.round(progress);
	return <Dialog.Root open={open}><Dialog.Portal><Dialog.Content
		className="fixed left-1/2 top-1/2 z-overlay w-[min(440px,calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 overflow-hidden rounded-lg border border-border bg-popover p-0 text-popover-foreground shadow-xl data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none"
		onEscapeKeyDown={(event) => event.preventDefault()}
		onInteractOutside={(event) => event.preventDefault()}
		onPointerDownOutside={(event) => event.preventDefault()}
	><div className="px-5 pb-5 pt-5">
		<Dialog.Title className="text-[18px] font-semibold text-[var(--color-text-import-title)]">{t("createProject.cloneProgressTitle", { defaultValue: "Creating the project" })}</Dialog.Title>
		<Dialog.Description className="sr-only">{t("createProject.cloneProgressDescription", { defaultValue: "Creating the project" })}</Dialog.Description>
		<div className="mt-6 space-y-3">
			<div aria-label={`${roundedProgress}%`} aria-valuemax={100} aria-valuemin={0} aria-valuenow={roundedProgress} className="h-2 w-full overflow-hidden rounded-full bg-muted" role="progressbar"><div className="h-full rounded-full bg-primary transition-[width] duration-300 ease-out" style={{ width: `${Math.max(0, Math.min(100, progress))}%` }} /></div>
			<p className="min-h-5 text-[13px] text-muted-foreground" role="status">{message}</p>
		</div>
	</div></Dialog.Content></Dialog.Portal></Dialog.Root>;
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
	onCreateStandaloneAgent,
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
	onCreateStandaloneAgent?: () => void;
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
							<ImportSourcePicker disabled={disabled} onClose={() => onOpenChange(false)} onSelect={onSelect} onCreateStandaloneAgent={onCreateStandaloneAgent} dialog />
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
	onCreateStandaloneAgent,
	onSelect,
}: {
	dialog?: boolean;
	disabled: boolean;
	onClose?: () => void;
	onCreateStandaloneAgent?: () => void;
	onSelect: (source: ProjectSource) => void;
}) {
	const { t } = useTranslation();
	const createStandaloneAgent = () => {
		onClose?.();
		onCreateStandaloneAgent?.();
	};
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
				{onCreateStandaloneAgent ? (
					<button type="button" className="group flex min-h-[76px] items-center gap-3 px-3.5 py-3 text-left hover:bg-accent/50" aria-label={t("home.newStandaloneAgent")} disabled={disabled} onClick={createStandaloneAgent}>
						<Bot className="size-5" aria-hidden="true" />
						<span><span className="block text-sm font-medium">{t("home.newStandaloneAgent")}</span><span className="mt-0.5 block text-[12px] leading-5 text-muted-foreground">{t("createProject.standaloneDesc")}</span></span>
					</button>
				) : null}
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
	githubRepository,
	onBack,
	onChangeApprovedActions,
	onChangeFolder,
	onChangeGitHubRepository,
	onChangeRemote,
	onContinue,
	onContinueProject,
	onOpenChange,
	onTryWorkspace,
	open,
	remoteUrl,
	shake,
	step,
	isPreparingGit,
	validation,
}: {
	approvedActions: string[];
	disabled: boolean;
	events: GitPreparationEvent[];
	githubRepository: ProjectGitHubRepository | null;
	onBack: () => void;
	onChangeApprovedActions: (actions: string[]) => void;
	onChangeFolder: () => void;
	onChangeGitHubRepository: (repository: ProjectGitHubRepository) => void;
	onChangeRemote: (value: string) => void;
	onContinue: () => void;
	onContinueProject: () => void;
	onOpenChange: (open: boolean) => void;
	onTryWorkspace: () => void;
	open: boolean;
	remoteUrl: string;
	shake: boolean;
	step: ProjectImportStep | null;
	isPreparingGit: boolean;
	validation: ImportValidationResult | null;
}) {
	const { t } = useTranslation();
	const requiredActions = validation?.root.requiredActions ?? [];
	const needsRemote = importNeedsRemoteSetup(requiredActions);
	const githubOwner = githubRepository?.owner.trim() ?? "";
	const githubName = githubRepository?.name.trim() ?? "";
	const isPrivate = githubRepository?.private ?? true;
	const visibilityLabel = isPrivate
		? t("createProject.privateRepository", { defaultValue: "Private repository" })
		: t("createProject.publicRepository", { defaultValue: "Public repository" });
	const visibilityHelper = isPrivate
		? t("createProject.privateRepositoryHelper", {
				defaultValue: "Only you and people you invite can see this repo",
		  })
		: t("createProject.publicRepositoryHelper", {
				defaultValue: "Anyone on the internet can see this repo",
		  });
	const [githubOwners, setGitHubOwners] = useState<GitHubOwner[]>([]);
	const [customGitHubOwner, setCustomGitHubOwner] = useState(false);
	const selectedGitHubOwner = githubOwners.find((owner) => owner.login === githubOwner);
	const [availability, setAvailability] = useState<GitHubRepositoryAvailability>({ state: "idle" });
	useEffect(() => {
		if (!open || !needsRemote) return;
		let cancelled = false;
		const applyOwners = (owners: GitHubOwner[]) => {
			if (!cancelled) {
				setGitHubOwners(owners);
				const owner = owners[0]?.login ?? "";
				if (owner && githubRepository?.owner === "" && !customGitHubOwner) {
					const next = { ...githubRepository, owner };
					onChangeGitHubRepository(next);
					onChangeRemote(githubRepositoryRemoteUrl(next));
				}
			}
		};
		void aoBridge.app.getCachedGitHubOwners().then(applyOwners).catch(() => undefined);
		void aoBridge.app.refreshGitHubOwners().then(applyOwners).catch(() => undefined);
		return () => { cancelled = true; };
	}, [customGitHubOwner, githubRepository, needsRemote, onChangeGitHubRepository, onChangeRemote, open]);
	useEffect(() => {
		if (!open || !needsRemote || !githubOwner || !githubName) {
			setAvailability({ state: "idle" });
			return;
		}
		setAvailability({ state: "checking" });
		let cancelled = false;
		const timer = window.setTimeout(() => {
			void aoBridge.app.checkGitHubRepositoryAvailability({ owner: githubOwner, name: githubName })
				.then((result) => {
					if (cancelled) return;
					setAvailability(result.available ? { state: "available" } : { state: "unavailable", message: result.message });
				})
				.catch(() => {
					if (!cancelled) setAvailability({ state: "unavailable", message: t("createProject.githubRepoUnavailable") });
				});
		}, 250);
		return () => {
			cancelled = true;
			window.clearTimeout(timer);
		};
	}, [githubName, githubOwner, needsRemote, open, t]);
	if (!validation || !step) return null;
	const hasChildRepos = (validation.childRepos?.length ?? 0) > 0;
	const mustImportAsWorkspace = step === "blocked" && validation.nextStep === "choose_import_kind" && hasChildRepos;
	const hasFailedStep = events.some((event) => event.state === "error");
	const latestEvents = mergePreparationEvents([], events);
	const missingApprovals = validation.root.requiredActions.filter((action) => !approvedActions.includes(action));
	const continueDisabled = disabled || missingApprovals.length > 0 || (needsRemote && (!githubOwner || !githubName || availability.state !== "available"));
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
						{mustImportAsWorkspace ? (
							<p className="text-[14px] leading-6 text-[var(--color-text-import-muted)]">
								{t("createProject.projectMustBeWorkspace", { defaultValue: "This folder contains child Git repositories. Import it as a workspace instead." })}
							</p>
						) : null}
						{validation.warning ? (
							<div className="border-l-2 border-amber-500/60 pl-3 text-[12px] leading-5 text-muted-foreground">
								{validation.warning}
							</div>
						) : null}
						{step === "prepare_git" ? (
							<section className="space-y-2">
								{needsRemote ? (
									<p className="text-[14px] leading-5 text-[var(--color-text-import-muted)]">
										<code className="rounded bg-muted px-1.5 py-0.5 font-mono text-[0.9em] text-foreground">{projectNameFromPath(validation.root.repoPath)}</code>{" "}
										{t("createProject.noGitHubRemote")}
									</p>
								) : null}
								{isPreparingGit ? (
									<span className="text-[11px] text-muted-foreground" role="status">
										{t("createProject.projectSetupRunning")}
									</span>
								) : null}
								{needsRemote ? (
									<div className="space-y-3 pt-1">
											<div className="space-y-1.5">
												<Label htmlFor="githubRepoOwner" className="text-[12px] font-medium text-[var(--color-text-import-title)]">{t("createProject.githubOwner")}</Label>
												<Select
													value={customGitHubOwner ? "__custom__" : githubRepository?.owner ?? ""}
													disabled={disabled}
													onValueChange={(owner) => {
														if (owner === "__custom__") {
															setCustomGitHubOwner(true);
															return;
														}
														setCustomGitHubOwner(false);
														const next = { owner, name: githubRepository?.name ?? "", private: githubRepository?.private ?? true };
														onChangeGitHubRepository(next);
														onChangeRemote(githubRepositoryRemoteUrl(next));
													}}
												>
													<SelectTrigger id="githubRepoOwner" size="sm" className="h-8 w-full bg-[var(--color-bg-import-card)] font-mono text-[12px]" aria-label={t("createProject.githubOwner")}>
														<SelectValue placeholder={t("createProject.githubOwner")}>
															{customGitHubOwner ? (
																<span className="flex items-center gap-2">
															<GitHubIcon className="size-4" />
																	{t("createProject.otherGitHubOwner")}
																</span>
															) : selectedGitHubOwner ? (
																<span className="flex items-center gap-2">
																	<img className="size-4 rounded-full" src={selectedGitHubOwner.avatarUrl} alt="" />
																	{selectedGitHubOwner.login}
																</span>
															) : null}
														</SelectValue>
													</SelectTrigger>
													<SelectContent position="popper" side="bottom" align="start" sideOffset={4}>
														{githubOwners.map((owner) => (
															<SelectItem key={owner.login} value={owner.login}>
																<span className="flex items-center gap-2">
																	<img className="size-4 rounded-full" src={owner.avatarUrl} alt="" />
																	{owner.login}
																</span>
															</SelectItem>
														))}
														<SelectItem value="__custom__">
															<span className="flex items-center gap-2">
															<GitHubIcon className="size-4" />
																	{t("createProject.useDifferentGitHubOwner")}
															</span>
														</SelectItem>
													</SelectContent>
												</Select>
												{customGitHubOwner ? (
													<Input
														id="githubRepoCustomOwner"
														aria-label={t("createProject.githubOwner")}
														className="h-8 bg-[var(--color-bg-import-card)] font-mono text-[12px]"
														disabled={disabled}
														placeholder={t("createProject.githubOwner")}
														value={githubRepository?.owner ?? ""}
														onChange={(event) => {
															const next = { owner: event.target.value, name: githubRepository?.name ?? "", private: githubRepository?.private ?? true };
															onChangeGitHubRepository(next);
															onChangeRemote(githubRepositoryRemoteUrl(next));
														}}
													/>
												) : null}
											</div>
											<div className="space-y-1.5">
												<div className="relative">
													<Label htmlFor="githubRepoName" className="text-[12px] font-medium text-[var(--color-text-import-title)]">{t("createProject.githubRepositoryName")}</Label>
													<AnimatePresence initial={false}>
														{availability.state === "unavailable" ? (
															<motion.p
																id="githubRepoNameError"
																initial={{ opacity: 0, filter: "blur(2px)" }}
																animate={{ opacity: 1, filter: "blur(0px)" }}
																exit={{ opacity: 0, filter: "blur(2px)" }}
																transition={{ duration: 0.15, ease: "easeOut" }}
																className="absolute right-0 top-0 max-w-[65%] truncate overflow-hidden whitespace-nowrap text-right text-[12px] leading-5 text-destructive"
																role="alert"
															>
																{availability.message ?? t("createProject.githubRepoUnavailable")}
															</motion.p>
														) : null}
													</AnimatePresence>
												</div>
												<div className="relative">
													<Input
														id="githubRepoName"
														aria-label={t("createProject.githubRepositoryName")}
														aria-describedby={availability.state === "unavailable" ? "githubRepoNameError" : undefined}
														aria-invalid={availability.state === "unavailable" ? true : undefined}
														className="h-8 bg-[var(--color-bg-import-card)] pr-8 font-mono text-[12px]"
														disabled={disabled}
														value={githubRepository?.name ?? ""}
														onChange={(event) => {
															const next = { owner: githubRepository?.owner ?? "", name: event.target.value, private: githubRepository?.private ?? true };
															onChangeGitHubRepository(next);
															onChangeRemote(githubRepositoryRemoteUrl(next));
														}}
													/>
													<AnimatePresence initial={false}>
														{availability.state === "checking" ? (
															<motion.span
																initial={{ opacity: 0, filter: "blur(2px)" }}
																animate={{ opacity: 1, filter: "blur(0px)" }}
																exit={{ opacity: 0, filter: "blur(2px)" }}
																transition={{ duration: 0.15, ease: "easeOut" }}
																className="pointer-events-none absolute right-2.5 top-1/2 -translate-y-1/2"
																aria-label={t("createProject.githubRepoChecking")}
																role="status"
															>
																<LoaderCircle className="size-3.5 animate-spin text-muted-foreground" aria-hidden="true" />
															</motion.span>
														) : null}
													</AnimatePresence>
												</div>
											</div>
											<div className="flex items-center justify-between gap-3 py-1">
												<Label htmlFor="githubRepoPrivate" className="flex cursor-pointer items-center gap-2.5 min-w-0">
													{isPrivate ? (
														<Lock className="size-4 shrink-0 text-[var(--color-text-import-muted)]" aria-hidden="true" />
													) : (
														<Globe className="size-4 shrink-0 text-[var(--color-text-import-muted)]" aria-hidden="true" />
													)}
													<div className="min-w-0 space-y-0.5">
														<span className="block text-[12px] font-medium leading-4 text-[var(--color-text-import-title)]">
															{visibilityLabel}
														</span>
														<span id="githubRepoVisibilityHelp" className="block text-[11px] leading-4 text-[var(--color-text-import-muted)]">
															{visibilityHelper}
														</span>
													</div>
												</Label>
												<Switch
													id="githubRepoPrivate"
													aria-label={visibilityLabel}
													aria-describedby="githubRepoVisibilityHelp"
													checked={isPrivate}
													disabled={disabled}
													onCheckedChange={(privateRepository) =>
														onChangeGitHubRepository({
															owner: githubRepository?.owner ?? "",
															name: githubRepository?.name ?? "",
															private: privateRepository,
														})
													}
												/>
											</div>
									</div>
								) : (
										<div className="space-y-2 rounded-md border border-border/70 bg-background/40 p-3">
											<GitSetupFields
												actions={validation.root.requiredActions}
												approved={missingApprovals.length === 0}
												disabled={disabled}
												onApprovalChange={(approved) => onChangeApprovedActions(approved ? [...validation.root.requiredActions] : [])}
												onRemoteChange={onChangeRemote}
												remoteUrl={remoteUrl}
												showActionSummary={false}
											/>
										</div>
									)}
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
						{mustImportAsWorkspace ? (
							<Button type="button" variant="primary" disabled={disabled} onClick={onTryWorkspace}>{t("createProject.importAsWorkspace")}</Button>
						) : step === "blocked" ? (
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
									{hasFailedStep
										? t("createProject.retry")
										: needsRemote
											? t("createProject.createRepositoryAndContinue")
											: t("createProject.cloneContinue")}
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
	onContinueAsProject,
	onOpenChange,
	open,
	scan,
	validation,
	isPreparingGit,
	shake,
}: {
	disabled: boolean;
	error: string | null;
	kind: ProjectKind;
	onBack: () => void;
	onChooseFolder: () => void;
	onContinue: () => void;
	onContinueAsProject: () => void;
	onOpenChange: (open: boolean) => void;
	open: boolean;
	scan: ImportFolderScan | null;
	validation: ImportValidationResult | null;
	isPreparingGit: boolean;
	shake: boolean;
}) {
	const { t } = useTranslation();
	const isWorkspace = kind === "workspace";
	const displayRepos = isWorkspace ? mergeWorkspaceImportRepos(scan, validation) : normalizeImportRepos(scan?.repos ?? []);
	const workspaceHasNoChildGitRepos = isWorkspace && validation?.blockingErrors.includes("WORKSPACE_CHILD_REPO_REQUIRED");
	const workspaceRootIsProject = isWorkspace && validation?.root.isRepo === true && validation.root.hasOrigin === true;
	const workspaceValidationBlocked = isWorkspace && validation !== null && (!validation.isValid || validation.nextStep === "error") && !workspaceHasNoChildGitRepos;
	const workspaceReposNeedSetup = isWorkspace && displayRepos.some((repo) => repo.isRepo && repo.requiredActions.length > 0);
	const workspaceSetupReady = !isWorkspace || (!workspaceHasNoChildGitRepos && !workspaceReposNeedSetup);
	const failedRepos =
		displayRepos.filter(
			(repo) =>
				(repo.status === "error" || !repo.hasRemote) &&
				repo.requiredActions.length === 0 &&
				!repo.needsGitInit &&
				repo.reason !== "Repository must have at least one commit.",
		) ?? [];
	const hasScan = scan !== null;
	return (
		<Dialog.Root open={open} onOpenChange={onOpenChange}>
			<Dialog.Portal>
				<Dialog.Content className={cn("fixed left-1/2 top-1/2 z-overlay flex max-h-[min(640px,calc(100svh-24px))] w-[min(640px,calc(100vw-24px))] -translate-x-1/2 -translate-y-1/2 flex-col overflow-hidden rounded-lg border border-border bg-popover p-0 text-popover-foreground shadow-xl data-[state=open]:animate-modal-in data-[state=closed]:animate-modal-out motion-reduce:animate-none", shake && "modal-shake")}>
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
								{!workspaceHasNoChildGitRepos ? <PathRow
									action={t("createProject.change")}
									disabled={disabled}
									icon={<Folder className="size-4 shrink-0 text-[var(--color-text-import-muted)]" aria-hidden="true" />}
									onClick={onChooseFolder}
								>
									{displayImportPath(scan.path)}
								</PathRow> : null}

								{error && !isWorkspace && (
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
								{workspaceHasNoChildGitRepos && !error ? <p className="text-[14px] leading-6 text-[var(--color-text-import-muted)]">{t("createProject.workspaceRequiresInitializedChildRepo")}</p> : null}
								{workspaceRootIsProject && !error ? <p className="text-[14px] leading-6 text-[var(--color-text-import-muted)]">{t("createProject.workspaceRootIsProject")}</p> : null}
								{workspaceReposNeedSetup && !error ? <p className="text-[13px] leading-5 text-destructive">{t("createProject.workspaceRemoteSetupRequired")}</p> : null}

							{workspaceRootIsProject || workspaceHasNoChildGitRepos ? null : isWorkspace ? <WorkspaceImportRepoList repos={displayRepos} /> : displayRepos.length > 0 ? <div className="divide-y divide-border/50 overflow-hidden rounded-sm bg-[var(--color-bg-import-card)]">
								{displayRepos.map((repo) => <ImportRepoRow key={repo.path} repo={repo} />)}
							</div> : null}

								{displayRepos.length === 0 && !workspaceHasNoChildGitRepos && !workspaceRootIsProject && (
									<div className="rounded-md border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] p-3 text-[12px] text-[var(--color-text-import-muted)]">
										{t("createProject.noRepos")}
									</div>
								)}
							</div>
						) : null}
					</div>
					<div className="flex shrink-0 justify-end gap-2 px-4 pb-4 pt-3">
						<div className="flex flex-wrap items-center justify-end gap-3">
							<Button type="button" variant="outline" disabled={disabled} onClick={workspaceHasNoChildGitRepos ? onBack : () => onOpenChange(false)}>
								{workspaceHasNoChildGitRepos ? "Go Back" : t("createProject.cancel")}
							</Button>
							{hasScan && workspaceRootIsProject && !error ? (
								<Button type="button" variant="primary" disabled={disabled} onClick={onContinueAsProject}>
									{t("createProject.importAsProject")}
								</Button>
							) : hasScan && workspaceHasNoChildGitRepos && !error ? (
								<Button type="button" variant="outline" disabled={disabled} onClick={onContinueAsProject}>
									{t("createProject.importAsProject")}
								</Button>
							) : hasScan && !workspaceValidationBlocked && failedRepos.length === 0 && (!error || isWorkspace) ? (
								<Button type="button" variant="primary" disabled={disabled || !workspaceSetupReady} onClick={onContinue}>
									{isPreparingGit ? <><CircleDashed className="size-4 animate-spin" aria-hidden="true" />{t("createProject.settingUp")}</> : t("createProject.cloneContinue")}
								</Button>
							) : null}
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
				{needsSetup ? onSetup ? <button type="button" aria-expanded={setupExpanded} className="rounded-sm border border-orange-400/40 bg-orange-500/15 px-2 py-0.5 text-orange-300 hover:bg-orange-500/25" onClick={onSetup}>{setupExpanded ? "Hide setup" : `${workspaceSetupLabel(repo)} · Set up`}</button> : <span className="rounded-sm border border-orange-400/40 bg-orange-500/15 px-2 py-0.5 text-orange-300">{t("createProject.setupRequired")}</span> : repositoryUrl ? <><GitBranch className="size-3.5 shrink-0" aria-hidden="true" /><a className="truncate underline decoration-border underline-offset-2 hover:text-foreground" href={repositoryUrl} rel="noreferrer" target="_blank">{repo.branch}</a></> : <><span className={cn("truncate", isPlainFolder && "rounded-sm bg-orange-500/15 px-2 py-0.5 text-orange-300")}>{isPlainFolder ? "Needs git init" : failed ? (repo.reason ?? t("createProject.repoCannotImport")) : repo.branch}</span></>}
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

function WorkspaceImportRepoList({ repos }: { repos: DisplayImportRepo[] }) {
	const orderedRepos = [...repos].sort((left, right) => (Number(right.requiredActions.length > 0) - Number(left.requiredActions.length > 0)) || left.name.localeCompare(right.name));
	return <div className="divide-y divide-border/50 overflow-hidden rounded-sm bg-[var(--color-bg-import-card)]">
		{orderedRepos.map((repo) => <ImportRepoRow key={repo.path} repo={repo} />)}
	</div>;
}

function GitSetupFields({ actions, approved, disabled, onApprovalChange, onRemoteChange, remoteAriaLabel = "Origin remote URL", remotePlaceholder = "https://github.com/owner/repository.git", remoteUrl, showActionSummary = true }: {
	actions: string[];
	approved: boolean;
	disabled: boolean;
	onApprovalChange: (approved: boolean) => void;
	onRemoteChange: (remoteUrl: string) => void;
	remoteAriaLabel?: string;
	remotePlaceholder?: string;
	remoteUrl: string;
	showActionSummary?: boolean;
}) {
	const { t } = useTranslation();
	return <div className="space-y-2">
		<label className="flex items-start gap-2 text-[12px] text-[var(--color-text-import-title)]">
			<Checkbox checked={approved} className="mt-0.5" disabled={disabled} onCheckedChange={(checked) => onApprovalChange(checked === true)} />
			<span className="min-w-0 flex-1"><span className="block font-medium">{t("createProject.setupGitProject")}</span>{showActionSummary ? <span className="block text-[11px] leading-4 text-[var(--color-text-import-muted)]">{actions.map(gitActionLabel).join(", ")}</span> : null}</span>
		</label>
		{actions.includes("set_remote") ? <Input aria-label={remoteAriaLabel} className="h-8 bg-[var(--color-bg-import-card)] font-mono text-[12px]" disabled={disabled} placeholder={remotePlaceholder} value={remoteUrl} onChange={(event) => onRemoteChange(event.target.value)} /> : null}
	</div>;
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
			hasRemote: status?.hasOrigin ?? repo?.hasRemote ?? false,
			status: repo?.status ?? (status?.blockingErrors.length ? "error" : "ok"),
			reason: repo?.reason ?? status?.blockingErrors[0],
			needsGitInit: status?.needsGitInit ?? repo?.needsGitInit,
			requiredActions: repo?.isRepo !== undefined ? scanRequiredActions(repo) : status?.requiredActions ?? [],
			blockingErrors: status?.blockingErrors ?? [],
			isRepo: status?.isRepo ?? repo?.isRepo ?? false,
			hasCommit: status?.hasCommit ?? repo?.hasCommit ?? false,
			hasOrigin: status?.hasOrigin ?? repo?.hasRemote,
		};
	}).filter((repo) => repo.isRepo).sort((left, right) => left.name.localeCompare(right.name));
}

function scanRequiredActions(repo: ImportFolderScan["repos"][number] | undefined): string[] {
	if (!repo || repo.status === "error") return [];
	if (!repo.isRepo) return [...GIT_PREPARATION_ACTIONS];
	return [...(!repo.hasCommit ? ["git_commit"] : []), ...(!repo.hasRemote ? ["set_remote"] : [])];
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
