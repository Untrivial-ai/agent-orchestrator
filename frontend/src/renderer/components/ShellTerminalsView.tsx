import { ChevronLeft, ChevronRight, Plus } from "lucide-react";
import { useCallback, useEffect } from "react";
import { Trans, useTranslation } from "react-i18next";
import { defaultShortcutBindings, shortcutBindingLabel } from "../../shared/shortcuts";
import { useOverflowScroll } from "../hooks/useOverflowScroll";
import { useCloseShellTerminal, useRenameShellTerminal, useShellTerminals } from "../hooks/useShellTerminals";
import { useShell } from "../lib/shell-context";
import { aoBridge } from "../lib/bridge";
import { isMacPlatform } from "../lib/platform";
import { cn } from "../lib/utils";
import { handleTerminalTabListKeyDown } from "../lib/terminal-tabs";
import { useResolvedTheme, useUiStore } from "../stores/ui-store";
import { ShellTerminalTab } from "./ShellTerminalTab";
import { TerminalPane } from "./TerminalPane";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

// The standalone terminals screen: shells with no agent session behind them,
// reachable from anywhere via the + at the end of a tab strip or ⌘T / Ctrl+T.
//
// This exists because the session view cannot be the only home for shells - it
// is unreachable in a project with no sessions, which is exactly when a user
// most wants a plain terminal. Inside a session, shells still appear as tabs
// beside that session's pane; this screen is where they live otherwise.
const isMac = isMacPlatform();
const newTerminalShortcutLabel = shortcutBindingLabel(defaultShortcutBindings("new-shell-terminal", isMac)[0], isMac);

export function ShellTerminalsView() {
	const { t } = useTranslation();
	const { daemonStatus } = useShell();
	const theme = useResolvedTheme();
	// The standalone screen shows only session-less shells; a session's own
	// shells belong to that session's tab strip, not this global list.
	const shellTerminals = (useShellTerminals().data ?? []).filter((s) => !s.sessionId);
	const closeShellTerminal = useCloseShellTerminal();
	const renameShellTerminal = useRenameShellTerminal();
	const requestNewShellTerminal = useUiStore((state) => state.requestNewShellTerminal);
	const activeHandleId = useUiStore((state) => state.activeShellTerminalHandleId);
	const setActiveShellTerminal = useUiStore((state) => state.setActiveShellTerminal);

	// Keep the selection pointed at a shell that still exists: closing the active
	// tab (or a daemon-side exit pruning it) would otherwise leave the pane bound
	// to a dead handle.
	const active = shellTerminals.find((s) => s.handleId === activeHandleId);
	const tabsOverflow = useOverflowScroll<HTMLDivElement>(shellTerminals.map((t) => t.handleId).join("|"));
	const selectAdjacentTab = useCallback(
		(direction: -1 | 1) => {
			if (shellTerminals.length === 0) return;
			const activeIndex = active ? shellTerminals.indexOf(active) : 0;
			const nextIndex = (activeIndex + direction + shellTerminals.length) % shellTerminals.length;
			const next = shellTerminals[nextIndex];
			if (next) setActiveShellTerminal(next.handleId);
		},
		[active, setActiveShellTerminal, shellTerminals],
	);
	useEffect(() => {
		if (shellTerminals.length === 0) {
			if (activeHandleId !== null) setActiveShellTerminal(null);
			return;
		}
		if (!active) setActiveShellTerminal(shellTerminals[0].handleId);
	}, [shellTerminals, active, activeHandleId, setActiveShellTerminal]);

	useEffect(
		() =>
			aoBridge.app.onCloseShellTerminalShortcut(() => {
				if (active) closeShellTerminal.mutate(active.handleId);
			}),
		[active, closeShellTerminal],
	);

	useEffect(() => {
		const disposePrevious = aoBridge.app.onPreviousTabShortcut(() => selectAdjacentTab(-1));
		const disposeNext = aoBridge.app.onNextTabShortcut(() => selectAdjacentTab(1));
		return () => {
			disposePrevious();
			disposeNext();
		};
	}, [selectAdjacentTab]);

	useEffect(() => {
		aoBridge.app.setCloseShellTerminalShortcutEnabled(Boolean(active));
		return () => aoBridge.app.setCloseShellTerminalShortcutEnabled(false);
	}, [active]);

	return (
		<div className="flex h-full min-h-0 flex-col text-foreground">
			<div className="flex h-inspector-tabs shrink-0 items-center gap-3 border-b border-border px-5">
				<span className="shrink-0 font-mono text-caption font-semibold uppercase tracking-wide-lg text-muted-foreground">
					{t("workbench.terminals")}
				</span>
				<Tooltip>
					<TooltipTrigger asChild>
						<span className="inline-flex">
							<button
								aria-label={t("terminal.scrollTabsLeft")}
								className={cn(
									"inline-flex size-control-sm shrink-0 items-center justify-center rounded-sm text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50 disabled:pointer-events-none disabled:opacity-0",
									!tabsOverflow.canScrollLeft && "invisible",
								)}
								disabled={!tabsOverflow.canScrollLeft}
								onClick={() => tabsOverflow.scrollByDirection(-1)}
								type="button"
							>
								<ChevronLeft aria-hidden="true" className="size-icon-md" />
							</button>
						</span>
					</TooltipTrigger>
					<TooltipContent side="bottom">{t("terminal.scrollTabsLeft")}</TooltipContent>
				</Tooltip>
				{/* Tabs shrink and truncate down to a minimum width; beyond that the
				    strip scrolls and edge chevrons reveal the overflow. */}
				<div
					ref={tabsOverflow.ref}
					aria-label={t("terminal.tabsAria")}
					className="scrollbar-none flex min-w-flex-min flex-1 items-center gap-3 overflow-x-auto"
					onKeyDown={handleTerminalTabListKeyDown}
					role="tablist"
				>
					{shellTerminals.map((shell) => {
						const isActive = shell.handleId === active?.handleId;
						return (
							<ShellTerminalTab
								key={shell.handleId}
								isActive={isActive}
								onClose={() => closeShellTerminal.mutate(shell.handleId)}
								onRename={(title) => renameShellTerminal.mutate({ handleId: shell.handleId, title })}
								onSelect={() => setActiveShellTerminal(shell.handleId)}
								shell={shell}
							/>
						);
					})}
				</div>
				<Tooltip>
					<TooltipTrigger asChild>
						<span className="inline-flex">
							<button
								aria-label={t("terminal.scrollTabsRight")}
								className={cn(
									"inline-flex size-control-sm shrink-0 items-center justify-center rounded-sm text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50 disabled:pointer-events-none disabled:opacity-0",
									!tabsOverflow.canScrollRight && "invisible",
								)}
								disabled={!tabsOverflow.canScrollRight}
								onClick={() => tabsOverflow.scrollByDirection(1)}
								type="button"
							>
								<ChevronRight aria-hidden="true" className="size-icon-md" />
							</button>
						</span>
					</TooltipTrigger>
					<TooltipContent side="bottom">{t("terminal.scrollTabsRight")}</TooltipContent>
				</Tooltip>
				<Tooltip>
					<TooltipTrigger asChild>
						<button
							aria-label={t("shortcut.new-shell-terminal")}
							className="ml-auto inline-flex size-control-sm shrink-0 items-center justify-center rounded-sm text-muted-foreground transition-colors hover:bg-interactive-hover hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50"
							onClick={requestNewShellTerminal}
							type="button"
						>
							<Plus aria-hidden="true" className="size-icon-md" />
						</button>
					</TooltipTrigger>
					<TooltipContent side="bottom">{t("terminal.newWithShortcut", { shortcut: newTerminalShortcutLabel })}</TooltipContent>
				</Tooltip>
			</div>
			<div className="min-h-0 flex-1">
				{active ? (
					<TerminalPane
						daemonReady={daemonStatus.state === "ready"}
						fontSize={12}
						terminalTarget={{
							generation: active.createdAt,
							kind: "shell",
							handleId: active.handleId,
							sessionId: active.sessionId,
							title: active.title,
						}}
						theme={theme}
					/>
				) : (
					<div className="grid h-full place-items-center bg-terminal font-mono text-control">
						<div className="text-center">
							<div className="text-terminal">{t("terminal.emptyTitle")}</div>
							<div className="mt-2 text-terminal-dim">
								<Trans
									components={{ shortcut: <span className="text-terminal" /> }}
									i18nKey="terminal.emptyHint"
									values={{ shortcut: newTerminalShortcutLabel }}
								/>
							</div>
						</div>
					</div>
				)}
			</div>
		</div>
	);
}
