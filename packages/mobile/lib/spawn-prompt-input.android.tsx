import { StyleSheet, TextInput } from "react-native";
import { useTheme } from "./ThemeProvider";

export type SpawnPromptInputProps = {
	value: string;
	onChangeText: (value: string) => void;
};

export function SpawnPromptInput({ value, onChangeText }: SpawnPromptInputProps) {
	const t = useTheme();
	return (
		<TextInput
			value={value}
			onChangeText={onChangeText}
			placeholder="What should this worker do?"
			placeholderTextColor={t.textTertiary}
			selectionColor={t.blue}
			multiline
			numberOfLines={3}
			maxLength={4096}
			textAlignVertical="top"
			style={[
				styles.input,
				{ color: t.textPrimary, backgroundColor: t.bgBase },
			]}
		/>
	);
}

const styles = StyleSheet.create({
	input: {
		flex: 1,
		height: 112,
		paddingHorizontal: 16,
		paddingVertical: 14,
		borderRadius: 16,
		borderCurve: "continuous",
		fontSize: 16,
		lineHeight: 22,
	},
});
