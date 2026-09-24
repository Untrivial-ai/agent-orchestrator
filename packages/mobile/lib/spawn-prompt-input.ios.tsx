import { Host, TextInput, useNativeState } from "@expo/ui";
import { frame, lineLimit, textFieldStyle } from "@expo/ui/swift-ui/modifiers";
import { useEffect } from "react";
import { useTheme, useThemeState } from "./ThemeProvider";
import type { SpawnPromptInputProps } from "./spawn-prompt-input.android";
import { type, space } from "./tokens";

export function SpawnPromptInput({ value, onChangeText, height = 112 }: SpawnPromptInputProps) {
	const t = useTheme();
	const { scheme } = useThemeState();
	const nativeValue = useNativeState(value);

	useEffect(() => {
		if (nativeValue.value !== value) nativeValue.value = value;
	}, [nativeValue, value]);

	// Expo's universal `numberOfLines` reserves every line in SwiftUI. In a tall
	// sheet that vertically displaced an empty prompt, then let the editor grow
	// behind the selector rail. A ranged line limit starts at one line, grows only
	// as text is entered, and scrolls after reaching the field's bounded height.
	const lines = Math.max(1, Math.floor((height - space.md * 2) / type.callout.lineHeight));

	return (
		<Host style={{ height }} colorScheme={scheme} seedColor={t.accent}>
			<TextInput
				value={nativeValue}
				onChangeText={onChangeText}
				placeholder="What should this worker do?"
				multiline
				autoFocus
				style={{ height, paddingHorizontal: space.lg, paddingVertical: space.md }}
				textStyle={{ fontFamily: "Geist_400Regular", color: t.textPrimary, fontSize: type.callout.fontSize }}
				placeholderTextColor={t.textTertiary}
				modifiers={[
					textFieldStyle("plain"),
					lineLimit({ min: 1, max: lines }),
					frame({ height, maxWidth: 1000, alignment: "topLeading" }),
				]}
			/>
		</Host>
	);
}
