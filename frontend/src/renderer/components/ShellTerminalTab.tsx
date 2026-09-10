import { SquareTerminal, X } from "lucide-react";
import { type MouseEvent, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useTruncatedText } from "../hooks/useTruncatedText";
import type { ShellTerminal } from "../hooks/useShellTerminals";
import { isWindowsPlatform } from "../lib/platform";
import { cn } from "../lib/utils";
import { TerminalTabFrame } from "./TerminalTabFrame";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

type ShellTerminalTabProps = {
	shell: ShellTerminal;
	isActive: boolean;
	onSelect: () => void;
	onClose: () => void;
	/** Visually connects the active tab to a terminal pane directly below it. */
	appearance?: "pill" | "connected";
	/** Commit a new tab title. Omitted where rename is not wired. */
	onRename?: (title: string) => void;
};

// One standalone-shell tab, shared by the session pane's tab strip (CenterPane)
// and the standalone /terminals screen (ShellTerminalsView) so the two never
// drift. Session-pane tabs use a connected treatment that visually continues
// into xterm below; the standalone terminals screen keeps its compact pill.
// The full title only becomes the hover tooltip when the strip truncates it.
//
// Renaming happens inline with the platform's native gesture: double-click on
// macOS/Linux, right-click on Windows. Enter or blur commits, Escape cancels,
// and an empty or unchanged name is discarded. The close control is a sibling
// button, not nested inside the tab button - nesting interactive elements is
// invalid HTML and breaks keyboard traversal. Connected session-strip tabs hug
// the title and cross-fade the terminal glyph into that sibling on hover.
export function ShellTerminalTab({
	shell,
	isActive,
	onSelect,
	onClose,
	appearance = "pill",
	onRename,
}: ShellTerminalTabProps) {
	const { t } = useTranslation();
	const title = shell.title;
	const { ref, isTruncated } = useTruncatedText<HTMLButtonElement>(title);
	const [isEditing, setIsEditing] = useState(false);
	const [draft, setDraft] = useState(shell.title);
	const inputRef = useRef<HTMLInputElement | null>(null);
	const lastClickAtRef = useRef(0);
	const canEdit = Boolean(onRename) && !shell.optimistic;
	// Rename gesture per platform: Windows uses right-click (its convention for
	// tab/file renames); macOS and Linux use double-click.
	const renameViaRightClick = isWindowsPlatform();

	useEffect(() => {
		if (isEditing) {
			inputRef.current?.focus();
			inputRef.current?.select();
		}
	}, [isEditing]);

	const beginEdit = () => {
		if (!canEdit || isEditing) return;
		setDraft(title);
		setIsEditing(true);
	};

	// Detect a double-click ourselves from two quick clicks rather than relying on
	// the native dblclick event: some trackpad configurations deliver two taps as
	// two separate clicks that never synthesize a dblclick, so onDoubleClick would
	// never fire. Two clicks within 500ms anywhere on the tab start the rename.
	const handleClick = (event: MouseEvent) => {
		onSelect();
		// Roving tab navigation activates the focused tab with HTMLElement.click(),
		// whose detail is 0. It should select, but must not participate in the
		// pointer-only double-click rename gesture.
		if (renameViaRightClick || event.detail === 0) return;
		const now = Date.now();
		const isDoubleClick = now - lastClickAtRef.current < 500;
		lastClickAtRef.current = isDoubleClick ? 0 : now;
		if (isDoubleClick) beginEdit();
	};

	// The selection button owns the whole visible tab surface. Windows uses
	// right-click for rename; macOS/Linux use the click-timing detector above.
	const tabRenameHandlers = isEditing
		? {}
		: renameViaRightClick
			? {
					onClick: handleClick,
					onContextMenu: (event: MouseEvent) => {
						event.preventDefault();
						beginEdit();
					},
				}
			: { onClick: handleClick, onDoubleClick: beginEdit };

	const commit = () => {
		if (!isEditing) return;
		setIsEditing(false);
		const next = draft.trim();
		if (next && next !== shell.title) onRename?.(next);
	};

	const cancel = () => {
		setIsEditing(false);
		setDraft(title);
	};

	const closeControl = {
		"aria-label": t("terminal.closeNamed", { title }),
		"data-terminal-tab-action": true,
		onClick: (event: MouseEvent) => {
			event.stopPropagation();
			onClose();
		},
		onDoubleClick: (event: MouseEvent) => event.stopPropagation(),
		onContextMenu: (event: MouseEvent) => event.stopPropagation(),
		type: "button" as const,
	};
	const connectedGlyphClass =
		"size-icon-sm shrink-0 translate-y-px";
	const isConnected = appearance === "connected";
	if (isConnected) {
		const closeAction = isEditing || shell.optimistic ? undefined : (
			<Tooltip>
				<TooltipTrigger asChild>
					<button
						{...closeControl}
						className="relative grid size-icon-sm place-items-center text-passive opacity-0 pointer-events-none before:absolute before:-inset-1.5 before:content-[''] transition-colors duration-fast ease-out hover:text-foreground group-hover:pointer-events-auto group-hover:opacity-100 group-focus-within:pointer-events-auto group-focus-within:opacity-100 focus-visible:pointer-events-auto focus-visible:opacity-100 focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50 motion-reduce:transition-none"
					>
						<X aria-hidden="true" className="size-icon-sm translate-y-px" />
					</button>
				</TooltipTrigger>
				<TooltipContent side="bottom">{t("terminal.closeNamed", { title })}</TooltipContent>
			</Tooltip>
		);
		const editingContent = isEditing ? (
			<input
				aria-label={t("terminal.rename", { title })}
				className="min-w-0 w-full rounded-sm border border-accent bg-background px-1 font-mono text-control font-semibold text-foreground shadow-sm outline-none ring-1 ring-accent"
				onBlur={commit}
				onChange={(event) => setDraft(event.target.value)}
				onKeyDown={(event) => {
					if (event.key === "Enter") {
						event.preventDefault();
						commit();
					} else if (event.key === "Escape") {
						event.preventDefault();
						cancel();
					}
				}}
				ref={inputRef}
				value={draft}
			/>
		) : undefined;
		return (
			<TerminalTabFrame
				action={closeAction}
				actionPosition="leading"
				active={isActive}
				buttonProps={{
					"aria-current": isActive,
					"aria-selected": isActive,
					...tabRenameHandlers,
					role: "tab",
					tabIndex: isActive ? 0 : -1,
					title: isTruncated
						? title
						: t(renameViaRightClick ? "terminal.renameHintRightClick" : "terminal.renameHintDoubleClick", {
								workingDir: shell.workingDir,
							}),
					type: "button",
				}}
				buttonRef={ref}
				className="max-w-shell-tab-max"
				editingContent={editingContent}
			>
				<SquareTerminal
					aria-hidden="true"
					className={cn("size-icon-sm shrink-0", "group-hover:opacity-0 group-focus-within:opacity-0")}
				/>
				<span className="truncate">{title}</span>
			</TerminalTabFrame>
		);
	}

	return (
		<span
			data-editing={isConnected && isEditing ? "true" : undefined}
			className={cn(
				"group relative h-full shrink-0 self-stretch items-center",
				isConnected
					? cn(
							"session-tab-icon-floor session-tab-icon-floor--closable relative inline-flex max-w-shell-tab-max border-x border-transparent",
							isEditing && "pl-2 pr-1",
						)
					: "inline-flex min-w-shell-tab-min shrink-0 items-center gap-1 rounded-md px-2 py-1",
				isConnected
					? isActive
						? "border-border-strong bg-overlay text-foreground"
						: "border-transparent text-passive hover:bg-raised hover:text-foreground"
					: isActive
						? "bg-interactive-active"
						: "hover:bg-interactive-hover/60",
			)}
		>
			{isConnected && isEditing ? (
				<SquareTerminal aria-hidden="true" className={cn("mr-1", connectedGlyphClass)} />
			) : null}
			{isEditing ? (
				<input
					aria-label={t("terminal.rename", { title })}
					className={cn(
						"rounded-sm border border-accent bg-background px-1 font-mono text-control font-semibold text-foreground shadow-sm outline-none ring-1 ring-accent",
						isConnected ? "min-w-0 w-full text-left" : "min-w-flex-min max-w-shell-tab-max",
					)}
					onBlur={commit}
					onChange={(event) => setDraft(event.target.value)}
					onKeyDown={(event) => {
						if (event.key === "Enter") {
							event.preventDefault();
							commit();
						} else if (event.key === "Escape") {
							event.preventDefault();
							cancel();
						}
					}}
					ref={inputRef}
					value={draft}
				/>
			) : (
				<button
					ref={ref}
					aria-current={isActive}
					aria-selected={isActive}
					className={cn(
						"select-none truncate text-control focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent/50",
						isConnected
							? "inline-flex h-full min-w-0 cursor-pointer items-center pl-2 pr-2 text-left"
							: "min-w-flex-min max-w-shell-tab-max cursor-pointer",
					isConnected ? "font-normal" : "font-mono font-semibold",
						isActive ? "text-foreground" : "text-passive group-hover:text-foreground",
					)}
					{...tabRenameHandlers}
					role="tab"
					tabIndex={isActive ? 0 : -1}
					title={
						isTruncated
							? title
							: t(renameViaRightClick ? "terminal.renameHintRightClick" : "terminal.renameHintDoubleClick", {
									workingDir: shell.workingDir,
								})
					}
					type="button"
				>
					<span className="inline-flex min-w-0 -translate-y-px items-center">
						{isConnected ? (
							<SquareTerminal
								aria-hidden="true"
								className={cn(
									"mr-1",
									connectedGlyphClass,
									"group-hover:opacity-0 group-focus-within:opacity-0",
								)}
							/>
						) : null}
						<span className="truncate">{title}</span>
					</span>
				</button>
			)}
			{isConnected && (isEditing || shell.optimistic) ? null : (
				<Tooltip>
					<TooltipTrigger asChild>
						<button
							{...closeControl}
							className={
								isConnected
									? "absolute top-[calc(50%_-_1px)] left-2 z-20 grid size-icon-sm -translate-y-1/2 place-items-center text-passive opacity-0 pointer-events-none before:absolute before:-inset-1.5 before:content-[''] transition-colors duration-fast ease-out hover:text-foreground group-hover:pointer-events-auto group-hover:opacity-100 group-focus-within:pointer-events-auto group-focus-within:opacity-100 focus-visible:pointer-events-auto focus-visible:opacity-100 focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50 motion-reduce:transition-none"
									: "inline-flex h-control-sm w-control-sm shrink-0 items-center justify-center overflow-hidden text-passive opacity-0 transition-colors duration-fast ease-out hover:text-foreground group-hover:opacity-100 group-focus-within:opacity-100 focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50 motion-reduce:transition-none"
							}
						>
							<X
								aria-hidden="true"
								className={isConnected ? "size-icon-sm translate-y-px" : "size-icon-sm"}
							/>
						</button>
					</TooltipTrigger>
					<TooltipContent side="bottom">{t("terminal.closeNamed", { title })}</TooltipContent>
				</Tooltip>
			)}
		</span>
	);
}
