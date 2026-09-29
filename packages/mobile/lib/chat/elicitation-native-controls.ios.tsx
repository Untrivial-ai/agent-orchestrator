import { Host } from "@expo/ui";
import { Button, Divider, HStack, Image, Spacer, Text, TextField, VStack, useNativeState } from "@expo/ui/swift-ui";
import {
	buttonStyle,
	controlSize,
	disabled as disabledModifier,
	fixedSize,
	font,
	foregroundStyle,
	frame,
	multilineTextAlignment,
	padding,
	textFieldStyle,
	tint,
} from "@expo/ui/swift-ui/modifiers";
import { useEffect } from "react";
import { glassPanel } from "../glass";
import { haptics } from "../haptics";
import { useTheme, useThemeState } from "../ThemeProvider";
import type { ElicitationActionProps, ElicitationChoiceListProps, ElicitationTextFieldProps } from "./elicitation-native-controls";
import { iconSize, type } from "../tokens";

export function ElicitationChoiceList({ choices, selected, multi, onChange }: ElicitationChoiceListProps) {
	const t = useTheme();
	const { scheme } = useThemeState();
	return <Host matchContents={{ vertical: true }} style={{ width: "100%" }} colorScheme={scheme} seedColor={t.accent}>
		<VStack alignment="leading" spacing={0} modifiers={[frame({ maxWidth: 1000, alignment: "leading" })]}>
			{choices.map((choice, index) => {
				const checked = selected(choice.value);
				return <VStack key={choice.value} alignment="leading" spacing={0} modifiers={[frame({ maxWidth: 1000, alignment: "leading" })]}>
					<Button
						onPress={() => { haptics.select(); onChange(choice.value); }}
						modifiers={[buttonStyle("plain"), frame({ maxWidth: 1000, minHeight: 56, alignment: "leading" })]}
					>
						<HStack alignment="top" spacing={10} modifiers={[frame({ maxWidth: 1000, alignment: "leading" }), padding({ vertical: 8, trailing: 4 })]}>
							<Image systemName={checked ? (multi ? "checkmark.circle.fill" : "largecircle.fill.circle") : "circle"} size={iconSize.md} color={checked ? t.accent : t.textTertiary} modifiers={[padding({ top: 1 })]} />
							<VStack alignment="leading" spacing={2} modifiers={[frame({ maxWidth: 1000, alignment: "leading" })]}>
								<Text modifiers={[font({ size: 14, weight: "semibold" }), foregroundStyle(checked ? t.accent : t.textPrimary), multilineTextAlignment("leading"), fixedSize({ horizontal: false, vertical: true })]}>{choice.label}</Text>
								{choice.description ? <Text modifiers={[font({ size: 12, weight: "regular" }), foregroundStyle({ type: "hierarchical", style: "secondary" }), multilineTextAlignment("leading"), fixedSize({ horizontal: false, vertical: true })]}>{choice.description}</Text> : null}
							</VStack>
							<Spacer minLength={0} />
						</HStack>
					</Button>
					{index < choices.length - 1 ? <Divider modifiers={[padding({ leading: 28 })]} /> : null}
				</VStack>;
			})}
		</VStack>
	</Host>;
}

export function ElicitationTextField({ value, label, numeric, maxLength, onChange }: ElicitationTextFieldProps) {
	const t = useTheme();
	const { scheme } = useThemeState();
	const text = useNativeState(value === undefined ? "" : String(value));
	useEffect(() => {
		const next = value === undefined ? "" : String(value);
		if (text.get() !== next) text.set(next);
	}, [text, value]);
	return <Host style={{ width: "100%", height: 46 }} colorScheme={scheme} seedColor={t.accent}>
		<TextField
			text={text}
			placeholder={label}
			maxLength={maxLength}
			onTextChange={(next) => onChange(numeric ? (next === "" ? "" : Number(next)) : next)}
			modifiers={[
				textFieldStyle("plain"),
				frame({ maxWidth: 1000, height: 44, alignment: "leading" }),
				padding({ horizontal: 14 }),
				glassPanel(),
				font({ size: 16 }),
			]}
		/>
	</Host>;
}

export function ElicitationAction({ label, primary, disabled, width, onPress }: ElicitationActionProps) {
	const t = useTheme();
	const { scheme } = useThemeState();
	const resolvedWidth = width ?? (primary ? 76 : 58);
	return <Host style={{ width: resolvedWidth, height: 36 }} colorScheme={scheme} seedColor={t.accent}>
		<Button
			label={label}
			onPress={() => { haptics.tap(); onPress(); }}
			modifiers={[
				buttonStyle(primary ? "borderedProminent" : "plain"),
				controlSize("regular"),
				frame({ width: resolvedWidth, height: 36 }),
				tint(primary ? t.accent : t.textSecondary),
				disabledModifier(disabled),
			]}
		/>
	</Host>;
}
