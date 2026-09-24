import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Loader2, MessageSquare, Play, Plus, TerminalSquare } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";
import { useUiStore } from "../../stores/ui-store";
import { apiErrorMessage } from "../../lib/api-client";
import { useNavigateToSession, useNavigateToTerminals } from "../../lib/navigate-to-session";
import { useInvokeCueMutation, useProjectCuesQuery } from "../../hooks/useCuesQuery";
import { fetchProjectCues, projectCuesQueryKey, type CueDTO } from "../../lib/cues";
import { shellTerminalsQueryKey, toShellTerminal, type ShellTerminal } from "../../hooks/useShellTerminals";
import { markTerminalHandleFresh } from "../../lib/fresh-terminal-handles";
import { terminalShellRequestValue, useTerminalShellStore } from "../../stores/terminal-shell-store";
import { commandCueInvocation, useCommandCueStore } from "../../stores/command-cue-store";
import { TopbarButton } from "../TopbarButton";
import {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "../ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "../ui/tooltip";

// Hovering the trigger opens the menu immediately — it is the discoverability
// affordance, not a tooltip. The close delay only lets the pointer cross the
// gap between the trigger and the menu without the menu vanishing underneath it.
const CUE_MENU_CLOSE_MS = 150;

// The one cue runner, mounted as a topbar icon button wherever cues apply: the
// project board (no sessionId) starts a worker for agent Cues or a project
// terminal for command Cues; the session topbar targets the selected session.
// Keying by target drops in-flight menu state when switching project or session.
export function CueRunMenu({
	projectId,
	sessionId,
	disabled = false,
}: {
	projectId: string;
	sessionId?: string;
	disabled?: boolean;
}) {
	return (
		<CueRunMenuTrigger
			key={JSON.stringify([projectId, sessionId ?? null])}
			projectId={projectId}
			sessionId={sessionId}
			disabled={disabled}
		/>
	);
}

function CueRunMenuTrigger({
	projectId,
	sessionId,
	disabled,
}: {
	projectId: string;
	sessionId?: string;
	disabled: boolean;
}) {
	const { t } = useTranslation();
	const showGlobalToast = useUiStore((state) => state.showGlobalToast);
	const queryClient = useQueryClient();
	const navigateToSession = useNavigateToSession();
	const navigateToTerminals = useNavigateToTerminals();
	const setActiveShellTerminal = useUiStore((state) => state.setActiveShellTerminal);
	const registerCommandCue = useCommandCueStore((state) => state.register);
	const [open, setOpen] = useState(false);
	const [runningPrimary, setRunningPrimary] = useState(false);
	const pending = useRef(false);
	const generation = useRef(0);
	const hoverCloseTimer = useRef<number | null>(null);
	// The menu is hover-driven, so the pointerdown Radix uses to toggle a menu —
	// and the dismiss layer that same press wakes up — must not close a menu the
	// pointer is still inside. Escape is the keyboard escape hatch.
	const pointerInsideControl = useRef(false);
	const escapeRequested = useRef(false);
	const invokeMutation = useInvokeCueMutation();
	const [invokingId, setInvokingId] = useState<string | null>(null);
	const busy = invokingId !== null;
	const spinner = busy || runningPrimary;

	const cancelHoverClose = () => {
		if (hoverCloseTimer.current === null) return;
		window.clearTimeout(hoverCloseTimer.current);
		hoverCloseTimer.current = null;
	};
	const scheduleHoverClose = () => {
		cancelHoverClose();
		hoverCloseTimer.current = window.setTimeout(() => {
			hoverCloseTimer.current = null;
			setOpen(false);
		}, CUE_MENU_CLOSE_MS);
	};
	useEffect(() => () => {
		generation.current++;
		if (hoverCloseTimer.current !== null) window.clearTimeout(hoverCloseTimer.current);
	}, []);

	const handlePointerEnter = () => {
		pointerInsideControl.current = true;
		cancelHoverClose();
		if (disabled || spinner) return;
		if (!open) openMenu();
	};

	const handlePointerLeave = () => {
		pointerInsideControl.current = false;
		scheduleHoverClose();
	};

	const handleMenuPointerEnter = () => {
		pointerInsideControl.current = true;
		cancelHoverClose();
	};

	// Opening never bumps the generation: with hover opening the menu, a pointer
	// drifting back over the trigger must not invalidate a dispatch that is still
	// in flight and drop its toast or navigation. Target changes and unmounts are
	// what retire stale work — this component is keyed by project and session.
	const openMenu = () => setOpen(true);

	const handleOpenChange = (next: boolean) => {
		if (next) {
			openMenu();
			return;
		}
		if (pointerInsideControl.current && !escapeRequested.current) return;
		escapeRequested.current = false;
		cancelHoverClose();
		setOpen(false);
	};

	const handleInvoke = async (cue: CueDTO) => {
		if (pending.current) return;
		pending.current = true;
		const invocation = cue.type === "command" && sessionId ? commandCueInvocation() : undefined;
		const origin = generation.current;
		setInvokingId(cue.id);
		try {
			await useTerminalShellStore.getState().load();
			const shell = terminalShellRequestValue(useTerminalShellStore.getState().preference);
			const result = await invokeMutation.mutateAsync({ cueId: cue.id, sessionId, shell });
			if (result.kind === "command") {
				if (!result.shellTerminal) throw new Error(t("cues.invokeFailed"));
				const terminal = toShellTerminal(result.shellTerminal);
				markTerminalHandleFresh(terminal.handleId);
				queryClient.setQueryData<ShellTerminal[]>(shellTerminalsQueryKey, (current = []) => [
					terminal,
					...current.filter((item) => item.handleId !== terminal.handleId),
				]);
				registerCommandCue({
					...invocation,
					projectId,
					sessionId,
					handleId: terminal.handleId,
					name: cue.name,
					command: cue.command ?? "",
					state: "starting",
				});
				// The command may finish launching after navigation. Keep its card in
				// the originating session without changing the newly selected view.
				if (origin !== generation.current) return;
				setActiveShellTerminal(terminal.handleId);
				showGlobalToast(t("cues.invokeCommandStarted"), t("cues.invokeCommandStartedBody", { name: cue.name }));
				if (!sessionId) navigateToTerminals();
				return;
			}
			if (origin !== generation.current) return;
			showGlobalToast(t("cues.invokeSent"), t("cues.invokeSentBody", { name: cue.name }));
			if (!sessionId && result.sessionId) navigateToSession(projectId, result.sessionId);
		} catch (error) {
			if (origin !== generation.current) return;
			showGlobalToast(t("cues.invokeFailed"), apiErrorMessage(error, t("cues.invokeFailed")), "error");
		} finally {
			pending.current = false;
			if (origin === generation.current) setInvokingId(null);
		}
	};

	// A tap runs the menu's primary cue — the first entry, which the daemon
	// returns in name order — instead of toggling the menu that hover owns.
	const runPrimaryCue = async () => {
		if (disabled || spinner) return;
		setRunningPrimary(true);
		try {
			const cues = await queryClient.fetchQuery({
				queryKey: projectCuesQueryKey(projectId),
				queryFn: () => fetchProjectCues(projectId),
				staleTime: 0,
			});
			const primary = cues[0];
			// Nothing to run: leave the hover menu up so its empty state can
			// offer creating the first cue.
			if (!primary) {
				openMenu();
				return;
			}
			await handleInvoke(primary);
		} catch (error) {
			showGlobalToast(t("cues.loadFailed"), apiErrorMessage(error, t("cues.loadFailed")), "error");
		} finally {
			setRunningPrimary(false);
		}
	};

	return (
		<DropdownMenu modal={false} open={open} onOpenChange={handleOpenChange}>
			<DropdownMenuTrigger asChild>
				<TopbarButton
					type="button"
					aria-label={t("cues.run")}
					// The menu is anchored to this button: pressing must not scale it,
					// or the open menu shifts with the trigger.
					className="topbar-control--static-press"
					data-priority="secondary"
					disabled={disabled || spinner}
					variant="icon"
					onPointerEnter={handlePointerEnter}
					onPointerLeave={handlePointerLeave}
					// Keyboard activation opens the menu (Radix); only pointer
					// presses run the primary cue, so Enter/Space stay safe.
					onClick={(event) => {
						if (event.detail === 0) return;
						void runPrimaryCue();
					}}
				>
					{spinner ? (
						<Loader2 className="size-icon-md animate-spin" aria-hidden="true" />
					) : (
						<Play className="size-icon-md" aria-hidden="true" />
					)}
				</TopbarButton>
			</DropdownMenuTrigger>

			{open ? (
				<CueMenuItems
					projectId={projectId}
					busy={busy}
					onInvoke={handleInvoke}
					onDismiss={() => setOpen(false)}
					onPointerEnter={handleMenuPointerEnter}
					onPointerLeave={handlePointerLeave}
					onEscapeKeyDown={() => {
						escapeRequested.current = true;
					}}
				/>
			) : null}
		</DropdownMenu>
	);
}

function CueMenuItems({
	projectId,
	busy,
	onInvoke,
	onDismiss,
	onPointerEnter,
	onPointerLeave,
	onEscapeKeyDown,
}: {
	projectId: string;
	busy: boolean;
	onInvoke: (cue: CueDTO) => Promise<void>;
	onDismiss: () => void;
	onPointerEnter: () => void;
	onPointerLeave: () => void;
	onEscapeKeyDown: () => void;
}) {
	const { t } = useTranslation();
	const openProjectSettings = useUiStore((state) => state.openProjectSettings);
	const query = useProjectCuesQuery(projectId);
	// The menu refreshes on every open, and hover opens it constantly, so cached
	// cues render straight away: the background refresh that keeps them fresh must
	// not blank the list into a loading row first. Loading is only for a cold read.
	const loading = query.isFetching && query.data === undefined && !query.isError;
	const canInvoke = query.isFetchedAfterMount && !query.isFetching && !query.isError;
	return (
		<DropdownMenuContent
			align="end"
			side="bottom"
			onPointerEnter={onPointerEnter}
			onPointerLeave={onPointerLeave}
			onEscapeKeyDown={onEscapeKeyDown}
		>
			{query.isError && !query.isFetching ? (
				<>
					<p role="alert" className="px-2 py-1 text-sm text-destructive">
						{t("cues.loadFailed")}
					</p>
					<DropdownMenuItem
						onSelect={(event) => {
							event.preventDefault();
							void query.refetch();
						}}
					>
						{t("cues.retry")}
					</DropdownMenuItem>
				</>
			) : loading ? (
				<DropdownMenuItem disabled>{t("cues.loading")}</DropdownMenuItem>
			) : query.data?.length ? (
				query.data.map((cue) => <CueMenuItem key={cue.id} cue={cue} busy={busy || !canInvoke} onInvoke={onInvoke} />)
			) : (
				<DropdownMenuItem disabled>{t("cues.emptyMenu")}</DropdownMenuItem>
			)}
			<DropdownMenuSeparator />
			<DropdownMenuItem
				onSelect={() => {
					// Settings replaces the menu: leaving a stale popover floating
					// over the dialog reads as a stuck control.
					onDismiss();
					openProjectSettings(projectId, { section: "cues" });
				}}
			>
				<Plus aria-hidden="true" />
				{t("cues.newCue")}
			</DropdownMenuItem>
		</DropdownMenuContent>
	);
}

// Rows stay one line: the name is the only thing that has to be scannable, so
// the description rides along as a tooltip instead of pushing rows taller.
function CueMenuItem({
	cue,
	busy,
	onInvoke,
}: {
	cue: CueDTO;
	busy: boolean;
	onInvoke: (cue: CueDTO) => Promise<void>;
}) {
	const item = (
		<DropdownMenuItem disabled={busy} onSelect={() => void onInvoke(cue)}>
			{cue.type === "agent" ? <MessageSquare aria-hidden="true" /> : <TerminalSquare aria-hidden="true" />}
			<span className="min-w-0 flex-1 truncate">{cue.name}</span>
		</DropdownMenuItem>
	);
	if (!cue.description) return item;
	return (
		<Tooltip>
			<TooltipTrigger asChild>{item}</TooltipTrigger>
			<TooltipContent side="left" className="max-w-64">
				{cue.description}
			</TooltipContent>
		</Tooltip>
	);
}
