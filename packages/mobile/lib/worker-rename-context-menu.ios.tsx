import { MenuView, type MenuAction, type NativeActionEvent } from "@expo/ui/community/menu";
import { type ReactNode } from "react";
import { Pressable } from "react-native";
import type { GestureType } from "react-native-gesture-handler";
import type { MutableRefObject } from "react";
import { workerActionSymbol, type WorkerAction, type WorkerActionId } from "./worker-action-model";

export function WorkerRowContextMenu({
	children,
	onPress,
	actions,
	onAction,
	gestureRef: _gestureRef,
	accessibilityLabel,
	accessibilityHint,
	style,
	pressedStyle,
}: {
	children: ReactNode;
	onPress(): void;
	actions: WorkerAction[];
	onAction(id: WorkerActionId): void;
	gestureRef: MutableRefObject<GestureType | undefined>;
	accessibilityLabel: string;
	accessibilityHint: string;
	style: object;
	pressedStyle: object;
}) {
	// iOS resolves SF Symbols by name at runtime, so every action can carry one.
	const menuActions: MenuAction[] = actions.map((action) => ({
		id: action.id,
		title: action.title,
		image: workerActionSymbol(action.id),
		attributes: action.destructive ? { destructive: true } : undefined,
	}));

	const chooseAction = (event: NativeActionEvent) => {
		onAction(event.nativeEvent.event as WorkerActionId);
	};

	return (
		<MenuView
			actions={menuActions}
			shouldOpenOnLongPress
			onPressAction={chooseAction}
			testID="worker-row-menu"
		>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={accessibilityLabel}
				accessibilityHint={accessibilityHint}
				onPress={onPress}
				style={({ pressed }) => [style, pressed && pressedStyle]}
			>
				{children}
			</Pressable>
		</MenuView>
	);
}
