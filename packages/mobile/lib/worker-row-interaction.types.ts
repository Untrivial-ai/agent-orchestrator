import type { ReactNode } from "react";
import type { StyleProp, ViewStyle } from "react-native";

export type WorkerRowInteractionProps = {
	sessionId: string;
	enabled: boolean;
	activeSwipeId?: string;
	children: ReactNode;
	rightActions: ReactNode;
	shellStyle: StyleProp<ViewStyle>;
	foregroundStyle: StyleProp<ViewStyle>;
	rowStyle: ViewStyle;
	pressedStyle: ViewStyle;
	accessibilityLabel: string;
	accessibilityHint: string;
	onPress(): void;
	onRenameRequest(): void;
	onSwipeOpen(id: string, close: () => void): void;
	onSwipeClose(id: string): void;
	onReady?(close: () => void): void;
};
