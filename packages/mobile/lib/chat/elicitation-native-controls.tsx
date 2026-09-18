import { Button, Column, Host, ListItem, Text as NativeText, TextInput as NativeTextInput, useNativeState } from "@expo/ui";
import { useEffect } from "react";
import { haptics } from "../haptics";
import { useTheme, useThemeState } from "../ThemeProvider";

export type ElicitationChoice = { value: string; label: string; description?: string };
export type ElicitationChoiceListProps = {
	choices: ElicitationChoice[];
	selected(value: string): boolean;
	multi: boolean;
	onChange(value: string): void;
};
export type ElicitationTextFieldProps = {
	value: unknown;
	label: string;
	autoFocus?: boolean;
	numeric?: boolean;
	maxLength?: number;
	onChange(value: string | number): void;
};
export type ElicitationActionProps = { label: string; primary?: boolean; disabled?: boolean; width?: number; onPress(): void };

export function ElicitationChoiceList({ choices, selected, multi, onChange }: ElicitationChoiceListProps) {
	const t = useTheme();
	const { scheme } = useThemeState();
	return (
		<Host matchContents={{ vertical: true }} style={{ width: "100%" }} colorScheme={scheme} seedColor={t.blue}>
			<Column spacing={0} style={{ width: "100%" }}>
				{choices.map((choice) => {
					const checked = selected(choice.value);
					return <ListItem
						key={choice.value}
						testID={`elicitation-choice-${choice.value}`}
						onPress={() => { haptics.select(); onChange(choice.value); }}
						leading={<NativeText textStyle={{ color: checked ? t.blue : t.textTertiary, fontSize: 20 }}>{checked ? (multi ? "✓" : "●") : "○"}</NativeText>}
						supportingText={choice.description}
					>
						<NativeText textStyle={{ color: t.textPrimary, fontSize: 14, fontWeight: checked ? "700" : "600" }}>{choice.label}</NativeText>
					</ListItem>;
				})}
			</Column>
		</Host>
	);
}

export function ElicitationTextField({ value, label, autoFocus, numeric, maxLength, onChange }: ElicitationTextFieldProps) {
	const t = useTheme();
	const { scheme } = useThemeState();
	const text = useNativeState(value === undefined ? "" : String(value));
	useEffect(() => {
		const next = value === undefined ? "" : String(value);
		if (text.value !== next) text.value = next;
	}, [text, value]);
	return <Host style={{ width: "100%", height: 58 }} colorScheme={scheme} seedColor={t.blue}>
		<NativeTextInput
			value={text}
			autoFocus={autoFocus}
			onChangeText={(next) => onChange(numeric ? (next === "" ? "" : Number(next)) : next)}
			placeholder={label}
			keyboardType={numeric ? "numeric" : "default"}
			maxLength={maxLength}
			style={{ width: "100%", height: 54, paddingHorizontal: 14, paddingVertical: 10, borderRadius: 14, backgroundColor: t.bgSubtle }}
			placeholderTextColor={t.textFaint}
		/>
	</Host>;
}

export function ElicitationAction({ label, primary, disabled, width, onPress }: ElicitationActionProps) {
	const t = useTheme();
	const { scheme } = useThemeState();
	const resolvedWidth = width ?? (primary ? 96 : 70);
	return <Host style={{ width: resolvedWidth, height: 44 }} colorScheme={scheme} seedColor={t.blue}>
		<Button label={label} variant={primary ? "filled" : "text"} disabled={disabled} onPress={() => { haptics.tap(); onPress(); }} style={{ width: resolvedWidth, height: 44, borderRadius: 14 }} />
	</Host>;
}
