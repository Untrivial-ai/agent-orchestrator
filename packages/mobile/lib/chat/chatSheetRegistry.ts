import type { ComposerPickerCatalog } from "./composerSuggestions";
import type { ConversationMarker } from "./timelineModel";
import type { ChatConfigOption, ChatModel, ConversationSnapshot, TurnSettings } from "./types";

export type ConversationActionsEntry = {
	kind: "conversation-actions";
	snapshot: ConversationSnapshot;
	sessionTitle: string;
	openingShell: boolean;
	compacting: boolean;
	mcpReloading: boolean;
	refreshing: boolean;
	compactSupported: boolean;
	mcpReloadSupported: boolean;
	interfaceSupported: boolean;
	interfaceReason?: string;
	interfaceSwitching: boolean;
	canPin: boolean;
	pinned: boolean;
	onMap(): void;
	onOpenShell(): void;
	onPreview(): void;
	onPullRequests(): void;
	onSettings(): void;
	onSwitchInterface(): void;
	onCompact(): void;
	onReload(): void;
	onRename(): void;
	onTogglePin(): void;
	onRefresh(): void;
};

export type ConversationRenameEntry = {
	kind: "conversation-rename";
	initialTitle: string;
	onRename(title: string): Promise<void> | void;
};

export type ChatSheetEntry =
	| { kind: "turn-settings"; snapshot: ConversationSnapshot; models: ChatModel[]; options: ChatConfigOption[]; disabled?: boolean; error?: string; onSettings(settings: TurnSettings): Promise<void>; onOption(id: string, value: { value: string } | { enabled: boolean }): Promise<ChatConfigOption[]>; onRefresh(): Promise<{ models: ChatModel[]; configOptions: ChatConfigOption[] }> }
	| { kind: "conversation-map"; markers: ConversationMarker[]; onSelect(sequence: number): void }
	| { kind: "composer-picker"; catalog: ComposerPickerCatalog; initialQuery?: string; truncated?: boolean; onSelect(value: string): void }
	| ConversationActionsEntry
	| ConversationRenameEntry;

const entries = new Map<string, ChatSheetEntry>();
let sequence = 0;

export function parkChatSheet<T extends ChatSheetEntry>(entry: T): string {
	const key = `chat-sheet-${++sequence}`;
	entries.set(key, entry);
	return key;
}

export function readChatSheet(key?: string): ChatSheetEntry | undefined { return key ? entries.get(key) : undefined; }
export function releaseChatSheet(key?: string): void { if (key) entries.delete(key); }
export function resetChatSheets(): void { entries.clear(); sequence = 0; }

export function chatSheetRoute(entry: ChatSheetEntry) {
	const sheetKey = parkChatSheet(entry);
	const pathname = {
		"turn-settings": "/sheets/chat-settings",
		"conversation-map": "/sheets/conversation-map",
		"conversation-rename": "/sheets/conversation-rename",
		"conversation-actions": "/sheets/conversation-actions",
		"composer-picker": "/sheets/composer-picker",
	}[entry.kind];
	return { pathname, params: { sheetKey } } as const;
}
