import type { ChatConfigOption, ChatModel, ConversationSnapshot, TurnSettings } from "./types";

export type ChatTurnSettingsControlProps = {
	snapshot: ConversationSnapshot;
	models: ChatModel[];
	options: ChatConfigOption[];
	disabled?: boolean;
	onSettings(settings: TurnSettings): Promise<void>;
	onOption(id: string, value: { value: string } | { enabled: boolean }): Promise<ChatConfigOption[]>;
	onOpenFallback(): void;
};
