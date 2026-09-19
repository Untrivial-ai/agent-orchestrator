import { Feather } from "@expo/vector-icons";
import { Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import { haptics } from "../haptics";
import { useTheme } from "../ThemeProvider";
import type {
	ElicitationActionProps,
	ElicitationChoiceListProps,
	ElicitationTextFieldProps,
} from "./elicitation-native-controls";

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
						android_ripple={{ color: t.tintBlue }}
						onPress={() => { haptics.select(); onChange(choice.value); }}
						style={[styles.choice, index > 0 && { borderTopColor: t.borderSubtle, borderTopWidth: StyleSheet.hairlineWidth }]}
					>
						<Feather
							name={checked ? (multi ? "check-square" : "disc") : (multi ? "square" : "circle")}
							size={22}
							color={checked ? t.blue : t.textTertiary}
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
			selectionColor={t.blue}
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
			android_ripple={{ color: primary ? "rgba(255,255,255,0.18)" : t.tintBlue }}
			onPress={() => { haptics.tap(); onPress(); }}
			style={({ pressed }) => [
				styles.action,
				{
					width: resolvedWidth,
					backgroundColor: primary ? t.blue : pressed ? t.bgSubtle : "transparent",
					opacity: disabled ? 0.45 : 1,
				},
			]}
		>
			<Text style={[styles.actionLabel, { color: primary ? t.onAccent : t.textPrimary }]}>{label}</Text>
		</Pressable>
	);
}

const styles = StyleSheet.create({
	choice: { minHeight: 66, flexDirection: "row", alignItems: "center", gap: 14, paddingVertical: 11, paddingHorizontal: 4 },
	choiceCopy: { flex: 1, gap: 3 },
	choiceLabel: { fontSize: 14, lineHeight: 19, fontWeight: "600" },
	choiceLabelSelected: { fontWeight: "700" },
	choiceDescription: { fontSize: 13, lineHeight: 18 },
	input: {
		flex: 1,
		height: 54,
		paddingHorizontal: 14,
		paddingVertical: 10,
		borderRadius: 14,
		borderCurve: "continuous",
		borderWidth: StyleSheet.hairlineWidth,
		fontSize: 15,
	},
	action: { height: 44, borderRadius: 14, alignItems: "center", justifyContent: "center", overflow: "hidden" },
	actionLabel: { fontSize: 14, fontWeight: "700" },
});
