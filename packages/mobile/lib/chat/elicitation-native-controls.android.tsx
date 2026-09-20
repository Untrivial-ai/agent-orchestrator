import { Feather } from "@expo/vector-icons";
import { Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import { haptics } from "../haptics";
import { useTheme } from "../ThemeProvider";
import type {
	ElicitationActionProps,
	ElicitationChoiceListProps,
	ElicitationTextFieldProps,
} from "./elicitation-native-controls";
import { iconSize, space, type } from "../tokens";

export function ElicitationChoiceList({ choices, selected, multi, onChange }: ElicitationChoiceListProps) {
	const t = useTheme();
	return (
		<View>
			{choices.map((choice, index) => {
				const checked = selected(choice.value);
				return (
					<Pressable
						key={choice.value}
						testID={`elicitation-choice-${choice.value}`}
						accessibilityRole={multi ? "checkbox" : "radio"}
						accessibilityState={{ checked }}
						android_ripple={{ color: t.accentTint }}
						onPress={() => { haptics.select(); onChange(choice.value); }}
						style={[styles.choice, index > 0 && { borderTopColor: t.borderSubtle, borderTopWidth: StyleSheet.hairlineWidth }]}
					>
						<Feather
							name={checked ? (multi ? "check-square" : "disc") : (multi ? "square" : "circle")}
							size={iconSize.lg}
							color={checked ? t.accent : t.textTertiary}
						/>
						<View style={styles.choiceCopy}>
							<Text style={[styles.choiceLabel, { color: t.textPrimary }, checked && styles.choiceLabelSelected]}>{choice.label}</Text>
							{choice.description ? <Text style={[styles.choiceDescription, { color: t.textSecondary }]}>{choice.description}</Text> : null}
						</View>
					</Pressable>
				);
			})}
		</View>
	);
}

export function ElicitationTextField({ value, label, autoFocus, numeric, maxLength, onChange }: ElicitationTextFieldProps) {
	const t = useTheme();
	return (
		<TextInput
			value={value === undefined ? "" : String(value)}
			autoFocus={autoFocus}
			onChangeText={(next) => onChange(numeric ? (next === "" ? "" : Number(next)) : next)}
			placeholder={label}
			placeholderTextColor={t.textFaint}
			selectionColor={t.accent}
			keyboardType={numeric ? "numeric" : "default"}
			maxLength={maxLength}
			style={[styles.input, { color: t.textPrimary, backgroundColor: t.bgSubtle, borderColor: t.borderDefault }]}
		/>
	);
}

export function ElicitationAction({ label, primary, disabled, width, onPress }: ElicitationActionProps) {
	const t = useTheme();
	const resolvedWidth = width ?? (primary ? 96 : 70);
	return (
		<Pressable
			accessibilityRole="button"
			disabled={disabled}
			android_ripple={{ color: primary ? "rgba(255,255,255,0.18)" : t.accentTint }}
			onPress={() => { haptics.tap(); onPress(); }}
			style={({ pressed }) => [
				styles.action,
				{
					width: resolvedWidth,
					backgroundColor: primary ? t.accent : pressed ? t.bgSubtle : "transparent",
					opacity: disabled ? 0.45 : 1,
				},
			]}
		>
			<Text style={[styles.actionLabel, { color: primary ? t.onAccent : t.textPrimary }]}>{label}</Text>
		</Pressable>
	);
}

const styles = StyleSheet.create({
	choice: { minHeight: 66, flexDirection: "row", alignItems: "center", gap: space.md, paddingVertical: space.md, paddingHorizontal: space.xxs },
	choiceCopy: { flex: 1, gap: space.hair },
	choiceLabel: { fontFamily: "Geist_600SemiBold", fontSize: type.subheadline.fontSize, lineHeight: type.subheadline.lineHeight, fontWeight: "600" },
	choiceLabelSelected: { fontFamily: "Geist_600SemiBold", fontWeight: "600" },
	choiceDescription: { fontFamily: "Geist_400Regular", fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight },
	input: { fontFamily: "Geist_400Regular",
		flex: 1,
		height: 54,
		paddingHorizontal: space.md,
		paddingVertical: space.sm,
		borderRadius: 12,
		borderCurve: "continuous",
		borderWidth: StyleSheet.hairlineWidth,
		fontSize: type.subheadline.fontSize,
	},
	action: { height: 44, borderRadius: 12, borderCurve: "continuous", alignItems: "center", justifyContent: "center", overflow: "hidden" },
	actionLabel: { fontFamily: "Geist_600SemiBold", fontSize: type.subheadline.fontSize, fontWeight: "600" },
});
