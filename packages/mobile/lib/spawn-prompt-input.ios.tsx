import { Host, TextInput, useNativeState } from "@expo/ui";
import { textFieldStyle } from "@expo/ui/swift-ui/modifiers";
import { useEffect } from "react";
import { useTheme, useThemeState } from "./ThemeProvider";
import type { SpawnPromptInputProps } from "./spawn-prompt-input.android";

export function SpawnPromptInput({ value, onChangeText }: SpawnPromptInputProps) {
	const t = useTheme();
	const { scheme } = useThemeState();
	const nativeValue = useNativeState(value);

	useEffect(() => {
		if (nativeValue.value !== value) nativeValue.value = value;
	}, [nativeValue, value]);

	return (
		<Host style={{ flex: 1, height: 112 }} colorScheme={scheme} seedColor={t.blue}>
			<TextInput
				value={nativeValue}
				onChangeText={onChangeText}
				placeholder="What should this worker do?"
				multiline
				numberOfLines={3}
				maxLength={4096}
				autoFocus
				style={{ height: 112, paddingHorizontal: 16, paddingVertical: 14 }}
				textStyle={{ color: t.textPrimary, fontSize: 16 }}
				placeholderTextColor={t.textTertiary}
				modifiers={[textFieldStyle("plain")]}
			/>
		</Host>
	);
}
