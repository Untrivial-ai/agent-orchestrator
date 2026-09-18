import { useEffect, useRef, useState } from "react";
import { View } from "react-native";
import type { GestureType } from "react-native-gesture-handler";
import Swipeable from "react-native-gesture-handler/Swipeable";
import { haptics } from "./haptics";
import { WORKER_ACTION_REVEAL_WIDTH } from "./worker-row-swipe-model";
import { WorkerRenameContextMenu } from "./worker-rename-context-menu";
import type { WorkerRowInteractionProps } from "./worker-row-interaction.types";

// Web keeps the existing gesture-handler implementation. Native platforms use
// their own files: iOS preserves its native context menu and Android owns all
// competing row gestures in one GestureDetector.
export function WorkerRowInteraction({
	sessionId,
	enabled,
	activeSwipeId,
	children,
	rightActions,
	shellStyle,
	foregroundStyle,
	rowStyle,
	pressedStyle,
	accessibilityLabel,
	accessibilityHint,
	onPress,
	onRenameRequest,
	onSwipeOpen,
	onSwipeClose,
	onReady,
}: WorkerRowInteractionProps) {
	const swipeableRef = useRef<Swipeable>(null);
	const renameGestureRef = useRef<GestureType | undefined>(undefined);
	const [actionsOpen, setActionsOpen] = useState(false);
	const close = () => swipeableRef.current?.close();

	useEffect(() => {
		onReady?.(close);
	}, [onReady]);
	useEffect(() => {
		if (activeSwipeId && activeSwipeId !== sessionId) close();
	}, [activeSwipeId, sessionId]);

	return (
		<Swipeable
			ref={swipeableRef}
			enabled={enabled}
			containerStyle={shellStyle}
			childrenContainerStyle={foregroundStyle}
			dragOffsetFromRightEdge={16}
			activeOffsetX={actionsOpen ? [-16, 16] : -16}
			failOffsetY={[-12, 12]}
			simultaneousHandlers={renameGestureRef}
			rightThreshold={WORKER_ACTION_REVEAL_WIDTH / 2}
			overshootRight={false}
			onSwipeableWillOpen={() => {
				setActionsOpen(true);
				onSwipeOpen(sessionId, close);
				haptics.select();
			}}
			onSwipeableClose={() => {
				setActionsOpen(false);
				onSwipeClose(sessionId);
			}}
			renderRightActions={() => rightActions}
		>
			{enabled ? (
				<WorkerRenameContextMenu
					gestureRef={renameGestureRef}
					onPress={onPress}
					onRename={() => {
						close();
						onRenameRequest();
					}}
					accessibilityLabel={accessibilityLabel}
					accessibilityHint={accessibilityHint}
					style={rowStyle}
					pressedStyle={pressedStyle}
				>
					{children}
				</WorkerRenameContextMenu>
			) : (
				<View style={rowStyle}>{children}</View>
			)}
		</Swipeable>
	);
}
