import { Feather } from "../icons";
import BottomSheet, { BottomSheetView } from "@expo/ui/community/bottom-sheet";
import { useState } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { iconSize, radius, space, type } from "../tokens";

export function ChatAttachmentMenu({
	disabled,
	canAttachFile,
	onChoosePhoto,
	onChooseFile,
}: {
	disabled: boolean;
	canAttachFile: boolean;
	onChoosePhoto(): void;
	onChooseFile(): void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const insets = useSafeAreaInsets();
	const [open, setOpen] = useState(false);

	const choose = (action: () => void) => {
		haptics.tap();
		setOpen(false);
		setTimeout(action, 180);
	};

	return (
		<>
			<Pressable
				accessibilityRole="button"
				hitSlop={{ top: 1, bottom: 1 }} accessibilityLabel="Attach"
				accessibilityState={{ disabled }}
				disabled={disabled}
				android_ripple={{ color: t.accentTint, borderless: true, radius: 20 }}
				onPress={() => {
					haptics.tap();
					setOpen(true);
				}}
				style={[styles.trigger, disabled && styles.disabled]}
			>
				<Feather name="paperclip" size={iconSize.lg} color={disabled ? t.textFaint : t.textSecondary} />
			</Pressable>

			<BottomSheet
				index={open ? 0 : -1}
				enablePanDownToClose
				enableDynamicSizing
				backgroundStyle={{ backgroundColor: t.bgSurface }}
				onClose={() => setOpen(false)}
			>
				<BottomSheetView style={[styles.sheet, { paddingBottom: Math.max(insets.bottom, 16) }]}>
					<View style={styles.header}>
						<Text style={styles.title}>Attach</Text>
						<Pressable accessibilityRole="button" accessibilityLabel="Close" hitSlop={12} onPress={() => setOpen(false)}>
							<Feather name="x" size={iconSize.lg} color={t.textSecondary} />
						</Pressable>
					</View>
					<View style={styles.choices}>
						<AttachmentChoice icon="image" label="Choose photo" onPress={() => choose(onChoosePhoto)} />
						{canAttachFile ? <AttachmentChoice icon="file-text" label="Choose file" onPress={() => choose(onChooseFile)} bordered /> : null}
					</View>
				</BottomSheetView>
			</BottomSheet>
		</>
	);
}

function AttachmentChoice({ icon, label, bordered, onPress }: {
	icon: keyof typeof Feather.glyphMap;
	label: string;
	bordered?: boolean;
	onPress(): void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return (
		<Pressable
			accessibilityRole="button"
			onPress={onPress}
			android_ripple={{ color: t.accentTint }}
			style={[styles.choice, bordered && styles.choiceBorder]}
		>
			<Feather name={icon} size={iconSize.lg} color={t.accent} />
			<Text style={styles.choiceLabel}>{label}</Text>
			<Feather name="chevron-right" size={iconSize.md} color={t.textFaint} />
		</Pressable>
	);
}

const makeStyles = (t: Theme) => StyleSheet.create({
	trigger: { width: 42, height: 42, borderRadius: radius.pill, borderCurve: "continuous", alignItems: "center", justifyContent: "center", overflow: "hidden" },
	disabled: { opacity: 0.55 },
	sheet: { paddingHorizontal: space.lg, backgroundColor: t.bgSurface },
	header: { minHeight: 54, paddingHorizontal: space.xxs, flexDirection: "row", alignItems: "center", justifyContent: "space-between" },
	title: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.title3.fontSize, lineHeight: type.title3.lineHeight, fontWeight: "600" },
	choices: { borderRadius: 16, borderCurve: "continuous", backgroundColor: t.bgElevated, overflow: "hidden" },
	choice: { minHeight: 54, paddingHorizontal: space.lg, flexDirection: "row", alignItems: "center", gap: space.md },
	choiceBorder: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle },
	choiceLabel: { fontFamily: "Geist_600SemiBold", flex: 1, color: t.textPrimary, fontSize: type.callout.fontSize, lineHeight: type.callout.lineHeight, fontWeight: "600" },
});
