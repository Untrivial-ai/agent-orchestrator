import { X } from "lucide-react";
import { useTranslation } from "react-i18next";
import type { SessionFileTabState } from "../lib/session-file-tabs";
import { cn } from "../lib/utils";
import { TerminalTabFrame } from "./TerminalTabFrame";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";
import { WorkspaceEntryIcon } from "./WorkspaceEntryIcon";

function basename(path: string): string {
	return path.split("/").pop() || path;
}

export function SessionFileTabs({
	state,
	onActivateFile,
	onCloseFile,
	dirtyPaths,
}: {
	state: SessionFileTabState;
	onActivateFile: (path: string) => void;
	onCloseFile: (path: string) => void;
	dirtyPaths?: ReadonlySet<string>;
}) {
	if (state.openPaths.length === 0) return null;
	return (
		<>
			{state.openPaths.map((path) => (
				<SessionFileTab
					active={state.activePath === path}
					dirty={dirtyPaths?.has(path)}
					key={path}
					onActivate={() => onActivateFile(path)}
					onClose={() => onCloseFile(path)}
					path={path}
				/>
			))}
		</>
	);
}

export function SessionFileTab({
	active,
	dirty = false,
	onActivate,
	onClose,
	path,
}: {
	active: boolean;
	dirty?: boolean;
	onActivate: () => void;
	onClose: () => void;
	path: string;
}) {
	const { t } = useTranslation();
	const name = basename(path);
	// The close button overlays the file icon's slot and replaces it on hover,
	// matching the shell terminal tabs. A dirty file keeps an always-visible dot
	// in that slot; the dot swaps to the cross on hover.
	const closeAction = (
		<span className="grid size-icon-base place-items-center">
			<Tooltip>
				<TooltipTrigger asChild>
					<button
						aria-label={t("files.closeTab", { name })}
						className={cn(
							"grid size-icon-sm place-items-center rounded-sm text-passive hover:bg-interactive-hover hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-accent/50",
							dirty
								? "pointer-events-auto opacity-100"
								: "pointer-events-none opacity-0 group-hover:pointer-events-auto group-hover:opacity-100 group-focus-within:pointer-events-auto group-focus-within:opacity-100 focus-visible:pointer-events-auto focus-visible:opacity-100",
						)}
						onClick={(event) => {
							event.stopPropagation();
							onClose();
						}}
						type="button"
					>
						{dirty ? (
							<>
								<span
									aria-hidden="true"
									className="size-2 rounded-full bg-foreground group-hover:hidden"
									data-testid="unsaved-tab-indicator"
								/>
								<X aria-hidden="true" className="hidden size-icon-sm group-hover:block" />
							</>
						) : <X className="size-icon-sm" aria-hidden="true" />}
					</button>
				</TooltipTrigger>
				<TooltipContent side="bottom">{t("files.closeTab", { name })}</TooltipContent>
			</Tooltip>
		</span>
	);
	return (
		<TerminalTabFrame
			action={closeAction}
			actionLayout="overlay"
			actionPosition="leading"
			active={active}
			buttonProps={{
				"aria-label": name,
				"aria-selected": active,
				onClick: onActivate,
				role: "tab",
				tabIndex: active ? 0 : -1,
				title: path,
				type: "button",
			}}
			className="max-w-shell-tab-max"
			contentClassName="font-medium"
		>
			<WorkspaceEntryIcon
				className={cn(
					"size-icon-base shrink-0",
					dirty ? "opacity-0" : "group-hover:opacity-0 group-focus-within:opacity-0",
				)}
				kind="file"
				name={name}
			/>
			<span className="truncate">{name}</span>
		</TerminalTabFrame>
	);
}
