import { Feather } from "@expo/vector-icons";
import { MenuView, type MenuAction, type NativeActionEvent } from "@expo/ui/community/menu";
import { StyleSheet, View } from "react-native";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { iconSize, type } from "../tokens";

export function ChatAttachmentMenu({
	disabled,
	canAttachFile,
	onChoosePhoto,
	onChooseFile,
}: {
	disabled: boolean;
	canAttachFile: boolean;
	onChoosePhoto(): void;
	onChooseFile(): void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const actions: MenuAction[] = [
		{ id: "photo", title: "Choose Photo", image: "photo" },
		...(canAttachFile ? [{ id: "file", title: "Choose File", image: "doc" } satisfies MenuAction] : []),
	];
	const trigger = (
		<View
			accessible
			accessibilityRole="button"
			accessibilityLabel="Attach"
			accessibilityState={{ disabled }}
			style={[styles.trigger, disabled && styles.disabled]}
		>
			<Feather name="paperclip" size={iconSize.lg} color={disabled ? t.textFaint : t.textSecondary} />
		</View>
	);

	if (disabled) return trigger;

	const choose = (event: NativeActionEvent) => {
		haptics.tap();
		if (event.nativeEvent.event === "photo") onChoosePhoto();
		if (event.nativeEvent.event === "file") onChooseFile();
	};

	return (
		<MenuView title="Attach" actions={actions} onPressAction={choose} testID="chat-attachment-menu">
			{trigger}
		</MenuView>
	);
}

const makeStyles = (_t: Theme) => StyleSheet.create({
	trigger: { width: 42, height: 42, borderRadius: 20, borderCurve: "continuous", alignItems: "center", justifyContent: "center" },
	disabled: { opacity: 0.55 },
});
