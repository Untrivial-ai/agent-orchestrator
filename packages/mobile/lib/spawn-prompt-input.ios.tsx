import { Host, TextInput, useNativeState } from "@expo/ui";
import { textFieldStyle } from "@expo/ui/swift-ui/modifiers";
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

	// `numberOfLines` becomes SwiftUI's `lineLimit(n, reservesSpace: true)`: the
	// field is exactly n lines tall and scrolls past them. At a fixed 3 it stayed a
	// 3-line strip however much room it had, so the count follows the height.
	const lines = Math.max(3, Math.floor((height - space.md * 2) / type.callout.lineHeight));

	return (
		<Host style={{ flex: 1, height }} colorScheme={scheme} seedColor={t.accent}>
			<TextInput
				value={nativeValue}
				onChangeText={onChangeText}
				placeholder="What should this worker do?"
				multiline
				numberOfLines={lines}
				autoFocus
				style={{ height, paddingHorizontal: space.lg, paddingVertical: space.md }}
				textStyle={{ fontFamily: "Geist_400Regular", color: t.textPrimary, fontSize: type.callout.fontSize }}
				placeholderTextColor={t.textTertiary}
				modifiers={[textFieldStyle("plain")]}
			/>
		</Host>
	);
}
