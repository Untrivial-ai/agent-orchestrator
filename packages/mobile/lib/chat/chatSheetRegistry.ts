import type { ComposerPickerCatalog } from "./composerSuggestions";
import type { ConversationMarker } from "./timelineModel";
import type { ChatConfigOption, ChatModel, ConversationSnapshot, TurnSettings } from "./types";

export type ChatSheetEntry =
	| { kind: "turn-settings"; snapshot: ConversationSnapshot; models: ChatModel[]; options: ChatConfigOption[]; disabled?: boolean; error?: string; onSettings(settings: TurnSettings): Promise<void>; onOption(id: string, value: { value: string } | { enabled: boolean }): Promise<ChatConfigOption[]>; onRefresh(): Promise<{ models: ChatModel[]; configOptions: ChatConfigOption[] }> }
	| { kind: "conversation-map"; markers: ConversationMarker[]; onSelect(sequence: number): void }
	| { kind: "composer-picker"; catalog: ComposerPickerCatalog; initialQuery?: string; truncated?: boolean; onSelect(value: string): void };

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
	const pathname = entry.kind === "turn-settings" ? "/sheets/chat-settings" : entry.kind === "conversation-map" ? "/sheets/conversation-map" : "/sheets/composer-picker";
	return { pathname, params: { sheetKey } } as const;
}
