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

	return (
		<Host style={{ flex: 1, height }} colorScheme={scheme} seedColor={t.accent}>
			<TextInput
				value={nativeValue}
				onChangeText={onChangeText}
				placeholder="What should this worker do?"
				multiline
				numberOfLines={3}
				maxLength={4096}
				autoFocus
				style={{ height, paddingHorizontal: space.lg, paddingVertical: space.md }}
				textStyle={{ color: t.textPrimary, fontSize: type.callout.fontSize }}
				placeholderTextColor={t.textTertiary}
				modifiers={[textFieldStyle("plain")]}
			/>
		</Host>
	);
}
