export type ConversationMenuAction =
	| "shell"
	| "preview"
	| "pull_requests"
	| "map"
	| "refresh"
	| "settings"
	| "rename"
	| "pin"
	| "compact"
	| "terminal_ui"
	| "reload_mcp"
	| "delete";

export type ConversationMenuSection = {
	// Delete acts on the session, not the conversation, and is the one entry
	// here that destroys something — so it sits in a section of its own,
	// last, rather than among actions you can take back.
	title: "Workspace" | "Conversation" | "Agent" | "Session";
	actions: ConversationMenuAction[];
};

export function conversationMenuSections({
	canRename,
	canPin,
	canCompact,
	canReloadMcp,
	canDelete,
}: {
	canRename: boolean;
	canPin: boolean;
	canCompact: boolean;
	canReloadMcp: boolean;
	canDelete: boolean;
}): ConversationMenuSection[] {
	return [
		{ title: "Workspace", actions: ["shell", "preview", "pull_requests"] },
		{
			title: "Conversation",
			actions: [
				"map",
				"refresh",
				"settings",
				...(canRename ? ["rename" as const] : []),
				...(canPin ? ["pin" as const] : []),
				...(canCompact ? ["compact" as const] : []),
			],
		},
		{
			title: "Agent",
			actions: [
				"terminal_ui",
				...(canReloadMcp ? ["reload_mcp" as const] : []),
			],
		},
		...(canDelete ? [{ title: "Session" as const, actions: ["delete" as const] }] : []),
	];
}

export function normalizeConversationTitle(value: string): string | undefined {
	const title = value.trim();
	return title || undefined;
}
