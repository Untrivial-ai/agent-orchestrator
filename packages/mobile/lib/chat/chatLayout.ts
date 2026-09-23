import { sheetInset, space } from "../tokens";

export const centeredConversationMenu = {
	position: "absolute",
	left: 16,
	right: 16,
} as const;

export const composerSheetContentStyle = {
	paddingHorizontal: sheetInset.horizontal,
	paddingTop: sheetInset.top,
	paddingBottom: sheetInset.bottom,
} as const;
