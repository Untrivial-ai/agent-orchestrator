import {
	Bot,
	Copy,
	File,
	Folder,
	FolderPlus,
	GitPullRequest,
	Settings,
	SquarePen,
	Sun,
	type LucideIcon,
} from "lucide-react";
import type { CommandGroupId, CommandItem } from "./command-palette";

/** Exact-id icons for known actions. Add new command ids here as they ship. */
const COMMAND_ICONS: Record<string, LucideIcon> = {
	"current-new-task": SquarePen,
	"current-open-orchestrator": Bot,
	"current-project-settings": Settings,
	"current-copy-branch": Copy,
	"current-search-files": Folder,
	"global-new-project": FolderPlus,
	"global-settings": Settings,
	"global-theme": Sun,
};

/** Fallback icons by group when no exact id match exists. */
const GROUP_ICONS: Partial<Record<CommandGroupId, LucideIcon>> = {
	projects: Folder,
	files: File,
	prs: GitPullRequest,
	// Sessions / attention stay text-only, matching Cursor's "Chats" rows.
};

export function iconForCommand(item: CommandItem): LucideIcon | null {
	return COMMAND_ICONS[item.id] ?? GROUP_ICONS[item.group] ?? null;
}
