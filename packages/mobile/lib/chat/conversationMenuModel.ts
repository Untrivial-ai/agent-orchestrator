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
	| "reload_mcp";

export type ConversationMenuSection = {
	title: "Workspace" | "Conversation" | "Agent";
	actions: ConversationMenuAction[];
};

export function conversationMenuSections({
	canRename,
	canPin,
	canCompact,
	canReloadMcp,
}: {
	canRename: boolean;
	canPin: boolean;
	canCompact: boolean;
	canReloadMcp: boolean;
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
	];
}

export function normalizeConversationTitle(value: string): string | undefined {
	const title = value.trim();
	return title || undefined;
}
