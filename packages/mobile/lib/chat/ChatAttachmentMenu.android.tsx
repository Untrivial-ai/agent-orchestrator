import { Feather } from "@expo/vector-icons";
import BottomSheet, { BottomSheetView } from "@expo/ui/community/bottom-sheet";
import { useState } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { haptics } from "../haptics";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";

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
				accessibilityLabel="Attach"
				accessibilityState={{ disabled }}
				disabled={disabled}
				android_ripple={{ color: t.tintBlue, borderless: true, radius: 20 }}
				onPress={() => {
					haptics.tap();
					setOpen(true);
				}}
				style={[styles.trigger, disabled && styles.disabled]}
			>
				<Feather name="paperclip" size={21} color={disabled ? t.textFaint : t.textSecondary} />
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
							<Feather name="x" size={21} color={t.textSecondary} />
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
			android_ripple={{ color: t.tintBlue }}
			style={[styles.choice, bordered && styles.choiceBorder]}
		>
			<Feather name={icon} size={19} color={t.blue} />
			<Text style={styles.choiceLabel}>{label}</Text>
			<Feather name="chevron-right" size={18} color={t.textFaint} />
		</Pressable>
	);
}

const makeStyles = (t: Theme) => StyleSheet.create({
	trigger: { width: 42, height: 42, borderRadius: 21, alignItems: "center", justifyContent: "center", overflow: "hidden" },
	disabled: { opacity: 0.55 },
	sheet: { paddingHorizontal: 16, backgroundColor: t.bgSurface },
	header: { minHeight: 54, paddingHorizontal: 4, flexDirection: "row", alignItems: "center", justifyContent: "space-between" },
	title: { color: t.textPrimary, fontSize: 20, lineHeight: 26, fontWeight: "700" },
	choices: { borderRadius: 16, backgroundColor: t.bgElevated, overflow: "hidden" },
	choice: { minHeight: 54, paddingHorizontal: 15, flexDirection: "row", alignItems: "center", gap: 11 },
	choiceBorder: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle },
	choiceLabel: { flex: 1, color: t.textPrimary, fontSize: 16, lineHeight: 21, fontWeight: "600" },
});
