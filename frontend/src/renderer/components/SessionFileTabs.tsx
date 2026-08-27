import { MoreHorizontal, Plus, X } from "lucide-react";
import { useTranslation } from "react-i18next";
import { cn } from "../lib/utils";
import type { SessionFileTabState } from "../lib/session-file-tabs";
import { Button } from "./ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "./ui/dropdown-menu";
import { WorkspaceEntryIcon } from "./WorkspaceEntryIcon";

function basename(path: string): string {
	return path.split("/").pop() || path;
}

export function SessionFileTabs({
	state,
	onAddFeedback,
	onActivateFile,
	onCloseFile,
	onCloseAll,
}: {
	state: SessionFileTabState;
	onAddFeedback: (path: string) => void;
	onActivateFile: (path: string) => void;
	onCloseFile: (path: string) => void;
	onCloseAll: () => void;
}) {
	const { t } = useTranslation();
	if (state.openPaths.length === 0) return null;
	return (
		<>
			{state.openPaths.map((path) => {
				const name = basename(path);
				const active = state.activePath === path;
				return (
					<span
						className={cn(
							"group relative inline-flex min-w-shell-tab-min max-w-shell-tab-max self-stretch items-center gap-1.5 border-r border-border py-0 pl-3 pr-1.5",
							active
								? "bg-overlay text-foreground after:absolute after:inset-x-0 after:bottom-0 after:h-0.5 after:bg-foreground/80"
								: "text-muted-foreground hover:bg-raised hover:text-foreground",
						)}
						key={path}
					>
						<button
							aria-label={name}
							aria-selected={active}
							className="inline-flex min-w-0 flex-1 items-center gap-1.5 truncate text-left text-control font-medium leading-none"
							onClick={() => onActivateFile(path)}
							role="tab"
							tabIndex={active ? 0 : -1}
							title={path}
							type="button"
						>
							<WorkspaceEntryIcon className="size-icon-base shrink-0" kind="file" name={name} />
							<span className="truncate">{name}</span>
						</button>
						{active ? (
							<button
								aria-label={t("files.addFileFeedback", { file: path })}
								className="grid size-5 shrink-0 place-items-center rounded-sm text-passive hover:bg-interactive-hover hover:text-foreground"
								onClick={() => onAddFeedback(path)}
								type="button"
							>
								<Plus className="size-3" aria-hidden="true" />
							</button>
						) : null}
						<button
							aria-label={t("files.closeTab", { name })}
							className="grid size-5 shrink-0 place-items-center rounded-sm text-passive opacity-70 hover:bg-interactive-hover hover:text-foreground hover:opacity-100"
							onClick={(event) => {
								event.stopPropagation();
								onCloseFile(path);
							}}
							type="button"
						>
							<X className="size-3" aria-hidden="true" />
						</button>
					</span>
				);
			})}
			<DropdownMenu>
				<DropdownMenuTrigger asChild>
					<Button aria-label={t("files.tabActions")} className="mx-1 self-center" size="icon-sm" type="button" variant="ghost">
						<MoreHorizontal className="size-icon-sm" aria-hidden="true" />
					</Button>
				</DropdownMenuTrigger>
				<DropdownMenuContent align="end">
					<DropdownMenuItem onSelect={onCloseAll}>{t("files.closeAllTabs")}</DropdownMenuItem>
				</DropdownMenuContent>
			</DropdownMenu>
		</>
	);
}
