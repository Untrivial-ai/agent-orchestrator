import { StyleSheet, TextInput } from "react-native";
import { useTheme, useThemeState } from "./ThemeProvider";
import type { SpawnPromptInputProps } from "./spawn-prompt-input.android";
import { space, type } from "./tokens";

export function SpawnPromptInput({ value, onChangeText, height = 112 }: SpawnPromptInputProps) {
	const t = useTheme();
	const { scheme } = useThemeState();

	return (
		<TextInput
			accessibilityLabel="Task prompt"
			value={value}
			onChangeText={onChangeText}
			placeholder="What should this worker do?"
			placeholderTextColor={t.textTertiary}
			selectionColor={t.accent}
			keyboardAppearance={scheme}
			multiline
			scrollEnabled
			textAlignVertical="top"
			autoFocus
			style={[styles.input, { height, color: t.textPrimary }]}
		/>
	);
}

const styles = StyleSheet.create({
	// UIKit gives the TextInput itself this complete rectangle, unlike a tall
	// SwiftUI Host around an intrinsic one-line TextField. Every visible point is
	// therefore a real editor hit target, and long prompts scroll within it.
	input: {
		width: "100%",
		fontFamily: "Geist_400Regular",
		fontSize: type.callout.fontSize,
		lineHeight: type.callout.lineHeight,
		paddingHorizontal: space.lg,
		paddingTop: space.huge,
		paddingBottom: space.md,
	},
});
