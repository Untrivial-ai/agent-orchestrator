import { MenuView, type MenuAction, type NativeActionEvent } from "@expo/ui/community/menu";
import { type ReactNode } from "react";
import { Pressable } from "react-native";
import type { GestureType } from "react-native-gesture-handler";
import type { MutableRefObject } from "react";
import { useTheme, useThemeState } from "./ThemeProvider";
import { workerRenameActions } from "./worker-action-model";

export function WorkerRenameContextMenu({
	children,
	onPress,
	onRename,
	gestureRef: _gestureRef,
	accessibilityLabel,
	accessibilityHint,
	style,
	pressedStyle,
}: {
	children: ReactNode;
	onPress(): void;
	onRename(): void;
	gestureRef: MutableRefObject<GestureType | undefined>;
	accessibilityLabel: string;
	accessibilityHint: string;
	style: object;
	pressedStyle: object;
}) {
	const t = useTheme();
	const { scheme } = useThemeState();
	const actions: MenuAction[] = workerRenameActions().map((action) => ({
		...action,
		image: require("../assets/icons/rename.xml"),
		imageColor: t.blue,
		titleColor: t.textPrimary,
	}));
	const chooseAction = (event: NativeActionEvent) => {
		if (event.nativeEvent.event === "rename") onRename();
	};

	return (
		<MenuView
			colorScheme={scheme}
			actions={actions}
			shouldOpenOnLongPress
			onPressAction={chooseAction}
			testID="worker-rename-menu"
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
