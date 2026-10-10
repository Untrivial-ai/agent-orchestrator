import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import { ArrowLeft, Loader2 } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState, type AnimationEvent } from "react";
import { useTranslation } from "react-i18next";
import { useCommandPaletteEnabled } from "../hooks/useCommandPaletteEnabled";
import { useCloudCp } from "../hooks/useCloudCp";
import { useRestoreSession } from "../hooks/useRestoreSession";
import { cloudSessionsQueryKey, useWorkspaceQuery, workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { aoBridge } from "../lib/bridge";
import { spawnCloudOrchestrator } from "../lib/cloud-orchestrator";
import {
	buildCommands,
	buildFileSessionCommands,
	buildSessionActions,
	buildWorkspaceFileCommands,
	displayGroups,
	filterCommands,
	findSession,
	type CommandItem as CommandItemModel,
	type NavigateTarget,
	type WorkspaceFileSearchTarget,
	sessionSupportsFileSearch,
	workspaceFileSearchTarget,
} from "../lib/command-palette";
import {
	type CommandPaletteFileSearchResponse,
	commandPaletteFileSearchAvailable,
	commandPaletteFileSearchQueryOptions,
} from "../lib/command-palette-file-search";
import { iconForCommand } from "../lib/command-palette-icons";
import { isDialogOrMenuOpen } from "../lib/dom-selectors";
import { isMacPlatform } from "../lib/platform";
import { sessionReviewsQueryOptions, type PRReviewState } from "../lib/session-reviews";
import { spawnOrchestrator } from "../lib/spawn-orchestrator";
import { useShell } from "../lib/shell-context";
import {
	findProjectOrchestrator,
	hasConfiguredOrchestratorAgent,
	openPRs,
	STANDALONE_WORKSPACE_ID,
	workerSessions,
} from "../types/workspace";
import { useUiStore } from "../stores/ui-store";
import { matchesRendererShortcut } from "../stores/keybindings-store";
import { Button } from "./ui/button";
import { CreateProjectFlow } from "./CreateProjectFlow";
import { TaskComposer } from "./TaskComposer";
import { CommandDialog, CommandEmpty, CommandFooter, CommandGroup, CommandInput, CommandItem, CommandList } from "./ui/command";

const PALETTE_REVIEW_STALE_TIME_MS = 60_000;
const PALETTE_REVIEW_DEFER_MS = 120;
const PALETTE_FILE_SEARCH_DEBOUNCE_MS = 200;
const PALETTE_FILE_SEARCH_MIN_CHARS = 2;
const EMPTY_REVIEW_STATES: Readonly<Record<string, PRReviewState[]>> = {};
type PaletteView =
	| { mode: "root" }
	| { mode: "session-actions"; sessionId: string }
	| { mode: "new-task"; projectId: string }
	| { mode: "file-session-picker"; projectId: string }
	| { mode: "file-search"; target: WorkspaceFileSearchTarget; parent: "root" | "picker" };

function terminalHasFocus(): boolean {
	if (typeof document === "undefined") return false;
	const active = document.activeElement;
	return active instanceof Element && active.closest(".xterm") !== null;
}

export function CommandPalette() {
	const { i18n, t } = useTranslation();
	const enabled = useCommandPaletteEnabled();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const cloud = useCloudCp();
	const restoreSessionById = useRestoreSession();
	const params = useParams({ strict: false }) as { hostId?: string; projectId?: string; sessionId?: string };
	const { cloneProject, createProject, initializeProjectRepository } = useShell();
	const resolvedTheme = useUiStore((s) => s.resolvedTheme);
	const setThemePreference = useUiStore((s) => s.setThemePreference);
	const isOpen = useUiStore((s) => s.isCommandPaletteOpen);
	const setOpen = useUiStore((s) => s.setCommandPaletteOpen);
	const restartingProjectIds = useUiStore((s) => s.restartingProjectIds);
	// The palette stays mounted to preserve its close animation and global
	// shortcut. While closed, commands are invisible, so retain the cached
	// snapshot without subscribing this hidden surface to streamed updates.
	const workspaces = useWorkspaceQuery({ subscribed: isOpen }).data ?? [];

	const [view, setView] = useState<PaletteView>({ mode: "root" });
	const [accountSlot, setAccountSlot] = useState<HTMLElement | null>(null);
	const [query, setQuery] = useState("");
	const [debouncedQuery, setDebouncedQuery] = useState("");
	const [selectedValue, setSelectedValue] = useState("");
	const [error, setError] = useState<string | null>(null);
	const [pendingId, setPendingId] = useState<string | null>(null);
	const [reviewStatesSnapshot, setReviewStatesSnapshot] = useState<Readonly<Record<string, PRReviewState[]>>>();
	const [reviewActionsReady, setReviewActionsReady] = useState(false);
	const [createProjectFlowMounted, setCreateProjectFlowMounted] = useState(false);
	const [createProjectFlowOpenSignal, setCreateProjectFlowOpenSignal] = useState(0);
	const [createProjectFlowPendingOpen, setCreateProjectFlowPendingOpen] = useState(false);
	const [pendingDismiss, setPendingDismiss] = useState<null | "pop" | "close">(null);
	const pendingRef = useRef(false);
	const runGenerationRef = useRef(0);
	const composerDirtyRef = useRef(false);
	const composerBusyRef = useRef(false);
	const viewRef = useRef(view);
	viewRef.current = view;
	const closeResetTimerRef = useRef<number | null>(null);

	const currentSessionContext = useMemo(() => {
		if (!params.sessionId) return undefined;
		for (const workspace of workspaces) {
			if ((workspace.hostId ?? "") !== (params.hostId ?? "")) continue;
			const session = workspace.sessions.find((candidate) => candidate.id === params.sessionId);
			if (session) return { workspace, session };
		}
		return undefined;
	}, [params.hostId, params.sessionId, workspaces]);
	const currentSession = currentSessionContext?.session;
	const currentProjectId = currentSession?.workspaceId ?? params.projectId;
	const currentWorkspace = useMemo(
		() => currentSessionContext?.workspace ?? workspaces.find((workspace) =>
			workspace.id === currentProjectId && (workspace.hostId ?? "") === (params.hostId ?? "")),
		[currentProjectId, currentSessionContext, params.hostId, workspaces],
	);
	const currentFileTarget = currentWorkspace && currentSession && sessionSupportsFileSearch(currentSession)
		? workspaceFileSearchTarget(currentWorkspace, currentSession)
		: undefined;

	useEffect(() => {
		if (!isOpen || view.mode === "new-task") {
			setDebouncedQuery("");
			return;
		}
		const timer = window.setTimeout(() => setDebouncedQuery(query.trim()), PALETTE_FILE_SEARCH_DEBOUNCE_MS);
		return () => window.clearTimeout(timer);
	}, [isOpen, query, view.mode]);

	const activeFileTarget = view.mode === "file-search"
		? view.target
		: view.mode === "root"
			? currentFileTarget
			: undefined;
	const normalizedFileQuery = debouncedQuery.trim();
	const fileSearchEnabled = isOpen
		&& normalizedFileQuery.length >= PALETTE_FILE_SEARCH_MIN_CHARS
		&& commandPaletteFileSearchAvailable(activeFileTarget, cloud.ready);
	const fileSearchOptions = commandPaletteFileSearchQueryOptions({
		target: activeFileTarget ?? { projectId: "", sessionId: "" },
		query: normalizedFileQuery,
		errorMessage: t("command.searchFilesError"),
		cloud,
	});
	const fileSearchQuery = useQuery<CommandPaletteFileSearchResponse>({ ...fileSearchOptions, enabled: fileSearchEnabled });

	const sessionsWithOpenPRs = useMemo(
		() =>
			(reviewActionsReady ? workspaces : []).flatMap((workspace) =>
				workerSessions(workspace.sessions).filter((session) => openPRs(session).length > 0),
			),
		[reviewActionsReady, workspaces],
	);
	// Opening the palette must first acknowledge the shortcut and focus its input.
	// Review actions are secondary and may require one query per PR-bearing session,
	// so defer their setup until the palette has had a chance to paint.
	useEffect(() => {
		if (!isOpen) {
			setReviewActionsReady(false);
			return;
		}
		const timer = window.setTimeout(() => setReviewActionsReady(true), PALETTE_REVIEW_DEFER_MS);
		return () => window.clearTimeout(timer);
	}, [isOpen]);
	// Review states are fetched only while the palette is open; the shared query
	// key means sessions already viewed in the inspector reuse the cached data.
	const reviewQuerySummary = useQueries({
		subscribed: reviewActionsReady,
		queries: sessionsWithOpenPRs.map((session) =>
			sessionReviewsQueryOptions(session, reviewActionsReady, PALETTE_REVIEW_STALE_TIME_MS),
		),
		combine: (results) => {
			const reviewStatesBySessionId: Record<string, PRReviewState[]> = {};
			results.forEach((result, index) => {
				const session = sessionsWithOpenPRs[index];
				if (session && result.data && !result.isFetching && !result.isError) {
					reviewStatesBySessionId[session.id] = result.data.reviews ?? [];
				}
			});
			return { reviewStatesBySessionId };
		},
	});

	// Each session's review action stays hidden until we know whether it's
	// safe to trigger, then is frozen for the rest of this open — merging in
	// newly-resolved sessions but never overwriting one already captured, so
	// a later poll (e.g. a running review completing) can't retitle or
	// re-sort a row the user may already have selected.
	useEffect(() => {
		if (!isOpen) {
			setReviewStatesSnapshot({});
			return;
		}
		if (!reviewActionsReady) return;
		setReviewStatesSnapshot((previous) => {
			let changed = false;
			const next = { ...previous };
			for (const [sessionId, reviews] of Object.entries(reviewQuerySummary.reviewStatesBySessionId)) {
				if (!(sessionId in next)) {
					next[sessionId] = reviews;
					changed = true;
				}
			}
			return changed ? next : previous;
		});
	}, [isOpen, reviewActionsReady, reviewQuerySummary.reviewStatesBySessionId]);
	const reviewStatesForCommands = isOpen && reviewActionsReady ? reviewStatesSnapshot : EMPTY_REVIEW_STATES;

	const rootItems = useMemo(
		() =>
			buildCommands({
				workspaces,
				currentProjectId,
				currentSessionId: params.sessionId,
				currentHostId: params.hostId,
				restartingProjectIds,
				reviewStatesBySessionId: reviewStatesForCommands,
			}, t),
		[workspaces, currentProjectId, params.hostId, params.sessionId, restartingProjectIds, reviewStatesForCommands, t, i18n.resolvedLanguage],
	);
	const scoped = useMemo(
		() => (view.mode === "session-actions" ? findSession(workspaces, view.sessionId) : undefined),
		[view, workspaces],
	);
	const sessionActionItems = useMemo(
		() => (scoped ? buildSessionActions(scoped.workspace, scoped.session, t) : []),
		[scoped, t],
	);
	const filePickerWorkspace = view.mode === "file-session-picker"
		? workspaces.find((workspace) => workspace.id === view.projectId && (workspace.hostId ?? "") === (params.hostId ?? ""))
		: undefined;
	const fileSessionItems = useMemo(
		() => filePickerWorkspace ? buildFileSessionCommands(filePickerWorkspace) : [],
		[filePickerWorkspace],
	);
	const fileItems = useMemo(
		() => activeFileTarget && fileSearchQuery.data
			? buildWorkspaceFileCommands(activeFileTarget, fileSearchQuery.data.results)
			: [],
		[activeFileTarget, fileSearchQuery.data],
	);

	const groups = useMemo(() => {
		if (view.mode === "session-actions") {
			return [{ id: "actions", label: "", items: filterCommands(sessionActionItems, query) }];
		}
		if (view.mode === "file-session-picker") {
			return [{ id: "file-sessions", label: "", items: filterCommands(fileSessionItems, query) }];
		}
		if (view.mode === "file-search") {
			return displayGroups(fileItems, normalizedFileQuery, t);
		}
		const settledFileItems = normalizedFileQuery === query.trim() ? fileItems : [];
		return displayGroups([...rootItems, ...settledFileItems], query, t);
	}, [view.mode, rootItems, sessionActionItems, fileSessionItems, fileItems, query, normalizedFileQuery, t, i18n.resolvedLanguage]);

	const visibleItems = useMemo(() => groups.flatMap((group) => group.items), [groups]);
	const value =
		(visibleItems.some((item) => item.id === selectedValue && !item.disabled)
			? selectedValue
			: (visibleItems.find((item) => !item.disabled) ?? visibleItems[0])?.id) ?? "";

	const resetTransient = useCallback(() => {
		runGenerationRef.current += 1;
		setQuery("");
		setDebouncedQuery("");
		setSelectedValue("");
		setError(null);
	}, []);

	const closePalette = useCallback(() => {
		// Keep the current query visible while Radix plays the closing animation.
		// Clearing it here causes the palette to flash an empty search before it
		// is removed, especially when an action also changes the theme.
		runGenerationRef.current += 1;
		setOpen(false);
		setView({ mode: "root" });
		setPendingDismiss(null);
		if (closeResetTimerRef.current !== null) window.clearTimeout(closeResetTimerRef.current);
		// Reduced-motion mode disables the closing animation, so keep a fallback
		// reset for environments where no animationend event will arrive.
		closeResetTimerRef.current = window.setTimeout(() => {
			closeResetTimerRef.current = null;
			if (!useUiStore.getState().isCommandPaletteOpen) resetTransient();
		}, 150);
	}, [resetTransient, setOpen]);
	// The import flow has its own cloud/query/dialog subtree. Mounting it beside a
	// permanently retained palette made every palette render pay for an unrelated
	// feature. Mount it only after the user chooses New project, then pulse its
	// existing programmatic-open signal on the following commit (the flow seeds its
	// signal ref on mount, so doing both in one render would intentionally no-op).
	useEffect(() => {
		if (!createProjectFlowMounted || !createProjectFlowPendingOpen) return;
		setCreateProjectFlowPendingOpen(false);
		setCreateProjectFlowOpenSignal((current) => current + 1);
	}, [createProjectFlowMounted, createProjectFlowPendingOpen]);
	const openNewProject = useCallback(() => {
		closePalette();
		setCreateProjectFlowMounted(true);
		setCreateProjectFlowPendingOpen(true);
	}, [closePalette]);
	const openExistingProject = useCallback(
		(path: string) => {
			const workspace = workspaces.find((candidate) => candidate.path === path);
			if (!workspace) return;
			closePalette();
			void navigate({ to: "/projects/$projectId", params: { projectId: workspace.id } });
		},
		[closePalette, navigate, workspaces],
	);

	const resetAfterClose = useCallback(() => {
		if (closeResetTimerRef.current !== null) {
			window.clearTimeout(closeResetTimerRef.current);
			closeResetTimerRef.current = null;
		}
		if (!useUiStore.getState().isCommandPaletteOpen) resetTransient();
	}, [resetTransient]);

	useEffect(
		() => () => {
			if (closeResetTimerRef.current !== null) window.clearTimeout(closeResetTimerRef.current);
		},
		[],
	);

	const handlePaletteAnimationEnd = useCallback(
		(event: AnimationEvent<HTMLDivElement>) => {
			if (event.target === event.currentTarget) resetAfterClose();
		},
		[resetAfterClose],
	);

	const popView = useCallback(() => {
		const current = viewRef.current;
		if (current.mode === "file-search" && current.parent === "picker") {
			setView({ mode: "file-session-picker", projectId: current.target.projectId });
		} else {
			setView({ mode: "root" });
		}
		setPendingDismiss(null);
		resetTransient();
	}, [resetTransient]);

	const pushView = useCallback(
		(next: PaletteView) => {
			setView(next);
			setPendingDismiss(null);
			resetTransient();
		},
		[resetTransient],
	);

	const requestDismiss = useCallback(
		(target: "pop" | "close") => {
			const current = viewRef.current;
			if (composerBusyRef.current) return;
			if (current.mode === "new-task" && composerDirtyRef.current) {
				setPendingDismiss(target);
				return;
			}
			if (target === "close" || current.mode === "root") {
				closePalette();
			} else {
				popView();
			}
		},
		[closePalette, popView],
	);

	const onComposerDirtyChange = useCallback((dirty: boolean) => {
		composerDirtyRef.current = dirty;
	}, []);

	const onComposerSubmittingChange = useCallback((submitting: boolean) => {
		composerBusyRef.current = submitting;
		if (submitting) setPendingDismiss(null);
	}, []);

	const confirmDiscard = useCallback(() => {
		if (composerBusyRef.current) return;
		const target = pendingDismiss;
		composerDirtyRef.current = false;
		setPendingDismiss(null);
		if (target === "close") closePalette();
		else popView();
	}, [pendingDismiss, closePalette, popView]);

	useEffect(() => {
		if (view.mode === "session-actions" && !scoped) {
			setView({ mode: "root" });
			setQuery("");
			setSelectedValue("");
			setError(t("command.sessionUnavailable"));
		}
	}, [view, scoped, t]);

	const toggleTheme = useCallback(() => {
		setThemePreference(resolvedTheme === "dark" ? "light" : "dark");
	}, [resolvedTheme, setThemePreference]);

	const navigateToTarget = useCallback(
		(target: NavigateTarget) => {
			switch (target.to) {
				case "/":
					void navigate({ to: target.to });
					break;
				case "/settings":
					// Modal — do not route to /settings (that legacy path redirects home).
					useUiStore.getState().openGlobalSettings();
					break;
				case "/sessions/$sessionId":
					void navigate({ to: target.to, params: target.params });
					break;
				case "/projects/$projectId":
					void navigate({ to: target.to, params: target.params });
					break;
				case "/projects/$projectId/settings":
					useUiStore.getState().openProjectSettings(target.params.projectId, { cloudOrgId: workspaces.find((workspace) => workspace.id === target.params.projectId)?.cloudOrgId });
					break;
				case "/projects/$projectId/sessions/$sessionId":
				case "/host/$hostId/project/$projectId/session/$sessionId":
				case "/host/$hostId/session/$sessionId":
					void navigate({ to: target.to, params: target.params });
					break;
			}
		},
		[navigate, workspaces],
	);

	const sessionRoute = useCallback(
		(projectId: string, sessionId: string): Extract<NavigateTarget, { to: "/sessions/$sessionId" | "/projects/$projectId/sessions/$sessionId" }> => {
			return projectId === STANDALONE_WORKSPACE_ID
				? { to: "/sessions/$sessionId", params: { sessionId } }
				: { to: "/projects/$projectId/sessions/$sessionId", params: { projectId, sessionId } };
		},
		[],
	);

	const fileSessionRoute = useCallback((target: WorkspaceFileSearchTarget): NavigateTarget => {
		if (target.hostId) {
			return target.projectId === STANDALONE_WORKSPACE_ID
				? { to: "/host/$hostId/session/$sessionId", params: { hostId: target.hostId, sessionId: target.sessionId } }
				: {
					to: "/host/$hostId/project/$projectId/session/$sessionId",
					params: { hostId: target.hostId, projectId: target.projectId, sessionId: target.sessionId },
				};
		}
		return sessionRoute(target.projectId, target.sessionId);
	}, [sessionRoute]);

	const blockedByRestart = useCallback((projectId: string) => {
		if (!useUiStore.getState().restartingProjectIds.has(projectId)) return false;
		setError(t("command.orchestratorRestarting"));
		return true;
	}, [t]);

	const openOrchestrator = useCallback(
		async (projectId: string) => {
			if (blockedByRestart(projectId)) return;
			const orchestrator = findProjectOrchestrator(workspaces, projectId);
			if (orchestrator) {
				navigateToTarget(sessionRoute(projectId, orchestrator.id));
				closePalette();
				return;
			}
			const workspace = workspaces.find((candidate) => candidate.id === projectId);
			// Cloud projects carry no local orchestrator-agent config; spawn the
			// orchestrator as a cloud session in its own sandbox instead of falling
			// through to the project-settings page.
			if (workspace?.kind === "cloud") {
				const sessionId = await spawnCloudOrchestrator(queryClient, projectId);
				await queryClient.invalidateQueries({ queryKey: cloudSessionsQueryKey });
				navigateToTarget(sessionRoute(projectId, sessionId));
				closePalette();
				return;
			}
			if (!hasConfiguredOrchestratorAgent(workspace)) {
				if (workspace) {
					navigateToTarget({ to: "/projects/$projectId/settings", params: { projectId } });
					closePalette();
				}
				return;
			}
			const sessionId = await spawnOrchestrator(projectId, "command_palette");
			await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
			navigateToTarget(sessionRoute(projectId, sessionId));
			closePalette();
		},
		[workspaces, navigateToTarget, queryClient, closePalette, blockedByRestart, sessionRoute],
	);

	const resumeSession = useCallback(
		async (sessionId: string) => {
			const result = await restoreSessionById(sessionId);
			if (result.status === "success") return null;
			if (result.status === "not_resumable") return t("command.resumeNotResumable");
			return result.message;
		},
		[restoreSessionById, t],
	);

	const runAction = useCallback(
		async (item: CommandItemModel) => {
			const action = item.action;
			if (!action) return;
			setError(null);
			pendingRef.current = true;
			setPendingId(item.id);
			const generation = runGenerationRef.current;
			const isCurrentRun = () => runGenerationRef.current === generation;
			try {
				switch (action.kind) {
					case "navigate":
						navigateToTarget(action.target);
						closePalette();
						break;
					case "toggle-theme":
						toggleTheme();
						closePalette();
						break;
					case "copy-branch":
						await aoBridge.clipboard.writeText(action.branch);
						closePalette();
						break;
				case "open-pr":
					await aoBridge.app.openExternal(action.url);
					closePalette();
					break;
				case "copy-pr-url":
					await aoBridge.clipboard.writeText(action.url);
					closePalette();
					break;
				case "trigger-review": {
					const { error: triggerError } = await apiClient.POST("/api/v1/sessions/{sessionId}/reviews/trigger", {
						params: { path: { sessionId: action.sessionId } },
					});
					if (triggerError) throw new Error(apiErrorMessage(triggerError, "Unable to start review"));
					await queryClient.invalidateQueries({ queryKey: ["session-reviews", action.sessionId] });
					await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
					closePalette();
					break;
				}
				case "open-session-actions":
					pushView({ mode: "session-actions", sessionId: action.sessionId });
					break;
				case "open-file-search":
					if (action.target) {
						pushView({
							mode: "file-search",
							target: action.target,
							parent: viewRef.current.mode === "file-session-picker" ? "picker" : "root",
						});
					} else {
						pushView({ mode: "file-session-picker", projectId: action.projectId });
					}
					break;
				case "open-workspace-file": {
					useUiStore.getState().requestWorkspaceFileOpen(
						action.target.sessionId,
						action.path,
						action.target.hostId,
					);
					const alreadyOpen = params.sessionId === action.target.sessionId
						&& (params.hostId ?? "") === (action.target.hostId ?? "");
					if (!alreadyOpen) navigateToTarget(fileSessionRoute(action.target));
					closePalette();
					break;
				}
				case "resume-session": {
					const message = await resumeSession(action.sessionId);
					if (!isCurrentRun()) break;
					if (message) {
						setError(message);
						break;
					}
					navigateToTarget(sessionRoute(action.projectId, action.sessionId));
						closePalette();
						break;
					}
					case "open-new-task":
						if (blockedByRestart(action.projectId)) break;
						pushView({ mode: "new-task", projectId: action.projectId });
						break;
					case "open-new-project":
						openNewProject();
						break;
					case "open-orchestrator":
							await openOrchestrator(action.projectId);
							break;
				}
			} catch (err) {
				if (isCurrentRun()) setError(err instanceof Error ? err.message : t("command.failed"));
			} finally {
				pendingRef.current = false;
				setPendingId(null);
			}
		},
		[navigateToTarget, closePalette, toggleTheme, openOrchestrator, resumeSession, pushView, blockedByRestart, openNewProject, queryClient, t, params.hostId, params.sessionId, fileSessionRoute],
	);

	const onSelectItem = useCallback(
		(item: CommandItemModel) => {
			if (item.disabled || !item.action) return;
			if (pendingRef.current) return;
			void runAction(item);
		},
		[runAction],
	);

	const handleTaskCreated = useCallback(
		async (projectId: string, sessionId: string) => {
			closePalette();
			await queryClient.invalidateQueries({ queryKey: workspaceQueryKey });
			void navigateToTarget(sessionRoute(projectId, sessionId));
		},
		[queryClient, closePalette, navigateToTarget, sessionRoute],
	);

	useEffect(() => {
		if (!enabled) return;
		const handleKeyDown = (event: KeyboardEvent) => {
			if (isOpen && (event.metaKey || event.ctrlKey) && /^[1-9]$/.test(event.key)) {
				event.preventDefault();
				event.stopPropagation();
				return;
			}

			if (!matchesRendererShortcut("command-palette", event)) return;

			if (isOpen) {
				event.preventDefault();
				requestDismiss("close");
				return;
			}
			// Preserve the default Ctrl+K readline command in terminals. A user
			// who deliberately assigns a different palette binding expects it to
			// work there too.
			if (
				!isMacPlatform() &&
				terminalHasFocus() &&
				event.key.toLowerCase() === "k" &&
				event.ctrlKey &&
				!event.metaKey &&
				!event.altKey &&
				!event.shiftKey
			)
				return;
			if (isDialogOrMenuOpen()) return;
			event.preventDefault();
			setOpen(true);
		};
		window.addEventListener("keydown", handleKeyDown, true);
		return () => window.removeEventListener("keydown", handleKeyDown, true);
	}, [enabled, isOpen, setOpen, requestDismiss]);

	if (!enabled) return null;

	const fileSearchSession = view.mode === "file-search"
		? workspaces
			.find((workspace) => workspace.id === view.target.projectId
				&& (workspace.hostId ?? "") === (view.target.hostId ?? ""))
			?.sessions.find((session) => session.id === view.target.sessionId)
		: undefined;
	const contextLabel =
		view.mode === "session-actions"
			? (scoped?.session.title ?? t("command.sessionFallback"))
			: view.mode === "new-task"
				? t("command.newTask")
				: view.mode === "file-session-picker"
					? t("command.chooseFileSession")
					: view.mode === "file-search"
						? (fileSearchSession?.title ?? t("command.sessionFallback"))
						: "";
	const fileSearchActive = view.mode === "file-search"
		|| (view.mode === "root" && Boolean(activeFileTarget) && query.trim().length >= PALETTE_FILE_SEARCH_MIN_CHARS);
	const fileSearchSettled = normalizedFileQuery === query.trim();
	const fileSearchError = fileSearchActive && fileSearchSettled && fileSearchQuery.error
		? (fileSearchQuery.error instanceof Error ? fileSearchQuery.error.message : t("command.searchFilesError"))
		: null;

	return (
		<>
				<CommandDialog
					// CommandDialog supplies an overlay plus trapped focus without the
					// body-wide scroll/pointer lock that made palette opening scale with
					// every retained shell node.
					modal={false}
					open={isOpen}
				onOpenChange={(open) => (open ? setOpen(true) : requestDismiss("close"))}
					contentProps={{
						onAnimationEnd: handlePaletteAnimationEnd,
					onEscapeKeyDown: (event) => {
						event.preventDefault();
						if (event.isComposing) return;
						if (pendingDismiss !== null) {
							setPendingDismiss(null);
							return;
						}
						requestDismiss(viewRef.current.mode === "root" ? "close" : "pop");
					},
				}}
				commandProps={{
					shouldFilter: false,
					value,
					onValueChange: setSelectedValue,
					loop: true,
					label: t("command.palette"),
				}}
			>
				{view.mode !== "root" && (
					<div className="flex items-center gap-2 border-b border-border px-3 py-2">
						<button
							type="button"
							onClick={() => requestDismiss("pop")}
							className="grid size-10 place-items-center rounded-md text-muted-foreground transition-colors hover:bg-surface hover:text-foreground"
							aria-label={t("command.back")}
						>
							<ArrowLeft className="size-icon-base" aria-hidden="true" />
						</button>
						<span className="min-w-0 truncate rounded-md bg-surface px-2 py-0.5 text-2xs font-medium text-muted-foreground">
							{contextLabel}
						</span>
						<div ref={setAccountSlot} className="ml-auto" />
					</div>
				)}

				{view.mode === "new-task" ? (
					<div onKeyDown={(event) => event.stopPropagation()}>
						{pendingDismiss !== null && (
							<div className="mx-3 mt-3 rounded-md border border-border bg-surface px-3 py-2 text-xs text-foreground">
								<p className="text-muted-foreground">{t("command.discardDraft")}</p>
								<div className="mt-2 flex justify-end gap-3">
									<Button type="button" variant="footer" onClick={() => setPendingDismiss(null)}>
										{t("command.keepEditing")}
									</Button>
									<Button type="button" variant="footer" className="text-destructive" onClick={confirmDiscard}>
										{t("command.discard")}
									</Button>
								</div>
							</div>
						)}
						<TaskComposer
							projectId={view.projectId}
							autoFocusTitle
							accountControlContainer={accountSlot}
							onDirtyChange={onComposerDirtyChange}
							onSubmittingChange={onComposerSubmittingChange}
							onCreated={(sessionId) => void handleTaskCreated(view.projectId, sessionId)}
						/>
					</div>
				) : (
					<>
						<CommandInput
							value={query}
								onValueChange={(next) => {
									setQuery(next);
									setError(null);
								}}
							placeholder={
								view.mode === "session-actions"
									? t("command.searchActionsPlaceholder")
									: view.mode === "file-session-picker"
										? t("command.chooseFileSession")
										: view.mode === "file-search"
											? t("command.searchFilesPlaceholder", { session: fileSearchSession?.title ?? t("command.sessionFallback") })
											: t("command.searchPlaceholder")
							}
							onKeyDown={(event) => {
								if (
									event.key === "Backspace" &&
									query === "" &&
									!event.nativeEvent.isComposing &&
									viewRef.current.mode !== "root"
								) {
									event.preventDefault();
									requestDismiss("pop");
								}
							}}
						/>
						<CommandList>
							<CommandEmpty>
								{view.mode === "file-search"
									? query.trim().length < PALETTE_FILE_SEARCH_MIN_CHARS
										? t("command.searchFilesHint")
										: fileSearchQuery.isFetching || !fileSearchSettled
											? t("command.searchFilesLoading")
											: t("command.searchFilesEmpty")
									: t("command.noResults")}
							</CommandEmpty>
							{error && (
								<div
									role="alert"
									className="mx-1 mb-1 overflow-hidden rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs wrap-break-word text-destructive"
								>
									{error}
								</div>
							)}
							{fileSearchError && (
								<div role="alert" className="mx-1 mb-1 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
									{fileSearchError}
								</div>
							)}
							{view.mode === "file-search" && fileSearchQuery.isFetching && fileItems.length > 0 && (
								<div role="status" className="px-3 py-1 text-xs text-muted-foreground">{t("command.searchFilesLoading")}</div>
							)}
							{groups.map((group) => (
								<CommandGroup key={group.id} heading={group.label || undefined}>
									{group.items.map((item) => {
										const Icon = iconForCommand(item);
										return (
										<CommandItem
											key={item.id}
											value={item.id}
											disabled={item.disabled || (pendingId !== null && pendingId !== item.id)}
											onSelect={() => onSelectItem(item)}
										>
											{Icon ? <Icon strokeWidth={1.75} aria-hidden="true" /> : null}
											<span className="min-w-0 flex-1 truncate">{item.title}</span>
											{pendingId === item.id ? (
												<Loader2 className="ml-auto size-3.5 animate-spin text-[var(--color-text-command-muted)]" aria-hidden="true" />
											) : item.disabled && item.disabledReason ? (
												<span className="ml-auto text-control text-[var(--color-text-command-muted)]">
													{item.disabledReason}
												</span>
											) : item.subtitle ? (
												<span className="ml-auto max-w-command-subtitle truncate text-control text-[var(--color-text-command-muted)]">
													{item.subtitle}
												</span>
											) : null}
										</CommandItem>
										);
									})}
								</CommandGroup>
							))}
							{fileSearchActive && fileSearchSettled && fileSearchQuery.data?.truncated && (
								<div role="status" className="px-3 py-2 text-xs text-muted-foreground">{t("command.searchFilesTruncated")}</div>
							)}
						</CommandList>
						<CommandFooter aria-hidden="true">
							<span className="inline-flex items-center gap-1.5">
								<span>↑↓</span>
								<span>{t("command.select")}</span>
							</span>
							<span className="inline-flex items-center gap-1.5">
								<span>↵</span>
								<span>{t("command.open")}</span>
							</span>
						</CommandFooter>
					</>
				)}
			</CommandDialog>

			{createProjectFlowMounted ? (
				<CreateProjectFlow
					mode="choose"
					openSignal={createProjectFlowOpenSignal}
					onCloneProject={cloneProject}
					onCreateProject={createProject}
					onCreateStandaloneAgent={() => useUiStore.getState().requestNewTask(STANDALONE_WORKSPACE_ID)}
					onInitializeProject={initializeProjectRepository}
					onOpenExistingProject={openExistingProject}
					existingProjectPaths={workspaces.map((workspace) => workspace.path)}
					existingProjectNames={workspaces.map((workspace) => workspace.name)}
				/>
			) : null}
		</>
	);
}
