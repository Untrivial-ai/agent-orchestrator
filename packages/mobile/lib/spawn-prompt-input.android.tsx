import { StyleSheet, TextInput } from "react-native";
import { useTheme } from "./ThemeProvider";
import { space, type } from "./tokens";

export type SpawnPromptInputProps = {
	value: string;
	onChangeText: (value: string) => void;
	/**
	 * How tall the field should be. iOS passes the room the sheet has left so the
	 * whole empty area is the field — at a fixed 112 only the top of the sheet
	 * took a tap. Android's sheet sizes itself to its content, so it leaves this
	 * unset and keeps the compact field.
	 */
	height?: number;
};

export function SpawnPromptInput({ value, onChangeText }: SpawnPromptInputProps) {
	const t = useTheme();
	return (
		<TextInput
			value={value}
			onChangeText={onChangeText}
			placeholder="What should this worker do?"
			placeholderTextColor={t.textTertiary}
			selectionColor={t.accent}
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
		paddingHorizontal: space.lg,
		paddingVertical: space.md,
		borderRadius: 16,
		borderCurve: "continuous",
		fontSize: type.callout.fontSize,
		lineHeight: type.callout.lineHeight,
	},
});
