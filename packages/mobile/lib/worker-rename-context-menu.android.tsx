import { MenuView, type MenuAction, type NativeActionEvent } from "@expo/ui/community/menu";
import { type MutableRefObject, type ReactNode } from "react";
import { Pressable } from "react-native";
import type { GestureType } from "react-native-gesture-handler";
import { useTheme, useThemeState } from "./ThemeProvider";
import { hasWorkerActionDrawable, type WorkerAction, type WorkerActionId } from "./worker-action-model";

// Android's drawables are bundled, not resolved by name, so every action needs a
// file here. `require` of a missing one fails at bundle time rather than
// rendering blank, so this map and assets/icons must agree exactly.
const DRAWABLES: Partial<Record<WorkerActionId, number>> = {
	pin: require("../assets/icons/pin.xml"),
	unpin: require("../assets/icons/unpin.xml"),
	rename: require("../assets/icons/rename.xml"),
	open: require("../assets/icons/open.xml"),
	resume: require("../assets/icons/resume.xml"),
	restore: require("../assets/icons/restore.xml"),
	openPr: require("../assets/icons/openPr.xml"),
	delete: require("../assets/icons/delete.xml"),
};

// MenuView owns Android's long-press natively. It must remain the trigger while
// the row is nested in Swipeable; React Native and Gesture Handler callbacks
// lose that touch race to the swipe container.
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
	const t = useTheme();
	const { scheme } = useThemeState();

	const menuActions: MenuAction[] = actions.map((action) => ({
		id: action.id,
		title: action.title,
		...(hasWorkerActionDrawable(action.id)
			? { image: DRAWABLES[action.id], imageColor: action.destructive ? t.red : t.accent }
			: {}),
		titleColor: action.destructive ? t.red : t.textPrimary,
		attributes: action.destructive ? { destructive: true } : undefined,
	}));

	const chooseAction = (event: NativeActionEvent) => {
		onAction(event.nativeEvent.event as WorkerActionId);
	};

	return (
		<MenuView
			colorScheme={scheme}
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
