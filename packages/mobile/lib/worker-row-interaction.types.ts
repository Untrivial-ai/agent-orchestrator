import type { ReactNode } from "react";
import type { StyleProp, ViewStyle } from "react-native";

import type { WorkerAction, WorkerActionId } from "./worker-action-model";

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
	/**
	 * The long-press menu's contents, decided by workerContextActions so the rules
	 * live in one tested place rather than in three platform components.
	 */
	actions: WorkerAction[];
	onAction(id: WorkerActionId): void;
	onSwipeOpen(id: string, close: () => void): void;
	onSwipeClose(id: string): void;
	onReady?(close: () => void): void;
};
